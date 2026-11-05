package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/store/credentials"
)

// credentialView 是一把密钥的对外视图，明文一律不出现。
// 指纹只用于区分“是哪一把”，不用于验证密钥是否正确。
type credentialView struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	HeaderKeys  []string `json:"headerKeys,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Configured  bool     `json:"configured"`

	Enabled bool   `json:"enabled"`
	State   string `json:"state"` // active | cooling | disabled

	DisabledKind string `json:"disabledKind,omitempty"`
	DisabledAt   int64  `json:"disabledAt,omitempty"`
	DisabledNote string `json:"disabledNote,omitempty"`

	CoolingUntil       int64 `json:"coolingUntil,omitempty"`
	CoolingForMS       int64 `json:"coolingForMs,omitempty"`
	ConsecutiveLimited int   `json:"consecutiveLimited,omitempty"`

	RPMLimit int `json:"rpmLimit"`

	RPMUsed   int   `json:"rpmUsed"`
	WindowEnd int64 `json:"windowEnd,omitempty"`

	Picks      uint64 `json:"picks"`
	LastUsedAt int64  `json:"lastUsedAt,omitempty"`
	LastError  string `json:"lastError,omitempty"`

	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// credentialRequest 是新增 / 编辑一把密钥的请求体：可直接给 headers，
// 也可给 value + authScheme 由服务端展开。
type credentialRequest struct {
	Label   string            `json:"label"`
	Headers map[string]string `json:"headers"`

	Value      string `json:"value"`
	AuthScheme string `json:"authScheme"` // bearer | x-api-key | header
	HeaderName string `json:"headerName"` // authScheme=header 时的头名

	RPMLimit int `json:"rpmLimit"`
}

// credentialImportRequest 是批量粘贴的请求体。
type credentialImportRequest struct {
	AuthScheme string `json:"authScheme"`
	HeaderName string `json:"headerName"`

	// Lines 一行一把，格式 "名称=值" 或直接 "值"。
	Lines []string `json:"lines"`

	RPMLimit int `json:"rpmLimit"`
}

func (a *API) handleListCredentials(c *gin.Context) {
	serverID := c.Param("id")

	creds, statuses, err := a.svc.Credentials(c.Request.Context(), serverID)
	if err != nil {
		failFor(c, err)
		return
	}

	byID := make(map[string]downstream.KeyStatus, len(statuses))
	for _, st := range statuses {
		byID[st.ID] = st
	}

	items := make([]credentialView, 0, len(creds))
	for _, c := range creds {
		items = append(items, toCredentialView(c, byID[c.ID]))
	}

	// keyPool 为空表示该 server 退回单密钥模式（沿用 servers.headers）。
	c.JSON(http.StatusOK, map[string]any{
		"total":   len(items),
		"items":   items,
		"keyPool": len(items) > 0,
	})
}

func (a *API) handleCreateCredential(c *gin.Context) {
	serverID := c.Param("id")

	var req credentialRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}

	headers, err := req.resolveHeaders(nil)
	if err != nil {
		failFor(c, err)
		return
	}
	if len(headers) == 0 {
		abortErr(c, http.StatusBadRequest, "invalid_request",
			"缺少凭据：请提供 headers，或提供 value 与 authScheme 由服务端展开")
		return
	}

	created, err := a.svc.AddCredential(c.Request.Context(), serverID, hub.CredentialSpec{
		Label:    req.Label,
		Headers:  headers,
		RPMLimit: req.RPMLimit,
	})
	if err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusCreated, a.credentialByID(c.Request.Context(), created.ServerID, created.ID))
}

func (a *API) handleUpdateCredential(c *gin.Context) {
	serverID, credID := c.Param("id"), c.Param("cid")

	var req credentialRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}

	// 编辑时总是读出旧凭据：它同时服务“headers 整体省略”与“回显的 *** 原样提交”；
	// 只在 headers 缺省时才读，会让后一种把凭据落成字面量 "***"。
	prev, err := a.svc.CredentialHeaders(c.Request.Context(), serverID, credID)
	if err != nil {
		failFor(c, err)
		return
	}

	headers, err := req.resolveHeaders(prev)
	if err != nil {
		failFor(c, err)
		return
	}

	if _, err := a.svc.UpdateCredential(c.Request.Context(), serverID, credID, hub.CredentialSpec{
		Label:    req.Label,
		Headers:  headers,
		RPMLimit: req.RPMLimit,
	}); err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, a.credentialByID(c.Request.Context(), serverID, credID))
}

func (a *API) handleDeleteCredential(c *gin.Context) {
	if err := a.svc.DeleteCredential(c.Request.Context(), c.Param("id"), c.Param("cid")); err != nil {
		failFor(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) handleEnableCredential(c *gin.Context) {
	a.setCredentialEnabled(c, true)
}

func (a *API) handleDisableCredential(c *gin.Context) {
	a.setCredentialEnabled(c, false)
}

func (a *API) setCredentialEnabled(c *gin.Context, enabled bool) {
	serverID, credID := c.Param("id"), c.Param("cid")
	if err := a.svc.SetCredentialEnabled(c.Request.Context(), serverID, credID, enabled); err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, a.credentialByID(c.Request.Context(), serverID, credID))
}

func (a *API) handleTestCredential(c *gin.Context) {
	serverID, credID := c.Param("id"), c.Param("cid")

	sv, err := a.svc.Store().Servers.Get(c.Request.Context(), serverID)
	if err != nil {
		failFor(c, err)
		return
	}

	res, verdict, err := a.svc.TestCredential(c.Request.Context(), serverID, credID)
	if err != nil {
		failFor(c, err)
		return
	}

	// verdict 是给界面看的“建议动作”；测试本身只读，不改密钥状态。
	c.JSON(http.StatusOK, map[string]any{
		"probe":   newProbeResponse(res, sv.Spec.Key),
		"verdict": verdict,
		"hint":    verdict.Human(),
	})
}

func (a *API) handleResetCredentialQuota(c *gin.Context) {
	serverID, credID := c.Param("id"), c.Param("cid")
	if err := a.svc.ResetCredentialQuota(c.Request.Context(), serverID, credID); err != nil {
		failFor(c, err)
		return
	}
	c.JSON(http.StatusOK, a.credentialByID(c.Request.Context(), serverID, credID))
}

func (a *API) handleImportCredentials(c *gin.Context) {
	serverID := c.Param("id")

	var req credentialImportRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}

	specs, err := parseCredentialLines(req.Lines, req.AuthScheme, req.HeaderName)
	if err != nil {
		failFor(c, err)
		return
	}
	if len(specs) == 0 {
		abortErr(c, http.StatusBadRequest, "invalid_request", "没有可导入的行")
		return
	}
	for i := range specs {
		specs[i].RPMLimit = req.RPMLimit
	}

	created, err := a.svc.ImportCredentials(c.Request.Context(), serverID, specs)
	if err != nil {
		failFor(c, err)
		return
	}

	// 回显每一把的 label 与指纹便于核对；不回显值，避免再次暴露刚粘贴的内容。
	out := make([]map[string]any, 0, len(created))
	for _, c := range created {
		out = append(out, map[string]any{
			"id":          c.ID,
			"label":       c.Label,
			"fingerprint": downstream.KeyFingerprint(c.Headers),
		})
	}
	c.JSON(http.StatusCreated, map[string]any{
		"created": len(out),
		"items":   out,
	})
}

// credentialByID 组装单把密钥的视图，出错时返回只带 id 的骨架，
// 避免“刚写完就查不出来”导致整个请求失败。
func (a *API) credentialByID(ctx context.Context, serverID, credID string) credentialView {
	creds, statuses, err := a.svc.Credentials(ctx, serverID)
	if err != nil {
		return credentialView{ID: credID}
	}
	var st downstream.KeyStatus
	for _, s := range statuses {
		if s.ID == credID {
			st = s
			break
		}
	}
	for _, c := range creds {
		if c.ID == credID {
			return toCredentialView(c, st)
		}
	}
	return credentialView{ID: credID}
}

func toCredentialView(c credentials.Credential, st downstream.KeyStatus) credentialView {
	out := credentialView{
		ID:          c.ID,
		Label:       c.LabelOrFallback(),
		HeaderKeys:  sortedKeys(c.Headers),
		Fingerprint: downstream.KeyFingerprint(c.Headers),
		Configured:  len(c.Headers) > 0,
		Enabled:     c.Enabled,
		State:       string(downstream.KeyActive),
		RPMLimit:    c.RPMLimit,
		CreatedAt:   c.CreatedAt.UnixMilli(),
		UpdatedAt:   c.UpdatedAt.UnixMilli(),
	}

	// 运行时状态来自密钥池；拿不到时落到持久状态，避免显示一切正常的空壳。
	if st.ID == "" {
		out.State = string(c.State)
		out.DisabledKind = string(c.DisabledKind)
		if !c.DisabledAt.IsZero() {
			out.DisabledAt = c.DisabledAt.UnixMilli()
		}
		out.DisabledNote = c.DisabledNote
		return out
	}

	out.State = string(st.State)
	if st.DisabledKind != "" {
		out.DisabledKind = string(st.DisabledKind)
	}
	if !st.DisabledAt.IsZero() {
		out.DisabledAt = st.DisabledAt.UnixMilli()
	}
	out.DisabledNote = st.DisabledNote
	if !st.CoolingUntil.IsZero() {
		out.CoolingUntil = st.CoolingUntil.UnixMilli()
		out.CoolingForMS = st.CoolingFor.Milliseconds()
	}
	out.ConsecutiveLimited = st.ConsecutiveLimited
	out.RPMUsed = st.RPMUsed
	out.Picks = st.Picks
	out.LastError = st.LastError
	if !st.WindowEnd.IsZero() {
		out.WindowEnd = st.WindowEnd.UnixMilli()
	}
	if !st.LastUsedAt.IsZero() {
		out.LastUsedAt = st.LastUsedAt.UnixMilli()
	}
	return out
}

// resolveHeaders 把请求折算成一组完整注入头。
// prev 非空表示“这次不动凭据”，用于编辑时不带 headers 的场景。
func (req credentialRequest) resolveHeaders(prev map[string]string) (map[string]string, error) {
	if len(req.Headers) > 0 {
		// 回显值是 ***；原样提交表示沿用原值，否则会把凭据抹成五个星号。
		return mergeSecrets(req.Headers, prev), nil
	}
	value := strings.TrimSpace(req.Value)
	if value == "" {
		return prev, nil
	}
	if value == secretMask {
		return prev, nil
	}
	expanded, err := expandCredential(req.AuthScheme, req.HeaderName, value)
	if err != nil {
		return nil, err
	}
	// 短途径是"换掉鉴权头"，不是"重写整组头"。
	return mergeAuthHeaders(expanded, prev), nil
}

// expandCredential 把一个凭据值展开成完整注入头。
func expandCredential(scheme, headerName, value string) (map[string]string, error) {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "", "bearer", "authorization":
		// 用户粘贴的值常自带 "Bearer " 前缀，不要重复添加。
		if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
			value = "Bearer " + value
		}
		return map[string]string{"Authorization": value}, nil

	case "x-api-key", "apikey", "api-key":
		return map[string]string{"X-API-Key": value}, nil

	case "header", "custom":
		name := strings.TrimSpace(headerName)
		if name == "" {
			return nil, badRequest("authScheme=header 时必须提供 headerName")
		}
		return map[string]string{name: value}, nil
	}
	return nil, badRequest("不支持的凭据类型 %q（可选：bearer / x-api-key / header）", scheme)
}

// mergeAuthHeaders 把展开出的鉴权头并入旧配置：value 短途径只换鉴权头，
// 不能整体覆盖，否则会丢掉供应商的附加头（租户标识、地域、版本）。
// 头名大小写不敏感，需先摘掉同名旧键；除鉴权头外一个键都不动。
func mergeAuthHeaders(expanded, prev map[string]string) map[string]string {
	out := make(map[string]string, len(prev)+len(expanded))
	for k, v := range prev {
		out[k] = v
	}
	for k, v := range expanded {
		for old := range out {
			if old != k && strings.EqualFold(old, k) {
				delete(out, old)
			}
		}
		out[k] = v
	}
	return out
}

// parseCredentialLines 把“一行一把”的粘贴内容展开成密钥列表。
// 行格式为 `名称=值` 或直接 `值`；也接受制表符与中英文逗号作为分隔。
func parseCredentialLines(lines []string, scheme, headerName string) ([]hub.CredentialSpec, error) {
	out := make([]hub.CredentialSpec, 0, len(lines))
	used := make(map[string]int, len(lines))

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		label, value := splitCredentialLine(line)
		if value == "" {
			return nil, badRequest("这一行没有凭据值: %q", line)
		}

		headers, err := expandCredential(scheme, headerName, value)
		if err != nil {
			return nil, err
		}
		if label == "" {
			label = "密钥 " + strconv.Itoa(len(out)+1)
		}
		// 重名会让界面与日志无法区分；自动加序号后缀，而不是让导入失败。
		if n, dup := used[label]; dup {
			used[label] = n + 1
			label = label + " (" + strconv.Itoa(n+1) + ")"
		} else {
			used[label] = 1
		}

		out = append(out, hub.CredentialSpec{Label: label, Headers: headers})
	}
	return out, nil
}

// splitCredentialLine 把一行拆成名称与值；没有名称时返回空名称。
// "=" 仅在其后还有非 "=" 内容时才算分隔符，避免把 base64 填充值拆坏；
// "名称=" 这类残缺行仍按缺值报错。
func splitCredentialLine(line string) (label, value string) {
	if idx := strings.Index(line, "="); idx >= 0 {
		rest := strings.TrimSpace(line[idx+1:])
		if rest == "" {
			return strings.TrimSpace(line[:idx]), ""
		}
		if strings.Trim(rest, "=") != "" {
			return strings.TrimSpace(line[:idx]), rest
		}
		// rest 是一段纯 "=" 填充：这个等号属于值本身，整行当值。
		return "", line
	}
	for _, sep := range []string{"\t", "，", ","} {
		if idx := strings.Index(line, sep); idx >= 0 {
			return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+len(sep):])
		}
	}
	return "", line
}
