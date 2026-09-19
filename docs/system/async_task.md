---
title: AsyncTask 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: async_task
maintainer: react-base-service 项目组
status: active
related_code:
  - controllers/http/react/async_task.go
  - service/react/async_task.go
  - service/react/async_task_query.go
  - service/asynctask/runtime.go
  - service/asynctask/types.go
  - models/llm/react_async_task.go
  - models/llm/react_async_task_sync.go
summary: 异步提交型工具的任务记录、pending 提醒注入、resolve/get 元工具、TTL 过期与调度系统 Provider 状态同步框架
---

# AsyncTask 模块功能文档

## 1. 模块概述

Async Task 模块处理"提交后不会立即得到最终结果"的业务 Tool（Tool `config` 中 `async=true`）。一次成功提交会被记录为 Session 级任务（`tblLlmReactAsyncTask`），后续 Run 以 `<async_tasks>` 块注入未完结提醒，模型通过查询工具确认结果后用 `resolve_async_task` 显式完结。

模块包含两条链路：本地链路（记录、提醒、回读、完结、TTL 过期）与可选的 Provider 同步链路（`config.asyncTask.schedulerType` 声明后，由调度系统 Provider 把第三方执行状态同步进任务行）。显式限制：外部任务完成不会自动唤醒原 Run，系统也不会在任务完成时主动通知模型，只有用户下次发消息、Run 重新构建上下文时才有机会处理提醒。

