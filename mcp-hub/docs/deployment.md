# mcp-hub 部署说明

mcp-hub 有两种运行形态：宿主机（`make dev` / `make run`）与整套容器（`make docker-up`）。两者互斥，都占 9090 端口。

所有 `make` 命令都在**仓库根**执行，即含 `Makefile` 与 `docker-compose.yml` 的那一层；`mcp-hub/` 是 Go 后端，`mcp-hub-admin/` 是前端。

---

## 1. 两种形态

### 1.1 宿主机形态

```bash
cd <仓库根>
cp mcp-hub/.env.example mcp-hub/.env    # 宿主机形态通常写在这里；进程环境变量优先级更高
make dev
```

`make dev` 依次：检查 `mcp-hub/.env` 存在（缺失则报错退出，避免静默退回 SQLite + 内存缓存）；`docker compose up -d --wait --wait-timeout 60 postgres redis` 只起数据库与缓存；在 `mcp-hub/` 下起 `mcp-hub serve` 并等 `/healthz` 通过（最多 30 秒）；起 Vite dev server 并跟踪日志。

| | 地址 |
|---|---|
| MCP 网关 | `http://127.0.0.1:9090/mcp` |
| 管理接口 | `http://127.0.0.1:9090/api/overview` |
| 管理后台（宿主机） | `http://127.0.0.1:5273` |
| 管理界面（容器） | `http://127.0.0.1:9090/` |

`Ctrl+C` 只结束前后端，数据库容器保留；彻底停止用 `make dev-stop`。日志在 `mcp-hub/logs/backend.log` 与 `mcp-hub/logs/frontend.log`。

只跑后端：`make run`（构建并 serve，不接受命令行参数，配置全来自 `.env` / 环境变量）。

`make run` 不检查 `.env`，缺失时退回零配置（SQLite + 内存缓存）；需自行保证 `mcp-hub/.env` 里的 `MCPHUB_DB` / `MCPHUB_CACHE` 指向的服务在运行（`make db-up` 可起它们）。

### 1.2 容器形态

在仓库根执行 `make docker-up`（= `docker compose up -d --wait --wait-timeout 120`），起 postgres、redis、mcp-hub 三个服务，等所有健康检查通过才返回。管理界面在该形态下是**二进制内嵌的**，不需要另起前端或反代。

日常命令：`make docker-logs`（后端容器日志）、`make docker-down`（停全套，命名卷保留）、`make docker-build`（改后端代码后重建镜像，容器不会自动更新）。

### 1.3 三条路的互斥关系

| 目标 | 命令 | 起什么 | 占 9090？ |
|---|---|---|---|
| 宿主机跑后端 | `make dev` / `make run` | postgres + redis 容器 + 宿主机后端 | 是 |
| 只起数据库 | `make db-up` | postgres + redis 容器 | 否 |
| 整套容器 | `make docker-up` | postgres + redis + 后端容器 | 是 |

1. **`make dev` / `make run` 与 `make docker-up` 互斥**。同时开会报"地址已被使用"，切换前先 `make dev-stop` 或 `make docker-down`。
2. **`make db-up` 与 `make docker-up` 的区别是后端在不在容器里**。`db-up` 只起数据库和缓存（`postgres redis`），后端留给 `make dev` / `make run`；`docker-up` 三个一起起。绕开 Makefile 直接 `docker compose up -d`（不点名服务）会把后端容器一起拉起，随后 `make dev` 会因 9090 被占而失败。

---

## 2. 容器不读 `.env`

容器的配置来自 compose 的 `environment`，不是 `mcp-hub/.env`。`docker-compose.yml` 里 mcp-hub 服务注入：

```yaml
environment:
  MCPHUB_DB: postgres://mcphub:mcphub@postgres:5432/mcphub?sslmode=disable
  MCPHUB_CACHE: redis://redis:6379/0
  MCPHUB_ADDR: ":9090"
```

进程环境变量优先级高于配置文件；镜像里也没有 `.env`：`.dockerignore` 排除 `**/.env`，只放行 `!**/.env.example`。改容器配置要改 **`docker-compose.yml` 的 `environment`**，然后 `make docker-up`。

容器网络里主机名是服务名，`127.0.0.1` 指容器自己：

