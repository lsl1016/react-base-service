# 交互运行时借鉴方案 —— Session 模型、Steering、运行时命令队列与 Subagent

| | |
|---|---|
| 日期 | 2026-09-25 |
| 状态 | 待评审 |
| 参照系 | 上级目录 `ZCode` 仓库（`apps/zcode-cli/packages/core`、`contracts`、`adapters`、`bootstrap`） |
| 适用范围 | `react-base-service` ReAct Runtime（`service/react`） |
| 关联文档 | `docs/plan/20260925_ToolRuntime流水线借鉴与优化方案.md`（工具执行层）、`docs/plan/20260925_AgentLoop终止边界与循环治理优化方案.md`（循环边界） |

---

## 0. TL;DR

对 ZCode 的 Session 持久化模型、Steering（运行中引导/排队）、Runtime Command Queue（Agent Loop 的"邮箱"）、Subagent 体系做了代码级对照（对应调研笔记第 18–22 条），结论：

1. **Session 模型（笔记 18/19）**：源码证实 ZCode **不是纯 Event Sourcing**——事件日志默认只在内存（会话去激活即清空、按 turn 窗口驱逐），冷恢复从持久行重建而非重放日志。ZCode 真实范式 = "持久行 + 内存追加事件 + 投影/重放"。**本项目的 MySQL 行式存储（session/run/message/run 内快照）已经是同一范式**，不需要也不应该引入事件日志。真正缺的是"会话级可排队意图"的持久账本（ZCode `session_input` 表）。
2. **Steering（笔记 20，最高优先）**：本项目对运行中 session 的新用户消息是**硬拒绝**（`ErrorReactRunActive`，错误码 6000100）——用户必须等 run 结束或另开会话，这是与 ZCode 体验差距最大的一处。ZCode 的 admission 决策树（idle→开轮 / busy+可引导→guide / busy→排队 / 仅内部路径拒绝）、guide 在**工具批提交后、下一次模型请求前**注入（永不插入 tool_use/tool_result 之间）、队列管理（编辑/删除/重排/预留）值得整套借鉴。
3. **Runtime Command Queue（笔记 21）**：ZCode 用两个不同的队列分别承载**用户意图**（pendingInputs）和**机器事件**（后台任务通知、子代理回复），后者在模型步边界被"吸收"进当前轮或批量合成一个新轮。本项目目前唯一的"邮箱"是 `clientMessageHub`，且只承载 toolUseId 寻址的工具应答——没有任何机器事件回灌通道。命令队列是 Steering 和后台子代理的共同基础设施，应与 Steering 同期建设。
4. **Subagent（笔记 22）**：本项目 `delegate_agent` 已实现隔离运行（独立 run 行、历史清空、工具/记忆过滤、模型继承含互备后实际模型、深度限制、取消级联、token 归集）——隔离清单与 ZCode 高度一致。缺口是**只能同步阻塞**：无后台委派（`async_launched` 等价物）、无完成通知回灌、无 SendMessage。

分 6 个阶段（编号独立于 ToolRuntime 方案）：

| 阶段 | 内容 | 优先级 |
|---|---|---|
| S1 | Steering 引导注入（guide）：运行中消息进入当前 run 的下一个模型轮 | **P0** |
| S2 | Steering 排队（queue）+ run 结束自动续跑 | **P0** |
| Q1 | 运行时通知邮箱：模型轮边界吸收机器事件（命令队列最小版） | P1 |
| A1 | 后台委派（async_launched 等价）+ 完成通知经 Q1 回灌 | P1 |
| S3 | 队列管理 API（编辑/删除/重排/预留）+ 自动续跑开关 | P2 |
| A2 | SendMessage（向运行中/已结束子代理发消息）+ 子代理看门狗 | P2 |

核心取舍：**引导注入点只有一处**——工具结果落库之后、下一次模型请求之前（对齐 ZCode `turn-tools.ts:469-470` 的边界纪律，保证 provider 语法永远合法）；**队列是内存的、账本才是持久的**（对齐 ZCode `session_input` 设计：崩溃不丢"排队意图存在过"这一事实，但重启后一律作废）。

---

## 1. Session 模型：范式核实与本项目对照

### 1.1 ZCode 的真实范式（笔记 19 的结论被源码证实）

