---
title: Agent 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: agent
maintainer: react-base-service 项目组
status: active
related_code:
  - controllers/http/agent/agent.go
  - service/agent/agent.go
  - service/agent/runtime.go
  - models/llm/agent.go
  - components/params/agent.go
  - service/react/delegate.go
summary: 子 Agent 注册表管理、Markdown 定义导入、运行期可见性解析与 delegate_agent 委派时创建隔离子 ReactRun 的边界协议
---

# Agent 模块功能文档

## 1. 模块概述

Agent 模块维护子 Agent 注册表（`tblLlmAgent`）。子 Agent 是注册类资源：主 Agent 通过内置 Meta Tool `delegate_agent` 把自包含子任务委派给它，引擎按定义装配隔离子 ReactRun 执行并回填结论。本文只覆盖注册表管理与运行期解析；Run 执行主循环、事件流与预算细节见 `docs/system/react_runtime.md`。

子 Agent 定义包含系统提示词、可选模型、Tool/Skill 白名单、步数上限、递归 token 预算与权限模式。定义支持结构化接口创建与「frontmatter + 正文」Markdown 导入两种形态。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/agent/create` | POST | 创建子 Agent | `agent.CreateAgent` |
| `/react-base-service/agent/update` | POST | 更新子 Agent | `agent.UpdateAgent` |
| `/react-base-service/agent/delete` | POST | 软删除子 Agent | `agent.DeleteAgent` |
| `/react-base-service/agent/list` | POST | 查询子 Agent 列表 | `agent.ListAgents` |
| `/react-base-service/agent/detail` | POST | 查询子 Agent 详情 | `agent.GetAgentDetail` |
| `/react-base-service/agent/import` | POST | 导入 Markdown 子 Agent 定义 | `agent.ImportAgent` |

## 3. 核心逻辑

### 3.1 注册、更新与 Markdown 导入

`CreateAgent` 校验链：`validateAgentKey`（非空，仅字母/数字/下划线/连字符）→ Caller 存在性（`caller_key=default` 除外）→ `validatePermissionMode`（`inherit` 默认 / `auto` / `confirm` / `confirm_risky`）→ `validateReferenceList`（tools 最多 64 项、skills 最多 32 项，不允许空项与重复项）→ 同 `caller + agent_key` 查重。`agentId` 生成为 `agent_` + 去连字符 UUID，`status` 必填且仅允许 0/1。

唯一键 `uk_caller_agent` 不含 `deleted_at`：软删行仍占用唯一键，同 `caller + agent_key` 重建走 `reviveDeletedAgent` 复活更新（沿用原 `agent_id`，定义字段整体覆盖，清零 `deleted_at`），不做插入。`UpdateAgent` 为部分更新（`systemPrompt`/`modelKey`/`maxSteps` 等指针字段非空才更新），改 `agentKey` 时在同 caller 内查重。删除为软删除。

`ImportAgent` 解析「frontmatter + 正文」：frontmatter 字段与 `CreateAgentReq` 一一对应（yaml 风格命名，如 `agent_key`、`system_prompt`、`max_tokens_per_run`），正文（第二个 `---` 之后）即 `systemPrompt`；`caller_key` / `route_values` 缺省回退请求体字段，`status` 缺省为 1；`agent_key` 缺省时从 `name` 清洗生成（`sanitizeAgentKeyFromName`）。frontmatter 与正文均不允许为空。普通导入遇到同 `caller + agent_key` 活跃行仍报 `ErrorAgentDuplicate`；Bundle 安装使用的同名覆盖路径是 `UpsertFromMarkdownForBundle`，返回覆盖前快照供卸载回滚。

### 3.2 运行期可见性与解析

`service/agent/runtime.go` 的 `Runtime` 是运行期解析器：

- `FindVisible`：`FindAgentsByCallerAndRoutes` 按 `caller_key IN (callerKey, default)` 且 `status = 1` 且 `route_values` 命中路由前缀集合（`[]` 与逐级前缀）查询，`caller_key=default` 的定义对全部 caller 生效；
- `Resolve`：在可见集合内按 `agent_key` 精确匹配，未命中返回 `nil`；
- `Policy`：把持久化定义转为 `RuntimePolicy`（ToolRefs / SkillRefs / PermissionMode / MaxSteps / MaxTokensPerRun），JSON 解析失败按空列表容错。

白名单语义是单向默认：`tools` 为空表示继承 caller 全部可见工具；`skills` 为空表示不注入任何 Skill。`FilterToolIndexSnapshot` / `FilterSkillIndexSnapshot` 按此语义过滤 run 索引快照，非空白名单按 `name` 或 `toolId`/`skillId` 双键匹配。

委派执行时 `resolveAgentForKey` 实时查库解析定义（管理面板修改从下一次委派起生效）；解析失败回退 run 装配时的快照（`findVisibleAgent`）。`delegate_agent` 工具描述中的可用清单是 run 装配快照，随下一个外层 run 刷新。

### 3.3 delegate_agent 委派与子 ReactRun

`service/react/delegate.go` 中 `delegate_agent` 不走 `get_tool`/`execute_tool` 两段式，schema 固定、描述动态渲染可见 Agent 清单（含各自 `agent_key`、名称、描述与 tools 白名单）。`executeDelegateAgent` 的执行边界：

| 边界项 | 行为 |
|---|---|
| `agentPath` | 外层 run 为 `main`，子 run 为 `main/<agentKey>`，嵌套委派继续拼接（`delegateAgentPath`） |
| `depth` | 外层 run 深度 0，子 run 为父 +1；超过 `subagent.max_depth`（默认 2）时拒绝并提示模型自行处理 |
| 上下文隔离 | 不继承父历史、父已加载工具、附件与长期记忆；上下文 = `agent.systemPrompt` + 过滤后的 Tool/Skill 索引 + 单条 user(task) |
| 模型 | `modelKey` 为空时继承父 run 当前模型（含互备后的实际模型）；非空时独立解析 |
| 步数上限 | `EffectiveMaxSteps`：`maxSteps > 0` 用定义值，否则 `subagent.default_max_steps`（默认 8） |
| token 预算 | `maxTokensPerRun` 是子 run 递归口径上限（自身输入+输出+委派孙代理 delegated_*），0 表示不限 |
| 权限模式 | `inherit`/空为不收紧；`confirm`/`confirm_risky` 作为子 run 内全部服务端工具的权限下限（与工具级配置取更严者） |
| 事件与历史 | 子 run 事件实时冒泡进父 WS 事件流（带 `agentPath`），但消息不进父的 LLM 历史 |
| HITL | 子 run 的 `ask_question` / client tool 走共享 clientHub，按 toolUseId 路由到各自等待者 |
| 取消传播 | 子 runCtx 从父 runCtx 派生，父取消/断线级联取消子 run |
| 结果回填 | 取子 run 最后一条有正文的 assistant 消息（`subAgentFinalResponse`）；子 run 软失败以 isError 结果回给模型自行调整，仅取消/断线向上传播 |
| 计量 | `accumulateDelegatedTokens` 把子 run token 消耗（含递归 delegated）原子累加进父 run |

子 run 与外层 run 同 session 落库，`parent_run_id` 与 `agent_path` 持久化在 Run 记录上，是多 Agent 执行树的权威关系字段。子 run 不注入 Session 级 `<async_tasks>` 提醒（见 `docs/system/async_task.md`）。

## 4. 数据模型

`tblLlmAgent`（`model.Agent`）：`agent_id`、`agent_key`（caller 内唯一，唯一键不含 `deleted_at`）、`name`、`description`、`caller_key`、`route_values`（JSON 数组序列化，默认 `'[]'`）、`system_prompt`（mediumtext）、`model_key` / `model_version`、`tools_json` / `skills_json`（默认 `'[]'`）、`max_steps`（默认 8）、`max_tokens_per_run`（默认 0）、`permission_mode`（默认 `'inherit'`）、`status`（默认 1）、审计字段与软删除 `deleted_at`。

本模块不新增其他数据表。

## 5. 配置项与限制

子 Agent 委派受 ReAct Runtime 配置 `subagent` 控制（见 `conf/config.go`）：

| 配置 | 缺省 | 含义 |
|---|---|---|
| `subagent.enabled` | false | 未配置时 `delegate_agent` 不装配 |
| `subagent.max_parallel` | 1 | 同一轮多个 delegate_agent 调用的并行上限，1 即完全串行 |
| `subagent.default_max_steps` | 8 | agent 未配置 `max_steps` 时子 run 步数上限 |
| `subagent.max_depth` | 2 | 委派嵌套深度上限，外层 run 深度为 0 |

定义级限制：tools 白名单最多 64 项，skills 白名单最多 32 项，不允许空项与重复项；`status` 仅允许 0/1。`delegate_agent` 是否暴露还要求可见 agent 非空且未达深度上限（装配规则见 `docs/system/react_runtime.md`）。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 Agent 模块文档基线 |