| 宿主机 `.env`（宿主机形态） | 容器内（compose 注入） |
|---|---|
| `postgres://mcphub:mcphub@127.0.0.1:5432/mcphub?sslmode=disable` | `postgres://mcphub:mcphub@postgres:5432/mcphub?sslmode=disable` |
| `redis://127.0.0.1:6379/0` | `redis://redis:6379/0` |

| 现象 | 原因 | 怎么办 |
|---|---|---|
| 改了 `.env`，容器行为没变 | 容器不读 `.env` | 改 compose 的 `environment` 后 `make docker-up` |
| 容器日志里数据库连接被拒绝 | 用了 `127.0.0.1`，那是容器自己 | 主机名换成服务名 `postgres` / `redis` |
| 宿主机上后端连不上数据库 | 容器端口只绑回环，需用 `127.0.0.1:5432` | 这条 `.env` 写法正确，不要改 |

---

## 3. 端口与暴露面

compose 三个服务的端口映射全部只绑回环：

| 服务 | 映射 | 宿主机可访问范围 |
|---|---|---|
| postgres | `127.0.0.1:5432:5432` | 仅本机 |
| redis | `127.0.0.1:6379:6379` | 仅本机 |
| mcp-hub | `127.0.0.1:9090:9090` | 仅本机 |

管理接口（`/api/*`）能增删下游 server、凭据、开关工具，等同半个 shell，回环绑定是默认安全姿态。

### 3.1 对外暴露必须配 `MCPHUB_API_KEY`

`MCPHUB_API_KEY` 非空时，`/api/*` 每个请求都要带 `X-API-Key` 头或 `Authorization: Bearer`，否则 401；留空则完全不校验，仅适用于只监听 `127.0.0.1` 的场景。

容器形态写在 compose 的 `environment`，宿主机形态写在 `mcp-hub/.env`：

```yaml
environment:
  MCPHUB_API_KEY: "换成你自己的密钥"
```

```
MCPHUB_API_KEY=换成你自己的密钥
```

> 两套凭据，不要混：
> - `MCPHUB_API_KEY` 保护**管理接口** `/api/*`。
> - **接入 key**（管理界面创建，对应 `/api/keys`）保护 **MCP 端点** `/mcp`。库里没有任何 key 时 `/mcp` 不校验；创建过第一把后，匿名请求一律 401。
>
> 对外暴露时两套都要配：前者挡住"谁能改配置"，后者挡住"谁能调工具"。

### 3.2 容器内 `MCPHUB_ADDR` 必须是 `:9090`

`:9090` 监听全部网卡，端口映射才能打进来；写成 `127.0.0.1:9090` 会导致容器活着、健康检查通过但外部连不上。对外可见范围由 compose 的 `ports`（`127.0.0.1:9090:9090`）控制。宿主机形态相反，`.env` 建议写 `127.0.0.1:9090`。

---

## 4. 数据与持久化

| 卷 | 挂在哪 | 装什么 | 说明 |
|---|---|---|---|
| `pgdata` | postgres 容器 `/var/lib/postgresql` | PostgreSQL 数据目录 | 用 PostgreSQL 时是全部数据 |
| `redisdata` | redis 容器 `/data` | Redis 持久化文件 | 丢失不影响正确性，只影响缓存命中率 |
| `hubdata` | mcp-hub 容器 `/data` | 引擎 dataDir（进程工作目录） | 用 PostgreSQL 时基本为空；SQLite 模式的库文件落在这里 |

### 4.1 `make db-reset CONFIRM=1` 会删掉全部卷

```bash
make db-reset CONFIRM=1
```

执行整个 compose 项目的 `down -v`，**删除全部三个命名卷**（`pgdata` / `redisdata` / `hubdata`）。必须显式带 `CONFIRM=1`，否则打印提示并退出。

### 4.2 两个后端的数据互不相通

PostgreSQL 与 SQLite 各有迁移树，切换不会迁移任何数据；表结构在首次连接时自动同步，PG 侧需先建好空库。

---

## 5. 镜像构建

```bash
make docker-build        # = docker compose build mcp-hub
```

