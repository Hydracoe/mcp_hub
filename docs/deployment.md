# mcp-hub 部署说明

这份文档回答一个问题：**mcp-hub 怎么跑起来，以及跑起来之后配置从哪来。**

两种形态：

| 形态 | 谁在跑后端 | 一条命令 | 适合 |
|---|---|---|---|
| 宿主机 | 宿主机上的二进制 | `make dev`（或 `make run`） | 本地开发、调后端、带热更新的前端 |
| 容器 | 容器里的二进制 | `make docker-up` | 部署到一台服务器、不想在机器上装 Go/Node |

两条路**互斥**：都占 9090 端口，同时开会撞车。

---

## 0. 先看这里

本文档里的所有 `make` 命令都在**项目根**执行 —— 也就是有 `Makefile` 和
`docker-compose.yml` 的那一层：

```
<项目根>/            ← make 命令在这里敲
├── Makefile
├── docker-compose.yml
├── .dockerignore
├── mcp-hub/         ← 本文档所在仓库（Go 后端，独立的 git 仓库）
└── mcp-hub-admin/   ← 前端（独立的 git 仓库）
```

仓库边界值得先说一句：`mcp-hub/` 自己是一个 git 仓库，项目根不是。
所以 `mcp-hub/docs/deployment.md` 跟后端代码一起进版本库，而
`docker-compose.yml` 与 `Makefile` 留在根上管着两个子项目 —— 编排脚本跟着
编排对象走。

三分钟速查：

```bash
# 形态一：宿主机跑后端（数据库仍在容器里）
cp mcp-hub/.env.example mcp-hub/.env   # 改配置：DB / CACHE / ADDR
make dev                                # 起 postgres+redis+后端(:9090)+前端(:5273)
make dev-status                         # 三件套健康一览
make dev-stop                           # 全停（数据保留）

# 形态二：整套容器
make docker-up                          # 起 postgres+redis+后端容器，全部等健康检查通过
make docker-logs                        # 跟后端容器日志
make docker-down                        # 停全套（命名卷保留）

# 只起数据库/缓存（后端在宿主机跑）
make db-up
make db-down
```

---

## 1. 两种形态

### 1.1 宿主机形态

```bash
cd <项目根>
cp mcp-hub/.env.example mcp-hub/.env    # 配置只有这一个来源
make dev
```

`make dev` 依次做四件事：

1. 检查 `mcp-hub/.env` 存在（不存在直接报错退出 —— 没有它，程序会退回
   "零配置"的 SQLite + 内存缓存，与 `.env` 里写的 PostgreSQL 对不上）；
2. `docker compose up -d --wait --wait-timeout 60 postgres redis` —— **只**起
   数据库和缓存，后端在宿主机上跑；
3. 在 `mcp-hub/` 目录下起 `mcp-hub serve`，等 `/healthz` 通过（最多 30 秒）；
4. 起前端 Vite dev server（`127.0.0.1:5273`），随后阻塞跟踪日志。

跑完是三个地址：

| | 地址 |
|---|---|
| MCP 网关 | `http://127.0.0.1:9090/mcp` |
| 管理接口 | `http://127.0.0.1:9090/api/overview` |
| 管理后台 | `http://127.0.0.1:5273` |

`Ctrl+C` 只结束前后端，数据库容器留着；要彻底停用 `make dev-stop`。
日志在 `mcp-hub/logs/backend.log` 与 `mcp-hub/logs/frontend.log`。

只跑后端、不跑前端：

```bash
make run          # 构建并 serve，不接受任何命令行参数，配置全来自 .env / 环境变量
```

`make run` 与 `make dev` 的差别：`make dev` 顺带把数据库容器和前端都拉起来，
并阻塞跟踪日志；`make run` 只跑后端，缺 `.env` 时也不报错 —— 它会退回零配置
（SQLite + 内存缓存）。所以用 `make run` 时，你要自己保证 `mcp-hub/.env` 里的
`MCPHUB_DB` / `MCPHUB_CACHE` 指向的东西真的在跑（`make db-up` 起它们）。

### 1.2 容器形态

```bash
cd <项目根>
make docker-up
```

`make docker-up` = `docker compose up -d --wait --wait-timeout 120`，**三个服务
全起**：postgres、redis、mcp-hub 后端容器。等所有健康检查通过才返回。

访问地址：

