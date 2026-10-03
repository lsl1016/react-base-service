#!/usr/bin/env bash
# 本地开发脚本：依赖容器化、服务本地直跑。
#
# 约束（详见 README「日常开发」一节）：
#   - MySQL / Redis / python 沙箱只在容器中运行，宿主机端口由 .env 固化：
#     MySQL=3317  Redis=16379  Sandbox=18190
#   - docker-compose.yml 里 sandbox 只挂 internal 网络（P0-1 网络硬隔离）、不发布宿主机
#     端口；本机 go run 需要直连，故一律叠加 docker-compose.dev.yml 覆盖层恢复端口。
#     **发布形态不要加载该覆盖层**（它会让沙箱重新可出网）。
#   - 业务服务本地 go run（监听 :8180），改代码后重跑即生效，不打包镜像；
#     web 前端为 embed 静态资源，同样随 go run 生效
#   - 容器版 service（:8080，docker compose up -d --build service）是发布形态，
#     仅在需要验证镜像时重建，日常开发不必也不应反复重建
#
# 用法：
#   ./dev.sh          # 起依赖容器 + 前台跑本地服务（日常开发，Ctrl+C 只停服务）
#   ./dev.sh deps     # 只起/刷新依赖容器（开机后一次即可）
#   ./dev.sh stop     # 停依赖容器
#   ./dev.sh status   # 查看依赖容器与端口状态
set -euo pipefail
cd "$(dirname "$0")"

# 本地开发固定叠加 dev 覆盖层：恢复 sandbox 宿主机端口，供 go run 直连（代价是放弃沙箱出网隔离）
compose=(docker compose -f docker-compose.yml -f docker-compose.dev.yml)

cmd="${1:-up}"
case "$cmd" in
  up)
    "${compose[@]}" up -d mysql redis sandbox searxng
    echo "依赖容器已就绪。本地服务启动：http://127.0.0.1:8180/react-base-service/react/playground"
    echo "（Ctrl+C 退出服务；依赖容器保持运行）"
    exec go run main.go
    ;;
  deps)
    "${compose[@]}" up -d mysql redis sandbox searxng
    "${compose[@]}" ps mysql redis sandbox searxng
    ;;
  stop)
    "${compose[@]}" stop mysql redis sandbox searxng
    ;;
  status)
    "${compose[@]}" ps mysql redis sandbox searxng
    echo "本机应监听: MySQL=3317 Redis=16379 Sandbox=18190 SearXNG=8888（由 .env 固化）"
    ;;
  *)
    echo "usage: ./dev.sh [up|deps|stop|status]" >&2
    exit 1
    ;;
esac
