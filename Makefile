# mcp-hub 的构建入口。
#
# 这个 Makefile 站在**项目根**，而不是 Go 模块目录里。三条理由：
#
#   1. 它编排的东西本来就跨目录：后端（mcp-hub）、前端（mcp-hub-admin）、
#      以及管着两者的 docker-compose.yml —— 后两者都在模块目录之外。
#   2. 路径只有一套。放在 mcp-hub/ 里就要写 ../mcp-hub-admin、
#      ../docker-compose.yml 这种带 `..` 的路径，读者得反推一层才知道是谁。
#   3. go 命令对模块外的当前目录无感，靠 `go -C $(MODULE)` 就能保持
#      "在模块里执行"的语义，不必真的 cd（`-C` 还顺带避免了
#      "cd 失败但后面的命令照跑"这类配方里的经典事故）。
#
# 产物仍然落在 Go 模块里 —— mcp-hub/bin/。理由反过来：它们是这个程序的产物，
# 和 go.mod 放一起才不被当成分层之外的散落物，mcp-hub/.gitignore 的 `/bin/`
# 也天然盖住了它。**编排脚本跟着编排对象走，构建产物跟着代码走。**
#
# 从哪个目录执行都可以：下面所有路径都由本文件位置推导，不依赖当前工作目录。

GO    ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOFMT ?= $(shell dirname $(GO))/gofmt
PKG   := ./...

# ─── 路径 ────────────────────────────────────────────────────────
# 用 MAKEFILE_LIST 推导根，而不是 $(CURDIR)。区别在 `make -f /path/Makefile`
# 或从子目录 `make -C ..`：那时 CURDIR 未必是本文件所在目录，而所有相对路径
# 都是相对 CURDIR 解析的，结果是产物撒到意想不到的地方、还未必报错。
ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

MODULE       := $(ROOT)/mcp-hub
FRONTEND_DIR := $(ROOT)/mcp-hub-admin

BIN_DIR     ?= $(MODULE)/bin
BIN         ?= $(BIN_DIR)/mcp-hub
BIN_DESKTOP ?= $(BIN_DIR)/mcp-hub-desktop

# 递归调用自己时带上 -C，否则 `make -f /abs/Makefile` 这种用法下
# 子 make 会在错误的目录里找 Makefile。
MAKE_SELF := $(MAKE) -C $(ROOT) -f $(ROOT)/Makefile --no-print-directory

# 前端产物在 Go 源码树里的落点。
#
# 必须落在这里而不是直接嵌 $(FRONTEND_DIR)/dist：go:embed 不能跨 `..`。
# 目录里预置了一个 .placeholder，让"还没跑过 make ui"时 go:embed 也有
# 东西可嵌（embed 不接受空目录）；它不会被服务出去。
WEBASSETS_DIR := $(MODULE)/internal/webassets/dist

# make dev 的运行时文件（日志 / pid）。放模块里不放根：
# 根目录只应该有编排文件与两个子项目，跑起来才产生的文件不配进那一层。
RUN_DIR      := $(MODULE)/logs
BACKEND_LOG  := $(RUN_DIR)/backend.log
FRONTEND_LOG := $(RUN_DIR)/frontend.log
BACKEND_PID  := $(RUN_DIR)/backend.pid
FRONTEND_PID := $(RUN_DIR)/frontend.pid

# ─── 开发环境编排 ────────────────────────────────────────────────
# 数据库走 docker compose，后端 serve 只读 .env 不收参数，前端是独立的
# Vite 工程。三者用 make dev 一键拉起。
DOCKER       ?= $(shell command -v docker 2>/dev/null || echo /usr/local/bin/docker)
COMPOSE_FILE ?= $(ROOT)/docker-compose.yml
COMPOSE      := $(DOCKER) compose -f $(COMPOSE_FILE)

# compose 只管 postgres 与 redis；后端容器（mcp-hub 服务）走 docker-* 目标。
# 这两条路互斥，所以每个 up/stop 都显式点名服务，绝不留下"顺手多起一个"的可能。
DB_SERVICES := postgres redis