| | 地址 |
|---|---|
| 管理界面 | `http://127.0.0.1:9090/` |
| MCP 端点 | `http://127.0.0.1:9090/mcp` |

注意管理界面在容器形态下是**二进制内嵌的**（构建期就编进去了），不需要另起
一个前端，也不需要反代。

日常操作：

```bash
make docker-logs     # 跟踪后端容器日志
make docker-down     # 停全套，命名卷保留，数据不丢
make docker-build    # 改了后端代码后重新构建镜像（改完必须 rebuild，容器不会自己变）
```

### 1.3 三条路的互斥关系

| 目标 | 命令 | 起什么 | 占 9090？ |
|---|---|---|---|
| 宿主机跑后端 | `make dev` / `make run` | postgres + redis 容器 + 宿主机后端 | 是 |
| 只起数据库 | `make db-up` | postgres + redis 容器 | 否 |
| 整套容器 | `make docker-up` | postgres + redis + 后端容器 | 是 |

两条规则：

1. **`make dev` / `make run` 与 `make docker-up` 互斥**。两者都要 9090，
   同开时后者起不来，宿主机的后端也起不来，报错是"地址已被使用"——
   而这个报错完全不会让人联想到是另一边占的。切形态前先 `make dev-stop`
   或 `make docker-down`。
2. **`make db-up` 与 `make docker-up` 的区别只是后端在不在容器里**。
   `db-up` 只起数据库和缓存，后端留给你在宿主机上跑（`make dev` / `make run`）。
   `docker-up` 三个一起起。

所以 `db-up` 与 `dev` 是**配套**的，不是替代关系：

```bash
make db-up     # 只起库
make dev       # 再起宿主机后端（db-up 已起过库，dev 会跳过重复部分）
```

Makefile 里**凡是只起数据库的** compose 调用都显式点名服务名
（`postgres redis`），就是为了不留下"顺手多起一个后端容器"的可能。绕开
Makefile 直接 `docker compose up -d`（不点名服务）会把后端容器一起拉起来，
等价于 `make docker-up`（只是没有 `--wait` 的健康等待）—— 那时 `make dev`
的后端就会因为 9090 被占而起不来。

---

## 2. 容器不读 `.env`（最常见的困惑）

**改 `mcp-hub/.env` 发现容器行为没变，不是缓存，也不是没重启 —— 容器
压根就没读过那个文件。**

原因有两层。

### 2.1 容器的配置来自 compose 的 `environment`

`docker-compose.yml` 里 mcp-hub 服务显式注入了三个变量：

```yaml
environment:
  MCPHUB_DB: postgres://mcphub:mcphub@postgres:5432/mcphub?sslmode=disable
  MCPHUB_CACHE: redis://redis:6379/0
  MCPHUB_ADDR: ":9090"
```

这些是**进程环境变量**，按配置层的优先级（见第 7 节）压过任何配置文件。
而镜像里本来也没有 `.env` 可读：构建上下文由 `.dockerignore` 收窄，
`**/.env` 被排除，`!**/.env.example` 才放行模板。

所以要改容器的配置，改的是 **`docker-compose.yml` 的 `environment`**，
改完 `make docker-up`（compose 检测到 environment 变化会重建容器）。

### 2.2 容器网络里，主机名是服务名

宿主机上，`.env` 里的 `127.0.0.1:5432` 指向宿主机自己的 postgres；
**在容器里，`127.0.0.1` 指的是容器自己** —— 那里没有 postgres，于是连接被拒绝。

容器之间靠 compose 网络上的**服务名**互相访问：

| 宿主机 `.env`（宿主机形态） | 容器内（compose 注入） |
|---|---|
| `postgres://mcphub:mcphub@127.0.0.1:5432/mcphub?sslmode=disable` | `postgres://mcphub:mcphub@postgres:5432/mcphub?sslmode=disable` |
| `redis://127.0.0.1:6379/0` | `redis://redis:6379/0` |

### 2.3 症状对照

| 现象 | 原因 | 怎么办 |
|---|---|---|
| 改了 `.env`，容器行为没变 | 容器不读 `.env` | 改 compose 的 `environment` 后 `make docker-up` |
| 容器日志里数据库连接被拒绝 | 用了 `127.0.0.1`，那是容器自己 | 主机名换成服务名 `postgres` / `redis` |
| 宿主机上后端连不上数据库 | 容器端口只绑回环，需要用 `127.0.0.1:5432` | 这条 `.env` 写法是对的，别改 |