ZCode 的 Session = **一个持久身份行 + 事实行集合（SQLite）+ 一个可随时丢弃和冷恢复的驻留运行实例（AgentRuntime）**。三层结构：

1. **持久行**（`adapters/src/storage/session-store/sqlite-session-store.ts:225`，默认 `~/.zcode/cli/db/db.sqlite`）：`session` / `message` / `part` / `todo` / `session_target` / `session_input` / `session_entry` / 三个 usage 表 / dwf 工作流日志表（22 个 migration）。**事实发生时内联落库**，如排队输入的晋升 = "账本置 promoted + 用户消息落库"同一事务（`session-store.port.ts:1162-1171`，注释明确"promotion（原子性硬要求）"）。
2. **内存事件流**：`SessionEventStorePort` 全仓**唯一**实现是内存版（`in-memory-session-event-store.ts:33-45`）；会话去激活时整体清空（`session-residency.ts:67`："去激活后内存 event store 必须与『从未加载』等价"）；已完成 turn 的流式瞬态事件按窗口驱逐（`session-event-retention.ts:5-8`）。
3. **投影**：`EventReducer`（`contracts/src/events/event-reducer.ts:74-574`）逐事件维护 UI 投影（mode/status/pendingPermissions/activeToolCalls/backgroundTasks/target 等）。

**"不是纯 Event Sourcing"的证据链**：

- 事件日志不是事实源：内存实现、去激活即清、按 turn 驱逐——纯 ES 日志永不删除；
- 冷恢复读行不重放：`runtime.resumeFromStore`（`core/src/runtime/methods/resume.ts:59-328`）从 message/part/todo/session_entry 重建历史与状态，只有 checkpoint/file-rewind/goal 等**少数被镜像进 `session_entry` 的事件**在恢复时物化回内存（`methods/events.ts:263-489`，注释："只写内存 eventStore 会导致冷启动后 goal iteration/todo 分组丢失"）；
- 关键状态转移是事务性行写（晋升、fork 的 `commitForkBundle` 全有或全无），不是从日志推导。

事件日志的实际用途：活投影、客户端增量同步（`getEventsAfter`）、少量恢复、usage 落库的来源。

### 1.2 本项目对照：已是同一范式

| ZCode 状态项 | ZCode 存储 | 本项目存储 | 缺口 |
|---|---|---|---|
| 会话身份 | `session` 行 | `tblLlmReactSession` | — |
| 运行实例状态机 | 内存 AgentRuntime + session 行 | `tblLlmReactRun`（state/step_index/token 总量） | — |
| 消息 | `message`/`part` 行 | `tblLlmReactMessage`（message_type 区分 user_input/assistant/assistant_partial/tool_result/compact_summary） | — |
| 工具调用状态 | ToolPart 行 | message JSON 内 tool_use parts + `tblLlmReactToolResult` | — |
| Todos | `todo` 表 | `run.TodoStateJSON` 快照 | — |
| 模型选择 | `session_entry` + 消息内锚点 | `run.ModelKey/ModelVersion` | — |
| Usage | 三个 usage 表 | run 行 token 列（实时写回，`engine.go:231-243`） | — |
| 排队/引导意图 | `session_input` 账本 | **无** | **S1/S2 补** |
| 机器通知账本 | 同上（kind=backgroundNotification） | **无** | **Q1 补** |
| Rewind/Fork（分支剪、重试/改写） | `revert` 游标 + fork 事务包 | **无** | 见 §5 取舍 |

同样**不需要补**的：持久事件日志。本项目的 WS 事件发射器（`emitter.EmitStep` 系列）≈ ZCode 的 event sinks（活投影 + 客户端推送），DB 行是唯一事实源——这正是 ZCode 的实际做法，当前的隐式架构选对了。

### 1.3 值得单独记录的 ZCode Session 语义（供后续实现引用）