NPM ?= $(shell command -v npm 2>/dev/null || echo npm)
# 直接执行 vite 本体而不是 npm run dev：拿到的 PID 就是 vite 进程本身，
# dev-stop 一个 kill 就能干净地停掉（npm 会隔一层 sh，信号经常传不下去）。
VITE_BIN := $(FRONTEND_DIR)/node_modules/.bin/vite

# macOS/arm64 上把 cgo 的部署目标对齐，否则 Wails 构建会吐三十行 ld 告警。
#
# 起因：Wails 自己的 #cgo LDFLAGS 写的是 -mmacosx-version-min=10.13，
# 而 Go 在 darwin/arm64 上是按 SDK 默认版本（新系统上就是 15.0）生成
# 它自己那些 cgo 目标文件的。两边不一致时 ld 每个目标文件报一句
# "object file was built for newer macOS version (15.0) than being linked (11.0)"，
# 三十行警告足以把真正的编译错误淹掉——实测过一次，很难看。
#
# 对齐到 11.0 是因为那就是 Go 在 arm64 上原本的默认值：产物目标版本不变，
# 只是不再自相矛盾。darwin/amd64 两边本来就都是 10.13，其他平台无关，
# 所以只在 darwin/arm64 上设。
ifeq ($(shell $(GO) env GOOS GOARCH),darwin arm64)
CGO_MIN_FLAG := -mmacosx-version-min=11.0
endif

BACKEND_PORT  ?= 9090
FRONTEND_PORT ?= 5273

# 前端只绑回环 IPv4。
#
# 不显式给 host 时 vite 绑的是字面量 "localhost"，而 macOS 上 Node 解析
# localhost 优先拿到 ::1 —— 于是它只监听 [::1]:5273。后果是下面那些用
# 127.0.0.1 的探测全部连不上：就绪检查白等 15 秒后打 ✗、dev-status 报
# "前端不可用"、横幅里印出来的地址点开是空白，而服务其实是好的。
# 这种"明明起来了却报没起来"最难查，所以不靠解析顺序，直接定死。
FRONTEND_HOST ?= 127.0.0.1

BACKEND_URL   ?= http://127.0.0.1:$(BACKEND_PORT)
FRONTEND_URL  ?= http://$(FRONTEND_HOST):$(FRONTEND_PORT)

.PHONY: all build build-desktop build-desktop-browser release test test-sqlite test-race vet fmt fmtcheck tidy vendor check clean \
        run run-desktop run-desktop-browser servers probe \
        ui ui-clean \
        dev stop-apps dev-stop dev-status dev-logs \
        db-up db-down db-reset db-logs \
        docker-build docker-up docker-down docker-logs

all: check build

build:
	@mkdir -p $(BIN_DIR)
	$(GO) -C $(MODULE) build -o $(BIN) ./cmd/mcp-hub

# 桌面形态的入口。它复用同一个引擎，差别只在"界面画在哪"。
#
# 两种壳，由构建标签二选一：
#
#   build-desktop          Wails v3 原生窗口。需要 cgo
#                          （macOS 走 Cocoa/WebKit，Linux 走 GTK 头文件），
#                          因此不能 CGO_ENABLED=0，也不能在没装 GTK 的
#                          机器上交叉编译到 Linux。
#   build-desktop-browser  打开系统默认浏览器。无 cgo，可自由交叉编译。
#
# 两者的业务行为完全一致——数据库、探活、MCP 端点都是同一份实现。
build-desktop:
	@mkdir -p $(BIN_DIR)
	CGO_CFLAGS="$(CGO_MIN_FLAG)" CGO_LDFLAGS="$(CGO_MIN_FLAG)" $(GO) -C $(MODULE) build -tags wails -o $(BIN_DESKTOP) ./cmd/mcp-hub-desktop

build-desktop-browser:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) -C $(MODULE) build -o $(BIN_DESKTOP)-browser ./cmd/mcp-hub-desktop

# release: 带上内嵌界面的两个二进制。这是"发给别人"的那种构建。
release: ui build build-desktop

# 默认口径是 PostgreSQL（storetest 的 Driver() 不认识 MCP_HUB_TEST_DRIVER
# 以外的值时按 postgres 走），所以本机没有 Docker 时这个目标会失败。
test:
	$(GO) -C $(MODULE) test $(PKG) -count=1