---

## 3. 端口与暴露面

compose 里三个服务的端口映射**全部只绑回环**：

| 服务 | 映射 | 宿主机可访问范围 |
|---|---|---|
| postgres | `127.0.0.1:5432:5432` | 仅本机 |
| redis | `127.0.0.1:6379:6379` | 仅本机 |
| mcp-hub | `127.0.0.1:9090:9090` | 仅本机 |

### 3.1 为什么 9090 只绑回环

管理接口（`/api/*`）能增删下游 server、增删下游凭据、开关工具 —— 它等于
半个 shell。把它放到局域网上，等于把一个能读你所有下游凭据的接口交给同网段
的任何人。回环绑定是默认的安全姿态。

### 3.2 要暴露到外网，必须配 `MCPHUB_API_KEY`

`MCPHUB_API_KEY` 非空时，`/api/*` 的每个请求都要带凭据（`X-API-Key` 头
或 `Authorization: Bearer`），否则 401。留空则完全不校验 —— 这在只监听
`127.0.0.1` 时没问题，一旦对外就是裸奔。

要在容器里启用它，在 compose 的 `environment` 里加一项：

```yaml
environment:
  MCPHUB_API_KEY: "换成你自己的密钥"
```

宿主机形态则写在 `mcp-hub/.env`：

```
MCPHUB_API_KEY=换成你自己的密钥
```

改完重启生效（配置没有热加载，改配置 = 改文件 + 重启）。

> 两套凭据，别混：
> - `MCPHUB_API_KEY` —— 保护**管理接口** `/api/*`。
> - **接入 key**（管理界面里创建，对应 `/api/keys`）—— 保护 **MCP 端点**
>   `/mcp`，给外部 AI 客户端用。库里一把都没有时 `/mcp` 不校验；一旦创建过
>   第一把，匿名请求一律 401。
>
> 对外暴露时两套都要配：前者挡住"谁能改配置"，后者挡住"谁能调工具"。

### 3.3 容器里 `MCPHUB_ADDR` 必须是 `:9090`

容器内 `MCPHUB_ADDR=":9090"` 监听**全部网卡**，这是端口映射能打进来的前提。
写成 `127.0.0.1:9090` 的话，服务只监听容器自己的 loopback，宿主机映射不进来
—— 现象是"容器活着、健康检查通过、外面连不上"。

对外可见范围由 compose 的 `ports` 收口（`127.0.0.1:9090:9090`），而不是由
容器内的监听地址决定。宿主机形态相反：`.env` 里建议写 `127.0.0.1:9090`，
别在宿主机上裸奔。

---

## 4. 数据与持久化

### 4.1 三个卷

| 卷 | 挂在哪 | 装什么 | 什么时候有用 |
|---|---|---|---|
| `pgdata` | postgres 容器 `/var/lib/postgresql` | PostgreSQL 数据目录 | 用 PostgreSQL 时是全部数据 |
| `redisdata` | redis 容器 `/data` | Redis 持久化文件 | Redis 做缓存时（缓存丢了不影响正确性，只影响命中率） |
| `hubdata` | mcp-hub 容器 `/data` | 引擎的 dataDir（进程工作目录） | **用 PostgreSQL 时基本是空的**；切到 SQLite 时库文件落在这里，保证换后端不丢数据 |

`hubdata` 之所以存在而不是删掉：引擎的 dataDir 取进程工作目录（镜像里是
`/data`），SQLite 模式下的 `mcp-hub.db` 就写在那儿。compose 把它挂成卷，
是为了"换后端"这件事不丢数据。日常用 PostgreSQL 部署时它确实基本是空的。

### 4.2 `make db-reset CONFIRM=1` 会删掉全部卷

```bash
make db-reset CONFIRM=1
```

它执行的是整个 compose 项目的 `down -v`，也就是**删掉全部三个命名卷**
（`pgdata` / `redisdata` / `hubdata`），不是只删数据库那一个。这是刻意的：
这条命令的定位是"我不在乎里面的东西了"，而 `hubdata` 里放的是 SQLite 模式
的库文件，同样是数据 —— 不假装能只删一半。

必须显式带 `CONFIRM=1`，否则命令会打印提示并退出。

