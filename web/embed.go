package web

import "embed"

// FS 嵌入内部联调页面，避免 Docker 运行镜像漏拷静态文件。
//
//go:embed react/index.html react/index.css react/index.js react/ips.js react/replay.html react/replay.css react/replay.js react/sql-client-tools.js all:sdk/dist
var FS embed.FS

// McpAdminFS 嵌入 MCP 网关管理台（自 mcp-server 项目移植的 Vue3 构建产物，
// 页面在 /react/mcp-admin，接口适配层在 controllers/http/mcpadmin）。
//
//go:embed all:mcp-admin/dist
var McpAdminFS embed.FS
