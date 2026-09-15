package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hydra/mcp-hub/internal/hub"
	"github.com/hydra/mcp-hub/internal/search"
	"go.uber.org/zap"
)

// embedConfigResponse 是嵌入配置的完整视图。
//
// KeyConfigured 与 Enabled 分开说：配置了模型但没给 API key 时，
// 界面要提示"去配 key"，而不是假装功能可用。
type embedConfigResponse struct {
	Model         string              `json:"model"`
	Dimensions    int                 `json:"dimensions"`
	BaseURL       string              `json:"baseUrl"`
	RerankModel   string              `json:"rerankModel,omitempty"`
	CorpusFields  search.CorpusFields `json:"corpusFields"`
	Enabled       bool                `json:"enabled"`       // embedder 可用（key + model 齐）
	Reranker      bool                `json:"reranker"`      // reranker 可用
	KeyConfigured bool                `json:"keyConfigured"` // 进程侧是否配了 API key
	Rebuild       *hub.RebuildStatus  `json:"rebuild,omitempty"`
}

// embedConfigRequest 是 PUT 的请求体。零值字段表示"不改"，
// 由 handler 合并当前配置后交给 EmbedManager.UpdateConfig。
type embedConfigRequest struct {
	Model        string               `json:"model"`
	Dimensions   int                  `json:"dimensions"`
	BaseURL      string               `json:"baseUrl"`
	RerankModel  string               `json:"rerankModel"`
	CorpusFields *search.CorpusFields `json:"corpusFields"`
}

func embedConfigResponseOf(em *hub.EmbedManager) embedConfigResponse {
	if em == nil {
		return embedConfigResponse{}
	}
	cfg := em.Config()
	st := em.Status()
	out := embedConfigResponse{
		Model:        cfg.Model,
		Dimensions:   cfg.Dimensions,
		BaseURL:      cfg.BaseURL,
		RerankModel:  cfg.RerankModel,
		CorpusFields: cfg.CorpusFields,
		Enabled:      em.Embedder() != nil,
		Reranker:     em.Reranker() != nil,
		Rebuild:      &st,
	}
	if em.Embedder() != nil {
		out.KeyConfigured = true
	}
	return out
}

func (a *API) handleGetEmbedConfig(c *gin.Context) {
	c.JSON(http.StatusOK, embedConfigResponseOf(a.svc.Embed()))
}

func (a *API) handleUpdateEmbedConfig(c *gin.Context) {
	var req embedConfigRequest
	if err := bindJSON(c, &req); err != nil {
		failFor(c, err)
		return
	}

	em := a.svc.Embed()
	if em == nil {
		abortErr(c, http.StatusServiceUnavailable, "embed_disabled", "嵌入检索未启用")
		return
	}

	// 零值字段继承当前配置，合并成完整配置再交给管理器：
	// UpdateConfig 契约是 model/维度必填（防误清空），部分更新语义在这一层。
	cur := em.Config()
	cfg := hub.EmbedConfig{
		Model:        req.Model,
		Dimensions:   req.Dimensions,
		BaseURL:      req.BaseURL,
		RerankModel:  req.RerankModel,
		CorpusFields: cur.CorpusFields,
	}
	if cfg.Model == "" {
		cfg.Model = cur.Model
	}
	if cfg.Dimensions <= 0 {
		cfg.Dimensions = cur.Dimensions
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = cur.BaseURL
	}
	if cfg.RerankModel == "" {
		cfg.RerankModel = cur.RerankModel
	}
	if req.CorpusFields != nil {
		cfg.CorpusFields = *req.CorpusFields
	}

	ctx := c.Request.Context()
	changed, err := em.UpdateConfig(ctx, cfg)
	if err != nil {
		failFor(c, err)
		return
	}

	// 模型、维度或语料字段变了，旧向量全部作废，需要全量重建。
	// 重建放后台：几百个工具 × 一次 embed 往返，同步做会挂死请求。
	if changed {
		a.log.Info("嵌入配置已变更，触发全量重建", zap.Bool("changed", changed))
		go em.Rebuild(context.Background())
	}

	c.JSON(http.StatusOK, embedConfigResponseOf(em))
}

func (a *API) handleGetEmbedRebuild(c *gin.Context) {
	em := a.svc.Embed()
	if em == nil {
		abortErr(c, http.StatusServiceUnavailable, "embed_disabled", "嵌入检索未启用")
		return
	}
	c.JSON(http.StatusOK, em.Status())
}

func (a *API) handleTriggerEmbedRebuild(c *gin.Context) {
	em := a.svc.Embed()
	if em == nil {
		abortErr(c, http.StatusServiceUnavailable, "embed_disabled", "嵌入检索未启用")
		return
	}
	go em.Rebuild(context.Background())
	c.JSON(http.StatusOK, em.Status())
}