本模块对外的 HTTP 管理接口只有异步任务列表查询；`resolve_async_task` / `get_async_task` 是 ReAct 引擎内置 Meta Tool，不走 HTTP。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/react/async_task/list` | POST | 游标分页查询会话待处理异步任务 | `react.ListAsyncTasks` |

## 3. 核心逻辑

### 3.1 提交记录（recordAsyncSubmit）

服务端业务 Tool 执行成功后（`executeServerTool` 内），`recordAsyncSubmit` 判定 `config.async=true` 才落库一条任务：`sessionId` / `runId` / `toolUseId` / `toolName` / `submit_input`（提交入参快照）/ `submit_result`（原始响应全文，列为 MEDIUMTEXT）/ `async_hint`（`config.asyncHint`，截断至 512 字符匹配 `varchar(512)` 列宽），初始 `state=pending`，`expire_at` 为当前时间 + 7 天（`reactAsyncTaskTTL`）。

落库失败只告警不阻断工具结果回填。落库成功后任务插入内存 pending 列表头部，本 run 后续步骤的提醒立即可见；列表超过 10 条（`maxReactAsyncTaskReminderItems`）截断并置 `pendingAsyncTasksHasMore`。传输层失败（`normalized.IsError`）不记录；HTTP 2xx 但响应体为业务错误时照常记录，依赖模型读到错误响应后调用 `resolve_async_task` 标记 `failed` 清除。

若 `config.asyncTask` 存在，`applyManagedAsyncTaskFields` 调用 `asynctask.IdentifySubmission` 由 Provider 识别任务并补写 `scheduler_type` / `task_key` / `task_info` / `execution_status=processing` / `next_sync_at`；识别失败降级为纯模型提醒模式，不影响 Tool 结果与当前 Run。

### 3.2 pending 注入提醒（<async_tasks> 块）

新外层 Run 初始化时 `loadPendingReactAsyncTasks` 先执行惰性过期（`ExpireReactAsyncTasks`，把 `expire_at` 已到期的 pending 置 `expired`），再按提交时间倒序取最多 10 条未完结任务。子 Agent Run 不注入这批 Session 级提醒。

发送模型前 `renderReactAsyncTaskReminder` 把任务渲染为 `<async_tasks>` 块，作为临时 user 消息附加（`contextMessages`，受执行档案 `InjectAsyncTaskReminder` 控制），不写入持久化聊天历史。每个内容字段截断至 2048 字符（`maxReactAsyncTaskRenderRunes`）并追加截断标记，落库仍存全文。块内固定 7 条处理规则，核心约束：只处理当前列表或可信历史来源的 toolUseId；内容截断时先 `get_async_task` 回读；确认完成或失败后必须 `resolve_async_task`；系统不会主动通知，结束回复不得向用户承诺自动处理。

### 3.3 resolve_async_task / get_async_task 元工具

`resolve_async_task`：入参 `toolUseId` + `status`（`done` / `failed`），把 pending 任务置 `resolved` 并写 `resolve_status`，同时从内存 pending 列表移除（本 run 后续不再提醒）。已完结的任务返回 `alreadyResolved` / `alreadyTerminal` 幂等结果，不报错不重试；不存在的 toolUseId 返回 `found=false` 并提示从最新提醒重读 ID。它是 pending 提醒的唯一显式清除入口，TTL 过期是兜底清除。

`get_async_task`：按 `toolUseId` 回读单个任务的完整提交记录（含未截断的 `submitInput` / `submitResult`、`state`、`resolveStatus`、`expireAt` 等），按 `sessionId` 隔离查询。两个工具名均在 Tool 模块的保留名清单内，业务工具不得同名注册。

### 3.4 Provider 同步框架

`service/asynctask` 定义调度系统接入协议：`SchedulerProvider` 接口（`Type` / `IdentifyTask` / `Start` / `Stop`），进程内注册表按类型去重（重复注册 panic）。基座服务默认无内置 Provider，由接入方自行注册；`ProviderRuntime` 由公共运行时实现（`EmitObservation` / `ClaimTasks` / `ScheduleRetry` / `ReleaseTask`），Provider 不直接操作数据库。

状态同步落库规则（`models/llm/react_async_task_sync.go`）：

- `ApplyReactAsyncTaskObservation` 按 `scheduler_type + task_key` 定位 `processing` 记录应用观测；以 `last_event_id`、`last_sequence`、`last_observed_at` 三重判据丢弃乱序与重复事件；终态（`succeeded` / `failed`）不可逆，写入时补 `completed_at` 并清空 `next_sync_at` 与租约；
- `ClaimReactAsyncTasks` 以 `FOR UPDATE SKIP LOCKED` + 租约（`lease_owner` / `lease_until`）抢占长期未更新且 `next_sync_at` 到期的任务，支持多实例并发对账；`ScheduleReactAsyncTaskRetry` 累加 `retry_count` 并记录 `last_sync_error`；
- `Start` / `Stop` 受 `async_task.enabled` 总开关控制，逐个启动已注册且 `Available()` 的 Provider，全部启动失败则不进入运行态。

### 3.5 查询接口（/react/async_task/list）

`ListSessionAsyncTasks` 校验 Session 存在性与历史查看权限（`canViewReactSessionHistory`），先做会话级惰性过期，再游标分页返回"第三方已终态但本地尚未处理"的任务：`state=pending` 且 `execution_status IN (succeeded, failed)`（`ListReadyReactAsyncTasksBySessionPage`）。`pageSize` 缺省 100、上限 200；游标是数字 ID 的 base64（RawURL）编码，还有下一页时返回 `nextCursor`。响应同时返回 `hasProcessingTasks`（会话是否仍有 Provider 跟踪中的 `processing` 任务）。响应项不含 `submit_input` / `submit_result` 快照与 `task_info`。

## 4. 数据模型

`tblLlmReactAsyncTask`（`model.ReactAsyncTask`）按字段组：

| 字段组 | 字段 |
|---|---|
| 身份与快照 | `session_id`、`run_id`、`tool_use_id`、`tool_name`、`submit_input` / `submit_result`（mediumtext）、`async_hint`（varchar(512)） |
| 本地状态机 | `state`（`pending` / `resolved` / `expired`）、`resolve_status`、`expire_at` |
| Provider 同步 | `scheduler_type`、`task_key`、`task_info`（json）、`execution_status`（`processing` / `succeeded` / `failed`）、`provider_status`、`progress`、`error_message`、`completed_at` |
| 同步调度 | `next_sync_at`、`retry_count`、`lease_owner`、`lease_until`、`last_sync_error`、`last_event_id`、`last_sequence`、`last_observed_at` |

未设置的时间与进度以数据库哨兵值（`1970-01-01 00:00:00`、`progress=-1`）写入，`AfterFind` 还原为 nil。

本模块不新增其他数据表。

## 5. 配置项与限制

| 项 | 值 | 说明 |
|---|---|---|
| `async_task.enabled` | 缺省 false | Provider 同步框架总开关；关闭时 `IdentifySubmission` 与 `Start` 均不生效，本地提醒链路不受影响 |
| Tool `config.async` | 布尔 | 开启后成功提交才落任务记录 |
| Tool `config.asyncTask.schedulerType` | 必填字符串 | 仅允许用于 `async=true` 的工具，需与已注册 Provider 的 `Type()` 一致 |
| 任务 TTL | 7 天（`reactAsyncTaskTTL`） | 读取 pending 前惰性置 `expired`，无定时任务 |
| 提醒条数上限 | 10 条（`maxReactAsyncTaskReminderItems`） | 超出部分标记"另有更早的未完结任务未列出" |
| 提醒字段截断 | 2048 字符（`maxReactAsyncTaskRenderRunes`） | 落库不截断，`get_async_task` 回读全文 |
| `async_hint` 截断 | 512 字符 | 匹配列宽 `varchar(512)` |
| 列表分页 | 缺省 100，上限 200 | 游标为 base64 编码的数字 ID |

显式限制汇总：外部任务完成不自动唤醒 Run、不主动通知模型；`async_hint` 与 Provider 增强均为可选能力，识别失败不影响 Tool 调用成功；`submit_input` / `submit_result` 列为 MEDIUMTEXT，超 16MB 时由 DB 写入报错触发不落库不提醒的降级。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 AsyncTask 模块文档基线 |
