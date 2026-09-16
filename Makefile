GO    ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOFMT ?= $(shell dirname $(GO))/gofmt
BIN   ?= bin/mcp-hub
PKG   := ./...

# ─── 开发环境编排 ────────────────────────────────────────────────
# 数据库走 docker compose（compose 文件在本仓库上一级），后端 serve 只读
# .env 不收参数，前端是独立的 Vite 工程。三者用 make dev 一键拉起。
DOCKER       ?= $(shell command -v docker 2>/dev/null || echo /usr/local/bin/docker)
COMPOSE_FILE ?= ../docker-compose.yml
COMPOSE      := $(DOCKER) compose -f $(COMPOSE_FILE)

FRONTEND_DIR ?= ../mcp-hub-admin
NPM          ?= $(shell command -v npm 2>/dev/null || echo npm)
# 直接执行 vite 本体而不是 npm run dev：拿到的 PID 就是 vite 进程本身，
# dev-stop 一个 kill 就能干净地停掉（npm 会隔一层 sh，信号经常传不下去）。
VITE_BIN     := $(FRONTEND_DIR)/node_modules/.bin/vite

RUN_DIR      := logs
BACKEND_LOG  := $(RUN_DIR)/backend.log
FRONTEND_LOG := $(RUN_DIR)/frontend.log
BACKEND_PID  := $(RUN_DIR)/backend.pid
FRONTEND_PID := $(RUN_DIR)/frontend.pid

BACKEND_PORT  ?= 9090
FRONTEND_PORT ?= 5273
BACKEND_URL   ?= http://127.0.0.1:$(BACKEND_PORT)
FRONTEND_URL  ?= http://127.0.0.1:$(FRONTEND_PORT)

.PHONY: all build test test-race vet fmt fmtcheck tidy vendor check clean \
        run servers probe \
        dev stop-apps dev-stop dev-status dev-logs \
        db-up db-down db-reset db-logs

all: check build

build:
	$(GO) build -o $(BIN) ./cmd/mcp-hub

test:
	$(GO) test $(PKG) -count=1

# 竞态检测单独一个目标：它会让测试慢一个数量级，
# 不适合挂在默认流程上，但并发相关的改动必须跑它。
test-race:
	$(GO) test $(PKG) -count=1 -race

vet:
	$(GO) vet $(PKG)

fmt:
	$(GOFMT) -w .

# 确认没有未格式化的文件（CI 用）。注意是 gofmt -l，不是 go fmt -l。
fmtcheck:
	@out=$$($(GOFMT) -l .); \
	if [ -n "$$out" ]; then echo "以下文件未格式化:"; echo "$$out"; exit 1; fi; \
	echo "格式检查通过"

tidy:
	$(GO) mod tidy

# 生成 vendor/ 以便离线构建。默认不提交——go.sum 已锁定版本，
# 只有需要完全离线或审计依赖时才执行。
vendor:
	$(GO) mod vendor

check: fmtcheck vet test

clean:
	rm -rf bin

# serve 的全部配置来自 .env，不收任何命令行参数。
run: build
	$(BIN) serve

# --db 缺省走配置层（同一个 viper：进程环境变量 > .env），
# 程序自己会加载 ./.env，不需要在这里 source 一遍。
servers: build
	$(BIN) servers

# 只探活不落库，用来快速确认某个地址能不能用。
probe:
	@test -n "$(URL)" || (echo "用法: make probe URL=https://host/mcp"; exit 2)
	$(BIN) probe --url "$(URL)"

# ─── 一键开发环境 ────────────────────────────────────────────────

## dev: 一键启动数据库( docker compose ) + 后端( :9090 ) + 前端( :5273 )，
##      阻塞跟踪日志。Ctrl+C 结束前后端（数据库保留），彻底停止用 make dev-stop。
##      已在运行的部分自动跳过，可安全重复执行。
dev:
	@test -f .env || { echo "缺少 .env —— 参照 .env.example 配置后重试"; exit 1; }
	@test -x "$(DOCKER)" || { echo "找不到 docker —— Docker Desktop 需要先启动"; exit 1; }
	@mkdir -p $(RUN_DIR)
	@$(MAKE) --no-print-directory build
	@echo "拉起数据库（docker compose）…"
	@$(COMPOSE) up -d --wait --wait-timeout 60
	@if [ -f $(BACKEND_PID) ] && kill -0 $$(cat $(BACKEND_PID)) 2>/dev/null; then \
		echo "后端已在运行（pid $$(cat $(BACKEND_PID))），跳过"; \
	else \
		echo "启动后端 $(BACKEND_URL)（日志: $(BACKEND_LOG)）"; \
		nohup $(BIN) serve >> $(BACKEND_LOG) 2>&1 & echo $$! > $(BACKEND_PID); \
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
		cd $(FRONTEND_DIR) && nohup node_modules/.bin/vite $(VITE_ARGS) >> $(CURDIR)/$(FRONTEND_LOG) 2>&1 & echo $$! > $(CURDIR)/$(FRONTEND_PID); \
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
	@trap '$(MAKE) --no-print-directory stop-apps' INT TERM; \
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

## dev-stop: 停前后端 + 数据库（compose stop，容器与数据保留）。
dev-stop: stop-apps
	@$(COMPOSE) stop
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
db-up:
	@test -x "$(DOCKER)" || { echo "找不到 docker —— Docker Desktop 需要先启动"; exit 1; }
	$(COMPOSE) up -d --wait --wait-timeout 60
	@echo "数据库就绪: postgres@5432  redis@6379"

## db-down: 停止并移除容器（命名卷保留，数据不丢）。
db-down:
	$(COMPOSE) down

## db-reset: ⚠ 清空数据库（连同数据卷一起删）。需要显式确认。
db-reset:
	@test "$(CONFIRM)" = "1" || { echo "危险操作：将删除 postgres/redis 的全部数据卷。确认请执行: make db-reset CONFIRM=1"; exit 1; }
	$(COMPOSE) down -v
	@echo "数据卷已删除。"

db-logs:
	$(COMPOSE) logs -f --tail=100
