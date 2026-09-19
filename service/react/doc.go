// Package react 实现服务端 Agent 的 ReAct Runtime 与其运行期编排能力。
//
// # 模块定位
//
// 本包负责“如何执行一次 Agent Run”，而不是把所有领域能力都实现一遍。
// 经过模块化拆分后，核心依赖方向是：
//
//   service/react
//       |
//       +-- service/tool    ：HTTP / MCP 等服务端 Business Tool 实际执行
//       +-- service/agent   ：子 Agent 可见性、解析与运行策略
//       +-- service/memory  ：长期记忆作用域、查询、写入与审计
//       +-- service/workspace：代码工作区生命周期与代码检索能力
//
// react 负责决定“何时调用这些能力、如何把结果放回模型上下文、如何持久化 Run 状态”；
// 领域服务负责“具体能力怎么实现”。不要把 HTTP/MCP、Memory DB、Agent 查询逻辑重新塞回核心循环。
//
// # 一次外层 Run 的主链路
//
//   Run / RunWithClientReader
//            |
//            v
//   prepareRuntimeRequest
//     - Caller / User
//     - Model / API Key
//     - Tool / Skill / Agent snapshot
//     - Memory / GraphMemory
//            |
//            v
//   createReactRunContext
//     - Session 行锁
//     - 同会话并发 Run 检查
//     - 创建 ReactRun
//     - 持久化 user input
//            |
//            v
//   executeReactLoop
//            |
//      +-----+-------------------------------+
//      |                                     |
//      v                                     |
//   model round                               |
//      |                                     |
//      +-- final answer -> finish             |
//      |                                     |
//      +-- tool_use                           |
//            |                                |
//            v                                |
//      executeToolCalls                       |
//            |                                |
//      persist tool_result                    |
//            |                                |
//            +--------------------------------+
//
// run() 负责 Run 生命周期；executeReactLoop() 只负责 ReAct 状态机。
// 两者不要重新合并成一个超大函数。
//
// # runtimeRequest 与 reactEngineState
//
// runtimeRequest 是“进入执行循环前已经解析好的运行快照”，包括：
//
//   identity      - user/session/caller runtime context
//   model         - apiKey/modelKey/modelVersion
//   capabilities  - prompt/tool/skill/memory/agent snapshot
//   execution     - agentPath/depth/HITL/token budget
//   conversation  - history/current input/attachments/recoverable state
//
// reactEngineState 是 executeReactLoop 生命周期内持续变化的状态，包括：
//
//   model state        - 当前模型与 failover 候选
//   conversation state - messages、activeTools、todo 等
//   async state        - Memory 写次数、未完结异步任务
//   usage state        - token/cache/delegated token 统计
//
// 简单理解：runtimeRequest 描述“以什么条件开始跑”，reactEngineState 描述“现在跑到哪里了”。
//
// # Business Tool 两阶段协议
//
// 为避免大量 Tool Schema 长期占据上下文，业务 Tool 不直接全部注册给模型，而采用：
//
//   Tool 轻量索引
//        |
//        v
//   get_tool
//     返回 parameters / outputSchema
//        |
//        v
//   activeTools
//        |
//        v
//   execute_tool
//        |
//        +-- Client Tool -> 浏览器/宿主执行
//        |
//        +-- Server Tool -> service/tool -> HTTP / MCP
//
// execute_tool 执行前会再次校验 Tool 可见性与 definition fingerprint。
// Tool 定义在 Run 中途发生变化时，模型必须重新 get_tool，不能继续按旧 Schema 调用。
//
// # Client Tool 与 HITL
//
// Client Tool、ask_question、tool_confirm 等交互都需要等待前端回包。
// 父 Run 与多个并行子 Agent 可能同时等待，因此所有上行消息统一经过 clientMessageHub：
//
//   WebSocket readClient
//          |
//          v
//   clientMessageHub  （唯一读者）
//      |      |      |
//      v      v      v
//   waiter  waiter  waiter
//      |
//   按 toolUseId 精确认领
//
// 任何新增长等待型交互都应复用 Hub，不要创建第二个直接读取 WebSocket 的 goroutine。
//
// # 多 Agent
//
// 子 Agent 的执行模型是：
//
//   Agent 配置
//      |
//      v
//   Child ReactRun
//      |
//      v
//   executeReactLoop
//
// 即“共享同一个 ReAct Engine，隔离不同 Run 状态”，而不是再实现一套 Agent 引擎。
// 子 Run 通过 parent_run_id / agent_path 建立父子关系，并继承 runtimeServices 与 clientHub。
// 父子模型历史、activeTools、token budget 等保持隔离。
//
// # Memory
//
// ReAct 只保留 Memory Tool adapter；owner scope、caller/caller_user 合并、敏感信息拦截、
// revision 审计、乐观锁等逻辑统一由 service/memory 负责。
// Memory 是 Runtime 内置能力，不走 Business Tool 的 get_tool/execute_tool 两阶段协议。
//
// # Async Task
//
// 当前 Async Task 解决“业务 Tool 提交后结果晚到”的跨 Run 提醒问题：
// 提交快照落库，后续用户再次发消息时模型可查询状态并 resolve。
// 它不是完整 Durable Execution：外部任务完成后不会自动唤醒已经结束的 Run。
// 自动暂停/恢复、后台 Worker、Lease/Scheduler 等属于后续 Durable Runtime 范畴。
//
// # Workspace
//
// load_runtime_code 只是 Workspace 的 Runtime 入口。
// Git/worktree/repo MCP 等实现位于 service/workspace，挂载完成后代码检索能力仍作为普通
// Business Tool 走 get_tool -> execute_tool，因此 ReAct Engine 不需要理解 Git 和文件系统协议。
//
// # Plan
//
// 当前 create_plan 仍是“普通 ReAct 中的计划确认卡片”，仅负责生成计划草稿并等待前端确认。
// 它不是正式 Plan Runtime。真正的 Plan Runtime 应位于 ReAct Runtime 上层：
//
//   PlanExecution
//        |
//       Step
//        |
//     Attempt
//        |
//   Scoped ReactRun
//        |
//   executeReactLoop
//
// 后续实现 Plan 时应复用本包的 ReAct Engine，而不是把 Plan Step 状态机继续堆进 create_plan。
//
// # 修改本包时的基本约束
//
//   1. 不要让消息落库顺序与下一轮模型上下文顺序不一致。
//   2. Tool/HITL 的取消和断连路径要补齐可回放终态。
//   3. 新领域能力优先通过 runtimeServices 或独立 service 注入，不要直接耦合进 engine.go。
//   4. 子 Agent / 未来 Plan Step 优先复用 executeReactLoop，不复制 ReAct Engine。
//   5. 新增受限执行域时优先通过 ExecutionProfile 控制能力暴露。
//   6. 长结果优先使用 resultRef，不要无限扩大模型上下文。
//   7. 日志示例使用格式化占位符，不使用字符串拼接。
package react
