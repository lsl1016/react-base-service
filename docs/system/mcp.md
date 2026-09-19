---
title: MCP 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: mcp
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/react/mcp_server.go
  - service/mcpclient/manager.go
  - service/mcpclient/client.go
  - service/mcpclient/httpclient.go
  - service/mcpclient/sdkclient.go
  - models/llm/mcp_server.go
  - service/tool/runtime.go
summary: MCP 服务器登记与连接管理、三种传输客户端、工具清单同步进 Business Tool 注册表及 execute_tool 执行链路
---

# MCP 模块功能文档

## 1. 模块概述

MCP 模块登记并管理外部 MCP 服务器连接，把服务器工具清单同步进 `tblLlmTool`（`tool_type=mcp`），ReAct 运行时经既有 `get_tool`/`execute_tool` 两段式加载使用，引擎无感知。传输支持三种 `kind`：`repo`（stdio 适配器白名单）、`http`（手写 Streamable HTTP）、`http_sdk`（官方 MCP Go SDK），三者实现同一 `Server` 接口。

连接有两个来源：DB 注册表（`tblLlmMcpServer`，管理接口可改）与 yaml 静态声明（`mcp.servers`，改配置文件后重启生效）。请求 DTO（`mcpListRequest`、`mcpCreateRequest` 等）定义在控制器文件内，`components/params` 无 MCP 条目。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/react/mcp/list` | POST | 列出 caller 下 MCP 连接（含运行状态与工具数） | `react.ListMcpServers` |
| `/react-base-service/react/mcp/detail` | POST | 查询单个连接详情与工具清单 | `react.GetMcpServerDetail` |
| `/react-base-service/react/mcp/create` | POST | 新增连接（rawConfig 粘贴或结构化字段） | `react.CreateMcpServer` |
| `/react-base-service/react/mcp/update` | POST | 更新端点/请求头/超时/绑定 caller/启停 | `react.UpdateMcpServer` |
| `/react-base-service/react/mcp/delete` | POST | 删除连接并清理注册表工具 | `react.DeleteMcpServer` |
| `/react-base-service/react/mcp/connect` | POST | 连接测试并同步工具清单 | `react.ConnectMcpServer` |

## 3. 核心逻辑

### 3.1 注册模型

连接记录在 `tblLlmMcpServer`：`name` 全局唯一（软删除行仍占用 `uk_name`，同名重建走 `ReviveMcpServerByName` 复活而非重复插入）；记录自带 `caller_key` 为属主 caller，恒定生效不占绑定行；`tblLlmMcpServerCaller` 保存额外绑定的 caller，一个连接的工具同步到属主与全部绑定 caller 名下。创建支持两种方式：`rawConfig` 粘贴标准 `mcpServers` JSON（仅 `url` 形式，`command/args` 形式直接拒绝），可一次登记多条；或结构化字段 `name` + `kind`（`http`/`repo`）+ `endpoint`/`headers`/`timeoutMs`。yaml 静态服务器在管理面以 `source=yaml`、合成 `serverId=yaml:<name>` 展示，`update`/`delete`/`connect` 对该前缀拒绝并提示改配置文件；与 DB 记录同名并存时两条独立可见，创建时直接拒绝与 yaml 同名（`EnsureServer` 同名会覆盖客户端）。

### 3.2 传输实现与安全约束

`Server` 接口统一 `Name`/`Start`/`Stop`/`ListTools`/`CallTool`，注册表同步与执行分发对传输形式无感知。stdio（`kind=repo`）：可执行文件是代码内白名单字面量路径 `bin/repo-mcp[.exe]`，不接收任意 command/args，argv 直传不经 shell，单服务器内请求互斥串行，stderr 透传服务日志。HTTP 两版均按次会话（每次操作独立完成 initialize → 操作 → 弃，无连接池）：手写版响应支持 `application/json` 与 SSE 流两种形态，SDK 版关闭常驻 GET 流（`DisableStandaloneSSE`）并把非文本内容降级为占位描述。HTTP 端点每次建会话前执行 SSRF 校验：仅 `http/https`，拒绝环回、私网、链路本地、组播、未指定、CGNAT、TEST-NET 及保留网段，域名解析出任一非公网地址即拒绝；`mcp.allow_private_endpoint` 显式开启才放行（仅限本机开发）。自定义 headers 附加到每个请求，但 `Host`/`Content-Type`/`Content-Length`/`Accept`/`Mcp-Session-Id` 等协议自管头不可覆盖。两种传输均无身份认证。

### 3.3 connect 语义与工具同步

`applyMcpConnect` 是统一的"连接"动作：`EnsureServer` 拉起（或同名替换并停旧客户端）→ `SyncServerRegistryScoped` 把 `tools/list` 结果 upsert 进 `tblLlmTool`（属主 toolId=`mcp_<server>_<tool>`，绑定 caller 追加 `__<callerKey>` 后缀避开全局唯一键，工具名 `<server>_<tool>` 不变，软删行复活）→ 结果写回 `last_check_status`/`last_check_message`（截 500 字）/`last_check_at`。触发时机：create（`status=1` 时）、update（`status=1` 时重连；停用则 `RemoveServer` + `RemoveRegistryTools` 清空全部工具副本并标记 `unknown`）、connect 显式测试（停用态拒绝）、服务启动 `Bootstrap`（先 yaml servers，再 DB 启用行，单条失败只记日志）。update 中 `boundCallers` 全量替换：解绑 caller 的工具副本立即清理，不论连接启停。delete：`RemoveServer` + `RemoveRegistryTools`（全部 caller）+ 软删连接记录。

### 3.4 工具执行链路

同步出的工具行就是普通 Business Tool（`route_values="[]"`、`config={mcpServer, mcpTool, inputSchema}`、`created_by=mcp-sync`），受工具管理面板启停与用户白名单（ToolUserPolicy）约束。引擎 `execute_tool` 调用 `service/tool` 的 `Runtime.Execute`：`tool_type=mcp` 或 config 含 `mcpServer` 时路由到 `mcpclient.Call`，超时取工具 config 的 `timeout_ms`（未配置用 60 秒）；`tools/call` 返回的 text 内容拼接回填，`isError` 转为错误。运行中服务器新增/变更工具不会自动重同步，需经 connect/update 触发或重启服务。

## 4. 数据模型

| 表 | 模型 | 说明 |
|---|---|---|
| `tblLlmMcpServer` | `McpServer` | 连接注册表：serverId、name（全局唯一）、kind、endpoint、headers/env（JSON 串）、timeout_ms、status、`last_check_*` 检测三元组、属主 caller_key，软删除 |
| `tblLlmMcpServerCaller` | `McpServerCaller` | 连接与 caller 的绑定（server_id + caller_key 唯一），caller 删除时级联软删 |

工具副本落在 `tblLlmTool`（`tool_type=mcp`），无独立 MCP 工具表。

## 5. 配置项

yaml 静态声明位于 `mcp` 段（`conf/mount/custom.yaml`，容器版在 `deploy/compose/conf/mount`）：`caller_key`（工具挂载调用方，空则不启用）、`servers[]`（`name`/`kind`/`env`/`endpoint`/`timeout_ms`/`headers`）、`allow_private_endpoint`（默认 false）。

数值限制：服务器名 1-32 字符、仅字母数字下划线中划线；`timeout_ms` 归一化默认 30000、上限 300000；headers JSON 上限 4096 字节；stdio `tools/call` 默认 60 秒超时；HTTP 客户端超时缺省 30 秒。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 MCP 模块文档基线 |
