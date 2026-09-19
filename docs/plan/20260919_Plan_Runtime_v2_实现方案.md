# Plan Runtime v2 实现方案

- 日期：2026-09-19
- 分支：`feature/plan-runtime`（自 main `2e4d3f9` 拉出）
- 参考实现：`feature/plan-runtime-v1`（13 个提交，+4040 行，不合入 main，仅作移植源）
- 上游方案：本会话第一轮讨论的 Plan Runtime 设计（Plan 是 Runtime 不是 Tool；Step 复用 ReAct 引擎；Plan/Step/Attempt/Wait 全持久化）

## 1. v1 审计结论

v1 与第一轮方案高度吻合：核心 10 项要点中 8 项完全符合（协议与默认值、独立包与依赖方向、Planner 结构化输出、6 张表、Scoped ReactRun 经 `executeReactLoop` 执行、Wait/Resume 双通道、Finalizer、事件协议、Step 受限 Profile），两个关键设计缺口均有处理机制。本方案以 v1 为底本重做，审计发现的偏离与缺陷如下（v2 修正项编号 D1–D9）：

| # | 发现 | 位置 | 定性 |
|---|---|---|---|
| P1 | 模式分流只在 WS 入口（`startWSRun`），HTTP Run 入口不分流 | `controllers/http/react/ws.go` | 偏离（效果等价但不完整） |
| P2 | Cancel 不级联取消后续 PENDING step，DB 残留 PENDING | `runtime.go cancelExecutionState` | 偏离方案"后续 PENDING → CANCELLED" |
| P3 | `waiting_plan` 外层 run 会被 30 分钟 stale 过期置 `expired`：会话解锁但 PlanExecution 仍 WAIT_*，resume 会把 expired run 拉回 running，状态不一致 | `main.go` + `RestoreExternalRun` | 方案未覆盖处的漏洞 |
| P4 | `timeout_seconds` 列存在但无任何代码消费（恒 0） | `service/plan` | 半成品 |
| P5 | `CREATED` 状态定义未用（建库直接 PLANNING）；`EXTERNAL_TASK` 等待类型有常量、有前端按钮，但 executor 无产生路径 | `plan_runtime.go` / `executor.go` | 半成品 |
| P6 | resume SDK 走 WS 命令，HTTP `/plan_execution/resume` 前端未用（后端双通道都开了） | `agent-client.ts` | 与方案倾向（HTTP 优先）不同，但 WS 有合理理由 |
| P7 | 前端测试在分支 HEAD 是红的：净增 13 个失败（4 个断言与实现脱节 + `AgentPanel` 对 `state.plans` 无空值保护殃及 9 个用例），且 CI 已接入会报红 | `plan-runtime.test.ts` 等 | 缺陷 |
| P8 | `service/react/meta_tools.go` 直接读 `PlanStepResult`/`PlanExecution` 模型（react 服务层经 models 层耦合 plan 数据，非服务层反向依赖） | `meta_tools.go readPlanStepResultRef` | 可接受，需在注释中显式声明边界 |

v1 已正确处理、v2 直接继承的两个关键设计（第一轮评估的两个必答缺口）：

- **缺口 A（WAIT 与单活跃 Run）**：外层 ReactRun 收敛为新活跃态 `waiting_plan`（纳入 `HasActiveReactRun`），Plan WAITING 期间同会话普通消息被 `run is active` 拒绝；并发护栏在 WS 层（run goroutine 存活时拒绝 plan 命令并处理收尾竞态）。
- **缺口 B（最终回答进入会话历史）**：Finalizer 答复经 `PersistExternalFinalAnswer` 写成 outer run 的普通 assistant 消息；step run 靠 `parent_run_id` 过滤 + `plan/` agentPath 前缀过滤，不混入 LLM 历史与主 timeline；会话历史回放追加每个 Plan 一条 `plan_view_update` 快照。

## 2. v2 决策（相对第一轮方案与 v1 的修正）

**D1 分流位置统一**：`executionMode` 分流收敛为 service 层入口函数 `react.RunDispatch(...)`（或等价 helper），WS 与 HTTP Run 入口共用；`react.RunWithClientReaderContext` 保持零改动，plan 包提供 `plan.RunWithClientReaderContext`。消除 P1。

**D2 活跃性一致性（修 P3）**：保留 `waiting_plan` 活跃态模型，但补三条规则：
1. `staleActiveReactRunCondition` 把 `waiting_plan` 从 30 分钟过期条件中排除（无限期等待，状态权威在 PlanExecution）；
2. `Resume` 前置校验外层 run 状态：`expired`/终态且 Plan 仍 WAIT_* 时，先将会话解锁视为既成事实，resume 走"复活路径"——把外层 run 置回 `waiting_plan` 再继续（语义：Plan 等待优先于 run 状态机），或直接拒绝并要求 cancel 后重发；本方案选**复活路径**（与 v1 行为一致但显式化、可测试）；
3. Cancel Plan 时同步清理外层 run 状态，不留孤儿活跃行。

