package hub

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/credentials"
	"github.com/hydra/mcp-hub/internal/store/servers"
	"github.com/hydra/mcp-hub/internal/store/storeutil"
	"go.uber.org/zap"
)

// CredentialSpec 是新增或编辑一把密钥的输入。
// headers 是完整的一组注入头而不是单个头字段：供应商凭据形态各异
// （Bearer / X-API-Key / 多字段签名），只有整组才是一次可独立轮换的单位。
type CredentialSpec struct {
	Label    string
	Headers  map[string]string
	RPMLimit int
}

// Credentials 返回某个 server 的密钥配置与运行时状态。
func (s *Service) Credentials(ctx context.Context, serverID string) ([]credentials.Credential, []downstream.KeyStatus, error) {
	if _, err := s.store.Servers.Get(ctx, serverID); err != nil {
		return nil, nil, err
	}
	list, err := s.store.Credentials.List(ctx, serverID)
	if err != nil {
		return nil, nil, err
	}
	return list, s.pool.KeyStatusesOf(serverID), nil
}

// AllKeyStatuses 返回全部 server 的密钥运行状态，键为 serverId。
func (s *Service) AllKeyStatuses() map[string][]downstream.KeyStatus {
	return s.pool.KeyStatuses()
}

// AddCredential 新增一把密钥并让它立即参与轮换。
// 保存前做同值检测（O3）：两把请求头完全相同的密钥不增加任何配额，
// 只会白占轮换名额——在保存时拦下，而不是等用户事后对着界面排查。
func (s *Service) AddCredential(ctx context.Context, serverID string, in CredentialSpec) (credentials.Credential, error) {
	sv, err := s.requireKeyPoolSupport(ctx, serverID)
	if err != nil {
		return credentials.Credential{}, err
	}
	if err := s.rejectDuplicateHeaders(ctx, serverID, "", in.Headers, in.Label); err != nil {
		return credentials.Credential{}, err
	}
	c, err := s.store.Credentials.Insert(ctx, credentials.Credential{
		ServerID: serverID,
		Label:    strings.TrimSpace(in.Label),
		Headers:  in.Headers,
		Enabled:  true,
		RPMLimit: max(in.RPMLimit, 0),
	})
	if err != nil {
		return credentials.Credential{}, err
	}
	s.log.Info("密钥已添加",
		zap.String("server", sv.Spec.Key),
		zap.String("credential", c.LabelOrFallback()),
		zap.String("id", c.ID))
	if err := s.reloadKeyRing(ctx, serverID); err != nil {
		return c, err
	}
	// O2：保存后自动试测一次（只读探活）。结果只记日志不改状态——
	// 失败可能只是下游在抖，"禁用"的判决留给在线结算。
	s.asyncTestCredential(sv.Spec, c)
	return c, nil
}

