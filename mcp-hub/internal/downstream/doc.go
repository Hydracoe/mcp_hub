// Package downstream 是网关与下游 MCP server 之间的那一层，自下而上分为
// 配置、传输、判定、连接、调度五层。
//
// 两条原则：失败必须归类（调用路径统一返回 *CallError）；判定要用结构化信息
// （观测点放在 RoundTripper，见 Recorder）。
//
// 测试全在 tests/ 子目录，只使用本包的导出 API。
package downstream
