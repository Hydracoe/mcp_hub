package pool

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub/callctx"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// Get 取出（必要时建立）某个 server 的连接；处于退避窗口内时
// 快速失败，不发任何网络请求。
func (p *Pool) Get(ctx context.Context, spec downstream.Spec) (*downstream.Conn, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	fp := spec.ConnFingerprint()

	p.mu.Lock()
	if f, ok := p.fail[spec.ID]; ok && p.now().Before(f.until) {
		remaining := f.until.Sub(p.now())
		p.mu.Unlock()
		return nil, &UnavailableError{
			ServerKey:  spec.Key,
			Kind:       f.kind,
			Detail:     f.detail,
			RetryAfter: remaining,
		}
	}

	// 池里那条连接若按另一份配置建，就不能再用它发请求。
	// 正常路径上 Sync 已淘汰过，这里是兜底：绕过 Sync 的写路径不该让
	// 请求打到旧目标上——症状是一切正常，实际全发去了上一个环境。
	var evicted *downstream.Conn
	entry, ok := p.conns[spec.ID]
	if ok && entry.fingerprint != fp {
		evicted = entry.conn
		entry = nil
		delete(p.conns, spec.ID)
	}

	if entry == nil {
		conn, err := downstream.NewConn(spec)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		entry = &pooled{conn: conn, spec: spec, fingerprint: fp}
		p.conns[spec.ID] = entry
	}
	p.mu.Unlock()

	if evicted != nil {
		p.retireConn(spec.ID, spec.Key, evicted)
	}

	entry.dialMu.Lock()
	_, err := entry.conn.Dial(ctx)
	entry.dialMu.Unlock()

	if err != nil {
		kind := downstream.CallErrorKind(err)
		// 密钥级失败（401/403/429/402）不该让整个 server 退避：那是某组
		// 凭据的处境，换一把就过去了。让 server 退避会连累池子里其他
		// 健康的密钥，把"换一把即可"变成"整个下游不可用"。
		verdict := downstream.VerdictOf(kind, downstream.CallErrorObservation(err))
		if !verdict.Retryable() {
			p.recordFailure(spec.ID, kind, err.Error(), kind == downstream.KindTimeout)
			// 这次失败已经记过账了。标出来，外层拿到同一个错误时就不该再记
			// 一次——一次失败被当成两次，退避窗口会翻倍，可用密钥也会被过快淘汰。
			return nil, &countedFailure{err: err}
		}
		return nil, err
	}

	p.recordSuccess(spec.ID)
	return entry.conn, nil
}

// CallTool 通过连接池转发一次工具调用。
//
// 配了密钥池时走"选密钥 → 调用 → 按判决重试"的预算循环，否则只有一次
// 机会。超时与 5xx 不重试：MCP 工具调用没有标准幂等键，盲目重试可能让
// 有副作用的工具执行两次。
func (p *Pool) CallTool(
	ctx context.Context,
	spec downstream.Spec,
	toolName string,
	args json.RawMessage,
) (*mcp.CallToolResult, error) {
	started := time.Now()
	var (
		credentialID string
		attempts     int
	)
	res, err := runWithKeys(p, ctx, spec,
		func(callCtx context.Context, lease *downstream.KeyLease) (*mcp.CallToolResult, error) {
			if lease != nil {
				credentialID = lease.ID()
			}
			attempts++
			conn, err := p.Get(callCtx, spec)
			if err != nil {
				return nil, err
			}
			res, err := conn.CallTool(callCtx, toolName, args)
			if err != nil {
				return nil, err
			}
			return res, nil
		})

	// 每次调用一行日志：排障时最先要回答的是"哪个 server、哪个工具、
	// 哪把密钥、试了几次、多久"。
	fields := []zap.Field{
		zap.String("server", spec.Key),
		zap.String("tool", toolName),
		zap.Int("attempts", attempts),
		zap.Int64("ms", time.Since(started).Milliseconds()),
	}
	if credentialID != "" {
		fields = append(fields, zap.String("credential", credentialID))
	}
	if err != nil {
		fields = append(fields,
			zap.String("kind", string(downstream.CallErrorKind(err))),
			zap.Error(err))
		p.log.Warn("工具调用失败", fields...)
	} else {
		p.log.Info("工具调用完成", fields...)
	}
	return res, err
}

