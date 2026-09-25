---
title: ReAct Runtime 模块功能文档
date: 2026-09-25
version: v1.1
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
  - models/llm/react_run.go
summary: ReAct Agent Run 的入口、执行循环、交互等待、能力装配和持久化边界
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

## 4. 数据模型

| 表/模型 | 作用 |
|---|---|
| `tblLlmReactSession` / `ReactSession` | 会话归属、标题和状态 |
| `tblLlmReactRun` / `ReactRun` | 单次 Run 状态、模型、步骤、能力快照和 token 使用量 |
| `tblLlmReactMessage` / `ReactMessage` | 用户、助手、工具等持久化消息 |
| `tblLlmReactToolResult` / `ReactToolResult` | 大工具结果和 `resultRef` |
| `tblLlmReactAsyncTask` / `ReactAsyncTask` | 异步提交型 Tool 的跨 Run 状态 |
| `tblLlmReactArtifact` / `ReactArtifact` | Python 等运行产物元信息 |

`ReactRun.state` 包含 `running`、`waiting_client_message`、`cancelling`、`finished`、`error`、`cancelled`、`expired`。

## 5. 配置项与限制

`conf/mount/custom.yaml` 的 `llm.react` 提供 `max_steps`、`stream_idle_timeout_sec`、`models.*`、`context_compact.*`、`tool_result.*` 和 `allow_plan` 等配置。

当前异步任务机制用于跨 Run 状态提醒，不会在外部任务完成后自动唤醒已结束的 Run。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 ReAct Runtime 文档基线 |
| v1.1 | 2026-09-25 | react-base-service 项目组 | 新增 3.6 节：Tool 结果闭合不变量（中断登记制/引擎单点落库/重建孤儿回填）与 tool_call id 双层去重 |