- **重启不保留队列**：恢复时所有 admitted 输入结算为 `discarded(session_resumed)`（`resume.ts:233`，`methods/events.ts:396-397`）——队列的存在性持久、内容不跨进程复活。这个语义简单诚实，照抄。
- **Retry/Edit = fork 的产品化**：`retryTurn` = rewind 截断该 turn + 重发原 prompt；`editUserQuery` = 同样截断 + 新文本（`bootstrap/src/zcode-protocol-v4/commands/handlers/fork-edit-retry.ts:4-5` 头注释原文）。rewind 是**分支剪**不删数据：`setRevert` 写 `keptMessageIDs/branchCutAfterMessageID/branchGeneration` 游标（`rewind-message.ts:566-579`），旧分支留库。
- **驻留池**：活跃会话驻内存，空闲去激活（需无 pending 交互/命令/订阅者），下次订阅即冷恢复（`session-residency.ts:70-110`）。本项目每 run 起goroutine、DB 重建上下文的模式与之等价，不必引入驻留池。

---

## 2. Steering（重点借鉴）：运行中用户消息的准入、引导与排队

### 2.1 ZCode 机制精读

**准入决策树**（`core/src/runtime/methods/prompt-admission.ts:20-126`；busy 判定 `runtime-command-queue.ts:100-109` 聚合 lease/前台执行/drain/队列/活跃 turn/预留六项）：

```
新用户输入
  ├─ idle（且无 reservation）→ 预留 turn 槽 + 入队 prompt 命令 → 立即开新 turn
  ├─ busy + requireIdle（内部路径）→ 拒绝 { kind:"rejected", reason }
  ├─ busy + 可引导（无附件、activeTurn.steerable）→ GUIDE：推入 activeTurn.pendingInputs
  └─ busy 其他情况（含有附件）→ DEFERRED QUEUE：仅持久化为 TurnSteerQueued 事件（账本 admitted）
```

关键点：

- **admission 原子性**（`prompt-admission.ts:15-19` 注释）：每个 runtime 自完成准入（busy 检查 + 槽位预留 + FIFO 入队一体完成），拆开就会打开"同一 session 开出两个 turn"的异步窗口。
- **guide 注入点只有三个**（`turn-guide-drain.ts:13-66`）：① 整个工具批结果提交后（`turn-tools.ts:469-470`，在 `persistToolModelStepFinish` 之后）；② 纯文本步结束、Stop hook 之前（`turn-stop.ts:195-200`——若此时有 guide，当前 turn **不结束**，guide 作为最新 user 消息继续下一轮）；③ 循环顶读取。注入即构造真实 user 消息进 history + 落库 + 账本晋升（同一事务），并把下一次模型请求的 trace 归因切到 guide 的 queryId。
- **顺序保证**：guide 永远落在整批 tool_result 之后，成为下一次请求的尾部 user 消息——绝不插入 assistant tool_use 与 tool_result 之间（这也是控制轮必须排队的同一 provider 语法约束）。
- **回退语义**（`fallbackPendingGuidesToQueue`，`steering.ts:478-526`）：turn 被打断/结果要求停轮时，未消费的 guide 自动降级为 queue（`guide.turnInterrupted` / `guide.noToolBoundary`），不丢输入。
- **队列管理**（`steering.ts:590-1013`）：按 `pendingInputId` 预留/晋升/删除/编辑/重排；多客户端预留（`reservePendingInputById` + `TurnSteerDispatchChanged` 事件）防止两个窗口同时晋升同一条；重排同时改写 `intent.queuePosition` 并落库（"只重排数组不更新账本 → 冷热投影事实分叉"）。
- **queueAutoDrain**（默认 true）：队首晋升走 `sendQueuedNow`，受 `foregroundPromotionLease` 保护——该 lease 解决"Stop 旧执行与晋升命令入队之间，后台通知 B 抢先出队"的窗口（`runtime-command-queue.ts:83-85`），出队与 lease 消费必须在**同一同步步**（`:72-87`）。

### 2.2 本项目现状：一律拒绝，无中途通道

- 服务层：`createReactRunContext`（`runtime.go:300-372`）事务内 `HasActiveReactRunWithDB`（`models/llm/react_run.go:198-209`，计数 running/waiting_client_message/waiting_plan/cancelling）→ 命中即 `ErrorReactRunActive`（`components/error.go:90-93`，错误码 6000100"当前会话已有运行中的 ReAct run"）。
- WS 层双重拦截：`handleWSMessage`（`controllers/http/react/ws.go:196-237`）第二个 `EventRun` 直接回错误事件，plan 命令同样被拒（`ws.go:204-219`）。
- 现有"邮箱"：`clientMessageHub`（`client_hub.go:39-50`）单 pump 多 waiter、按 toolUseId 谓词路由、缓冲 16 丢旧、断连即死——**只承载工具应答/确认答复两类可寻址消息**，自由文本会被丢弃并打日志（`client_hub.go:193-199`）。用户输入到达运行中 run 的唯一途径是模型预先 `ask_question`。
- `feedback.go` 纯事后评价，不进运行上下文。

