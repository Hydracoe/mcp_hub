package downstream

import (
	"context"
	"encoding/json"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Prober 对单个下游 server 执行 L1–L3 探活。
//
// 每次 Probe 都新建连接、用完即弃，不触碰生产连接池——
// 探活失败不应污染在线状态，探活成功也应通过刷新工具缓存生效。
type Prober struct {
	Spec Spec
}

// NewProber 构造探活器。
func NewProber(spec Spec) *Prober {
	return &Prober{Spec: spec}
}

// Probe 执行完整探活。永不返回 nil，失败信息在结果里。
func (p *Prober) Probe(ctx context.Context) *ProbeResult {
	res := &ProbeResult{CheckedAt: time.Now()}
	started := time.Now()
	defer func() { res.Latency.Total = msSince(started) }()

	if err := p.Spec.Validate(); err != nil {
		res.fail(StageConnect, KindUnknown, err)
		return res
	}

	if p.Spec.Transport == TransportStdio {
		p.probeStdio(ctx, res)
	} else {
		p.probeRemote(ctx, res)
	}
	return res
}

// probeRemote 走 L1 网络 → L2 协商 → L3 工具列表。
func (p *Prober) probeRemote(ctx context.Context, res *ProbeResult) {
	timeout := p.Spec.TimeoutOr()

	// L1：只做连接性探测，不发 MCP 报文。限额 10s——
	// 连都连不上的话，没必要等满整个请求超时。
	netTimeout := timeout
	if netTimeout > 10*time.Second {
		netTimeout = 10 * time.Second
	}
	netDur, kind, err := probeNetwork(ctx, p.Spec.URL, netTimeout)
	res.Latency.Network = netDur.Milliseconds()
	if err != nil {
		res.fail(StageNetwork, kind, err)
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rec := NewRecorder()
	// 探活 client 用完即弃。先于 c.Close 注册（defer LIFO），
	// 保证先关 client 再回收 Transport 空闲池，不留 90s 残连。
	defer rec.CloseIdleConnections()
	c, err := buildClient(p.Spec, rec, nil, nil)
	if err != nil {
		res.fail(StageConnect, KindUnknown, err)
		return
	}
	defer c.Close()

	// 无论成败，都把这次往返观测里与判决相关的部分带进结果：
	// 状态码、Retry-After 与响应体片段是密钥判决的输入，
	// 而它们只在传输层还看得到。
	defer func() { res.capture(rec.Last()) }()

	connectStart := time.Now()
	if err := c.Start(runCtx); err != nil {
		res.Latency.Connect = msSince(connectStart)
		res.fail(StageConnect, classify(err, rec.Last()), err)
		return
	}
	res.Latency.Connect = msSince(connectStart)

	p.negotiate(runCtx, c, rec, res, StageDiscover)
	if !res.OK && res.Kind != KindNone {
		return
	}

	p.listTools(runCtx, c, rec, res)
}

// probeStdio 走子进程启动 → L2 协商 → L3 工具列表。
//
// stdio 没有网络层，进程能否启动并完成协商就是可达性的全部含义。
func (p *Prober) probeStdio(ctx context.Context, res *ProbeResult) {
	timeout := p.Spec.TimeoutOr()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stderr := &stderrSink{}
	c, err := buildClient(p.Spec, nil, stderr, nil)
	if err != nil {
		res.fail(StageConnect, classifySpawn(err), err)
		return
	}
	defer c.Close()

	connectStart := time.Now()
	if err := c.Start(runCtx); err != nil {
		res.Latency.Connect = msSince(connectStart)
		res.fail(StageConnect, classifySpawn(err), err)
		res.Stderr = stderr.String()
		return
	}
	res.Latency.Connect = msSince(connectStart)

	p.negotiate(runCtx, c, nil, res, StageDiscover)
	if !res.OK && res.Kind != KindNone {
		res.Stderr = stderr.String()
		return
	}

	p.listTools(runCtx, c, nil, res)
	if !res.OK {
		res.Stderr = stderr.String()
	}
}

// negotiate 执行 L2：协议协商。
//
// mcp-go 的 Initialize 内部已实现双栈——先按新版发 server/discover，
// 失败则回退旧版 initialize 握手。网关只需读取协商结果。
func (p *Prober) negotiate(ctx context.Context, c *mcpclient.Client, rec *Recorder, res *ProbeResult, stage Stage) {
	started := time.Now()
	needed, err := negotiate(ctx, c, p.Spec)
	res.Latency.Discover = msSince(started)
	if err != nil {
		res.fail(stage, p.classify(err, rec), err)
		return
	}

	res.ProtocolVersion = needed.ProtocolVersion
	res.ServerName = needed.ServerName
	res.ServerVersion = needed.ServerVersion
	res.Instructions = needed.Instructions
	res.Legacy = needed.Legacy
	res.Capabilities = needed.Capabilities
}

// negotiate 完成协议协商并返回结果。探活与常驻连接共用这一条路径，
// 保证"测试通过的配置"与"实际调用时的配置"行为一致。
//
// 协商结果必须缓存（见 Conn），否则每次调用都要先试新版再降级旧版，
// 两次往返直接吃光延迟预算。
func negotiate(ctx context.Context, c *mcpclient.Client, spec Spec) (Negotiated, error) {
	init, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: spec.ProtocolVersion,
			ClientInfo:      mcp.Implementation{Name: ClientName, Version: ClientVersion},
			Capabilities:    mcp.ClientCapabilities{},
		},
	})
	if err != nil {
		return Negotiated{}, err
	}

	return Negotiated{
		ProtocolVersion: init.ProtocolVersion,
		ServerName:      init.ServerInfo.Name,
		ServerVersion:   init.ServerInfo.Version,
		Instructions:    init.Instructions,
		Capabilities:    capabilityNames(init.Capabilities),
		Legacy:          !mcp.IsModernProtocol(init.ProtocolVersion),
	}, nil
}