### 4.3 两个后端的数据互不相通

PostgreSQL 与 SQLite 各有各的迁移树，切过去**不会迁移任何东西**。
表结构在首次连接时自动同步，所以 PG 侧要先建好空库。

---

## 5. 镜像构建

### 5.1 构建上下文必须是项目根

```bash
make docker-build        # = docker compose build mcp-hub
```

compose 里写死了：

```yaml
build:
  context: .                    # 项目根
  dockerfile: mcp-hub/Dockerfile
```

原因在 Dockerfile 的 `COPY` 指令里：镜像要同时装入 **Go 后端**和 **Node
前端**，所以要 `COPY mcp-hub/…` 和 `COPY mcp-hub-admin/…` 两条路径。
上下文指向 `mcp-hub/` 的话，`mcp-hub-admin/` 根本不在上下文里，构建直接失败。

配套的 `.dockerignore` 也在项目根，负责把上下文从几十 MB 收窄到几百 KB：
`mcp-hub-admin/node_modules`、`mcp-hub/bin`、`mcp-hub/logs`、`**/.git`、
`**/.env` 全部排除。

手工构建（不走 compose）时上下文同理：

```bash
docker build -f mcp-hub/Dockerfile -t mcp-hub:local .
```

### 5.2 三个阶段

| 阶段 | 做的事 | 产物 |
|---|---|---|
| `ui` | `npm ci` + `npm run build`（= `tsc -b && vite build`） | 前端 `dist/` |
| `build` | `go build -trimpath -ldflags='-s -w'` | `/out/mcp-hub` |
| 运行 | alpine + ca-certificates + tzdata，非 root 用户 `mcphub`，`WORKDIR /data` | 镜像 |

运行阶段装 `ca-certificates` 不是可选项：下游 MCP server 基本都是 HTTPS，
缺根证书会全线失败，报错是 `x509: certificate signed by unknown authority`
—— 不会有人从这个信息联想到"镜像里少装了个包"。

### 5.3 换源：`BASE_REGISTRY`

基础镜像默认走国内源（`docker.m.daocloud.io`）。原因是 dockerd 跑在
OrbStack 这类 VM 里，**不吃宿主机的 HTTP_PROXY**，直连 Docker Hub 实测
25 秒零进度。

```bash
BASE_REGISTRY=docker.io make docker-build
```

注意必须是**registry 主机名**，不是完整前缀 —— Dockerfile 里统一拼
`/library/`。compose 已经把它做成了构建参数：

```yaml
args:
  BASE_REGISTRY: ${BASE_REGISTRY:-docker.m.daocloud.io}
```

所以在命令行给一个环境变量就够，不必改文件。同理还有两个可换的源：

| 参数 | 默认 | 作用 |
|---|---|---|
| `BASE_REGISTRY` | `docker.m.daocloud.io` | 三个基础镜像（node / golang / alpine）的来源 |
| `NPM_REGISTRY` | `https://registry.npmmirror.com` | 前端依赖源（Dockerfile 里的构建参数，compose 未透出，手工 `docker build` 时用 `--build-arg`） |
| `GOPROXY` | `https://goproxy.cn,direct` | Go 模块源（同上） |

### 5.4 管理界面怎么进二进制：`go:embed`

界面**不是**运行时去读的静态目录，它在构建期就被编进二进制：

```
mcp-hub-admin/  --npm run build-->  dist/
        ↓ 拷进
mcp-hub/internal/webassets/dist/
        ↓ go:embed all:dist
   编译进二进制
```

宿主机上的做法是 `make ui`：

```bash
make ui          # 构建前端并把产物拷进 mcp-hub/internal/webassets/dist/
make build       # 之后 go:embed 把它编进二进制
```

`make ui` 用"先建暂存目录、再整体换上去"的方式替换产物，避免中途只剩
`.placeholder` 的窗口里有人 `go build`，嵌出一个没有界面的二进制。目录里
预置的 `.placeholder` 是 `go:embed` 的兜底（embed 不接受空目录），它不会
被服务出去。

**镜像里的界面是另一条路**：Dockerfile 的第一阶段在容器内现建前端，
产物经 `COPY --from=ui` 覆盖到 `internal/webassets/dist/`。宿主机上
`make ui` 留下的陈旧产物被 `.dockerignore` 排除掉了 —— 所以镜像里
**总是**是当前代码的界面，不会出现"前端改了、镜像重建了，界面没变"。

