---
title: ReAct Runtime 模块功能文档
date: 2026-09-25
version: v1.3
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

## 4. 数据模型

| 表/模型 | 作用 |
|---|---|
| `tblLlmReactSession` / `ReactSession` | 会话归属、标题和状态 |
| `tblLlmReactRun` / `ReactRun` | 单次 Run 状态、模型、步骤、能力快照和 token 使用量 |
| `tblLlmReactMessage` / `ReactMessage` | 用户、助手、工具等持久化消息 |
| `tblLlmReactToolResult` / `ReactToolResult` | 大工具结果和 `resultRef` |
| `tblLlmReactPendingInput` / `ReactPendingInput` | Steering 排队输入账本（guide/queue 准入与结算） |
| `tblLlmReactAsyncTask` / `ReactAsyncTask` | 异步提交型 Tool 的跨 Run 状态 |
| `tblLlmReactArtifact` / `ReactArtifact` | Python 等运行产物元信息 |

`ReactRun.state` 包含 `running`、`waiting_client_message`、`waiting_plan`、`cancelling`、`finished`、`error`、`cancelled`、`expired`、`timeout`。

`ReactPendingInput.status` 流转：`admitted` →（guide 被消费）`guided` / （不可引导降级）`queued` / （结算）`discarded`；`queued` →（晋升为某 run 的输入，自动续跑或 S3 显式发送）`guided` 或（用户取消，S3）`cancelled`；`guided`/`cancelled`/`discarded` 为终态。

## 5. 配置项与限制

`conf/mount/custom.yaml` 的 `llm.react` 提供 `max_steps`、`stream_idle_timeout_sec`、`models.*`、`context_compact.*`、`tool_result.*`、`allow_plan`、`steering.enabled`（guide 引导注入总开关，默认 false）、`steering.queue`（排队开关，默认 false）和 `steering.queue_auto_drain`（run 正常结束后自动续跑队首，默认 true，仅 queue 开启时生效）等配置。

当前异步任务机制用于跨 Run 状态提醒，不会在外部任务完成后自动唤醒已结束的 Run。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 ReAct Runtime 文档基线 |
| v1.1 | 2026-09-25 | react-base-service 项目组 | 新增 3.6 节：Tool 结果闭合不变量（中断登记制/引擎单点落库/重建孤儿回填）与 tool_call id 双层去重 |
| v1.2 | 2026-09-25 | react-base-service 项目组 | 新增 3.7 节：Steering 引导注入（准入决策、tblLlmReactPendingInput 账本、唯一注入点、三路结算与 WS steer_* 事件）；补全 run 状态枚举与数据模型 |
| v1.3 | 2026-09-25 | react-base-service 项目组 | 3.7 节扩展 S2：排队（payload 快照 + queueLength 回执）、run 结束自动续跑（claim-once 晋升原子化）、guide 降级排队与失败暂停语义；新增 queue_auto_drain 配置 |