// listTools 执行 L3：拉取工具清单。
//
// 这也是健康探针的动作——新版协议已移除 ping，tools/list 是唯一
// 既确认存活又能顺带刷新缓存的调用。
func (p *Prober) listTools(ctx context.Context, c *mcpclient.Client, rec *Recorder, res *ProbeResult) {
	started := time.Now()
	listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	res.Latency.ListTools = msSince(started)
	if err != nil {
		kind := p.classify(err, rec)
		if kind == KindUnknown && len(res.Capabilities) > 0 && !hasCapability(res.Capabilities, "tools") {
			kind = KindCapabilityMissing
		}
		res.fail(StageListTools, kind, err)
		return
	}

	res.ToolCount = len(listed.Tools)
	res.Tools = make([]ToolInfo, 0, len(listed.Tools))
	for _, t := range listed.Tools {
		res.Tools = append(res.Tools, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: marshalSchema(t.InputSchema),
		})
	}
	res.OK = true
}

// marshalSchema 把工具入参 schema 序列化，失败时返回 nil（不影响探活结论）。
//
// 保真度边界：这里走的是"结构化往返"——mcp-go 客户端把下游的
// inputSchema 解析成 mcp.ToolInputSchema（$defs / type / properties /
// required / additionalProperties）后再由本函数序列化回去。该结构覆盖
// 绝大多数工具声明的入参形态，但顶层的 oneOf / anyOf / $ref / if-then
// 这类关键字不会保留（properties 内部的关键字是 map[string]any，不受影响）。
//
// 要做到逐字节透传，必须绕过 mcp-go 的客户端解析自己发原始 JSON-RPC。
// 那会换来约 200 行协议代码和一个长期维护负担，而收益只在罕见的
// 非对象形态入参上体现——当前取舍是接受这个边界，并在文档中写明。
func marshalSchema(s mcp.ToolInputSchema) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return b
}

// classify 按传输类型选择分类器。
//
// stdio 没有网络层与 HTTP 语义，失败原因来自进程生命周期，
// 因此不能复用网络分类器。
func (p *Prober) classify(err error, rec *Recorder) ErrorKind {
	if p.Spec.Transport == TransportStdio {
		return classifySpawn(err)
	}
	return classifyFor(err, rec)
}

// classifyFor 在 nil Recorder 下也能安全分类。
func classifyFor(err error, rec *Recorder) ErrorKind {
	if rec == nil {
		return classify(err, Observation{})
	}
	return classify(err, rec.Last())
}

// capabilityNames 把能力集压成便于展示的名称列表。
func capabilityNames(c mcp.ServerCapabilities) []string {
	names := make([]string, 0, 4)
	if c.Tools != nil {
		names = append(names, "tools")
	}
	if c.Resources != nil {
		names = append(names, "resources")
	}
	if c.Prompts != nil {
		names = append(names, "prompts")
	}
	if c.Logging != nil {
		names = append(names, "logging")
	}
	return names
}

func hasCapability(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func msSince(t time.Time) int64 {
	return time.Since(t).Milliseconds()
}