# 本机没跑 PostgreSQL 时的可跑口径：SQLite 走临时目录，零外部依赖。
# 两条驱动跑的是同一套用例（storetest 按驱动给 DSN），所以这不是"降级测试"。
test-sqlite:
	MCP_HUB_TEST_DRIVER=sqlite $(GO) -C $(MODULE) test $(PKG) -count=1

# 竞态检测单独一个目标：它会让测试慢一个数量级，
# 不适合挂在默认流程上，但并发相关的改动必须跑它。
test-race:
	$(GO) -C $(MODULE) test $(PKG) -count=1 -race

vet:
	$(GO) -C $(MODULE) vet $(PKG)

fmt:
	$(GOFMT) -w $(MODULE)

# 确认没有未格式化的文件（CI 用）。注意是 gofmt -l，不是 go fmt -l。
fmtcheck:
	@out=$$($(GOFMT) -l $(MODULE)); \
	if [ -n "$$out" ]; then echo "以下文件未格式化:"; echo "$$out"; exit 1; fi; \
	echo "格式检查通过"

tidy:
	$(GO) -C $(MODULE) mod tidy

# 生成 vendor/ 以便离线构建。默认不提交——go.sum 已锁定版本，
# 只有需要完全离线或审计依赖时才执行。
vendor:
	$(GO) -C $(MODULE) mod vendor

check: fmtcheck vet test

# 只清产物，不碰前端依赖、不碰数据目录。
clean:
	rm -rf $(BIN_DIR)

# ─── 管理界面（前端产物 → 二进制）──────────────────────────────
#
# 这一步是"两种部署形态共用一份界面"的全部代价：前端只构建一次，
# 产物拷进 Go 源码树后被 go:embed 打进二进制。之后无论跑命令行形态
# 还是桌面形态，界面都从同一个地方来——不需要额外的静态文件服务器，
# 也不会出现"二进制与 dist 版本对不上"。
#
# 纯 API 部署（不带界面）可以完全跳过这一步：Available() 会如实
# 报告没有产物，引擎就不挂界面，其余功能一字不变。

# 产物替换一律走"先建暂存目录、再整体换上去"，不就地删旧文件。
#
# 两个原因，第二个才是主因：
#   - 就地删一次要处理四十来个路径（vite 的 chunk 名带内容哈希，每次构建
#     整批都变），整体替换只要删一个目录。
#   - 就地删+拷之间有一个窗口，$(WEBASSETS_DIR) 里只剩 .placeholder。
#     如果这时有人恰好在 `go build`，go:embed 会安静地嵌出一个没有界面的
#     二进制——它照样能跑、照样返回 200，只是界面不见了。整体替换没有这个
#     窗口：dist 要么是旧的完整产物，要么是新的完整产物。
#
# 暂存目录必须由"当前 dist 里的 .placeholder"喂养，而不是新写一份——
# 那份文件是 go:embed 在"没产物"时的唯一兜底，内容变了就失去意义了。
STAGING_DIR := $(WEBASSETS_DIR).staging

# ui: 构建前端并拷贝产物。
ui:
	@test -d $(FRONTEND_DIR) || { echo "找不到前端工程: $(FRONTEND_DIR)"; exit 1; }
	@if [ ! -x $(VITE_BIN) ]; then \
		echo "安装前端依赖（首次较慢）…"; \
		$(NPM) --prefix $(FRONTEND_DIR) install || exit 1; \
	fi
	@echo "构建前端…"
	@$(NPM) --prefix $(FRONTEND_DIR) run build || exit 1
	@test -f $(FRONTEND_DIR)/dist/index.html || { echo "构建结束但 $(FRONTEND_DIR)/dist/index.html 不存在"; exit 1; }
	@test -f $(WEBASSETS_DIR)/.placeholder || { echo "缺少 $(WEBASSETS_DIR)/.placeholder —— go:embed 需要它兜底"; exit 1; }
	@rm -rf $(STAGING_DIR) && mkdir -p $(STAGING_DIR)
	@cp -R $(FRONTEND_DIR)/dist/. $(STAGING_DIR)/
	@cp $(WEBASSETS_DIR)/.placeholder $(STAGING_DIR)/.placeholder
	@rm -rf $(WEBASSETS_DIR)
	@mv $(STAGING_DIR) $(WEBASSETS_DIR)
	@echo "界面已内嵌就绪: $(WEBASSETS_DIR)（$$(find $(WEBASSETS_DIR) -type f | wc -l | tr -d ' ') 个文件）"

