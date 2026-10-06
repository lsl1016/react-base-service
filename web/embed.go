package web

import "embed"

// FS 嵌入内部联调页面，避免 Docker 运行镜像漏拷静态文件。
//
//go:embed react/index.html react/index.css react/index.js react/ips.js react/replay.html react/replay.css react/replay.js react/sql-client-tools.js react/workflow-admin.html all:sdk/dist
var FS embed.FS