### 2.3 落地方案

**S1（P0）：guide 引导注入。**（✅ 已实施，2026-09-25，见 `docs/changelog/20260925_v1.0_新增Steering引导注入与运行中消息准入账本.md`）

1. 准入改造：`StartRun`/WS 消息处理中，命中 active run 时不再直接报错——若该 run 状态为 `running`（非 waiting_*/cancelling）、非软着陆收尾、输入无附件、配置允许 steering → 返回 `{kind:"guided", pendingInputId, queueLength}`；否则走 S2 排队；`waiting_client_message`/`waiting_plan`/`cancelling` 状态一律走排队或拒绝（不打断 HITL 等待）。
   - 实施记录：准入决策纯函数化（`steerAdmissionDecision`），queue 决策分支已实现但由 `steering.queue`（默认 false）灰度；软着陆经进程内注册表对准入 goroutine 可见；账本表增加 `settle_reason` 列记录结算原因（方案表格未列，属最小必要扩展）。
2. 存储：新增 `tblLlmReactPendingInput`（session_id、run_id、kind=user_input/notification、delivery=guide/queue、status=admitted/guided/queued/cancelled/discarded、content、seq、created_at）。**guide 消费 = 同一事务内"账本置 guided + 用户消息落库"**（对齐 ZCode promoteSessionInput 原子性；本项目事务内先 `persistUserInput` 等价物再更新账本行）。（✅ 已实施，含 settle_reason 列）
3. 注入点（唯一）：`engine.go:217-218` 之后、下一次 `callModelRound` 之前——本轮所有 tool_result 已落库，注入的 user 消息成为请求尾部，天然满足"不插入 tool_use/tool_result 之间"。（✅ 已实施，另补纯文本 finish 边界）
4. 纯文本边界续跑：`engine.go:177-189` 的 finish 分支先查 pending guide——有则**不结束 run**，注入后 `continue`（对齐 `turn-stop.ts:195-200`）。需配套防抖：guide 循环受循环预算/软着陆约束，软着陆激活期间不再接收 guide。（✅ 已实施，另规定输出截断续写优先于 guide）
5. 取消/出错回退：run 取消或硬错误时，未消费 admitted 行结算为 `discarded(turn_cancelled)`（S1 的 Phase 1 闭合修复已在 `executeToolCalls` 收口，此处一致）；进程重启后残留 admitted 行在下一次 `createReactRunContext` 统一结算 `discarded(session_resumed)`。（✅ 已实施，采用 ZCode 完整词汇：cancelled/断连→turn_cancelled、error/timeout/expired→turn_failed、正常结束未消费→run_finished）
6. WS/前端协议：新增 `steer_queued/guided` 上行回执与 `turn_steer_*` 下行事件（对齐 ZCode 事件词汇：queued/delivery_changed/drained/rejected/discarded/reordered）。（✅ 已实施为 `steer_guided/steer_queued/steer_rejected/steer_drained/steer_discarded` 下行事件——本项目上行/下行统一走 WS 事件通道，未拆双通道；S3 做队列管理时再补 delivery_changed/reordered）

**S2（P0）：queue 排队 + run 结束自动续跑。**（✅ 已实施，2026-09-25，见 `docs/changelog/20260925_v1.0_新增Steering排队与run结束自动续跑.md`）

