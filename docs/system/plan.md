---
title: Plan 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: plan
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/react/ws.go
  - controllers/http/react/plan_execution.go
  - service/plan/runtime.go
  - service/plan/executor.go
  - service/react/external_runtime.go
  - models/llm/plan_runtime.go
  - sql/plan_runtime_v1.sql
summary: Plan Runtime 执行模式：结构化规划、逐步 Scoped ReactRun 执行、持久化等待与恢复、控制命令与事件协议
---

# Plan 模块功能文档

## 1. 模块概述

Plan 模块是 ReAct Runtime 之上的第二种执行范式：`executionMode=plan` 的 Run 先由 Planner 生成结构化计划并落库，再由 Executor 逐步创建隔离的 Scoped ReactRun 执行 `AGENT` 步骤，`USER_INPUT` / `USER_ACTION` 步骤进入持久化等待，由用户经命令恢复。Plan / Version / Step / Attempt / Result / Wait 全量持久化，等待与恢复不依赖原 goroutine。

依赖方向为 `service/plan → service/react`（经 `external_runtime.go` 窄接口复用 ReAct 引擎），react 服务层不依赖 plan。普通模式（`executionMode` 缺省 `react`）行为不变。

## 2. 接口清单

| 路径 / 通道 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| WS `run`（`executionMode: plan`） | - | 发起 Plan Run（与普通 Run 同入口分流） | `react.startWSRun` → `dispatchRunExecution` |
| WS `plan_resume` | - | 恢复等待中的 Plan（携带 wait 响应） | `react.startWSPlanCommand` |
| WS `plan_retry` / `plan_skip` / `plan_cancel` | - | 步骤重试 / 跳过 / 整体取消 | `react.startWSPlanCommand` |
| `/react-base-service/react/plan_execution/detail` | POST | Plan 公开视图 + Attempt 清单 | `react.GetPlanExecutionDetail` |
| `/react-base-service/react/plan_execution/events` | POST | 按 Attempt 重建步骤事件流 | `react.GetPlanStepEvents` |
| `/react-base-service/react/plan_execution/resume` | POST | HTTP 恢复（无交互场景兜底） | `react.ResumePlanExecution` |
| `/react-base-service/react/plan_execution/retry` | POST | HTTP 重试失败步骤 | `react.RetryPlanExecution` |
| `/react-base-service/react/plan_execution/skip` | POST | HTTP 跳过步骤 | `react.SkipPlanExecution` |
| `/react-base-service/react/plan_execution/cancel` | POST | HTTP 取消 Plan | `react.CancelPlanExecution` |

## 3. 核心逻辑

### 3.1 分流与入口

`dispatchRunExecution`（`controllers/http/react/ws.go`）按 `payload.ExecutionMode` 分流到 `react` 或 `plan` 的 `RunWithClientReaderContext`；分流位于 controller 层以保持 `plan → react` 单向依赖。Plan 入口先 `PrepareExternalRun` 建立外层 Run 锚点（复用 `prepareRuntimeRequest` + `createReactRunContext`，含同会话活跃 Run 互斥检查）。

### 3.2 规划（Planner）

`CompleteStructured` 用单一虚拟工具 `submit_plan` 的 JSON Schema 约束输出（不进 ReAct、不执行业务工具）。步骤定义含 `stepKey/name/instruction/stepType/expectedOutput/successCriteria/dependsOn/maxAttempts/required/question/responseSchema`。`validateDraft` 强制线性依赖（只能引用更早步骤，无依赖时自动补前一步），`stepType` 仅 `AGENT/USER_INPUT/USER_ACTION`。

### 3.3 步骤执行（Executor）

循环：取最小 `step_order` 的 PENDING 步骤 → `claimStep` CAS 占用（`plan WHERE status='RUNNING' AND current_step_id=''` + `step WHERE status='PENDING'`，同一时刻最多一个 RUNNING 步骤）→ 新建不可变 Attempt → `RunScopedStep` 执行。

Scoped ReactRun 隔离规则：清空外层历史 / Todo / 已加载工具，`agentPath=plan/<stepKey>`，独立 clientHub；继承 apiKey 与 Tool/Skill 快照；`ParentRunID=outer`（因此不进入后续普通 Run 的 LLM 历史）。Step Prompt 仅含用户目标、当前步骤指令与前置步骤摘要 + `result_ref`（长结果经 `read_tool_result` 按 `plan_result_` 前缀按需读取，授权边界为 session）。步骤执行受 `ExecutionProfile` 限制：`plan/` 前缀 Run 禁用 `create_plan` 防嵌套。