# ui-clean: 删掉产物，回到"这个二进制不带界面"的状态。
# 只动 $(WEBASSETS_DIR)，不碰别处；同样保留 .placeholder。
ui-clean:
	@test -f $(WEBASSETS_DIR)/.placeholder || { echo "缺少 $(WEBASSETS_DIR)/.placeholder —— go:embed 需要它兜底"; exit 1; }
	@rm -rf $(STAGING_DIR) && mkdir -p $(STAGING_DIR)
	@cp $(WEBASSETS_DIR)/.placeholder $(STAGING_DIR)/.placeholder
	@rm -rf $(WEBASSETS_DIR)
	@mv $(STAGING_DIR) $(WEBASSETS_DIR)
	@echo "界面产物已清除（$(WEBASSETS_DIR) 只剩 .placeholder）"

# ─── 运行 ────────────────────────────────────────────────────────
#
# 下面几条都要在**模块目录**里跑二进制，不是路径洁癖：
# 命令行形态的 dataDir 取进程工作目录（见 cmd/mcp-hub/serve.go），
# 而且配置文件查找是 `$MCPHUB_ENV_FILE → ./.env → 可执行文件旁的 .env`。
# 从项目根直接跑 ./mcp-hub/bin/mcp-hub，会找不到 mcp-hub/.env，
# 静默退回"零配置"——SQLite + 内存缓存——而 .env 里写着 PostgreSQL。
# 用 exec 让信号直达二进制，Ctrl+C 不会被中间那层 sh 吃掉。

# serve 的全部配置来自 .env，不收任何命令行参数。
run: build
	cd $(MODULE) && exec $(BIN) serve

# 起桌面形态（Wails 原生窗口）。没跑过 make ui 时会开出一个 404 的窗口——
# 那不是故障，是"这个二进制里没有界面"，日志与窗口标题里会说明。
# 这条不需要 cd：桌面端的数据目录取自 os.UserConfigDir()，与 cwd 无关。
run-desktop: build-desktop
	$(BIN_DESKTOP)

# 浏览器壳：不需要 GUI 工具链，适合 CI / 容器 / 交叉编译验证。
run-desktop-browser: build-desktop-browser
	$(BIN_DESKTOP)-browser

# --db 缺省走配置层（同一个 viper：进程环境变量 > .env），
# 程序自己会加载 ./.env，不需要在这里 source 一遍。
servers: build
	cd $(MODULE) && exec $(BIN) servers

# 只探活不落库，用来快速确认某个地址能不能用。
probe:
	@test -n "$(URL)" || (echo "用法: make probe URL=https://host/mcp"; exit 2)
	cd $(MODULE) && exec $(BIN) probe --url "$(URL)"

# ─── 一键开发环境 ────────────────────────────────────────────────