- busy 且不可引导（有附件/waiting 状态/配置关闭）→ 账本落 `queued` 行，回执 `{kind:"queued"}`。（✅ 已实施；准入时保存原始 run 请求快照 payload_json——steer 消息不带模型选择时回填被引导 run 的实际模型，晋升重建 run 请求时 prepareRuntimeRequest 仍全量重新解析）
- `state.finish`（含 `finishExhausted`）之后检查本 session 未消费 queued 行：开启 auto-drain（默认开）则用队首内容**自动开启新 run**（复用现有 `run()` 入口，模型/工具快照按新 run 正常解析），其余排队项顺延；未开启则留待用户显式"发送队列项"。（✅ 已实施：run() 循环化 + runSingle 抽取，同一 WS 连接内续跑整条队列；晋升 claim-once（条件更新）与新 run 创建、用户消息落库同一事务，实现 promoteSessionInput 原子性）
- 失败语义对齐 ZCode：run 非 cancel 失败（error 态）时暂停自动续跑（queue paused），cancel 时剩余 guide 降级 queue——留给用户决定是否继续，避免错误循环烧钱。（✅ 已实施：自动续跑仅在 run 成功收敛后触发，cancel/error/timeout 天然暂停；queue 开启时 run 终态的未消费 guide 一律降级排队而非作废，广播 steer_delivery_changed）

**S3（P2）：队列管理 API**——删除/编辑/重排/预留（多端抢占防护）、auto-drain 开关、队列表查询接口。重排必须同步更新账本序号（对齐 ZCode"冷热事实分叉"教训）。

---

## 3. Runtime Command Queue：Agent Loop 的"邮箱"

### 3.1 ZCode 机制精读

**两个队列不可混淆**：

| | Steering pendingInputs | Runtime command queue |
|---|---|---|
| 承载 | 用户意图 | 机器事件：prompt 命令、后台任务通知、子代理回复、控制轮、goal 续跑 |
| 持久化 | 账本 admitted（唯一持久痕迹） | 纯内存，入队时镜像账本行（`background-notifications.ts:58-71`："runtime 命令队列是纯内存的，账本是唯一 durable 痕迹"） |
| 消费点 | 工具批后/文本步结束的 guide drain；queue 项由外层 bootstrap 晋升 | 外层 drain（整命令=整 turn）+ **轮内吸收器** |

**轮内吸收器**是关键设计（`turn-loop.ts:55-65` → `runtime-command-active-loop.ts:22-90`）：从第二次模型步开始，每次迭代顶部取优先级 ≤"next" 的 task-notification/subagent-message 命令，逐条落库为**model-only 的 user 角色 entry**，拼进当前轮请求——不新开 turn、不发 TurnStarted。两条硬规则：控制轮命令让吸收器停止（通知描述的是设置轮刚建的 run，模型必须先读设置轮）；吸收即重置重复调用检测器。

**批合并**：`dequeueNextBatch`（`runtime-command-queue.ts:184-203`）把队首同优先级的全部 task-notification 一次取出，外层消费时合成**一条** user 消息（文本 `"\n\n"` 连接，`background-notifications.ts:166-218`）——避免每条通知单独发起一次模型请求。

**并发保证清单**（全部有对应失效模式注释）：admission+预留+入队一体化（防双 turn）；lease 出队同同步步（防通知插队）；`branchGeneration` 陈旧围栏——入队和持久化/注入前**两次**校验（`runtime-command-generation.ts:6-30`："rewind 与后台 completion 存在竞态"）；claim-once 通知（executor 先 `claimRuntimeBackgroundTaskNotification` 再入队，`background-tasks.ts:534-546`，防止同一终态既出现在工具结果又出现在通知）；cancel-pending 窗口（命令已出队未执行时被 abort → 标记后在执行起点拒绝）。

### 3.2 本项目现状

- 无后台任务通知源：`async_task.go:46-50` 明确"不会因为外部任务完成而自动唤醒原 Run…不承担后台长期执行编排"；delegate 全同步。
- `clientMessageHub` 是工具应答邮箱，不承载机器事件。
- 但本项目有一个天然优势：`state.messages` 只被 run 的循环 goroutine 触碰——Go 单 goroutine 消费 + 边界注入即可获得 ZCode 用命令队列实现的全部串行化保证，无需复杂锁。

### 3.3 落地方案（Q1，P1）

