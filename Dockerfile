FROM node:22-alpine AS sdk-builder

WORKDIR /apps/web/sdk/

COPY web/sdk/package.json web/sdk/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/sdk/ ./
RUN npm run build

FROM golang:1.25-alpine AS builder

# 国内网络环境使用公共 Go 模块代理；如无需代理可删除此行
ENV GOPROXY=https://goproxy.cn,direct

ARG APP_NAME
ENV APP_NAME=$APP_NAME

WORKDIR $GOPATH/${APP_NAME}/

COPY go.mod $GOPATH/${APP_NAME}/
COPY go.sum $GOPATH/${APP_NAME}/
RUN go mod download
# 全量拷贝源码进 builder。conf/mount/custom.yaml 经 .dockerignore 排除（环境特定
# 凭证/路径不进任何镜像层）；运行时配置由 deploy/compose/conf/mount 挂载提供。
COPY . $GOPATH/${APP_NAME}/
COPY --from=sdk-builder /apps/web/sdk/dist $GOPATH/${APP_NAME}/web/sdk/dist
RUN go build -o /usr/local/bin/react-base-service main.go
RUN go build -o /usr/local/bin/repo-mcp ./cmd/repo-mcp

FROM alpine:3.20

# workspace 代码工作区依赖系统 git（runGit 直接 exec.Command("git")）；
# ca-certificates 供 https 仓库 TLS 校验。SSH 私库场景需另加 openssh-client
# 并以 secret/volume 挂载私钥（勿打进镜像）。
RUN apk add --no-cache git ca-certificates

ARG APP_NAME
ENV APP_NAME=$APP_NAME

WORKDIR /usr/local/bin/

COPY --from=builder /usr/local/bin/react-base-service /usr/local/bin/
# repo-mcp 由 mcpclient 以 stdio 子进程拉起，spawnCommand 固定按相对路径
# bin/repo-mcp 查找（相对进程工作目录），故须落在 WORKDIR 下的 bin/ 子目录。
COPY --from=builder /usr/local/bin/repo-mcp /usr/local/bin/bin/repo-mcp

CMD ["/usr/local/bin/react-base-service"]
