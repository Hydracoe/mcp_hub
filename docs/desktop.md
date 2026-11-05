# mcp-hub-desktop

桌面形态入口：自带引擎与存储，本机即完整 MCP Hub，零外部依赖。界面画在 **Wails v3** 的原生窗口里。

## 用法

构建入口在**仓库根**（`Makefile` 与 `mcp-hub/` 同级），产物落在 `mcp-hub/bin/`：

```bash
cd <仓库根>
make build-desktop                     # go build -tags wails -o mcp-hub/bin/mcp-hub-desktop ./cmd/mcp-hub-desktop
./mcp-hub/bin/mcp-hub-desktop          # 起引擎 + 开原生窗口

make ui && make build-desktop          # 带上管理界面
make run-desktop                       # 构建并直接运行
```

两种壳，构建标签二选一：

| | `make build-desktop` | `make build-desktop-browser` |
|---|---|---|
| 界面画在哪 | Wails 原生窗口（webview） | 系统默认浏览器 |
| 构建标签 / cgo | `-tags wails` / **需要**（macOS 走 Cocoa/WebKit，Linux 走 GTK） | 无 / 不需要 |
| 交叉编译 | 不能（Linux 目标需 GTK 头文件） | 可以，三平台都行 |

两者业务行为一致：同一个 `internal/engine`、同一份界面、同一个 MCP 端点，差别只在 `shell_browser.go` / `shell_wails.go`。

界面在构建时内嵌。**没跑过 `make ui` 也能编译、能启动，只是不带界面**——窗口里是 404 而 `/api`、`/mcp` 一切正常，这是合法形态。

首次启动会在应用数据目录建库、跑迁移：

| 平台 | 数据目录 |
|---|---|
| macOS | `~/Library/Application Support/mcp-hub/` |
| Windows | `%AppData%\mcp-hub\` |
| Linux | `$XDG_CONFIG_HOME/mcp-hub/`（默认 `~/.config/mcp-hub/`） |

目录内容：`mcp-hub.db`（SQLite 库，含 schema_migrations）、`desktop.log`（运行日志，含 Wails 系统日志）、`.env`（可选配置，留空即全默认）。

## 与命令行形态的关系

差别只在几个装配参数，写在 `main.go` 里：

| | `cmd/mcp-hub serve` | `cmd/mcp-hub-desktop` |
|---|---|---|
| 数据目录 | 当前工作目录 | 应用数据目录 |
| 监听地址 | 默认 `127.0.0.1:9090` | 默认 `127.0.0.1:9090` |
| 生命周期 | 等 SIGTERM | 关窗 / SIGTERM |
| 配置查找 | `$MCPHUB_ENV_FILE → ./.env → exe 旁 .env` | `$MCPHUB_ENV_FILE → <数据目录>/.env`，**不回退到 cwd** |

数据库与缓存后端选择、迁移、探活、向量重建、优雅退出全部走 `internal/engine`。桌面形态的配置查找**锚定**在应用数据目录；要覆盖位置就用 `$MCPHUB_ENV_FILE`。

## 默认配置（零配置可用）

```
MCPHUB_DB_DRIVER=auto     → 空路径 + 非 postgres:// ⇒ sqlite
MCPHUB_DB=（空）          → <应用数据目录>/mcp-hub.db
MCPHUB_CACHE_DRIVER=auto  → 空连接串 ⇒ memory
MCPHUB_ADDR=（空）        → 127.0.0.1:9090
```

接外部 PostgreSQL / Redis（多实例共享），在数据目录的 `.env` 里写：

```
MCPHUB_DB=postgres://user:pw@127.0.0.1:5432/mcphub?sslmode=disable
MCPHUB_CACHE=redis://127.0.0.1:6379/0
```

## 端口

界面与后端同源：`eng.Handler()` 交给 Wails 的资源服务器，webview 的每个请求（HTML、chunk、`/api`、`/mcp`）都在进程内打到同一台 gin，因此不需要 CORS。端口只用于**外部 AI 客户端连接**，默认固定 `127.0.0.1:9090`；被占用时退回系统分配，真实地址写进日志与**窗口标题**，界面照常打开，用户可进去改配置。

## Wails 接缝

- **用 `OnShutdown` 收尾，而不是等 `Run()` 返回。** macOS 上 `app.Run()` 走 `[NSApp run]`，终止不保证回到 Go；`application.Options.OnShutdown` 是框架承诺会阻塞等待的钩子，stdio 子进程在这里收干净。`main` 的 `defer eng.Stop()` 保留（`Stop` 可重复调用）。
- **不要注册 `/wails/*` 路由。** Wails 自己的中间件已挡住 `/wails/runtime.js`、`/wails/transport.js` 等路径。`httpd` 把 `eng.Handler()` 放在 gin 的 `NoRoute`（路由表最后），并对 `/api`、`/mcp` 等保留命名空间的未命中保持 404。

前端配合两点：路由用 Hash 模式（`mcp-hub-admin/src/router.tsx`，History 模式刷新即 404）；`vite.config.ts` 的 `base` 设为 `'./'`，让同一份产物可用于根路径、子路径反代和 `wails://` 资源服务器（`internal/webassets` 的 `TestEmbeddedDistShape` 会检查）。

前端热更新：`internal/webassets` 的界面是编译时固定的（`go:embed`）；开发时可设 `FRONTEND_DEVSERVER_URL` 让窗口指向 Vite dev server（Wails 开发模式代理），生产构建使用内嵌产物。