1. 每 run 一个 `runtimeCommandBox`：mutex + 有序切片 + notify channel；命令类型先只做一种 `notification`（后台委派完成），结构预留 kind 字段。
2. 消费点：引擎循环顶部、`step > 0` 时（对齐 ZCode 吸收器位置），全部取出、合并为**一条** user 消息（内容以 `<task-notification>` 风格 XML 信封拼接：task-id/status/summary/result/usage），落库为新 message_type（如 `notice`，前端渲染为系统卡片、可选不进历史回放——即 model-only 语义）。
3. 落库镜像与恢复：入队时写 `tblLlmReactPendingInput`（kind=notification，status=admitted）；消费即置 guided；run 已结束/取消时消费前校验 run 状态，陈旧即丢弃并结算 `discarded(run_finished)`；run 重启不复活（对齐"后台子进程随重启已死"）。
4. claim-once：通知入队用条件更新（`UPDATE ... SET status='notified' WHERE id=? AND status='pending'`）原子抢占，防重复回灌。

---

## 4. Subagent：从"同步阻塞函数调用"到"可后台的 Agent→Agent"

### 4.1 ZCode 体系精读（`core/src/subagent/runner.ts` + `runtime/methods/subagent.ts`）

- **链路**：Agent 工具（`tool/handlers/agent.ts:224-280`）→ `SubagentPort`（由**父 runtime 自建**，`createDefaultSubagentPort`，`subagent.ts:59-422`；宿主不提供）→ 子 `AgentRuntime` 全量构建（`subagent.ts:239-368`），子会话先持久化再发布（"把持久化提升为发布前闸门"，`subagent.ts:378-381`）。
- **隔离清单**：独立 sessionId（`subagent_<agentId>`）与 session 行（`taskType:"subagent_child"`，`parentID` 指向父）；**不继承父历史**（子上下文 = prompt + profile systemPrompt）；工具白名单 = 父可见工具 − MCP 独立派生 − plan 工具 − **Agent/Task（递归禁用，双重保险：子配置硬编码 `subagents.enabled=false`，`subagent.ts:284-287`）** + `RespondToCoordinator` 恒追加（"子控制通道不受 profile 工具列表约束"）；模型 = profile 指定 > 调用覆盖 > **继承父当前模型**；abort controller 挂父信号链（`runner.ts:626-633`），后台化时 `detachParent()`（`:635-637`）；HITL 交互经 `deriveChildClientPorts` 路由回**父会话**（`subagent.ts:206-226`）。
- **同步/后台双模式**：同步路径有逃生口（`Promise.race` + autoBackground 计时器，`runner.ts:302-316`）；后台返回 `{status:"async_launched", agentId, backgroundTaskId}`，给模型的文案包括"You will be notified automatically"与"Briefly tell the user what you launched and end your response"（`agent.ts:153-171`）。完成时 `finalizeBackgroundCompletion` → 格式化 `<task-notification>`（结果截断 120k）→ **claim-once 后**入父 runtime 命令队列 + 账本镜像。
- **SendMessage 双向通道**：父→子 `port.sendMessage`：运行中走 `messageSink`（内部即 `childRuntime.steerTurn({delivery:"guide"})`，重试 20×10ms，`message-steering.ts:29-48`——**子代理引导复用主代理的 steering 机制**）；未运行则排队、终态则 `resumeTerminalAgentInBackground` 冷恢复续聊。子→父 `RespondToCoordinator` 同步入父命令队列（XML 信封 model-only user entry）。
- **父取消语义**：前台子随父 abort 级联（Agent 工具 promise 以 ToolCancelled 拒绝）；**后台子 detach 后存活**，跑完经通知回灌；父 runtime 关闭时通知丢弃 + 陈旧分支围栏。
- **看门狗**：子不活跃超时 `ToolTimeout` "Subagent was inactive for Nms"（`runner.ts:1191-1267`）。

### 4.2 本项目 delegate 现状：隔离已齐，缺后台与通道

**已对齐**（无需重做）：独立 run 行 + `agentPath`（`delegate.go:272-294`）；历史/附件/记忆/todo 全清空（`delegate.go:224-246`）；工具/技能按 agent policy 过滤；模型继承含互备后实际模型（`delegate.go:189-200`）；深度限制 `MaxDepth=2` 默认（`delegate.go:117-121`，`conf/config.go:58`）；取消级联（子 `subCtx` 派生父 `runCtx` + 全局 cancel 注册表，`delegate.go:135-136`）；token 递归归集（`delegate.go:358-374`）；并行 HITL 经共享 clientHub 按 toolUseId 路由（e2e 已覆盖）。