**D3 Cancel 级联补齐（修 P2）**：`cancelExecutionState` 增加一步：后续所有 `PENDING` step 批量置 `CANCELLED`（单事务）。

**D4 step 超时启用（修 P4）**：`runAgentStep` 用 `context.WithTimeout` 包裹 stepCtx（缺省 600s，`timeout_seconds` 为 0 时用缺省）；超时按 Attempt 失败处理，进入正常重试轨道。列保留，语义生效。

**D5 EXTERNAL_TASK 收口（修 P5）**：第一版保留"手动恢复"语义：executor 不自动产生 EXTERNAL_TASK 等待；Planner 不规划该类型；前端已有"任务已完成，继续执行"按钮仅服务于 `WAIT_EXTERNAL_TASK` 终态遗留视图。`CREATED` 常量删除（建库即 `PLANNING`）。自动轮询与 Watcher 留二期。

**D6 恢复通道定案（收 P6）**：双通道并存——SDK 默认 WS `plan_resume`（step 内 Client Tool/HITL 需要 `readClient`，这是 v1 实践验证的真实需求）；HTTP `/react/plan_execution/resume|retry|skip|cancel` 保留给服务端步骤与脚本化场景。方案原文"HTTP 优先"修正为"WS 交互优先、HTTP 兜底"。

**D7 前端修复清单（修 P7，随移植一并执行）**：
1. `AgentPanel.tsx` 对 `store.state.plans` 加空值保护（`?? {}`）；
2. `plan-runtime.test.ts` 断言对齐 04bc6b7 之后的实现（`idle` 语义、"需要完成操作"文案、EXTERNAL_TASK 恢复按钮）；
3. 移植后 vitest 全绿为合并门槛（main 基线 2 个预存环境性失败除外）。

**D8 `confirmPlan` 旧流共存**：`create_plan` 计划卡片与"开始执行刚才的计划"保留（ReAct 模式的能力），Plan 模式下 Planner 强制先行，两流互不影响。与第一轮方案一致。

**D9 模型层边界显式化（收 P8）**：`readPlanStepResultRef` 保留在 react 包，函数注释声明"经 models 层读取 plan 结果引用是 plan→react 的既定例外，禁止扩展为服务层调用"。

## 3. 架构与代码结构

沿用 v1 结构（与第一轮方案一致）：

```text
service/plan/
├── runtime.go        # 入口（RunWithClientReaderContext / Resume / Retry / Skip / Cancel）
├── planner.go        # 结构化规划（submit_plan 虚拟工具 + CompleteStructured）
├── executor.go       # step 推进、buildStepPrompt、pauseForUser、finalize
├── state.go          # PlanDraft / validateDraft（线性依赖校验）
├── repository.go     # 6 表读写 + 全部 CAS 事务
├── public_view.go    # PlanPublicView / detail / events / 会话历史快照
└── emitter.go        # plan_view_update / plan_step_event / client_tool 顶层透出

service/react/
└── external_runtime.go  # plan→react 桥接窄接口：
                         # PrepareExternalRun / RestoreExternalRun
                         # CompleteText / CompleteStructured
                         # RunScopedStep（内部 executeReactLoop，agentPath=plan/<stepKey>）
                         # PersistExternalFinalAnswer / MarkExternalRunState / AddExternalRunUsage
```

依赖方向不变：`service/plan → service/react`（经 external_runtime 窄接口），react 服务层不 import plan；`models/llm/plan_runtime.go` 为共享模型层。

Step 执行语义（继承 v1，符合方案）：`nextPendingStep → claimStep（CAS）→ 新建 Attempt（immutable）→ RunScopedStep（克隆 runtimeRequest、清空历史/todo、独立 clientHub、继承 apiKey/Tool/Skill 快照、ParentRunID=outer、ExecutionProfile 禁 create_plan）→ 完成写 StepResult（summary + result_ref，step 内可经 read_tool_result 按需读取）`。

Step Prompt 只含：用户目标 + 当前步骤（instruction/expectedOutput/successCriteria）+ 前序步骤 ResultSummary 与 ResultRef 指引。

## 4. 数据库

复用 v1 的 6 张表 DDL（`sql/plan_runtime_v1.sql` 并入 `sql/init.sql`）：`tblLlmPlanExecution` / `tblLlmPlanVersion` / `tblLlmPlanStep` / `tblLlmPlanStepAttempt` / `tblLlmPlanStepResult` / `tblLlmPlanWait`。变更点：