每个 Attempt 独立超时：`timeout_seconds`（缺省 600s）经 `context.WithTimeout` 包裹；超时按 Attempt 失败处理，未耗尽 `max_attempts` 时自动新建 Attempt 重试（手工 Retry 在耗尽后允许再追加一个）。

### 3.4 等待与恢复（Wait / Resume）

`USER_INPUT` / `USER_ACTION` 步骤不执行模型，直接落库 WaitRequest（Step=WAITING、Plan=WAIT_*、外层 Run=`waiting_plan`），当前 goroutine 结束。`waiting_plan` 计入 `HasActiveReactRun`（等待期间同会话普通消息被拒），但不参与 30 分钟 stale 过期——等待态权威在 PlanExecution。

恢复双通道：SDK 默认 WS `plan_resume`（step 内 Client Tool 交互需要 readClient）；HTTP 端点用于服务端步骤与脚本化场景。Resume 校验 pending wait 与响应 Schema；`USER_ACTION` 提交 `approved: false` 时整体取消。恢复前校验外层 Run 状态：`finished/cancelled/error` 拒绝，`expired` 走复活路径（置回 running 续跑）。用户回答经 ResultSummary 进入后续步骤 Prompt。

### 3.5 取消 / 重试 / 跳过 / 终态

- **Cancel**：先取消外层 context（RUNNING 步骤随父级联），再持久化级联——pending Wait、当前步骤、**全部后续 PENDING 步骤**与 Plan 整体置 CANCELLED。
- **Retry**：仅 FAILED 步骤；事务内 Step→PENDING、Plan→RUNNING，后续按 Attempt 序号续跑。
- **Skip**：仅 `required=false` 且 PENDING/FAILED/WAITING 步骤。
- **Finalize**：全部步骤终态后 `CompleteText` 纯文本总结（不暴露工具），结果写入 `result_json`，最终答复作为外层 Run 的 assistant 消息落库（`PersistExternalFinalAnswer`），后续普通 Run 的历史天然可见计划结论。

### 3.6 事件协议

`plan_view_update`（PlanPublicView 快照）驱动进度卡片；`plan_step_event` 包装步骤 Run 的原生事件（含 planExecutionId/stepId/stepAttemptId/stepRunId）；`client_tool_use_start/end` 额外带归因字段顶层透出。Attempt 事件经 `/plan_execution/events` 从持久化消息重建，无独立事件表。会话历史回放为每个 Plan 追加一条最新 view 快照。

## 4. 数据模型

六张表（DDL 见 `sql/plan_runtime_v1.sql`，已并入 `sql/init.sql`）：

| 表 | 职责 |
|---|---|
| `tblLlmPlanExecution` | Plan 实例：状态（PLANNING/RUNNING/WAIT_*/SUCCEEDED/FAILED/CANCELLED）、outer_run_id、current_version_id/current_step_id、result_json、error_code/error_summary |
| `tblLlmPlanVersion` | 计划版本（V1 恒 version_no=1, reason=initial） |
| `tblLlmPlanStep` | 步骤定义与状态（PENDING/RUNNING/WAITING/SUCCEEDED/FAILED/CANCELLED/SKIPPED）、step_type、max_attempts、timeout_seconds、depends_on_json、result_summary/result_ref |
| `tblLlmPlanStepAttempt` | 不可变执行尝试（唯一键 step_id+attempt_no，step_run_id 关联 Scoped ReactRun） |
| `tblLlmPlanStepResult` | 步骤结果快照（summary + result_json + result_ref） |
| `tblLlmPlanWait` | 等待请求（wait_type/question/response_schema_json/response_json/status/resolved_at） |

`tblLlmReactRun.state` 新增 `waiting_plan` 活跃态。所有状态迁移均为单事务条件 UPDATE + RowsAffected 断言（CAS）。

## 5. 配置项与限制

- 复用 `llm.react.*` 运行时配置；不新增独立 YAML 开关。
- V1 限制（显式不做，留二期）：自动 Replan、并行步骤、完整 DAG、外部任务自动轮询（`WAIT_EXTERNAL_TASK` 仅手动恢复）、服务重启自动恢复 Worker、多节点抢占。PlanExecution 已落库，补 Resume Worker 与 Lease 即可演进为 Durable Runtime。
- Planner 规划步骤数上限 20；线性依赖强制。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 基于 feature/plan-runtime-v1 移植并按 v2 方案修正（分流收敛、waiting_plan 免 stale 过期、取消级联、步骤超时启用、常量清理） |
