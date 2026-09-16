// Package retrieval 负责"工具怎么被搜到"：语料收集、内存索引、
// 三路检索（keyword / BM25 / 向量）的融合与精排，以及向量按"代"的
// 重建与分类自动归类。
//
// 它不持有 MCP 端点，也不管连接：输入是库里的 server/工具行，输出是
// 候选工具。索引与聚合快照同源、同步重建，因此"搜出来的工具"必然在
// "能调用的工具列表"里。
package retrieval

import (
	"sync"

	"github.com/hydra/mcp-hub/internal/store"
	"go.uber.org/zap"
)

// Engine 是检索能力在 hub 内的宿主：持有内存索引与嵌入管理器。
type Engine struct {
	st  *store.Store
	log *zap.Logger

	// embed 由 AttachEmbedder 挂上，可为 nil——此时向量一路走降级阶梯。
	em *EmbedManager

	// 索引随聚合快照一起整体替换，读路径只在取指针时加一次锁。
	mu sync.Mutex
	ix *searchIndex
}

// NewEngine 构造检索引擎。嵌入管理器稍后由 AttachEmbedder 挂上：
// 它需要 handler 先拿到 Service 自身，构造期是环。
func NewEngine(st *store.Store, log *zap.Logger) *Engine {
	if log == nil {
		log = zap.NewNop()
	}
	return &Engine{st: st, log: log}
}

// AttachEmbedder 挂上嵌入管理器，nil 表示未启用向量检索。
func (e *Engine) AttachEmbedder(em *EmbedManager) { e.em = em }

// Embedder 返回嵌入管理器，未启用时为 nil。
func (e *Engine) Embedder() *EmbedManager { return e.em }