// Probe 对下游执行一次探活。
//
// 配了密钥池时走与调用相同的选钥与判决路径：探活也是真实请求，只用
// servers.headers 探活会让"只配了密钥池"的 server 每次都被判 401，
// 进而把整个 server 拖进退避，拦住池里完全健康的密钥。
func (p *Pool) Probe(ctx context.Context, spec downstream.Spec) *downstream.ProbeResult {
	ring := p.KeyRing(spec.ID)
	if ring == nil {
		return downstream.NewProber(spec).Probe(ctx)
	}

	budget := p.attemptsFor(ring)
	var (
		excluded map[string]bool
		last     *downstream.ProbeResult
	)
	for attempt := 0; attempt < budget; attempt++ {
		lease, err := ring.Acquire(excluded)
		if err != nil {
			if last != nil {
				return last
			}
			return probeUnavailable(err)
		}

		probeSpec := spec
		probeSpec.Headers = lease.Headers()
		res := downstream.NewProber(probeSpec).Probe(ctx)
		if res.OK {
			lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
			return res
		}

		last = res
		obs := res.Observation()
		verdict := downstream.VerdictOf(res.Kind, obs)
		p.emitKeyEvent(spec, lease, lease.Done(downstream.LeaseResult{Verdict: verdict, Obs: obs}))
		if !verdict.Retryable() {
			return res
		}
		if excluded == nil {
			excluded = make(map[string]bool, budget)
		}
		excluded[lease.ID()] = true
	}
	if last != nil {
		return last
	}
	return probeUnavailable(errors.New("探活未执行：密钥池无可用密钥"))
}

// probeUnavailable 把"借不到密钥"折成一次探活失败，保持调用方只处理一种返回。
func probeUnavailable(err error) *downstream.ProbeResult {
	return &downstream.ProbeResult{
		CheckedAt: time.Now(),
		Stage:     downstream.StageConnect,
		Kind:      downstream.KindNoUsableKey,
		Detail:    err.Error(),
	}
}

// ListTools 通过连接池拉取某个 server 的工具清单。
// 与 CallTool 共用同一条密钥选择与重试路径：探活也是真实调用，限流同样
// 会打到它，两条路径行为一致才不会出现"调用能过、刷工具却报 429"。
func (p *Pool) ListTools(ctx context.Context, spec downstream.Spec) ([]mcp.Tool, error) {
	tools, err := runWithKeys(p, ctx, spec,
		func(callCtx context.Context, _ *downstream.KeyLease) ([]mcp.Tool, error) {
			conn, err := p.Get(callCtx, spec)
			if err != nil {
				return nil, err
			}
			out, err := conn.ListTools(callCtx)
			if err != nil {
				return nil, err
			}
			return out, nil
		})
	if err != nil {
		return nil, err
	}
	return tools, nil
}

// runWithKeys 是密钥池下的统一执行路径。
// 写成包级泛型函数而不是方法：Go 的方法不能带类型参数，而这里的逻辑
// 对 tools/call 与 tools/list 完全相同。
func runWithKeys[T any](
	p *Pool,
	ctx context.Context,
	spec downstream.Spec,
	op func(ctx context.Context, lease *downstream.KeyLease) (T, error),
) (T, error) {
	var zero T

	ring := p.KeyRing(spec.ID)
	if ring == nil {
		// 单密钥模式：一次调用、一次机会。这是回归底线——
		// 只填一把密钥的用户不该感知到任何差别。
		res, err := op(ctx, nil)
		if err != nil {
			p.noteFailure(spec, err, downstream.VerdictNeutral)
			return zero, err
		}
		p.recordSuccess(spec.ID)
		return res, nil
	}

	budget := p.attemptsFor(ring)
	var (
		excluded map[string]bool
		lastErr  error
	)

	for attempt := 0; attempt < budget; attempt++ {
		lease, err := ring.Acquire(excluded)
		if err != nil {
			// 借不到密钥：池里的密钥要么本轮已失败、要么本就不可用。
			// 上抛前不带 exclude 重新探测——刚失败的几把此刻已是冷却或
			// 弃用状态，带 exclude 会把它们的计数算成 0。上抛的
			// ErrNoUsableKey 带 retryAfter 与各状态计数，并把刚才那次
			// 429/401 附为 Cause：这是密钥级事件唯一可见于调用方的出口。
			if noKey := ring.Unavailable(nil); noKey != nil {
				noKey.Cause = lastErr
				return zero, noKey
			}
			// 理论不可达：带 exclude 借不到，不带 exclude 更借不到；
			// 除非调用期间恰好写入了一把新可用密钥。返回最后一次失败。
			return zero, lastErr
		}

		callCtx := downstream.WithCredentialHeaders(ctx, lease.Headers())
		res, err := op(callCtx, lease)
		if err == nil {
			lease.Done(downstream.LeaseResult{Verdict: downstream.VerdictOK})
			p.recordSuccess(spec.ID)
			p.noteAttempt(ctx, lease.ID(), attempt+1)
			return res, nil
		}

		lastErr = err

		// 退避窗口内的失败没有真正发出请求：它对下游零压力，也不携带任何
		// 关于这把密钥的新信息。若照样结算并累加失败，窗口会被自己对调用
		// 的拒绝不断推后——只要调用不停就永远出不来（实测 46s 未自愈）。
		// 同时它会把"没发出去"误记成这把密钥的失败，进而把健康的钥推进冷却。
		if isUnavailable(err) {
			return zero, err
		}

		obs := downstream.CallErrorObservation(err)
		verdict := downstream.VerdictOf(downstream.CallErrorKind(err), obs)
		p.emitKeyEvent(spec, lease, lease.Done(downstream.LeaseResult{
			Verdict: verdict,
			Obs:     obs,
		}))
		p.noteAttempt(ctx, lease.ID(), attempt+1)

		if !verdict.Retryable() {
			p.noteFailure(spec, err, verdict)
			return zero, err
		}

		// 每次重试必须换密钥：同一把再打一次毫无意义，还会加重限流。
		if excluded == nil {
			excluded = make(map[string]bool, budget)
		}
		excluded[lease.ID()] = true
	}

	// 预算耗尽：池子已被这几轮失败打空（全冷却 / 全弃用），返回
	// ErrNoUsableKey。它带 retryAfter 与各状态计数，调用方才能告诉用户
	// "约 30 秒后重试"，而"RATE_LIMITED"只会让人干等着猜。
	//
	// 探测时不带 exclude：刚失败的几把已是冷却或弃用状态，排除在外只会
	// 让冷却计数变成 0——而那正是用户最想知道的。
	p.log.Warn("调用内密钥重试预算耗尽",
		zap.String("server", spec.Key), zap.Int("attempts", budget), zap.Error(lastErr))

	if noKey := ring.Unavailable(nil); noKey != nil {
		noKey.Cause = lastErr
		p.log.Warn("无可用密钥",
			zap.String("server", spec.Key),
			zap.Int("total", noKey.Total),
			zap.Int("cooling", noKey.Cooling),
			zap.Int("exhausted", noKey.Exhausted),
			zap.Int("disabled", noKey.Disabled),
			zap.Duration("retryAfter", noKey.RetryAfter),
			zap.Error(noKey.Cause))
		return zero, noKey
	}

	// 兜底：调用期间恰好写入了新密钥，预算已陈旧。返回最后一次失败。
	return zero, lastErr
}