## dev: 一键启动数据库( docker compose ) + 后端( :9090 ) + 前端( :5273 )，
##      阻塞跟踪日志。Ctrl+C 结束前后端（数据库保留），彻底停止用 make dev-stop。
##      已在运行的部分自动跳过，可安全重复执行。
dev:
	@test -f $(MODULE)/.env || { echo "缺少 $(MODULE)/.env —— 参照 .env.example 配置后重试"; exit 1; }
	@test -x "$(DOCKER)" || { echo "找不到 docker —— Docker Desktop 需要先启动"; exit 1; }
	@mkdir -p $(RUN_DIR)
	@$(MAKE_SELF) build
	@echo "拉起数据库（docker compose）…"
	@# 服务名必须显式列出，见 DB_SERVICES 的注释。
	@$(COMPOSE) up -d --wait --wait-timeout 60 $(DB_SERVICES)
	@if [ -f $(BACKEND_PID) ] && kill -0 $$(cat $(BACKEND_PID)) 2>/dev/null; then \
		echo "后端已在运行（pid $$(cat $(BACKEND_PID))），跳过"; \
	else \
		echo "启动后端 $(BACKEND_URL)（日志: $(BACKEND_LOG)）"; \
		( cd $(MODULE) && exec nohup $(BIN) serve >> $(BACKEND_LOG) 2>&1 ) & echo $$! > $(BACKEND_PID); \
	fi
	@printf '等待后端就绪 '; i=0; \
	while [ $$i -lt 60 ]; do \
		curl -sf $(BACKEND_URL)/healthz >/dev/null 2>&1 && break; \
		printf '.'; sleep 0.5; i=$$((i+1)); \
	done; \
	if [ $$i -lt 60 ]; then echo ' ✓'; else \
		echo ' ✗ 30 秒内未就绪，最近日志:'; tail -n 40 $(BACKEND_LOG); exit 1; fi
	@if [ -f $(FRONTEND_PID) ] && kill -0 $$(cat $(FRONTEND_PID)) 2>/dev/null; then \
		echo "前端已在运行（pid $$(cat $(FRONTEND_PID))），跳过"; \
	else \
		if [ ! -x $(VITE_BIN) ]; then \
			echo "安装前端依赖（首次较慢）…"; \
			$(NPM) --prefix $(FRONTEND_DIR) install; \
		fi; \
		echo "启动前端 $(FRONTEND_URL)（日志: $(FRONTEND_LOG)）"; \
		( cd $(FRONTEND_DIR) && exec nohup $(VITE_BIN) $(VITE_ARGS) --host $(FRONTEND_HOST) >> $(FRONTEND_LOG) 2>&1 ) & echo $$! > $(FRONTEND_PID); \
	fi
	@printf '等待前端就绪 '; i=0; \
	while [ $$i -lt 30 ]; do \
		curl -sf $(FRONTEND_URL) >/dev/null 2>&1 && break; \
		printf '.'; sleep 0.5; i=$$((i+1)); \
	done; \
	if [ $$i -lt 30 ]; then echo ' ✓'; else echo ' ✗（看日志: make dev-logs）'; fi
	@echo ""
	@echo "  MCP 网关   $(BACKEND_URL)/mcp"
	@echo "  管理接口   $(BACKEND_URL)/api/overview"
	@echo "  管理后台   $(FRONTEND_URL)"
	@echo ""
	@echo "  Ctrl+C 结束前后端（数据库保留）  彻底停止: make dev-stop  状态: make dev-status"
	@echo ""
	@trap '$(MAKE_SELF) stop-apps' INT TERM; \
	tail -n 20 -f $(BACKEND_LOG) $(FRONTEND_LOG)

## stop-apps: 只停前后端，不动数据库。
stop-apps:
	@for f in $(BACKEND_PID) $(FRONTEND_PID); do \
		if [ -f $$f ]; then \
			pid=$$(cat $$f 2>/dev/null); \
			if [ -n "$$pid" ] && kill -0 $$pid 2>/dev/null; then \
				echo "停止 $$f（pid $$pid）"; kill $$pid 2>/dev/null; \
			fi; \
			rm -f $$f; \
		fi; \
	done
# 兜底：pid 文件丢失但端口还被残留进程占着（上次终端直接关掉之类）。
# 这两个端口是本项目的开发端口约定（.env / vite.config.ts），按约定清理。
	@for port in $(BACKEND_PORT) $(FRONTEND_PORT); do \
		pids=$$(lsof -ti tcp:$$port 2>/dev/null); \
		if [ -n "$$pids" ]; then echo "清理端口 $$port 上的残留进程: $$pids"; kill $$pids 2>/dev/null; fi; \
	done

## dev-stop: 停前后端 + 数据库（容器与数据保留）。
#
# 只停 postgres / redis。后端**容器**（docker-* 那条路）不动——
# 两个入口各自负责自己拉起的东西，一条命令顺手停掉另一条路的服务
# 属于"猜用户意图"，而猜错的代价是别人以为自己的容器还在跑。
dev-stop: stop-apps
	@$(COMPOSE) stop $(DB_SERVICES)
	@echo "已全部停止（数据保留）。恢复: make dev  清空数据: make db-reset CONFIRM=1"

