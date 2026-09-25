---
title: ReAct Runtime 模块功能文档
date: 2026-09-25
version: v1.5
type: system
module: react_runtime
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/react/ws.go
  - service/react/doc.go
  - service/react/runtime.go
  - service/react/engine.go
  - service/react/steering.go
  - models/llm/react_run.go
summary: ReAct Agent Run 的入口、执行循环、交互等待、Steering 引导/排队/自动续跑、能力装配和持久化边界
---

# ReAct Runtime 模块功能文档

## 1. 模块概述

ReAct Runtime 负责执行一次服务端 Agent Run。入口层完成会话与运行参数装配，运行期通过模型轮次、工具调用和工具结果回填形成循环，直到最终回答、取消或失败。

Runtime 通过独立 service 复用 Tool、Memory、Agent、Workspace 等能力；当前 `create_plan` 仍是 ReAct 内的计划确认能力，不是独立 Plan Runtime。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/react/ws` | GET | WebSocket 单入口，处理 run/cancel 与 Client Tool/HITL 回包 | `react.WS` |
| `/react-base-service/react/models` | GET | 查询可选模型 | `react.GetModels` |
| `/react-base-service/react/session/list` | POST | 查询会话列表 | `react.ListSessions` |
| `/react-base-service/react/session/events` | POST | 重建持久化会话事件流 | `react.GetSessionEvents` |
| `/react-base-service/react/async_task/list` | POST | 查询会话异步任务 | `react.ListAsyncTasks` |
| `/react-base-service/react/session/feedback` | POST | 查询 Run 反馈 | `react.ListSessionFeedback` |
| `/react-base-service/react/run/feedback` | POST | 更新 Run 反馈 | `react.UpdateRunFeedback` |
| `/react-base-service/react/artifact/:artifactId` | GET | 鉴权读取运行产物 | `react.GetArtifact` |

## 3. 核心逻辑

### 3.1 Run 生命周期与执行循环

`RunWithClientReaderContext` 建立运行环境后进入 `executeReactLoop()`。`runtimeRequest` 保存进入循环前解析好的身份、模型、Tool/Skill/Memory/Agent 能力和会话输入；`reactEngineState` 保存循环中变化的消息、激活工具、Todo、异步状态和 token 使用量。

### 3.2 Tool 两阶段加载

业务 Tool 先以轻量索引进入上下文。模型调用 `get_tool` 获取完整 Schema 后再调用 `execute_tool`。Client Tool 由前端/宿主执行，Server Tool 进入 `service/tool` 后分发到 HTTP 或 MCP。

### 3.3 WebSocket 与等待

同一连接只允许一个外层活跃 Run。Client Tool、`ask_question`、危险操作确认等上行消息由统一 Hub 读取，再按 `toolUseId` 分发给等待者。连接使用协议层 ping 做断连检测，并保留应用层 heartbeat。

### 3.4 子 Agent、Memory 与 Workspace

子 Agent 创建独立 `ReactRun` 并复用同一个 `executeReactLoop()`；父子 Run 的模型历史与激活工具隔离。Memory 是 Runtime 内置能力，不走 Business Tool 两阶段协议。Workspace 只暴露运行时入口，Git/worktree 与代码检索由 `service/workspace` 实现。

### 3.5 上下文与长结果

运行时支持历史上下文压缩；大 Tool Result 可落库并通过 `resultRef` 按需读取，避免全量结果持续占用模型上下文。

### 3.6 Tool 结果闭合不变量与调用去重

「每个 assistant tool_use 必须有配对 tool_result」是结构保证（闭合治理 Phase 1，见 `service/react/tool_closure.go`），由三道防线构成：

1. **中断登记制**：取消/断线/确认与问答等待被打断时，中断路径只把合成结果登记到 `reactEngineState.roundInterruptedResults`（同时保留 resultRef 落库与卡片收敛事件），不单独写 tool_result 消息；
2. **引擎单点落库**：`executeToolCalls` 返回的 results 永远与 calls 等长且槽位配对（中断登记优先、未开始调用合成"未执行"、异常缺位合成"状态未知"），模型轮循环无论执行是否出错，先落库本轮 tool_result 再返回；预算超限终止前同样回填；
3. **重建孤儿回填**：`reactMessagesToChatMessagesWithRefs` 构建上下文时对存量数据或极端故障（结果落库失败）留下的孤儿 tool_use 插入合成结果，保证恢复后的 provider 请求永远合法。

tool_call id 双层去重（Phase 2）：流收集阶段按非空 id 去重（防 adapter/协议重复投递），执行入口对同轮重复 id 直接回灌拒绝结果不执行。中断结果文案区分 not_executed（未执行、可按失败处理）与 unknown_execution_state（副作用状态未知、先核实再重试）两种语义。

### 3.7 Steering 引导注入与排队（S1/S2）

运行中会话收到新用户消息不再硬拒绝，走准入账本机制（`service/react/steering.go`，对齐 ZCode prompt-admission / steering）：

1. **准入决策**：run 处于 `running`、非软着陆收尾、无附件且 `llm.react.steering.enabled` 开启 → guide（回执 `steer_guided`，账本落 admitted 行）；`waiting_client_message`/`waiting_plan`/`cancelling`、软着陆窗口或带附件 → `steering.queue` 开启时落 queued（回执 `steer_queued`，附 queueLength），否则明确拒绝（`steer_rejected`，reason 为 run_not_steerable/soft_landing/attachments_unsupported）。准入与活跃 run 检查在同一持有 session 行锁的事务内完成；HITL 等待与取消收尾绝不被引导打断。准入同时保存原始 run 请求快照（payload_json，模型选择缺省时回填被引导 run 的实际模型）。
2. **账本**：`tblLlmReactPendingInput`（session_id/run_id/kind/delivery/status/content/seq/payload_json/settle_reason）。准入即落账本（崩溃不丢"输入存在过"这一事实）；guide 消费 = 同一事务内"账本置 guided + 用户消息落库"；重启不复活队列——新建 run 前，残留 admitted 一律作废，上一进程遗留的 queued 行（created_at 早于进程启动）也一并作废（session_resumed）。
3. **注入点唯一**：整批 tool_result 落库之后、下一次模型请求之前，或纯文本 finish 判定之前（有 guide 则不结束 run、注入后继续）。注入的 user 消息成为请求尾部，绝不插入 assistant tool_use 与 tool_result 之间；输出截断续写优先于 guide；软着陆收尾窗口内不再接收/消费 guide。
4. **排队与自动续跑（S2）**：run 正常结束（含 finishExhausted）后，`steering.queue_auto_drain`（默认 true）开启时取队首 queued 输入在同一连接内自动开启新 run（模型/工具快照按新 run 重新解析），循环直至队列排空；晋升 claim-once（条件更新）与新 run 创建、用户消息落库在同一事务，杜绝双 run 消费同一条。run 出错/取消/超时收敛不自动续跑（失败后暂停，留给用户决定）。
5. **终态结算**：queue 开启时，未消费的 admitted guide 在 run 终态降级排队（delivery→queued，广播 `steer_delivery_changed`）；queue 关闭时结算 discarded——取消/断连 → `turn_cancelled`、出错/超时/过期 → `turn_failed`、正常结束仍未消费 → `run_finished`。结算/降级事件先于终态事件（cancelled/error/timeout/done）发送。
6. **WS 事件词汇**：`steer_guided`/`steer_queued`/`steer_rejected`（准入回执）、`steer_drained`（guide 边界消费 / 自动续跑队首晋升）、`steer_delivery_changed`（guide 降级排队）、`steer_discarded`（结算作废）。

### 3.8 运行时通知邮箱与后台委派（Q1/A1）

机器事件与用户意图分流：Steering 账本承载用户输入（§3.7），运行时命令箱（`service/react/command_box.go`，对齐 ZCode runtime command queue / 轮内吸收器）承载后台任务完成通知：

1. **命令箱**：每个 run 一个 `runtimeCommandBox`（mutex + FIFO 切片），完成监视 goroutine 投递、引擎循环唯一消费；通知投递时镜像账本行（`tblLlmReactPendingInput`，kind=notification、status=admitted），账本行是唯一持久痕迹，命令箱随进程死亡（重启不复活，残留 admitted 由 session_resumed 作废）。
2. **投递围栏**：后台子 run 终态后，完成监视 goroutine 在 run 行锁事务内校验父 run 活跃并落账本行——父 run 已终态则不落账不入箱（结果留在子 run 记录可查，绝不为通知复活 run）；行锁把投递与父 run 的终态收敛串行化，不会产生无人消费的残留行。
3. **吸收点**：引擎循环顶部（step>0 起）与纯文本 finish 判定前；全部取出、条件更新 claim-once（admitted→guided）、合并为**一条** model-only 的 user 消息（`react_notice` 类型，`<task-notification>` XML 信封：task-id/agent/status/summary(超长截断)/usage）落库并拼进当前轮请求——不新开 turn；吸收即重置重复调用检测器；通知不受软着陆约束。run 收敛时未消费通知一律结算 discarded（不降级排队）。
4. **后台委派（A1）**：`delegate_agent` 新增 `background` 参数——true 时组装与同步路径一致的隔离子 run 后立即返回启动回执（async_launched 文案纪律：简要转达、结束本轮回复、不等待不编造结论），子 run 在监视 goroutine 中执行到终态并经命令箱回灌。生命周期锚在父 runCtx（父取消/断连级联取消后台子 run——与 ZCode detach 存活的有意分歧，成本可控优先）；gin ctx 使用 headless 快照（后台子 run 可能比 WS 连接活得久）并保留父请求 Cookie。
5. **WS 事件**：`notice_drained`（通知被吸收，payload 含 messageId/count）；前端按 `react_notice` 消息类型渲染系统卡片。

### 3.9 队列管理与 SendMessage/看门狗（S3/A2）

1. **队列管理 API（S3）**：`/react/queue/{list,update,reorder,delete}` 四个端点（会话五元组归属校验）。编辑/重排/删除均为条件写或事务内行锁读+无条件写，与自动续跑晋升 claim-once 互斥；**重排按请求顺序同步改写账本 seq**（从当前最小 seq 起连续分配），防冷热事实分叉，实测确认自动续跑按重排后顺序消费；删除置 cancelled。显式发送走 WS `queue_send` 消息（同一连接 claim-once 晋升开新 run，先回 steer_drained），不提供 HTTP 入口（HTTP 无法回传 run 事件流）。
2. **SendMessage（A2）**：新 Meta Tool `send_message`（与 delegate_agent 同 AllowSubagent 门控）——父模型按 agent_path/run_id 向**本 run 直接委派**的子 run 发送补充消息，作为 guide 落账本并在子引擎模型步边界消费（完全复用 S1 机制，对齐 ZCode messageSink）。目标选择活跃优先；子 run 已结束/软着陆收尾/消息落账后被终态结算时，均得到明确错误结果——子 run 的定向消息**绝不降级会话队列**（settleRunPendingInputs 按 isOuterRun 区分）。
3. **子代理看门狗（A2）**：`subagent.max_run_seconds`（默认 0=不限）到点以 ErrReactRunTimeout 取消子 run，终态置 timeout（对齐外层 run 超时语义），后台完成通知按 timeout（失败）回灌。
4. **前端事件投影**：SDK（web/sdk）接入全部 steer_* 与 notice_drained 事件——steerState 投影 + main lane 系统标记条（NoticeChip，引导/排队/作废/通知文案），notice_drained 历史回放携带信封正文可展开；react_notice 消息经 session/events 映射为同词汇历史事件。

## 4. 数据模型

| 表/模型 | 作用 |
|---|---|
| `tblLlmReactSession` / `ReactSession` | 会话归属、标题和状态 |
| `tblLlmReactRun` / `ReactRun` | 单次 Run 状态、模型、步骤、能力快照和 token 使用量 |
| `tblLlmReactMessage` / `ReactMessage` | 用户、助手、工具等持久化消息 |
| `tblLlmReactToolResult` / `ReactToolResult` | 大工具结果和 `resultRef` |
| `tblLlmReactPendingInput` / `ReactPendingInput` | Steering 排队输入账本 + 后台通知镜像（kind=user_input/notification） |
| `tblLlmReactAsyncTask` / `ReactAsyncTask` | 异步提交型 Tool 的跨 Run 状态 |
| `tblLlmReactArtifact` / `ReactArtifact` | Python 等运行产物元信息 |

`ReactRun.state` 包含 `running`、`waiting_client_message`、`waiting_plan`、`cancelling`、`finished`、`error`、`cancelled`、`expired`、`timeout`。

`ReactPendingInput.status` 流转：`admitted` →（guide 被消费 / 通知被吸收）`guided` / （不可引导降级）`queued` / （结算）`discarded`；`queued` →（晋升为某 run 的输入，自动续跑或 S3 显式发送）`guided` 或（用户取消，S3）`cancelled`；`guided`/`cancelled`/`discarded` 为终态。kind=notification 的行绝不进入 queue 降级与自动续跑。

## 5. 配置项与限制

`conf/mount/custom.yaml` 的 `llm.react` 提供 `max_steps`、`stream_idle_timeout_sec`、`models.*`、`context_compact.*`、`tool_result.*`、`allow_plan`、`steering.enabled`（guide 引导注入总开关，默认 false）、`steering.queue`（排队开关，默认 false）、`steering.queue_auto_drain`（run 正常结束后自动续跑队首，默认 true，仅 queue 开启时生效）和 `subagent.max_run_seconds`（子代理墙钟看门狗，默认 0=不限）等配置。

当前异步任务机制用于跨 Run 状态提醒，不会在外部任务完成后自动唤醒已结束的 Run；后台委派（§3.8）的完成通知只回灌仍处于运行中的父 run。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 ReAct Runtime 文档基线 |
| v1.1 | 2026-09-25 | react-base-service 项目组 | 新增 3.6 节：Tool 结果闭合不变量（中断登记制/引擎单点落库/重建孤儿回填）与 tool_call id 双层去重 |
| v1.2 | 2026-09-25 | react-base-service 项目组 | 新增 3.7 节：Steering 引导注入（准入决策、tblLlmReactPendingInput 账本、唯一注入点、三路结算与 WS steer_* 事件）；补全 run 状态枚举与数据模型 |
| v1.3 | 2026-09-25 | react-base-service 项目组 | 3.7 节扩展 S2：排队（payload 快照 + queueLength 回执）、run 结束自动续跑（claim-once 晋升原子化）、guide 降级排队与失败暂停语义；新增 queue_auto_drain 配置 |
| v1.4 | 2026-09-25 | react-base-service 项目组 | 新增 3.8 节：运行时通知邮箱（runtimeCommandBox、模型步边界吸收、react_notice 消息、通知结算与 queue 降级隔离）与后台委派（delegate_agent background 参数、async_launched 回执、完成通知经命令箱回灌、父取消级联语义） |
| v1.5 | 2026-09-25 | react-base-service 项目组 | 新增 3.9 节：队列管理 API（list/update/reorder/delete + WS queue_send 显式发送，重排同步改写账本 seq）、SendMessage 父→子消息通道（软着陆拒收、定向消息不降级排队）与子代理看门狗（max_run_seconds）；SDK 接入 Steering/通知事件与 NoticeChip 系统标记条 |