**缺口**：① 全同步阻塞（`delegate.go:64`"阻塞等待其结论后返回"），无后台模式；② 完成即 DB 行，无通知回灌；③ 无 SendMessage；④ 子 run 无墙钟看门狗（只有 token 预算与 HITL 交互超时）。

### 4.3 落地方案

**A1（P1）：后台委派 + 完成通知（依赖 Q1）。**

1. `delegate_agent` 入参加 `background`（工具描述同步更新）；background=true 时：注册子 run（复用现有 `createSubAgentRunRecord` + 全局 cancel 注册）后**立即返回**工具结果："已后台启动委派 agent X（runId=…），完成后自动通知；请简要告知用户已启动并结束本轮回复"（对齐 ZCode async_launched 文案纪律）。
2. 完成监视 goroutine：子 run 终态（finished/error/cancelled/timeout）→ 取最后非空 assistant 消息（复用 `subAgentFinalResponse`）+ usage → 条件更新 claim → 写账本行 + 入父 run 命令箱。
3. 父 run 已结束时：通知结算 `discarded(run_finished)`（不复活 run，结果可在前端子 run 卡片查看）。与 ZCode 的一个**有意分歧**：父取消时级联取消后台子 run（ZCode detach 存活）——成本可控优先，避免用户取消后仍在烧 token；如需 detach 语义后续加开关。

**A2（P2）：SendMessage + 看门狗。** 父→子自由文本 = 向子 run 的 pendingInputs 注入 guide（**完全复用 S1 机制**，这正是 ZCode messageSink 的做法）；子已终态则报错或（远期）以该 agentPath 历史重建续聊 run。看门狗：`SubAgent.MaxRunSeconds`（默认 0=不限）超时置 timeout 终态，通知按失败回灌。

---

## 5. 依赖关系与落地顺序

```
S1 guide 注入 ──┬──> S2 queue+自动续跑 ──> S3 队列管理 API
                │
                └──> Q1 通知邮箱（共享账本表 tblLlmReactPendingInput + 注入/结算机制）
                          │
                          └──> A1 后台委派（依赖 Q1 消费端）──> A2 SendMessage（依赖 S1+子 run 可寻址）
```

| 阶段 | 优先级 | 触碰文件 | 前置 |
|---|---|---|---|
| S1 guide 注入 | P0 | `runtime.go`、`engine.go`、新增 `steering.go`、`models/llm`（新表）、`controllers/http/react/ws.go` | ToolRuntime 方案 Phase 1（闭合修复）先行，保证注入点前后历史配对 |
| S2 queue+续跑 | P0 | `runtime.go`、`runtime_state.go`（finish 收口）、新表 | S1 |
| Q1 通知邮箱 | P1 | 新增 `command_box.go`、`engine.go` | S1 的账本表与结算机制 |
| A1 后台委派 | P1 | `delegate.go`、`tool_dispatch.go`（工具描述）、`meta_tools.go` | Q1 |
| S3 队列管理 | P2 | `steering.go`、`ws.go`/HTTP API | S2 |
| A2 SendMessage | P2 | `delegate.go`、新增 handler | S1 + A1 |

S1 建议先补失败测试：run 运行中发送用户消息 → 断言返回 guided 回执且下一个模型请求尾部包含该 user 消息；工具批中途注入 → 断言消息落在整批 tool_result 之后。

---

## 6. 不照搬清单

| 项 | 理由 |
|---|---|
| 持久事件日志 / EventReducer 全套投影 | 两边范式一致：DB 行即事实源；本项目 WS 发射器已承担活投影，引入双写只增复杂度 |
| 驻留池（resident pool） | 本项目"每 run goroutine + DB 重建上下文"已是等价物 |
| `foregroundPromotionLease` 完整租约机制 | 该 lease 解决的是 JS 单线程异步窗口下的出队竞争；Go 侧 S2 的"run 结束后取队首开新 run"用一条条件更新即可原子化，无需租约抽象 |
| Rewind / Fork / Retry-Edit（分支剪 + fork 事务包） | 独立大特性（依赖消息分支游标与 fork 事务），先落地 Steering/通知；compact 边界对齐已为将来分叉打好基础，列为远期（P3）单独立项 |
| 子代理 session 共享父 eventStore 等宿主级细节 | 本项目子 run 天然走同一 MySQL、事件经 agentPath 分 lane 上行，已等价 |