没跑过 `make ui` 的二进制**照样能跑**：`/api` 与 `/mcp` 完全正常，只是
`/` 是干净的 404，启动横幅会说明这一点。想让容器形态带界面不需要在宿主机
上跑 `make ui`，镜像构建过程本身就带了。

### 5.5 镜像里没有桌面形态

`cmd/mcp-hub-desktop` 不进镜像：它需要 Cocoa/GTK 的 webview，容器里没有
窗口系统可以承载。需要桌面版就在宿主机上 `make build-desktop`（或
`make build-desktop-browser`，后者无 cgo、可自由交叉编译）。

---

## 6. 健康检查与排错

### 6.1 `/healthz` 与 `/readyz` 的区别

| 端点 | 回答什么 | 什么时候不 OK |
|---|---|---|
| `/healthz` | 进程还活着吗 | 进程没起来（连不上） |
| `/readyz` | 有没有可用的工具 | **一个工具都没有时返回 503** |

`/readyz` 返回 503 是**刻意**的：它描述的是"就绪"，不是"活着"。没有可用
下游（`toolCount == 0`）时就 503，并且响应里带上 `toolCount` 与
`unhealthyNodes` 方便定位。

所以：**用 `/healthz` 做存活探针，不要用 `/readyz`。** 把 `/readyz` 当健康检查
用的话，下游全部挂掉的瞬间，容器会被判定为不健康并被重启 —— 而重启并不能
修好下游。

compose 的健康检查正是这么写的：

```yaml
healthcheck:
  test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:9090/healthz"]
```

### 6.2 三个服务的健康检查

| 服务 | 检查 | 间隔 / 超时 / 重试 | 备注 |
|---|---|---|---|
| postgres | `pg_isready -U mcphub -d mcphub` | 3s / 3s / 10 | |
| redis | `redis-cli ping` | 3s / 3s / 10 | |
| mcp-hub | `wget http://127.0.0.1:9090/healthz` | 5s / 3s / 10 | `start_period: 15s` |

`start_period: 15s` 是给**首次启动跑数据库迁移**留的起步时间：这段时间里
探针失败不算 unhealthy，15 秒之后才算。

mcp-hub 服务 `depends_on` postgres 与 redis，且条件是 `service_healthy` ——
数据库和缓存先健康，后端容器才启动。

### 6.3 常见症状

| 现象 | 原因 | 处理 |
|---|---|---|
| 后端起不来，报"地址已被使用" | 容器形态的后端还占着 9090（`make docker-up` 起的） | `make docker-down`，或 `make dev-stop` 停宿主机那侧 |
| `make db-up` 之后 9090 突然被占 | 直接 `docker compose up` 不点名服务，把后端容器也拉起来了 | 用 `make db-up`（显式点名 postgres redis） |
| 容器日志：数据库连接被拒绝 | 连接串里的 `127.0.0.1` 在容器里是容器自己 | 主机名用服务名 `postgres` / `redis` |
| 改了 `.env` 容器没变化 | 容器不读 `.env` | 改 compose 的 `environment` 后 `make docker-up` |
| 容器活着但宿主机连不上 9090 | 容器内 `MCPHUB_ADDR` 写成了 `127.0.0.1:9090` | 必须是 `:9090`（监听全部网卡） |
| `/readyz` 返回 503 | 没有可用下游（工具数为 0） | 不是故障。看 `make docker-logs` 里的探活结果 |
| 构建卡在拉基础镜像 | 直连 Docker Hub 拉不动 | `BASE_REGISTRY=docker.io make docker-build` |
| 镜像重建后界面没变 | 用的是宿主机陈旧产物 | 镜像构建期现产前端；确认没把 `internal/webassets/dist` 拷进上下文 |
| 启动报"配置错误" | 类型不对 / 枚举越界 / 连接串与后端不匹配 | 错误一次报完，逐条改；清单见 `mcp-hub env` |

排错时先看日志：

```bash
make docker-logs    # 后端容器
make db-logs        # 全部服务
make dev-logs       # 宿主机形态的前后端
```

---

## 7. 配置优先级与 `.env` 的查找

### 7.1 优先级

```
进程环境变量  >  .env 文件  >  内置默认值
```

进程环境变量最高，所以：