构建上下文必须是仓库根：compose 里 `build.context: .`、`dockerfile: mcp-hub/Dockerfile`。镜像同时装入 Go 后端与 Node 前端（`COPY mcp-hub/…` 与 `COPY mcp-hub-admin/…`），上下文指向 `mcp-hub/` 会构建失败。`.dockerignore` 在仓库根，排除 `mcp-hub-admin/node_modules`、`mcp-hub/bin`、`mcp-hub/logs`、`**/.git`、`**/.env`。手工构建同理：

```bash
docker build -f mcp-hub/Dockerfile -t mcp-hub:local .
```

| 阶段 | 做的事 | 产物 |
|---|---|---|
| `ui` | `npm ci` + `npm run build`（= `tsc -b && vite build`） | 前端 `dist/` |
| `build` | `go build -trimpath -ldflags='-s -w'` | `/out/mcp-hub` |
| 运行 | alpine + ca-certificates + tzdata，非 root 用户 `mcphub`，`WORKDIR /data` | 镜像 |

运行阶段必须装 `ca-certificates`：下游 MCP server 基本是 HTTPS，缺根证书会报 `x509: certificate signed by unknown authority`。

### 5.1 换源参数

换源时执行 `BASE_REGISTRY=docker.io make docker-build`。`BASE_REGISTRY` 必须是 registry 主机名（Dockerfile 里统一拼 `/library/`），compose 已做成构建参数 `${BASE_REGISTRY:-docker.m.daocloud.io}`。

| 参数 | 默认 | 作用 |
|---|---|---|
| `BASE_REGISTRY` | `docker.m.daocloud.io` | 三个基础镜像（node / golang / alpine）的来源 |
| `NPM_REGISTRY` | `https://registry.npmmirror.com` | 前端依赖源（Dockerfile 构建参数，compose 未透出，手工构建用 `--build-arg`） |
| `GOPROXY` | `https://goproxy.cn,direct` | Go 模块源（同上） |

### 5.2 管理界面通过 `go:embed` 进二进制

前端产物（`mcp-hub-admin/` 执行 `npm run build` 得到的 `dist/`）拷进 `mcp-hub/internal/webassets/dist/`，再由 `go:embed all:dist` 编进二进制。宿主机做法：

```bash
make ui          # 构建前端并把产物拷进 mcp-hub/internal/webassets/dist/
make build       # go:embed 把它编进二进制
```

目录里的 `.placeholder` 是 `go:embed` 的兜底（embed 不接受空目录），不会被服务出去。没跑过 `make ui` 的二进制照样能跑：`/api` 与 `/mcp` 正常，`/` 是干净的 404，启动横幅会说明。镜像里的界面由 Dockerfile 第一阶段现建，`COPY --from=ui` 覆盖到 `internal/webassets/dist/`；宿主机的陈旧产物被 `.dockerignore` 排除，因此镜像总是当前代码的界面。

### 5.3 镜像里没有桌面形态

`cmd/mcp-hub-desktop` 需要 Cocoa/GTK webview，容器里没有窗口系统，因此不进镜像。桌面版在宿主机 `make build-desktop`（或 `make build-desktop-browser`，无 cgo、可交叉编译）。

---

## 6. 健康检查与排错

| 端点 | 回答什么 | 什么时候不 OK |
|---|---|---|
| `/healthz` | 进程还活着吗 | 进程没起来（连不上） |
| `/readyz` | 有没有可用的工具 | **一个工具都没有时返回 503** |

`/readyz` 503 表示未就绪，响应带 `toolCount` 与 `unhealthyNodes`。**存活探针用 `/healthz`，不要用 `/readyz`**：下游全挂时 `/readyz` 会让容器被重启，而重启修不好下游。

| 服务 | 检查 | 间隔 / 超时 / 重试 | 备注 |
|---|---|---|---|
| postgres | `pg_isready -U mcphub -d mcphub` | 3s / 3s / 10 | |
| redis | `redis-cli ping` | 3s / 3s / 10 | |
| mcp-hub | `wget http://127.0.0.1:9090/healthz` | 5s / 3s / 10 | `start_period: 15s`（覆盖首次启动的数据库迁移） |

mcp-hub 的 `depends_on` postgres / redis 条件为 `service_healthy`。

