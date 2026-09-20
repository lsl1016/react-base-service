---
title: MCP 服务端网关模块功能文档
date: 2026-09-20
version: v1.4
type: system
module: mcp-gateway
maintainer: react-base-service 项目组
status: active
related_code:
  - service/mcpgateway/server.go
  - service/mcpgateway/auth.go
  - service/mcpgateway/registry.go
  - service/mcpgateway/dispatch.go
  - service/mcpgateway/output_projection.go
  - service/mcpgateway/toolconfig/
  - service/mcpgateway/audit.go
  - controllers/http/react/mcp_app.go
  - controllers/http/mcpadmin/mcpadmin.go
  - cmd/mcp-gateway/main.go
  - models/llm/mcp_app.go
  - web/mcp-admin/
summary: 把 tblLlmTool 的 http 工具作为全局基础集合，按应用绑定白名单（tblLlmMcpAppTool）经 MCP Streamable HTTP 对外暴露；核心行为（鉴权/分派/输出投影+字段描述/审计）自 mcp-server 项目移植
---

# MCP 服务端网关模块功能文档

## 1. 模块概述

react-base-service 同时是 MCP **客户端**（`service/mcpclient`，连接上游 MCP 服务器）与 MCP **服务端**（`service/mcpgateway`，对外提供 MCP 端点）。网关把 `tblLlmTool` 中 `tool_type=http` 且 `status=1` 的工具视为**全局基础集合**（不按 caller 划分，caller_key 仅作归属标记）暴露成 MCP 协议，外部 MCP 客户端（Claude/Cursor 等）可凭应用凭证接入调用——工具注册一处（playground 工具管理），LLM 运行时与 MCP 对外暴露同源。

**按业务方收窄可见范围的方式**：应用（app）绑定工具基础集合的一个子集（`tblLlmMcpAppTool` 白名单）；把哪个 app 的凭证发给哪个业务方，该业务方的 MCP 连接就只看得到、调得了这个 app 绑定的工具子集。

核心行为自通用 MCP 网关项目 `mcp-server` 移植（网关核心 Go 代码与 Vue3 管理台均已入仓：前者在 `service/mcpgateway`，后者在 `web/mcp-admin`，不再依赖外部工程）：Bearer 应用凭证鉴权（常量时间比较、失败 403）、通用 JSON HTTP 分派（GET/DELETE 入参转 query、响应信封归一）、**输出投影 + 字段描述渲染**（`x-output-projection`）、随 initialize 下发的 server instructions、调用审计异步落库。协议实现用官方 SDK（`modelcontextprotocol/go-sdk` v1.7.0，`NewStreamableHTTPHandler`，`Stateless + JSONResponse`：每请求自成生命周期，无会话状态，可水平扩容）。

两个进程形态共用同一网关核心（`mcpgateway.Bootstrap`）：