- 删除 `CREATED` 状态常量（D5）；
- `ReactRun.state` 新增 `waiting_plan`（v1 已有，v2 修正 stale 过期条件，见 D2）；
- 其余字段、唯一键（`uk_plan_step_order`、`uk_plan_attempt_no`、`uk_plan_version_no`）原样保留。

## 5. 协议

- `RunPayload.executionMode: 'react' | 'plan'`，缺省 react（后端 `components/params/react.go`，前端 `protocol/types.ts`）。
- WS 上行新增：`plan_resume | plan_retry | plan_skip | plan_cancel`（SDK 主通道）。
- HTTP 新增：`/react/plan_execution/detail | events | resume | retry | skip | cancel`（detail/events 与 SDK 既有调用路径一致）。
- WS 下行事件：`plan_view_update`（PlanPublicView 快照）、`plan_step_event`（包装 step run 原生事件）；`client_tool_use_start/end` 额外带 `planExecutionId/stepAttemptId` 顶层透出（SDK ToolExecutor 只认顶层事件）。
- 终态事件 `done` 的 `TerminationReason=plan_completed`。

## 6. 移植策略（v1 → feature/plan-runtime）

采用**文件级搬运 + 按修正清单改造**，不 cherry-pick v1 提交历史（其 13 个提交中近半是迭代修复，历史无需保留）：

| v1 资产 | 处置 |
|---|---|
| `service/plan/` 全包（~2000 行） | 整体搬运，按 D2/D3/D4/D5 改造 |
| `service/react/external_runtime.go` | 整体搬运；适配 main 的 runtime 模块化重构（`runtime_state.go` 拆分后 `runtimeRequest` 字段归属变化，逐处核对） |
| `models/llm/plan_runtime.go`、`sql/plan_runtime_v1.sql` | 搬运 + D5 常量清理 |
| `react_run.go`/`react_message.go`/`history.go` 的小改动 | 按 diff 重放（waiting_plan 状态、plan/ 过滤、GetRunHistoryEvents）；`staleActiveReactRunCondition` 按 D2 修改 |
| `controllers/http/react/ws.go`、`plan_execution.go`、`session.go`、`router/http.go` | 按 diff 重放 + D1 分流收敛 |
| `web/sdk` 全部改动 | 整体搬运 + D7 修复清单 |
| `meta_tools.go` 的 `readPlanStepResultRef` | 搬运 + D9 注释 |

预计冲突点集中在 `service/react/`（v1 基于旧 runtime 结构，main 已完成 request/engine state 拆分）与 `ws.go`（D1 重排）。

## 7. 实施里程碑

- **M1 持久化与协议**：6 表模型 + DDL、`executionMode` 协议、D1 分流入口、状态机常量；单测覆盖状态枚举与 CAS 条件形状。
- **M2 规划与执行**：planner（结构化输出 + 线性校验）、executor 主循环、RunScopedStep 桥接（适配 main 重构）、StepResult/resultRef、ExecutionProfile 受限。
- **M3 等待与恢复**：pauseForUser、双通道 resume（D6）、D2 活跃性一致性、Cancel/Retry/Skip（D3 级联）、D4 超时、Finalizer。
- **M4 前端与收尾**：SDK 全量移植 + D7 修复、模式开关、原生 plan 卡片、vitest 全绿；`docs/system/plan.md` 模块文档（模块枚举 + 索引 + check-docs 通过）；README 能力清单与边界表述更新；changelog 按仓库规范补记。

## 8. 验收标准

1. 旧客户端不传 `executionMode` 行为与 main 完全一致（回归）。
2. Plan 全链路：开关 → 规划 → 逐步执行（进度可见）→ USER_INPUT 表单等待 → resume → USER_ACTION 确认/拒绝 → 终态 + Finalizer 总结；断连后经 `/plan_execution/detail` + 历史回放可还原等待态。
3. 状态一致性：WAIT 期间普通消息被拒；stale 过期后 resume 走复活路径且状态机自洽；Cancel 后无 PENDING 残留、无孤儿活跃 run。
4. `go test ./...` 与 `bash scripts/check-docs.sh` 通过；SDK vitest 除 main 预存 2 个失败外全绿。

## 9. 二期不做清单（沿用第一轮方案）

自动 Replan、并行 Step、完整 DAG、调度器、服务重启自动恢复、External Task 自动轮询、多节点 Worker 抢占、Lease/Heartbeat、Temporal 类 Durable Engine。二期升级路径：PlanExecution 状态已落库，补 Resume Worker 与 Lease 即可演进为 Durable，不推翻本结构。