// ImportCredentials 批量追加密钥。
func (s *Service) ImportCredentials(ctx context.Context, serverID string, in []CredentialSpec) ([]credentials.Credential, error) {
	sv, err := s.requireKeyPoolSupport(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if len(in) == 0 {
		return nil, nil
	}

	// O3：批量导入同样要拦同值密钥——批量粘贴是"同一个 Key 粘两遍"的
	// 高发场景。对比范围：本 server 存量、其他 server 存量（K7）、本批次内部。
	existing, err := s.store.Credentials.List(ctx, serverID)
	if err != nil {
		return nil, err
	}
	seen := make([]map[string]string, 0, len(existing)+len(in))
	for _, c := range existing {
		if len(c.Headers) > 0 {
			seen = append(seen, canonicalHeaders(c.Headers))
		}
	}
	all, err := s.store.Credentials.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, others := range all {
		for _, c := range others {
			if c.ServerID != serverID && len(c.Headers) > 0 {
				seen = append(seen, canonicalHeaders(c.Headers))
			}
		}
	}
	for _, item := range in {
		if len(item.Headers) == 0 {
			continue
		}
		cand := canonicalHeaders(item.Headers)
		for _, prev := range seen {
			if maps.Equal(cand, prev) {
				return nil, fmt.Errorf("%w：%q 与库中（含其他 server）或本批次内另一把密钥的请求头完全相同，请直接复用或删除重复项", ErrDuplicateCredential, item.Label)
			}
		}
		seen = append(seen, cand)
	}

	creds := make([]credentials.Credential, 0, len(in))
	for _, item := range in {
		creds = append(creds, credentials.Credential{
			Label:    strings.TrimSpace(item.Label),
			Headers:  item.Headers,
			Enabled:  true,
			RPMLimit: max(item.RPMLimit, 0),
		})
	}

	out, err := s.store.Credentials.InsertBatch(ctx, serverID, creds)
	if err != nil {
		return nil, err
	}
	s.log.Info("密钥已批量导入",
		zap.String("server", sv.Spec.Key), zap.Int("count", len(out)))
	if err := s.reloadKeyRing(ctx, serverID); err != nil {
		return out, err
	}
	// O2：批量导入逐把自动试测（串行，避免对下游形成突发）。
	for _, c := range out {
		s.asyncTestCredential(sv.Spec, c)
	}
	return out, nil
}

// UpdateCredential 编辑一把密钥。
// 刻意不允许改状态：状态是判决结果，改配置不该顺手复位它，否则
// "这把密钥为什么被禁"的线索会凭空消失。重新启用请走 SetCredentialEnabled。
func (s *Service) UpdateCredential(ctx context.Context, serverID, credID string, in CredentialSpec) (credentials.Credential, error) {
	sv, err := s.requireKeyPoolSupport(ctx, serverID)
	if err != nil {
		return credentials.Credential{}, err
	}
	existing, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return credentials.Credential{}, err
	}

	existing.Label = strings.TrimSpace(in.Label)
	if in.Headers != nil {
		existing.Headers = in.Headers
	}
	existing.RPMLimit = max(in.RPMLimit, 0)

	// O3：编辑同样不放过同值——把 A 改成和 B 一样，等于绕过新增时的检测。
	if err := s.rejectDuplicateHeaders(ctx, serverID, credID, existing.Headers, existing.Label); err != nil {
		return credentials.Credential{}, err
	}
	if err := s.store.Credentials.Update(ctx, existing); err != nil {
		return credentials.Credential{}, err
	}
	if err := s.reloadKeyRing(ctx, serverID); err != nil {
		return existing, err
	}
	saved, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return existing, err
	}
	s.log.Info("密钥已更新",
		zap.String("server", sv.Spec.Key),
		zap.String("credential", saved.LabelOrFallback()),
		zap.String("id", credID))
	return saved, nil
}

// DeleteCredential 删除一把密钥。
func (s *Service) DeleteCredential(ctx context.Context, serverID, credID string) error {
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return err
	}
	if err := s.store.Credentials.Delete(ctx, credID); err != nil {
		return err
	}
	s.log.Info("密钥已删除",
		zap.String("server", s.serverKey(ctx, serverID)),
		zap.String("credential", cred.LabelOrFallback()),
		zap.String("id", credID))
	return s.reloadKeyRing(ctx, serverID)
}

// SetCredentialEnabled 人工启停一把密钥。
// 启用会顺带清掉冷却：用户点"启用"就是要现在用它，残留的冷却会让
// 这个动作静默失效（点完还是不用，而两个按钮长得一模一样）。
func (s *Service) SetCredentialEnabled(ctx context.Context, serverID, credID string, enabled bool) error {
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return err
	}
	if err := s.store.Credentials.SetEnabled(ctx, credID, enabled); err != nil {
		return err
	}
	if enabled {
		if ring := s.pool.KeyRing(serverID); ring != nil {
			ring.ClearCooling(credID)
		}
	}
	action := "server 密钥已停用"
	if enabled {
		action = "server 密钥已启用"
	}
	s.log.Info(action,
		zap.String("server", s.serverKey(ctx, serverID)),
		zap.String("credential", cred.LabelOrFallback()),
		zap.String("id", credID))
	return s.reloadKeyRing(ctx, serverID)
}

// ResetCredentialQuota 清零一把密钥的本地配额计数。
// 只清计数不动冷却：本地计数是网关的保守估计，用户更清楚真实配额；
// 冷却是下游给的客观信号，清掉等于骗自己。
func (s *Service) ResetCredentialQuota(ctx context.Context, serverID, credID string) error {
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return err
	}
	ring := s.pool.KeyRing(serverID)
	if ring == nil || !ring.ResetQuota(credID) {
		return storeutil.ErrNotFound
	}
	s.log.Info("密钥配额计数已清零",
		zap.String("server", s.serverKey(ctx, serverID)),
		zap.String("credential", cred.LabelOrFallback()),
		zap.String("id", credID))
	return nil
}

