GO    ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOFMT ?= $(shell dirname $(GO))/gofmt
BIN   ?= bin/mcp-hub
PKG   := ./...
DB    ?= mcp-hub.db
ADDR  ?= :9090

.PHONY: all build test vet fmt tidy vendor clean \
        run servers probe add fmtcheck check

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

run: build
	$(BIN) serve --db $(DB) --addr $(ADDR)

# 只探活不落库，用来快速确认某个地址能不能用。
probe:
	@test -n "$(URL)" || (echo "用法: make probe URL=https://host/mcp"; exit 2)
	$(BIN) probe --url "$(URL)"

servers: build
	$(BIN) servers --db $(DB)