| 形态 | 端点 | 启动 |
|---|---|---|
| 主服务内置 | `POST/GET/DELETE /react-base-service/mcp` | 随 `go run main.go`（`mcp_server.enabled: true` 时挂载） |
| 独立网关进程 | `/mcp`（默认 `:8090`） | `go run ./cmd/mcp-gateway`（`-addr` 或 `MCP_GATEWAY_ADDR` 覆盖；只加载 conf+MySQL+网关路由） |

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/mcp` | POST/GET/DELETE | MCP 协议端点（initialize/tools/list/tools/call 等） | `mcpgateway.Handler` |
| `/react-base-service/react/mcpapp/list` | POST | 应用列表（secret 打码） | `react.ListMcpApps` |
| `/react-base-service/react/mcpapp/create` | POST | 创建凭证（secret 仅本次响应完整返回） | `react.CreateMcpApp` |
| `/react-base-service/react/mcpapp/update` | POST | 改名/启停 | `react.UpdateMcpApp` |
| `/react-base-service/react/mcpapp/delete` | POST | 软删凭证（持凭证客户端立即失联） | `react.DeleteMcpApp` |
| `/react-base-service/react/mcpapp/reset_secret` | POST | 重置密钥（新 secret 仅本次响应返回） | `react.ResetMcpAppSecret` |
| `/react-base-service/react/mcpapp/grant_tools` | POST | 全量替换应用绑定工具清单（空=清空绑定） | `react.GrantMcpAppTools` |
| `/react-base-service/react/mcpapp/list_tools` | POST | 可绑定工具清单（基础集合 + 当前绑定标记） | `react.ListMcpAppGrantableTools` |
| `/react-base-service/react/mcpapp/logs` | POST | 调用审计分页查询（appKey/toolName 过滤） | `react.ListMcpAppLogs` |
| `/api/manage/*` | POST | 网关管理台（Vue SPA）协议兼容层（tools×5 + app×5；SPA 的 axios baseURL 构建期固定为 /api，故挂根路径） | `mcpadmin` |

## 3. 核心逻辑

### 3.1 鉴权与工具可见性（绑定即权限）

`Authorization: Bearer <app_key>:<app_secret>`（secret 可含冒号，按第一个冒号切分）。`mcpgateway.Auth` 查 `tblLlmMcpApp`（app_key 全局唯一，须 status=1）并常量时间比较 secret；失败返回 **403**（刻意不用 401，避免客户端触发 OAuth 发现丢弃错误体）。可选 `X-MCP-User` 头仅作审计维度。

工具可见性 = **应用绑定的工具子集**：工具基础集合（全部启用的 http 工具，跨 caller，`route_values` 不参与；client/mcp 型工具不对外）∩ `tblLlmMcpAppTool` 白名单。应用未绑定任何工具时 tools/list 为空；要调整某个业务方可见范围，改它所用 app 的绑定清单即可（playground「MCP 应用」页「工具授权」）。同名工具存在于多个 caller 名下时按 name/id 排序保留第一条（MCP 工具名须全局唯一，管理台注册/改名已做全局查重）；tools/call 时按名复核（客户端可能缓存旧清单）。

> 历史说明：v1.1 引入过 tblLlmMcpAppTool 白名单但叠加 caller 作用域过滤，v1.3 一度简化为「caller 作用域即权限」，v1.4 定稿为「工具=全局基础集合 + 应用绑定白名单」——工具不再与 caller 耦合，按业务方的差异化可见范围全部由 app 承载。

### 3.2 工具暴露与执行链路

```
tools/list → ListGatewayHTTPTools(基础集合=全部启用 http 工具) ∩ ListMcpAppToolIDs(appID 白名单)
           → loadBaseTools 同名去重（name/id 排序保留第一条）
           → buildToolBinding：config 解析 URL/method/headers/timeout + schema 原文
           → buildToolDefinition：sdk.Tool 透传 input/outputSchema；
             GET→ReadOnlyHint，非 GET→DestructiveHint；schema 非法整条跳过（记日志）
tools/call → 复核可见性（按 appID 重取白名单） → 入参 JSON Schema 校验（缺必填字段生成引导文案，含各字段 description）
           → Dispatch：GET/DELETE 转 query，其余 JSON body；headers 附加；
             forward_cookies 配置的调用方 Cookie 透传上游
           → 响应归一（errNo/errno/code + errStr/errMsg/message/msg + success 识别成败）
           → 输出投影（见 3.3）→ StructuredContent + Content 文本返回
           → 异步审计（ObserveMiddleware 计时投递）
```

### 3.3 输出投影 + 字段描述（自 mcp-server 原样移植）

工具 `outputSchema` 声明 `"x-output-projection": true` 时：

1. **裁剪**：按 schema 递归投影上游响应——`properties`/`patternProperties`/`additionalProperties`/`items`/`prefixItems` 声明内的字段保留，未声明的丢弃；支持 `$ref`（本地）合并、`allOf`、`oneOf`/`anyOf`（按值匹配分支）、`if/then/else`；深度上限 64，字段说明上限 200 条。
2. **描述渲染**：schema 里的根 `description` 与各字段 `description` 渲染进 Content 文本——「结果说明：… / 字段说明：- path：description / 结果：{json}」；`StructuredContent` 同时携带投影后的结构化 JSON。
3. **注册时校验**：管理台批量注册与 buildToolDefinition 均调 `toolconfig.ValidateOutputSchema`——投影语法（循环 $ref、外部 $ref、非法 patternProperties 正则等）在保存阶段拒绝。

未开启投影时行为完全兼容：成功保留完整结构化 JSON，失败只回传上游错误文案。

### 3.4 调用审计

`tools/call` 经 SDK 接收中间件（`ObserveMiddleware`）计时并投递审计：单协程组批（批量 100 / 间隔 1s / 队列 4096 写满丢弃不阻塞业务）+ 信号量限并发（默认 10，`mcp_server.audit.worker_num` 可调）批量写 `tblLlmMcpCallLog`。入参快照 ≤4KB（服务端补全后），响应摘要 ≤32KB；停机时 `CloseAuditWriter` 排空队列。

## 4. 数据模型

| 表 | 模型 | 说明 |
|---|---|---|
| `tblLlmMcpApp` | `McpApp` | 应用凭证：app_id/app_name/app_key（均唯一）/app_secret/status，软删除；secret 仅创建/重置时完整返回，列表打码 |
| `tblLlmMcpAppTool` | `McpAppTool` | 应用工具绑定白名单：app_id+tool_id（指向 tblLlmTool.tool_id）唯一、软删除、全量替换语义；未绑定任何工具的 app tools/list 为空 |
| `tblLlmMcpCallLog` | `McpCallLog` | 调用审计（只插不改）：app_key/user_name(X-MCP-User)/tool_name/arguments/response_text/result_code(0 成功/-1 工具错误/上游码)/cost_ms/client_ip/client_info |

工具本体不建新表——就是 `tblLlmTool` 的 http 行（config 含 `url/method/headers/timeout_ms/inputSchema/outputSchema`）。建表脚本：`sql/mcp_gateway_v1.sql`（已并入 `sql/init.sql`）。

## 5. 配置项（custom.yaml `mcp_server` 段）

```yaml
mcp_server:
  enabled: true            # 主服务是否挂载 /mcp 端点（cmd/mcp-gateway 恒挂载）
  name: react-base-service # initialize 返回的 server 标识
  version: 1.0.0
  # instructions: |        # 覆盖随 initialize 下发的模型使用规范（默认内置文案）
  # forward_cookies:       # 透传调用方 Cookie 给上游（上游依赖登录态时）
  #   - SESSION
  audit:
    worker_num: 10         # 审计落库并发
```

## 6. playground 页面与管理台

- **MCP 应用**（playground tab）：应用卡片（appKey 一键复制/secret 打码/绑定工具数/接入端点）+ 创建与重置密钥弹窗（完整 secret 一次性展示）+ **工具授权弹窗**（勾选基础集合中的 http 工具，全量替换该 app 的绑定白名单）+ 调用记录分页弹层。「MCP 连接」「MCP 应用」两页工具栏有「网关管理台 ↗」入口。
- **网关管理台**（`/react-base-service/react/mcp-admin`）：Vue3 + TS + Vite + Element Plus 管理台，**源码内嵌本仓** `web/mcp-admin/`（自 mcp-server 项目复制，此后在本仓独立演进，不依赖外部工程；`npm run build` 产出 `dist/` 提交入库，经 go:embed 内嵌，见 `web/mcp-admin/README.md`）。功能：工具列表/批量注册（多草稿）/编辑/批量上下线 + 应用管理页（创建应用、凭证展示、**按应用勾选绑定工具白名单**——`grantTools` 持久化到 `tblLlmMcpAppTool`，绑定状态三态：已绑定/已下线/未绑定）。后端为 `/api/manage/*` 协议兼容层（`controllers/http/mcpadmin`），字段映射：数字 id ↔ tblLlmTool 主键、bizTag ↔ caller 归属标记（不参与可见性）、requestConfig/readOnly ↔ config 的 method/headers/timeout（GET=只读）、status 1/2 ↔ 0/1；http 工具名在管理台注册/改名时做全局查重（MCP 工具名须全局唯一）。工具绑定编辑在管理台与 playground「工具授权」弹窗两处均可，同一份白名单。
- **管理台鉴权**：`X-Admin-Token` 静态令牌；`mcp_server.admin_tokens` 配置白名单时按白名单校验，空列表 = 接受任意非空令牌（内网联调默认）；无效令牌返回 errNo 2001 触发右上角「管理令牌」弹窗。`X-Admin-User` 记录操作者。

## 7. 边界与决策

- 只暴露 `tool_type=http` 工具（client 型由前端执行、mcp 型是下游代理，不转发）；
- 工具可见性采用 mcp-server 的显式白名单语义（tblLlmMcpAppTool 全量替换），工具基础集合不按 caller 划分；绑定但已下线的工具不出现在 tools/list（重新上线自动恢复可见）；
- MCP 工具名全局唯一：基础集合加载与管理台注册/改名均做同名去重/查重；
- Redis 限流、Prometheus 指标、CodeMCP（local:// 代码检索工具）不移植；
- 输出投影只作用于网关出口，LLM 运行时的 execute_tool 行为不变；
- 独立网关进程与主服务共享 llm 库，可同时运行（同一凭证两处均可用）。

## 8. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.4 | 2026-09-20 | react-base-service 项目组 | 工具改为全局基础集合（不再按 caller 划分可见性），恢复 tblLlmMcpAppTool 应用绑定白名单（grant_tools/list_tools 接口 + playground「工具授权」弹窗）；应用去掉 caller 绑定；管理台 Vue 源码内嵌本仓 web/mcp-admin（自 mcp-server 复制后独立演进），应用页支持勾选绑定工具（grantTools 持久化） |
| v1.3 | 2026-09-20 | react-base-service 项目组 | 移除按应用工具授权层（tblLlmMcpAppTool 及 grant_tools/list_tools 接口），作用域即权限；管理台重建：创建应用可选绑定 caller、授权区改为作用域说明与可见工具清单 |
| v1.2 | 2026-09-20 | react-base-service 项目组 | 内嵌 mcp-server 原版 Vue3 管理台（/react/mcp-admin + /api/manage 兼容层 + X-Admin-Token 鉴权），playground 增加入口 |
| v1.1 | 2026-09-20 | react-base-service 项目组 | 补齐 mcp-server 的按应用工具授权（tblLlmMcpAppTool 显式白名单 + grant_tools/list_tools 接口 + 授权弹窗） |
| v1.0 | 2026-09-20 | react-base-service 项目组 | 自 mcp-server 移植网关核心，与 tblLlmTool 工具体系原生融合；新增应用凭证/审计两表与 playground 管理页 |