// TestCredential 用单把密钥做一次探活。
//
// 只读：不改密钥状态、不动工具缓存，只回答"这个配置现在能用吗"。
// 刻意不按判决弃用密钥——连续次数（如 403 的两次阈值）是状态机的一部分，
// 绕过它单独禁用会让手工测试与在线调用的结论互相矛盾。
func (s *Service) TestCredential(ctx context.Context, serverID, credID string) (*downstream.ProbeResult, downstream.KeyVerdict, error) {
	sv, err := s.store.Servers.Get(ctx, serverID)
	if err != nil {
		return nil, "", err
	}
	cred, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return nil, "", err
	}

	spec := sv.Spec
	spec.Headers = cred.Headers
	res := downstream.NewProber(spec).Probe(ctx)

	verdict := downstream.VerdictOK
	if !res.OK {
		verdict = downstream.VerdictOf(res.Kind, res.Observation())
	}
	return res, verdict, nil
}

// KeySummary 是密钥池的整体状况，供概览展示。
// 三种"用不上"的原因分开计数：合并成一个数字会丢掉用户唯一需要的线索
// ——冷却会自己好、弃用要换密钥、人工停用要人改主意，动作完全不同。
type KeySummary struct {
	Total int `json:"total"`
	// Servers 是配了密钥池的 server 数（未配的走单密钥模式，不在统计内）。
	Servers int `json:"servers"`

	Usable   int `json:"usable"`
	Cooling  int `json:"cooling"`
	Disabled int `json:"disabled"`

	// Unusable 是 Cooling + Disabled 的合计，直接给前端用：
	// 让它在两处分别相加，早晚会有一处漏掉。
	Unusable int `json:"unusable"`
}

// KeySummary 汇总全部 server 的密钥状态。
func (s *Service) KeySummary(ctx context.Context) (KeySummary, error) {
	all, err := s.store.Credentials.ListAll(ctx)
	if err != nil {
		return KeySummary{}, err
	}
	live := s.pool.KeyStatuses()

	var out KeySummary
	for serverID, creds := range all {
		if len(creds) == 0 {
			continue
		}
		out.Servers++

		byID := make(map[string]downstream.KeyStatus, len(live[serverID]))
		for _, st := range live[serverID] {
			byID[st.ID] = st
		}

		for _, c := range creds {
			out.Total++
			st, ok := byID[c.ID]
			if !ok {
				// 池子还没装载（比如 server 停用中）：退到持久状态。
				// 宁可显示旧的结论，也不要显示一个看起来正常的空壳。
				if c.Usable() {
					out.Usable++
				} else {
					out.Disabled++
				}
				continue
			}
			switch st.State {
			case downstream.KeyCooling:
				out.Cooling++
			case downstream.KeyDisabled:
				out.Disabled++
			default:
				out.Usable++
			}
		}
	}
	out.Unusable = out.Cooling + out.Disabled
	return out, nil
}

// ActiveKeyCounts 返回每个 server 当前可参与轮换的密钥数。
func (s *Service) ActiveKeyCounts() map[string]int {
	p := s.pool.KeyStatuses()
	out := make(map[string]int, len(p))
	for serverID, list := range p {
		n := 0
		for _, k := range list {
			if k.State == downstream.KeyActive {
				n++
			}
		}
		out[serverID] = n
	}
	return out
}

// CredentialHeaders 返回一把密钥的完整注入头。
// 只给内部用（编辑时把回显的 *** 换回真值）：明文绝不出现在任何对外
// 响应里，调用点必须留在管理 API 的入参处理那一步。
func (s *Service) CredentialHeaders(ctx context.Context, serverID, credID string) (map[string]string, error) {
	c, err := s.credentialOf(ctx, serverID, credID)
	if err != nil {
		return nil, err
	}
	return c.Headers, nil
}

