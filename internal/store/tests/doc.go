// Package tests 存放 internal/store 的测试。
//
// store 拆成子包后，根包与子包互相引用，同目录的白盒测试会构成
// import cycle（servers 的测试要用 store.Open，而 store 又 import servers）。
// 因此这里统一做黑盒测试：从 store.Open 拿到装配好的 *store.Store，
// 走各子包的导出 API 验证行为。个别纯函数的白盒测试（如 logs 的
// likePattern）不依赖根包，仍留在各自子包目录内。
package tests
