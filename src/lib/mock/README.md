# MSW Mock 层

数据全部走 mock，前端静态流程无后端依赖。

## 开关
- 开：在入口处 `await startMockWorker()`（见 `browser.ts`）。
- 关：移除该调用，或设 `VITE_USE_MOCK=false`（由入口条件判断）。
- 鉴权：默认不校验。设 `VITE_MOCK_API_KEY=devkey` 后，请求头 `X-API-Key` 必须与之相等，否则 401（完整验证鉴权 UI）。

## 缺口 mock 策略
- **B1** `/api/logs/key-events`：真实后端无此端点，由 `createSeedKeyEvents()` 生成，是前端唯一数据源。
- **B2** `POST /api/servers/:id/call`：按 server 状态决定成败，失败给 errorKind+中文 hint，并头插一条调用日志、返回 `logId`。
- **B3** `GET /api/overview` 的 `keyDetails`：遍历所有 server 的冷却/弃用密钥，给出剩余时间倒计时与弃用原因。
- **B4** 最近全量测试结果：前端内存保存 `test-all` 响应即可（刷新丢失，无需后端）。

## 注意
- 写操作延迟 200–500ms、读 80–320ms，loading 骨架可见。
- `stdio` server 的密钥池操作返回 400 `key_pool_unsupported`。
- 停止的 server 在 `disable` 后进入 draining，3 秒后自动结束。
