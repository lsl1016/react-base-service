---
title: Tool 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: tool
maintainer: react-base-service 项目组
status: active
related_code:
  - controllers/http/tool/tool.go
  - controllers/http/tool/user_policy.go
  - service/tool/tool.go
  - service/tool/executor.go
  - service/tool/runtime.go
  - service/tool/visibility.go
  - models/llm/tool.go
  - models/llm/tool_user_policy.go
summary: 业务 Tool 注册表、用户白名单可见性策略、服务端 HTTP/MCP 转发执行以及 ReAct 引擎 get_tool/execute_tool 两阶段消费协议
---

# Tool 模块功能文档

## 1. 模块概述

Tool 模块维护 Caller 作用域下的业务 Tool 注册表（`tblLlmTool`），并承担两部分运行时职责：一是服务端 Tool 的实际执行（`http` 类型转发 HTTP 请求、`mcp` 类型转发 MCP 调用），二是向 ReAct 引擎供给可见 Tool 集合，由 `get_tool` / `execute_tool` 两阶段协议按需加载与执行。`client` 类型工具不在服务端执行，由前端经 WebSocket 执行后回填。

工具级用户白名单（`tblLlmToolUserPolicy`）在运行时对受限工具按用户名过滤可见性；白名单外的用户看不到该工具，`get_tool` 也无法加载。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/tool/register` | POST | 注册工具 | `tool.RegisterTool` |
| `/react-base-service/tool/update` | POST | 更新工具 | `tool.UpdateTool` |
| `/react-base-service/tool/delete` | POST | 软删除工具 | `tool.DeleteTool` |
| `/react-base-service/tool/list` | POST | 查询工具列表 | `tool.ListTools` |
| `/react-base-service/tool/detail` | POST | 查询工具详情 | `tool.GetToolDetail` |
| `/react-base-service/tool/whitelist/create` | POST | 创建工具用户白名单 | `tool.CreateToolUserPolicy` |
| `/react-base-service/tool/whitelist/update` | POST | 更新工具用户白名单 | `tool.UpdateToolUserPolicy` |
| `/react-base-service/tool/whitelist/delete` | POST | 删除工具用户白名单 | `tool.DeleteToolUserPolicy` |
| `/react-base-service/tool/whitelist/detail` | POST | 查询白名单详情 | `tool.GetToolUserPolicyDetail` |
| `/react-base-service/tool/whitelist/list` | POST | 批量查询白名单 | `tool.ListToolUserPolicies` |

## 3. 核心逻辑

### 3.1 注册、更新与删除

`RegisterTool` 的校验链依次为：Caller 存在性（`caller_key=default` 是默认作用域伪 caller，不要求真实存在）→ `validateToolName` → `description` 非空 → `validateToolConfig` → `NormalizePermissionMode` → 同 caller 同名查重（`ExistToolByCallerAndName`）。`toolId` 生成为 `tool_` + 去连字符 UUID。

`validateToolConfig` 的规则：

- `toolType` 归一小写后仅允许 `http` / `client` / `mcp`；
- `config` 必须是 JSON 对象且不超过 32768 字节（`maxToolConfigLen`）；
- 拒绝 snake_case 键名 `input_schema` / `output_schema` / `frontend_hint`，要求使用 camelCase；
- `config.asyncTask` 仅允许出现在 `async=true` 的工具上，且必须带 `schedulerType`；
- `http` 工具必须含 `url` 与 `inputSchema`；`mcp` 工具必须含 `mcpServer` 与 `mcpTool`。

`validateToolName` 拒绝与内置 Meta Tool 同名：`reservedToolNames` 含 21 个保留名（如 `get_tool`、`execute_tool`、`python_exec` 等；`delegate_agent` 不在清单内，由 Agent 模块动态装配）。

更新采用非空字段部分更新；修改 `toolType` 时必须同时传 `config`。`mcp` 类型工具的 `config` 与 `toolType` 由「MCP 连接」同步管理（`tools/list` upsert 覆盖），接口拒绝手工修改 config 和 toolType，也拒绝单独删除，只能通过删除或停用对应 MCP 连接清理。删除为软删除（`SoftDeleteToolByToolID`）。

列表接口：`callerKey` 为空时跨 caller 全量返回（`ListAllTools`）；否则按 `callerKey` + 序列化 `routeValues` 精确匹配（`ListToolsByCallerAndExactRoute`）。

### 3.2 服务端执行（HTTP / MCP 转发）

`service/tool/runtime.go` 的 `Runtime.Execute` 是服务端 Tool 执行的统一入口：`mcp` 类型（或 `config` 带 `mcpServer`）走 `mcpclient.Call(cfg.MCPServer, cfg.MCPTool, input, timeout)`，超时未配置默认 60 秒；其余走 `ExecuteHTTPTool`。

`ExecuteHTTPTool` 的行为：

- `method` 归一为大写，缺省 `POST`；`timeout_ms` 缺省 10000；
- GET 请求把入参对象展开为 query 参数（数组值逐项 `Add`），非 GET 请求把入参序列化为 JSON body；
- 应用 `config.headers`，`Content-Type` 缺省补 `application/json`；上游 cookies 经 `BuildCookieHeader` 透传；`logID` / `requestId` 从 context 透传为 `X-Log-Id` / `Uber-Trace-Id`；
- 响应非 2xx 时返回 `HTTP <code>: <body>` 形式错误；超时归类为 `ErrorToolExecTimeout`；
- 请求日志对 `authorization` / `cookie` / `set-cookie` / `x-service-token` 头掩码（仅保留首尾字符）。

`ParseToolConfig` 优先按 camelCase 解析，存量数据缺少 camelCase 字段时回退 snake_case 兼容解析。

### 3.3 可见性与用户白名单

`FindVisibleToolsByCallerAndRoutes` 是运行时唯一的可见性入口，分两步：

1. `FindToolsByCallerAndRoutes`：`caller_key IN (callerKey, default)` 且 `status = 1` 且 `route_values` 命中路由前缀集合（`[]` 与逐级前缀）；
2. 按 `ListToolUserPoliciesByToolIDs` 结果过滤：无策略的工具对所有人可见；有策略的工具仅白名单内用户可见；策略 JSON 解析失败按"不可见"处理并告警。

白名单策略的配置窗口要求工具先处于停用状态（`validateToolPolicyTarget` 要求 `status == 0`，否则报"工具需先关闭后再配置用户白名单"），配置后再启用生效；更新白名单同样要求先停用。`CreateOrRestoreToolUserPolicy` 对同 `tool_id` 的软删行做复活更新而不是新插入。`BlackUserList` 是预留字段，不参与接口和运行时判断。

### 3.4 ReAct 两阶段消费（get_tool / execute_tool）

ReAct 引擎不把全部业务 Tool 的完整 Schema 注入 System Prompt，而是（见 `service/react/business_tool.go`、`service/react/tool_dispatch.go`）：

- run 初始化注入轻量摘要索引（`buildToolIndexSnapshotJSON`，仅 toolId/name/description）；
- `get_tool` 按 `toolId` 或 `name` 从可见集合加载完整 `parameters` / `outputSchema`，写入 `activeTools` 并持久化 `active_tool_ids` / `active_tool_defs_json` 到 Run 记录；
- `execute_tool` 只执行 `activeTools` 中的工具，执行前做三层校验：信封解析、`inputSchema` 校验（`ValidateJSONSchemaValue`）、`revalidateBusinessToolForExecution` 重查最新可见工具并比较 definition 指纹（`toolDefinitionFingerprint`）；定义在加载后发生变化则拒绝执行并要求重新 `get_tool`。

未命中 `activeTools` 时走 `reloadBusinessToolIfUnchanged` 自愈：仅当本会话此前加载过且定义未变化才自动激活。新外层 run 通过 `restoreActiveToolsFromPreviousRun` 按指纹一致原则恢复上一 run 已加载的工具。执行调度上，`executeToolCalls` 默认串行（错误即停），仅 `delegate_agent` 允许并行（见 Agent 模块文档）。

## 4. 数据模型

`tblLlmTool`（`model.Tool`）：`tool_id`、`name`、`description`、`tool_type`、`caller_key`、`route_values`（JSON 数组序列化）、`config`（JSON 字符串）、`permission_mode`（`auto` 默认 / `confirm` / `confirm_risky`）、`status`、审计字段与软删除 `deleted_at`。

`tblLlmToolUserPolicy`（`model.ToolUserPolicy`）：`tool_id`（唯一）、`white_user_list`（JSON 数组）、`black_user_list`（预留）、审计字段与软删除。

`config`（`ToolConfig`）的主要字段：`url`、`method`、`timeout_ms`、`headers`、`description`、`inputSchema`、`outputSchema`、`frontendHint`、`mcpServer` / `mcpTool`、`async`、`asyncHint`、`asyncTask.schedulerType`、`riskPatterns`（`confirm_risky` 模式自定义风险正则，为空用内置默认风险表）。

本模块不新增其他数据表。

## 5. 配置项与限制

本模块无独立 YAML 开关。硬性限制数值：

| 限制 | 数值 |
|---|---|
| `config` 最大长度 | 32768 字节 |
| 白名单单工具最大用户数 | 200（`maxToolUserPolicyUsers`） |
| 白名单批量查询最大 toolId 数 | 200（`maxToolUserPolicyToolIDs`） |
| 白名单用户名最大长度 | 128 字符（`maxToolUserNameLength`） |
| 工具名保留字 | 21 个内置 Meta Tool 名（`reservedToolNames`） |
| HTTP 工具默认超时 | 10000 ms |
| MCP 工具默认超时 | 60 s |

`permission_mode` 取值 `auto`（默认，自动执行）/ `confirm`（每次人工确认）/ `confirm_risky`（入参或工具名命中风险正则才确认），确认门在 `executeServerTool` 的 `confirmServerToolIfNeeded` 实施。异步提交型工具（`async=true`）的提交记录与提醒由 Async Task 模块承接，见 `docs/system/async_task.md`。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 Tool 模块文档基线 |
