package downstream

import (
	"fmt"
	"io"
	"net/http"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
)

// 网关作为 MCP 客户端时对外声明的身份。
const (
	ClientName    = "mcp-hub"
	ClientVersion = "0.1.0"
)

// buildClient 按传输类型构造下游 client，但不启动。
//
// 这里刻意绕开 client.NewStreamableHttpClient 这类便捷构造函数：
// 它们不接受 ClientOption，无法注入协议版本偏好。改为自行组合
// transport + NewClient，把选项控制权留在网关手里。
//
// 启动统一由探活流程负责，以便套上超时。
//
// hdrFn 非空时，远程传输改为"每个请求现场取凭据"，而不是构造时
// 写死一份。这是密钥轮换能做到调用方无感的前提：轮换只需换掉
// 那次回调的返回值，连接本身不用重建，也就不存在"重建打断在飞调用"
// 的问题。探活路径每次新建 client，传 nil 走静态 headers 即可。
func buildClient(spec Spec, rec *Recorder, stderr io.Writer, hdrFn transport.HTTPHeaderFunc) (*mcpclient.Client, error) {
	clientOpts := make([]mcpclient.ClientOption, 0, 2)
	if spec.ProtocolVersion != "" {
		clientOpts = append(clientOpts, mcpclient.WithProtocolVersion(spec.ProtocolVersion))
	}
	if spec.LegacyOnly {
		clientOpts = append(clientOpts, mcpclient.WithLegacyProtocolOnly())
	}

	switch spec.Transport {
	case TransportStreamableHTTP:
		httpOpts := []transport.StreamableHTTPCOption{
			transport.WithHTTPBasicClient(&http.Client{Transport: rec}),
			transport.WithHTTPTimeout(spec.TimeoutOr()),
		}
		switch {
		case hdrFn != nil:
			httpOpts = append(httpOpts, transport.WithHTTPHeaderFunc(hdrFn))
		case len(spec.Headers) > 0:
			httpOpts = append(httpOpts, transport.WithHTTPHeaders(spec.Headers))
		}
		trans, err := transport.NewStreamableHTTP(spec.URL, httpOpts...)
		if err != nil {
			return nil, err
		}
		return mcpclient.NewClient(trans, clientOpts...), nil

	case TransportSSE:
		sseOpts := []transport.ClientOption{
			transport.WithHTTPClient(&http.Client{Transport: rec}),
			transport.WithResponseTimeout(spec.TimeoutOr()),
		}
		switch {
		case hdrFn != nil:
			sseOpts = append(sseOpts, transport.WithHeaderFunc(hdrFn))
		case len(spec.Headers) > 0:
			sseOpts = append(sseOpts, transport.WithHeaders(spec.Headers))
		}
		trans, err := transport.NewSSE(spec.URL, sseOpts...)
		if err != nil {
			return nil, err
		}
		return mcpclient.NewClient(trans, clientOpts...), nil

	case TransportStdio:
		stdioOpts := make([]transport.StdioOption, 0, 1)
		if stderr != nil {
			stdioOpts = append(stdioOpts, transport.WithCommandStderrWriter(stderr))
		}
		trans := transport.NewStdioWithOptions(spec.Command, spec.Env, spec.Args, stdioOpts...)
		return mcpclient.NewClient(trans, clientOpts...), nil
	}

	return nil, fmt.Errorf("unsupported transport %q", spec.Transport)
}
