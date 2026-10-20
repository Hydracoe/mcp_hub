# mcp-hub-desktop

桌面形态入口：自带引擎与存储，本机即完整 MCP Hub，零外部依赖。
界面画在 **Wails v3** 的原生窗口里。

## 用法

构建入口在**项目根**（与 `mcp-hub/` 同级的 `Makefile`），产物落在 `mcp-hub/bin/`：

```bash
cd ../../..                         # 到项目根
make build-desktop                     # go build -tags wails -o mcp-hub/bin/mcp-hub-desktop ./cmd/mcp-hub-desktop
./mcp-hub/bin/mcp-hub-desktop          # 起引擎 + 开原生窗口

make ui && make build-desktop          # 带上管理界面（见下）
make run-desktop                       # 构建并直接运行
```

命令从别处敲也行：Makefile 里所有路径都由它自己的位置推导，不依赖当前目录。

两种壳，构建标签二选一：

| | `make build-desktop` | `make build-desktop-browser` |
|---|---|---|
| 界面画在哪 | Wails 原生窗口（webview） | 系统默认浏览器 |
| 构建标签 | `-tags wails` | 无 |
| cgo | **需要**（macOS 走 Cocoa/WebKit，Linux 走 GTK） | 不需要 |
| 交叉编译 | 不能（Linux 目标需 GTK 头文件） | 可以，三平台都行 |

两者业务行为完全一致：同一个 `internal/engine`、同一份界面、同一个 MCP 端点。
差别只在 `shell_browser.go` / `shell_wails.go` 这两个文件里。

界面在构建时就已内嵌。**没跑过 `make ui` 的话它照样能编译、能启动，只是不带界面**——
窗口里是 404 而 `/api`、`/mcp` 一切正常。这是合法形态，不是故障。

首次启动会在应用数据目录建库、跑迁移，开箱即用：

| 平台 | 数据目录 |
|---|---|
| macOS | `~/Library/Application Support/mcp-hub/` |
| Windows | `%AppData%\mcp-hub\` |
| Linux | `$XDG_CONFIG_HOME/mcp-hub/`（默认 `~/.config/mcp-hub/`） |

目录内容：

```
mcp-hub.db       SQLite 库（含 schema_migrations）
desktop.log      运行日志（含 Wails 自身的系统日志）
.env             可选配置，留空即全默认
```

## 与命令行形态的关系

差别只有三个装配参数，写在 `main.go` 里：

| | `cmd/mcp-hub serve` | `cmd/mcp-hub-desktop` |
|---|---|---|
| 数据目录 | 当前工作目录 | 应用数据目录 |
| 监听地址 | `:9090`（全部网卡） | 默认 `127.0.0.1:9090` |
| 生命周期 | 等 SIGTERM | 关窗 / SIGTERM |
| 管理界面 | 同一个 `internal/webassets` | 同一个 `internal/webassets` |
| 配置查找 | `$MCPHUB_ENV_FILE → ./.env → exe 旁 .env` | `$MCPHUB_ENV_FILE → <数据目录>/.env`，**不回退到 cwd** |

数据库后端选择、缓存后端选择、迁移、探活、向量重建、优雅退出，全部走
`internal/engine` —— 两份部署路线共享同一份业务实现。

最后一行值得单独说：桌面形态的配置查找是**锚定**在应用数据目录上的。
初版让它回退到 `./.env`，于是从仓库目录启动时会读到仓库那份配置（指向
PostgreSQL），一个本该零配置可用的应用直接启动失败。工作目录不该决定一个
桌面应用读到哪份配置——双击启动时它是 `/` 或 `System32`，从终端启动时是
随手进的某个目录。要覆盖位置就用 `$MCPHUB_ENV_FILE`，那是一个写下来的意图。

## 默认配置（零配置可用）

不写任何配置时：

```
MCPHUB_DB_DRIVER=auto     → 空路径 + 非 postgres:// ⇒ sqlite
MCPHUB_DB=（空）          → <应用数据目录>/mcp-hub.db
MCPHUB_CACHE_DRIVER=auto  → 空连接串 ⇒ memory
MCPHUB_ADDR=（空）        → 127.0.0.1:9090
```

要接外部 PostgreSQL / Redis（多实例共享），在数据目录的 `.env` 里写：

```
MCPHUB_DB=postgres://user:pw@127.0.0.1:5432/mcphub?sslmode=disable
MCPHUB_CACHE=redis://127.0.0.1:6379/0
```

## 为什么监听端口是固定的 9090

初版用的是 `127.0.0.1:0`（系统分配），理由是"不占固定端口、也不会因为被占用而起不来"。
接上 Wails 之后这个理由不再成立：

- **界面已经不走这个端口了**。`eng.Handler()` 直接交给 Wails 的资源服务器，
  webview 的每个请求（HTML、chunk、`/api`、`/mcp`）都在进程内打到同一台 gin 上。
  于是界面与后端天然同源（前端 `fetch('/api/...')` 一行不用改，也不必开 CORS），
  端口冲突也不再影响界面能不能打开。
- 于是端口的唯一用途是**给外部 AI 客户端连**，而一个每次启动都变的地址
  等于客户端永远配不对——桌面端的核心价值就没了。

9090 被占用时退回系统分配，真实地址写进日志与**窗口标题**。这一步不是静默降级：
界面照常打开、用户能进去改配置，比让一个 GUI 应用因为端口冲突起不来要好。

## Wails 接缝的两个细节

如果以后要动这块，这两点值得知道（都是踩过的）：

**一、`OnShutdown` 而不是 `Run()` 返回后收尾。**
macOS 上 `app.Run()` 走的是 `[NSApp run]`，应用终止不保证回到 Go——
`main` 里那条 `defer eng.Stop()` 有可能永远执行不到，而 stdio 子进程挂在引擎上，
不收干净就是一地孤儿。`application.Options.OnShutdown` 是框架明确承诺
会阻塞等待的钩子，位置才对。（`main` 的 defer 保留着，因为 `Stop` 可重复调用。）

**二、不要试图注册 `/wails/*` 路由。**
Wails 的 `/wails/runtime.js`、`/wails/transport.js`、事件 payload 与 GoStream
轮询路径，由它自己的中间件挡在 `Assets.Handler` 之前。所以：
不要把 `eng.Handler()` 挂成通配或挂在前面——`httpd` 那边已经把它放在 gin 的
`NoRoute`（路由表最后一位），并且对 `/api`、`/mcp` 等保留命名空间的未命中
保持 404，不让 SPA 回退吃掉。

前端那边配合的两点也**早就改好了**，这里只记录原因：

- 路由用 Hash 模式（`mcp-hub-admin/src/router.tsx`）：Web 部署下后端只在 `/api`、`/mcp`
  有路由，桌面端同理；History 模式下刷新即 404。
- `vite.config.ts` 的 `base` 设为 `'./'`：相对基址让同一份产物可用于根路径、
  子路径反代、以及 `wails://` 资源服务器三种加载方式。`internal/webassets`
  的测试会检查这条（`TestEmbeddedDistShape`）——改成绝对路径后现象只是白屏，
  在 Go 侧完全看不出来。

## 前端热更新

`internal/webassets` 里的界面是**编译时就固定**的（`go:embed`）。
想在 webview 里做前端热更新是另一条路：开发时让窗口指向 Vite 的 dev server
（`FRONTEND_DEVSERVER_URL`，Wails 的开发模式会代理过去），生产构建再回到内嵌产物。
两种模式不必同时存在，目前只实现了后者。