```bash
MCPHUB_ADDR=:8080 mcp-hub serve        # 一次性覆盖 .env 里的值
```

`.env` 里已经有值也不会被进程环境变量吃掉 —— 这就是**容器 / K8s 注入的变量
永远赢过文件**的原因。compose 的 `environment`、K8s 的 `env` 落到容器里
都是进程环境变量，因此部署参数永远压过 `.env`（而容器里本来也没有
`.env`）。

`.env` 只加载**第一个命中的**文件，查找顺序：

```
$MCPHUB_ENV_FILE  →  ./.env  →  可执行文件旁的 .env
```

`MCPHUB_ENV_FILE` 只能来自进程环境变量 —— 它决定"去哪找配置"，不能反过来
由配置决定。

> 桌面形态（`mcp-hub-desktop`）多一条锚点：`<应用数据目录>/.env`，而且
> **不回退**到当前工作目录。详见 `../cmd/mcp-hub-desktop/README.md`。

### 7.2 为什么宿主机形态必须在 `mcp-hub/` 目录下跑

命令行形态的 dataDir 取**进程当前工作目录**，配置文件查找也是从 `./.env`
找起。从项目根直接跑 `./mcp-hub/bin/mcp-hub serve`，工作目录是项目根，
找不到 `mcp-hub/.env` —— 程序不会报错，而是**静默退回"零配置"**：
SQLite + 内存缓存，而 `.env` 里明明写着 PostgreSQL。

`make run` 内部会 `cd mcp-hub` 再执行二进制，就是为了这个。

### 7.3 全部配置项

```bash
mcp-hub env          # 打印全部配置项：变量名 / 默认值 / 说明
```

这份清单由代码里的声明表驱动，同时负责绑定、启动校验和 `env` 的输出，
所以**不存在"文档与实现两套"**的问题。配置项以 `mcp-hub env` 的输出为准。

几个高频项：

| 变量 | 默认 | 说明 |
|---|---|---|
| `MCPHUB_DB` | 空 | `postgres://…` 走 PostgreSQL，文件路径走 SQLite，留空则建 `mcp-hub.db` |
| `MCPHUB_DB_DRIVER` | `auto` | `auto` \| `postgres` \| `sqlite` |
| `MCPHUB_CACHE` | 空 | `redis://…` 走 Redis，留空走进程内缓存 |
| `MCPHUB_CACHE_DRIVER` | `auto` | `auto` \| `redis` \| `memory` |
| `MCPHUB_ADDR` | `:9090` | 监听地址；只写 `:9090` 等于监听全部网卡 |
| `MCPHUB_API_KEY` | 空 | 管理接口鉴权密钥；留空不校验 |
| `MCPHUB_ENV_FILE` | 空 | 显式指定配置文件路径 |
| `MCPHUB_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `MCPHUB_PROBE_INTERVAL` | `45s` | 周期探活间隔，`0` 关闭 |

`.env` 里写了不认识的 `MCPHUB_*` 名字，启动时会**点名提醒**（不会静默
忽略），所以把 `MCPHUB_EMBED_DIMS` 拼成 `MCPHUB_EMBED_DIM` 这种错不会只剩
一句"改了却没生效"。

### 7.4 配置没有热加载

改配置 = 改文件 + 重启。管理界面上没有配置入口，库里也不存配置，
所以不存在"界面改完被重启打回"的情况。

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

---

## 附：事实来源

本文档的每条结论都来自仓库里可核对的地方，不是推测：

| 事实 | 出处 |
|---|---|
| 三个服务的镜像、端口、卷、健康检查、环境变量 | `../../docker-compose.yml`（项目根，在本仓库之外） |
| 各 make 目标的语义与互斥关系 | `../../Makefile`（项目根，在本仓库之外） |
| 构建上下文、三阶段、基础镜像参数、运行阶段 | `../Dockerfile` |
| 构建上下文收敛清单 | `../../.dockerignore`（项目根，在本仓库之外） |
| 配置项清单、优先级、查找顺序 | `../internal/config/config.go`、`../cmd/mcp-hub/README.md` |
| `/healthz` 与 `/readyz` 的语义 | `../internal/httpd/server.go` |
| 管理接口鉴权与接入 key | `../internal/api/api.go` |
| 数据目录取工作目录 | `../cmd/mcp-hub/serve.go` |