## dev-status: 三件套健康状态一览。
dev-status:
	@printf '后端  %s  ' $(BACKEND_URL); \
	curl -sf -m 2 $(BACKEND_URL)/healthz >/dev/null 2>&1 && echo "✓ healthz 正常" || echo "✗ 不可用"
	@printf '前端  %s  ' $(FRONTEND_URL); \
	curl -sf -m 2 $(FRONTEND_URL) >/dev/null 2>&1 && echo "✓ 可访问" || echo "✗ 不可用"
	@$(COMPOSE) ps

## dev-logs: 跟踪前后端日志（不启停任何服务）。
dev-logs:
	@test -f $(BACKEND_LOG) -o -f $(FRONTEND_LOG) || { echo "还没有日志 —— 先 make dev"; exit 1; }
	tail -n 40 -f $(BACKEND_LOG) $(FRONTEND_LOG)

# ─── 数据库（docker compose）─────────────────────────────────────

## db-up: 启动 postgres + redis，等健康检查通过。
#
# 服务名必须显式列出（$(DB_SERVICES)）。compose 文件里现在还有 mcp-hub 这个
# 服务，不写服务名就会把它一起拉起来、占掉 9090，然后宿主机上 `make dev`
# 的后端起不来，报"端口被占用"——而那个报错完全不会让人联想到是 db-up 干的。
db-up:
	@test -x "$(DOCKER)" || { echo "找不到 docker —— Docker Desktop 需要先启动"; exit 1; }
	$(COMPOSE) up -d --wait --wait-timeout 60 $(DB_SERVICES)
	@echo "数据库就绪: postgres@5432  redis@6379"

## db-down: 停止并移除数据库容器（命名卷保留，数据不丢）。
db-down:
	$(COMPOSE) down $(DB_SERVICES)

## db-reset: ⚠ 清空数据库（连同数据卷一起删）。需要显式确认。
#
# 注意这里是整个 compose 项目的 `down -v`，不是只针对两个服务——
# `-v` 会删掉 compose 里声明的**全部**命名卷（pgdata / redisdata / hubdata）。
# 这是刻意的：这条命令的定位就是"我不在乎里面的东西了"，而 hubdata 里放的是
# SQLite 模式的库文件，同样是数据。不假装能只删一半。
db-reset:
	@test "$(CONFIRM)" = "1" || { echo "危险操作：将删除全部数据卷（pgdata / redisdata / hubdata）。确认请执行: make db-reset CONFIRM=1"; exit 1; }
	$(COMPOSE) down -v
	@echo "数据卷已删除。"

db-logs:
	$(COMPOSE) logs -f --tail=100

# ─── 容器化后端（docker compose）─────────────────────────────────
#
# 与上面的 db-* 是两条互斥的路：
#   db-*      只起数据库和缓存，后端在宿主机跑（make dev / make run）
#   docker-*  后端也在容器里，整条链路一键起
# 两条同时用会撞 9090。完整的容器说明见 mcp-hub/docs/deployment.md。

## docker-build: 构建后端镜像（三阶段：前端产物 → Go 编译 → alpine 运行）。
docker-build:
	@test -x "$(DOCKER)" || { echo "找不到 docker —— Docker Desktop 需要先启动"; exit 1; }
	$(COMPOSE) build mcp-hub

## docker-up: 起全套（postgres + redis + mcp-hub），等健康检查通过。
docker-up:
	@test -x "$(DOCKER)" || { echo "找不到 docker —— Docker Desktop 需要先启动"; exit 1; }
	$(COMPOSE) up -d --wait --wait-timeout 120
	@echo "网关就绪: http://127.0.0.1:9090/    MCP 端点: http://127.0.0.1:9090/mcp"

## docker-down: 停全套（命名卷保留，数据不丢）。
docker-down:
	$(COMPOSE) down

## docker-logs: 跟踪后端容器日志。
docker-logs:
	$(COMPOSE) logs -f --tail=100 mcp-hub