| 现象 | 原因 | 处理 |
|---|---|---|
| 后端起不来，报"地址已被使用" | 容器形态的后端还占着 9090 | `make docker-down` 或 `make dev-stop` |
| `/readyz` 返回 503 | 没有可用下游（工具数为 0） | 不是故障，看 `make docker-logs` 的探活结果 |
| 构建卡在拉基础镜像 | 直连 Docker Hub 拉不动 | `BASE_REGISTRY=docker.io make docker-build` |
| 启动报"配置错误" | 类型不对 / 枚举越界 / 连接串与后端不匹配 | 错误一次报完，逐条改；清单见 `mcp-hub env` |

排错命令：`make docker-logs`（后端容器）、`make db-logs`（全部服务）、`make dev-logs`（宿主机形态前后端）。

---

## 7. 配置优先级与 `.env` 查找

优先级：进程环境变量 > `.env` 文件 > 内置默认值。例如 `MCPHUB_ADDR=:8080 mcp-hub serve` 可一次性覆盖 `.env` 里的值。compose 的 `environment`、K8s 的 `env` 都是进程环境变量，因此部署参数压过 `.env`。

`.env` 只加载**第一个命中的**文件：`$MCPHUB_ENV_FILE` → `./.env` → 可执行文件旁的 `.env`。`MCPHUB_ENV_FILE` 只能来自进程环境变量。桌面形态（`mcp-hub-desktop`）多一条锚点 `<应用数据目录>/.env`，且**不回退**到当前工作目录，详见 [cmd/mcp-hub-desktop/README.md](../cmd/mcp-hub-desktop/README.md)。

命令行形态的 dataDir 与配置查找都基于进程当前工作目录。从仓库根跑 `./mcp-hub/bin/mcp-hub serve` 会找不到 `mcp-hub/.env`，并静默退回 SQLite + 内存缓存；`make run` 内部会先 `cd mcp-hub`。

### 7.1 全部配置项

```bash
mcp-hub env          # 打印全部配置项：变量名 / 默认值 / 说明
```

清单由代码里的声明表驱动，同时负责绑定、启动校验和 `env` 输出，以 `mcp-hub env` 为准。高频项：

| 变量 | 默认 | 说明 |
|---|---|---|
| `MCPHUB_DB` | 空 | `postgres://…` 走 PostgreSQL，文件路径走 SQLite，留空则建 `mcp-hub.db` |
| `MCPHUB_DB_DRIVER` | `auto` | `auto` \| `postgres` \| `sqlite` |
| `MCPHUB_CACHE` | 空 | `redis://…` 走 Redis，留空走进程内缓存 |
| `MCPHUB_CACHE_DRIVER` | `auto` | `auto` \| `redis` \| `memory` |
| `MCPHUB_ADDR` | `127.0.0.1:9090` | 监听地址；只写 `:9090` 等于监听全部网卡 |
| `MCPHUB_API_KEY` | 空 | 管理接口鉴权密钥；留空不校验 |
| `MCPHUB_ENV_FILE` | 空 | 显式指定配置文件路径 |
| `MCPHUB_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `MCPHUB_PROBE_INTERVAL` | `45s` | 周期探活间隔，`0` 关闭 |

`.env` 里不认识的 `MCPHUB_*` 名字会在启动时点名提醒。配置没有热加载：改配置 = 改文件 + 重启；管理界面没有配置入口，库里也不存配置。

---

## 8. 命令速查

| 目标 | 命令 |
|---|---|
| 宿主机：起全套（库 + 后端 + 前端） | `make dev` |
| 宿主机：只起后端 | `make run` |
| 宿主机：状态 / 日志 / 停止 | `make dev-status` / `make dev-logs` / `make dev-stop` |
| 宿主机：带界面 | `make ui && make build` |
| 库：起 / 停 / 清空 | `make db-up` / `make db-down` / `make db-reset CONFIRM=1` |
| 库：日志 | `make db-logs` |
| 容器：构建 / 起 / 日志 / 停 | `make docker-build` / `make docker-up` / `make docker-logs` / `make docker-down` |
| 桌面形态 | `make build-desktop` / `make build-desktop-browser` |
| 列出全部配置项 | `mcp-hub env` |
