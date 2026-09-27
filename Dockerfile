# mcp-hub 的**服务端**镜像。
#
# 刻意不写 `# syntax=docker/dockerfile:1`。本文件没用任何需要新 frontend
# 的语法（heredoc、COPY --chmod 之类），而那一行会让 BuildKit 额外去拉
# docker/dockerfile 这个 frontend 镜像——它走 Docker Hub，**不受下面
# BASE_REGISTRY 影响**，在国内网络下就是「构建还没开始就先卡一次」。
#
# 三阶段：前端产物 → Go 编译 → 运行。
#
# 为什么要有第一阶段：管理界面是通过 go:embed 编进二进制的。不带界面的
# 二进制照样能跑（/api、/mcp 都不受影响），但那样的镜像对使用者没有意义，
# 所以这里把界面一起构建进去。
#
# 注意构建上下文是**项目根**，不是本目录：
#     docker build -f mcp-hub/Dockerfile .
# 原因见下面各 COPY 的路径。配套的 .dockerignore 也在项目根。
#
# 桌面形态（cmd/mcp-hub-desktop）不进这个镜像：它需要 Cocoa/GTK webview，
# 容器里没有窗口系统可以承载。

# ─── 基础镜像来源 ─────────────────────────────────────────────────
#
# 默认走国内镜像源。这不是洁癖问题，是可用性问题：直连 Docker Hub 拉
# golang 镜像（最大的那层 100MB+）实测 25 秒零进度，构建永远走不到第二步。
# 原因是 dockerd 跑在 OrbStack 的 VM 里，VM 有自己的网络栈，**不吃宿主机
# 上那套 HTTP_PROXY**，所以国际出口基本等于没用。
#
# 换网络环境（或用自建 registry）：
#     docker compose build --build-arg BASE_REGISTRY=docker.io mcp-hub
# 注意必须是「registry 主机名」而不是完整前缀：下面统一拼 /library/。
ARG BASE_REGISTRY=docker.m.daocloud.io

# ─── Stage 1 / ui：构建管理界面 ───────────────────────────────────
FROM ${BASE_REGISTRY}/library/node:22-bookworm-slim AS ui

WORKDIR /src/mcp-hub-admin

# 先只拷依赖清单。package.json 与 lock 不变时这一层永远命中缓存，
# 改业务代码不会触发重新安装依赖。
COPY mcp-hub-admin/package.json mcp-hub-admin/package-lock.json ./

# registry 做成参数：国内直连 npmjs 基本拉不动，但硬编码镜像源对
# 其他网络环境是多余的。想换：--build-arg NPM_REGISTRY=https://registry.npmjs.org
ARG NPM_REGISTRY=https://registry.npmmirror.com
RUN npm ci --no-audit --no-fund --registry=${NPM_REGISTRY}

COPY mcp-hub-admin/ ./

# 等同于 tsc -b && vite build。类型错误会让构建失败，这是想要的：
# 镜像不该带着类型错误发出去。
RUN npm run build


# ─── Stage 2 / build：编译 Go 二进制 ──────────────────────────────
FROM ${BASE_REGISTRY}/library/golang:1.25-bookworm AS build

WORKDIR /src/mcp-hub

# CGO_ENABLED=0 是这一整套设计的地基：SQLite 走 modernc.org/sqlite（纯 Go
# 转译实现），所以服务端不需要 C 工具链，运行阶段才敢落到 alpine（musl）。
# 一旦这里改成 1，运行基底、交叉编译能力、镜像体积三件事同时变。
#
# GOPROXY 同理做成参数，默认走国内镜像源。
ARG GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0 \
    GOPROXY=${GOPROXY}

# 依赖单独成层，与源码变更解耦。
COPY mcp-hub/go.mod mcp-hub/go.sum ./
RUN go mod download

COPY mcp-hub/ ./

# 界面产物落进 go:embed 的目标目录。
# .dockerignore 已排除宿主机的同目录产物，所以这里是唯一来源——
# 少了这条排除，一次本地 make ui 留下的陈旧界面会被静默嵌进镜像，
# 症状是「前端改了、镜像重建了，界面没变」。
COPY --from=ui /src/mcp-hub-admin/dist/ ./internal/webassets/dist/

# 清掉 .placeholder。它存在的唯一理由是让「没跑过 make ui」的本地
# go:embed 也有东西可嵌（embed 不接受空目录），嵌进镜像毫无意义。
RUN rm -f ./internal/webassets/dist/.placeholder

# -trimpath  产物里不出现构建机的绝对路径（构建可复现，也不泄漏目录结构）
# -s -w      去掉符号表与 DWARF
# 本地 make build 不这么做是为了留符号便于调试；镜像里没人会去 delve 它，
# 省下的体积是实打实的（实测约 20MB）。
RUN go build -trimpath -ldflags='-s -w' -o /out/mcp-hub ./cmd/mcp-hub


# ─── Stage 3：运行 ────────────────────────────────────────────────
FROM ${BASE_REGISTRY}/library/alpine:3.21

LABEL org.opencontainers.image.title="mcp-hub" \
      org.opencontainers.image.description="MCP 聚合网关：把多个下游 MCP server 聚合成单一端点"

# ca-certificates 不是可选项：下游 MCP server 基本都是 HTTPS，缺根证书会
# 全线失败，而报错是 "x509: certificate signed by unknown authority"——
# 不会有人从这个信息联想到「镜像里少装了个包」。
# tzdata 给日志时间戳用。
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -g 10001 -S mcphub \
 && adduser -u 10001 -S -G mcphub -h /data mcphub \
 && mkdir -p /data \
 && chown mcphub:mcphub /data

COPY --from=build /out/mcp-hub /usr/local/bin/mcp-hub

# 引擎的 dataDir 取 os.Getwd()（见 cmd/mcp-hub/serve.go）。
# 用 PostgreSQL 时这里不落任何东西；切到 SQLite 时库文件就落在这个目录。
# compose 把它挂成卷，保证两种后端下都有持久化位置。
WORKDIR /data

# 非 root 运行。
USER mcphub

# 必须绑全部网卡。容器里写 127.0.0.1 只会让引擎监听在容器自己的 loopback 上，
# 宿主机的端口映射打不进去——现象是"容器活着、健康检查通过、外面连不上"。
# 对外暴露到什么范围由 compose 的 ports 决定。
ENV MCPHUB_ADDR=:9090

EXPOSE 9090

# 这里刻意不写 HEALTHCHECK：它要探测的地址由 MCPHUB_ADDR 决定，
# 而那个值由 compose 注入。写在知道端口的那个地方，才不会同一件事
# 写两遍、然后其中一遍悄悄过期。见 docker-compose.yml。

ENTRYPOINT ["/usr/local/bin/mcp-hub"]
CMD ["serve"]