func (s *Service) requireKeyPoolSupport(ctx context.Context, serverID string) (servers.Server, error) {
	sv, err := s.store.Servers.Get(ctx, serverID)
	if err != nil {
		return servers.Server{}, err
	}
	if !sv.Spec.IsRemote() {
		return servers.Server{}, fmt.Errorf(
			"%w：stdio 的凭据在子进程的环境变量里，改它需要重启进程，无法按调用轮换", ErrKeyPoolUnsupported)
	}
	return sv, nil
}

// credentialOf 读取一把密钥并确认它确实属于该 server。
// 归属校验不能省：密钥 id 全局唯一，漏掉就意味着拿着 A 的密钥 id
// 能改到 B 的配置上。
func (s *Service) credentialOf(ctx context.Context, serverID, credID string) (credentials.Credential, error) {
	c, err := s.store.Credentials.Get(ctx, credID)
	if err != nil {
		return credentials.Credential{}, err
	}
	if c.ServerID != serverID {
		return credentials.Credential{}, storeutil.ErrNotFound
	}
	return c, nil
}

// --- 内部 ---

// rejectDuplicateHeaders 在保存前检测同值密钥（O3 / K6 / K7）。
// excludeID 是编辑场景里要跳过的自己（新增时传空）。头名按 HTTP 惯例
// 不区分大小写，头值精确相等。incomingLabel 只用于错误文案。
// 两层拦截：同 server 内同值（白占轮换名额），以及跨 server 同值
// （把限流压力集中到同一个上游身份，用量归属随之搅浑）。
func (s *Service) rejectDuplicateHeaders(ctx context.Context, serverID, excludeID string, headers map[string]string, incomingLabel string) error {
	if len(headers) == 0 {
		return nil
	}
	list, err := s.store.Credentials.List(ctx, serverID)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.ID == excludeID {
			continue
		}
		if sameHeaders(headers, c.Headers) {
			return fmt.Errorf("%w：%q 与现有密钥 %q（id %s）的请求头完全相同。同一份凭据不增加配额，只会白占轮换名额，请直接复用或先删除旧密钥",
				ErrDuplicateCredential, incomingLabel, c.LabelOrFallback(), c.ID)
		}
	}
	all, err := s.store.Credentials.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, others := range all {
		for _, c := range others {
			if c.ServerID == serverID || c.ID == excludeID {
				continue
			}
			if sameHeaders(headers, c.Headers) {
				return fmt.Errorf("%w：%q 已被其他 server 上的密钥 %q（id %s）使用。同一份凭据不允许配到两个 server——限流压力会集中到同一个上游身份",
					ErrDuplicateCredential, incomingLabel, c.LabelOrFallback(), c.ID)
			}
		}
	}
	return nil
}

// canonicalHeaders 把头名统一为小写，供等价比较。
func canonicalHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[strings.ToLower(k)] = v
	}
	return out
}

// sameHeaders 报告两组注入头是否等价（头名不分大小写，头值精确相等）。
func sameHeaders(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	return maps.Equal(canonicalHeaders(a), canonicalHeaders(b))
}

// credentialTestTimeout 是自动试测的单钥超时。
const credentialTestTimeout = 30 * time.Second

// asyncTestCredential 在后台对一把密钥做一次只读探活（O2）。
// 只记日志，不改任何状态：试测失败可能只是下游在抖，禁用判决必须
// 留给在线结算（连续次数是状态机的一部分，旁路测试不参与）。
func (s *Service) asyncTestCredential(spec downstream.Spec, c credentials.Credential) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), credentialTestTimeout)
		defer cancel()

		probeSpec := spec
		probeSpec.Headers = c.Headers
		res := downstream.NewProber(probeSpec).Probe(ctx)

		if res.OK {
			s.log.Info("新密钥试测通过",
				zap.String("server", spec.Key),
				zap.String("credential", c.LabelOrFallback()),
				zap.Int("toolCount", res.ToolCount),
				zap.Int64("ms", res.Latency.Total))
			return
		}
		s.log.Warn("新密钥试测未通过（已照常入池，可在管理界面复核）",
			zap.String("server", spec.Key),
			zap.String("credential", c.LabelOrFallback()),
			zap.String("kind", string(res.Kind)),
			zap.String("stage", string(res.Stage)),
			zap.String("detail", res.Detail))
	}()
}