// noteAttempt 把"这次用了哪把密钥、第几次尝试"交给调用方的日志。
func (p *Pool) noteAttempt(ctx context.Context, credentialID string, attempt int) {
	if meta := callctx.MetaFrom(ctx); meta != nil {
		meta.Record(credentialID, attempt)
	}
}

// isUnavailable 判断这次失败是不是"压根没发出去"——处于退避窗口内被快速
// 拒绝，一个字节都没到下游。
//
// 它必须被单独识别：这类失败既不代表下游又坏了（没有新的观测），也不该
// 由它继续延长那个把它拒掉的窗口。
func isUnavailable(err error) bool {
	var ue *UnavailableError
	return errors.As(err, &ue)
}

// noteFailure 记录一次与密钥无关的失败。
// 密钥级判决（限流、无效、额度耗尽）不进这里：那是某一把密钥的处境，
// 不该让整个 server 退避，否则会连累池子里其他健康的密钥。
func (p *Pool) noteFailure(spec downstream.Spec, err error, verdict downstream.KeyVerdict) {
	// 没发出去的请求不算失败：计入它会让退避窗口自己给自己续期。
	if isUnavailable(err) {
		return
	}
	if verdict.Retryable() {
		return
	}
	// 传输层断裂说明这条长连接已经死了（下游重启、LB 空闲回收），
	// 置为失效，下一次调用会重新握手。
	if isConnLost(err) {
		p.Drop(spec.ID)
		return
	}
	// 拨号失败已经在 Get 里记过账：同一次失败只能计一次。
	// 两条路径都会记的话，一次失败被算成两次，退避窗口翻倍、密钥淘汰翻倍。
	if alreadyCounted(err) {
		return
	}
	kind := downstream.CallErrorKind(err)
	p.recordFailure(spec.ID, kind, err.Error(), kind == downstream.KindTimeout)
}

// countedFailure 标记"这次失败已经记过账了"。
//
// 拨号失败由 Get 记账：那里同时握着错误分类与密钥判决，只有它能分清
// "这是连接级失败（该让 server 退避）"还是"这是密钥级失败（换一把就好）"。
// 同一个错误继续往上传到 runWithKeys 时，那里若再记一次，一次失败就被
// 当成两次——退避窗口翻倍，可用密钥被过快淘汰。
type countedFailure struct{ err error }

func (e *countedFailure) Error() string { return e.err.Error() }

// Unwrap 让 errors.Is / errors.As 能穿透到原始错误：
// 分类与观测都还在原始的 CallError 里，不能被这层标记吃掉。
func (e *countedFailure) Unwrap() error { return e.err }

// alreadyCounted 报告这次失败是否已经记过账。
func alreadyCounted(err error) bool {
	var cf *countedFailure
	return errors.As(err, &cf)
}
