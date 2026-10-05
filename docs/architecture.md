# 系统架构

> 本文描述 react-base-service 的整体架构与关键设计。ReAct 运行时细节另见 `service/react/doc.go` 包注释；MCP 能力边界见 `docs/mcp.md`、网关设计见 `docs/system/mcp-gateway.md`、记忆体系蓝本见 `docs/memory.md`、定时工作流设计蓝本见 `docs/定时触发工作流实现方案.md`。

## 1. 总体分层

```
                ┌──────────────────────────────────────────────────────────────────┐
                │                            接入层                                 │
                │  Web 前端(SDK) · playground/回放页 · 服务间调用方 Caller            │
                │  外部 MCP 客户端（Claude/Cursor 等，经 Bearer app_key:secret）      │
                └───────────────┬──────────────────────────────┬───────────────────┘
                                │ WebSocket / HTTP              │ MCP Streamable HTTP
                ┌───────────────▼──────────────────────────────▼───────────────────┐
                │        router + middleware（AnonymousAuth 免鉴权，X-User-Name；   │
                │        MCP 网关 Bearer 鉴权；管理台静态令牌 X-Admin-Token）        │
                └───────────────┬──────────────────────────────────────────────────┘
        ┌───────────────────────┼───────────────────────────────────────┐
        ▼                       ▼                                       ▼
┌───────────────────────────────────────────────────────────────────────────────┐
│                                 controllers/http                              │
│  react(WS+会话+队列+MCP+memory+bundle+plan_execution+usage+workflow) · agent ·  │
│  tool · skill · systemprompt · caller · apikey · llmmodel(连接/模型/白名单/积分)│
│  · setting(在线配置) · mcpadmin(网关管理台兼容层) · attachment(附件上传)         │
└───────────────────────────────────────┬───────────────────────────────────────┘
                                        ▼
┌───────────────────────────────────────────────────────────────────────────────┐
│                                 service 层                                     │
│                                                                               │
│  service/react —— ReAct 运行时核心（只管"如何执行一次 Agent Run"）              │
│  ├─ runtime.go / runtime_state.go   Run 入口与运行态（runtimeRequest 快照 +     │
│  │                                  reactEngineState 可变状态）                 │
│  ├─ engine.go        executeReactLoop 主循环（模型轮 → 解析 → 执行 → 回填）      │
│  ├─ tool_dispatch.go 工具调度（串行为主，只读类 4 路并发）                       │
│  ├─ meta_tools.go / tool_meta.go    27 个内置 Meta Tool + ToolMeta 声明注册表   │
│  ├─ business_tool.go 两段式业务工具（get_tool 激活 / execute_tool 执行）        │
│  ├─ delegate.go / wait_agent.go / send_message.go   子智能体委派体系            │
│  ├─ steering.go / queue_manager.go / command_box.go  插话引导 / 队列 / 通知箱   │
│  ├─ boundaries.go / entry_check.go   软着陆 / token 预算 / 入口检查              │
│  ├─ microcompact.go / runtime_support.go / context_breakdown.go  上下文三层管理 │
│  ├─ model_failover.go / model_retry.go   模型互备 + 失败分类重试                │
│  ├─ reasoning.go     思考程度三态（off/auto/custom）                            │
│  ├─ memory*.go / graph_memory.go      长期记忆工具与注入                        │
│  ├─ plan.go / external_runtime.go     计划确认卡片 + Plan Runtime 窄接口        │
│  ├─ web_fetch.go / web_search.go      网页抓取 / SearXNG 检索                   │
│  ├─ tool_confirm.go / todo / ask_question / client_tool / python_exec / ...    │
│  └─ ws.go / client_hub.go / history.go   手写 WS + 上行消息分发 + 历史回放      │
│                                                                               │
│  service/agent      子代理定义与内置 profile（DB > 内置，@none/@readonly token）│
│  service/plan       Plan Runtime V1（时序编排执行器，派生 Scoped ReactRun）     │
│  service/workflow   cron 定时触发工作流（调度器 + 裁判外环 + 熔断 + webhook）   │
│  service/judge      run 质量裁判（语义判级纯结构包，LLM 经 Invoker 注入）        │
│  service/memory     长期记忆（双层模型 + 唯一写核心 + extractor/resolver）      │
│  service/graphmemory  Graphiti 图谱记忆瘦客户端（group_id 作用域映射）           │
│  service/skill      技能 + bundled-skills 内置技能包（go:embed，幂等补种）      │
│  service/mcpclient  MCP 客户端（连外部服务器，同步工具进注册表，三种传输）       │
│  service/mcpgateway MCP 服务端网关（本服务 http 工具经 MCP 协议对外供给）        │
│  service/bundle     Agent 插件包安装/卸载/回滚                                  │
│  service/workspace  服务端代码工作区（mirror + worktree + 动态挂载 repo MCP）    │
│  service/{tool,systemprompt,caller,apikey}  注册类资源管理                      │
│  service/llmmodel + credits    连接与模型管理、用户积分                          │
│  service/setting    运行时在线配置（DB 覆盖 > yaml > 默认，TTL 刷新）            │
│  service/asynctask  异步任务 Provider 状态同步框架                              │
│  service/skillchatfile  附件存储（COS）与解码                                   │
└──────────┬──────────────────┬──────────────────────┬───────────────────────────┘
           ▼                  ▼                      ▼
  ┌─────────────────┐  ┌──────────────────┐  ┌────────────────────────────────┐
  │ api/llm         │  │ MySQL（38 张表）  │  │ 外部依赖                        │
  │ claude / gpt /  │  │ Redis（附件元数据）│  │ api/pythonexec（Python 沙箱）   │
  │ minimax 流式 +  │  │ 对象存储（MinIO）  │  │ 外部 MCP 服务器 / SearXNG /    │
  │ 工具调用 + 能力 │  └──────────────────┘  │ Graphiti / 上游 http 工具 /     │
  │ 目录门禁        │                        │ 企业微信·飞书 webhook           │
  └─────────────────┘                        └────────────────────────────────┘

  独立进程：cmd/mcp-gateway（网关独立部署，:8090，仅挂 /mcp）
           cmd/repo-mcp（stdio 只读代码检索 MCP 服务器，被 mcpclient 拉起）
           cmd/livetest/*（7 个真实模型全链路实测客户端）
```

进程形态：主服务 `:8180`（前缀 `/react-base-service`）承载全部业务；网关可随主服务内嵌（`/react-base-service/mcp`，`mcp_server.enabled` 控制）也可经 `cmd/mcp-gateway` 独立部署（共享同一 llm 库）；`repo-mcp` 由 mcpclient 作为子进程按需拉起，不独立部署。

## 2. ReAct 运行时核心链路

一次 run 的生命周期（入口链路：`Run/RunWithClientReader → run → runSingle → prepareRuntimeRequest → createReactRunContext → executeReactLoop`）：

```
WS 连接 /react/ws（run / steer / cancel / queue_send / plan_* / tool_use_answer / ...）
  └─ dispatchRunExecution：executionMode=plan 走 Plan Runtime（§10），否则普通 ReAct
      └─ run()（外层 for：S2 队列自动续跑循环）→ runSingle()
          ├─ prepareRuntimeRequest
          │    ├─ caller 校验 + 用户名解析（X-User-Name 头，默认 anonymous）
          │    ├─ 模型解析：用户自定义模型（连接/自包含双模式）或全局模型目录
          │    ├─ ResolveSystemPrompt（caller+route 前缀匹配，多条拼接）
          │    ├─ 工具/Skill/Agent 索引快照（含白名单与用户策略过滤）
          │    ├─ 长期记忆注入：<memory>（分层条目）+ <graph_memory>（按本次输入检索）
          │    ├─ reasoning 三态解析（off/auto/custom，快照进 runtimeRequest）
          │    └─ executionProfileForRun：外层/子代理/反思三种执行档案（§2.2）
          ├─ createReactRunContext（单事务：session 行锁 + 并发 run 检查/Steering 准入
          │    + 历史装载 + run 创建 + 用户输入落库 + 队列晋升 claim）
          └─ executeReactLoop（每轮 step）
               ├─ drainRuntimeNotifications（命令箱：吸收后台委派完成通知为一条
               │    react_notice 消息，claim-once，不新开 turn）
               ├─ maybeEnterSoftLanding（步数/时间/token 临近耗尽 → 收尾窗口）
               ├─ UpdateReactRun step_index
               ├─ maybeMicrocompactContext（微压缩，§6）
               ├─ maybeCompactContext（全量压缩，§6）
               ├─ buildToolDefinitions（只暴露 Meta Tool；业务工具走摘要索引）
               ├─ callModelRound（同模型退避重试 → 模型互备；流式 + 空闲超时检测）
               ├─ 无 tool call → 输出截断续写判定 → guide 注入点 → finish() 收敛
               └─ 有 tool call → executeToolCalls（§2.4 调度）
                        ├─ Meta Tool → executeInternalTool（get_tool/execute_tool/
                        │    delegate_agent/todo_write/python_exec/...，§3）
                        ├─ client 工具 → 通知前端执行 + 阻塞等 WS 回填
                        │    （run 转 waiting_client_message，clientMessageHub 按
                        │     toolUseId 路由，支持并行 HITL）
                        └─ http/mcp 工具 → 只读域拦截 → 确认门（permission_mode）
                             → 后端代理执行 → normalizeToolResult（大结果落 resultRef）
```

### 2.1 系统提示词装配

```
system = systemPrompt（注册表解析，多条按路由短→长拼接）
       + 工具索引摘要（## 当前 run 可用 Business Tool 摘要索引 …，仅 toolId/name/描述）
       + Skill 索引摘要（## 当前 run 可用 Skill 摘要索引 …）
       + <memory> 长期记忆块（分层条目，§9）
       + <graph_memory> 图谱记忆块（按本次输入检索，失败静默为空）
```

每轮还会在上下文末尾**临时**追加（不持久化）：`<async_tasks>` 未完结任务提醒、todo 提醒、软着陆收尾提醒、重复调用告警等 system-reminder。

### 2.2 ExecutionProfile（能力装配档案）

描述"某类 Run 允许看到哪些运行时能力"，控制 Meta Tool 装配（装配层 + `executeToolCall` 分发前硬检查，双重拦截）：

- `outerExecutionProfile`：外层对话默认全开（各开关合并 conf + 管理面板 DB 覆盖）；
- `subAgentExecutionProfile`：= 外层档案但 `InjectAsyncTaskReminder=false`（子 run 上下文由委派任务主导）；
- `reflectionExecutionProfile`：只开 `AllowMemory`，其余全关（记忆反思子 run 用）；
- agentPath 以 `plan/` 前缀的 run 强制 `AllowPlan=false`（Plan Step 内禁止递归生成第二套计划）。

### 2.3 两段式业务工具加载

1. 模型只看到工具**摘要索引**（system 前缀），不含 parameters；
2. `get_tool(toolId|name)` → 激活并返回完整 parameters/outputSchema（定义指纹落 run，激活清单持久化供下一 run 恢复）；
3. `execute_tool(toolId|name|callName, input)` → 输入 JSON Schema 校验 + 定义指纹复核 + 可见性复核 → 执行；
4. 定义变更可自愈：execute_tool 未命中时现查最新工具，定义与上次一致则自动激活；
5. 子代理白名单（`agentToolRefAllows`）在 get_tool 与自愈路径**硬检查**——索引过滤只决定"模型看到什么"，白名单决定"能不能加载"，只读执行域（§4.4）决定"能不能执行"。

MCP 工具没有专门通道：它就是 `tblLlmTool` 中 `tool_type=mcp` 的普通业务工具，走同一两段式协议，仅在 `service/tool` 执行层按 `config.mcpServer` 分流到 mcpclient（§8）。

### 2.4 工具执行调度

混合调度（`tool_dispatch.go`）：默认按模型返回顺序**串行**（串行工具出错即停，后续补"未执行"回填）；满足条件的调用进入并行批次：

- 可并行判定：`delegate_agent` 仅当 `subagent.enabled && max_parallel > 1`；其余工具按 `ToolMeta.ConcurrentSafe`（内置查注册表，业务工具查 `config.readOnly`）；
- 并行度：只读类固定 **4 路**；含 delegate 的批次取 `max(subagent.max_parallel, 4)`；
- 并行调用经 semaphore 限流 goroutine 执行，**结果仍按原 ToolCall 索引回填**，顺序不被完成先后打乱；重复调用槽位（参数签名相同）永不并行；
- `tool_closure.go` 保证每轮 results 与 calls 等长、每个 tool_use 都有配对 tool_result（孤儿回填）。

### 2.5 Run 状态机

9 个状态（`models/llm/react_run.go`）：

- 活跃态：`running`、`waiting_client_message`（ask_question / client 工具 / 工具确认等待前端回包）、`waiting_plan`（Plan 暂停等用户）、`cancelling`；
- 终态：`finished`、`error`、`cancelled`、`expired`、`timeout`（看门狗/wall-clock 终态）。

取消经 `Cancel` 置 cancelling + context 取消传播（子 run 从父 runCtx 派生，级联取消）；进程退出/重启时活跃 run 由启动任务 `ExpireStaleActiveReactRuns`（超 30 分钟未更新）统一置 expired，避免残留 run 永久阻塞会话。

## 3. 内置 Meta Tool 与 ToolMeta

### 3.1 ToolMeta 声明模型（`tool_meta.go`）

```go
type ToolMeta struct {
    ReadOnly       bool   // 只读工具：无外部副作用，可进只读执行域
    ConcurrentSafe bool   // 同轮多调用可并行
    SideEffect     string // none | session | network | system
    RiskLevel      string // low | medium | high
    TimeoutMs      int    // 单次执行超时；0=跟随全局默认
    MaxOutputBytes int    // 输出字节上限；0=跟随全局 InlineLimitBytes
}
```

`metaToolRegistry` 一次性登记全部内置工具语义；业务工具经 `config`（readOnly/riskLevel/maxOutputBytes/timeout_ms）解析出同构 ToolMeta，解析失败保守按可写。声明驱动四类行为：只读执行域硬拦截、并发调度、差异化输出预算（截断内容走 resultRef 分页续读）、RiskLevel/ReadOnly 随 tool_use 事件下发（前端徽标与确认门口径）。

### 3.2 内置工具清单（27 个登记，满配对外暴露 25 个）

| 工具 | 用途 | 装配条件 |
|---|---|---|
| get_tool / execute_tool | 两段式业务工具加载/执行 | 总是 |
| get_skill | 加载 Skill 完整说明 | 总是 |
| read_tool_result | 分页读取 resultRef 大结果或 `plan_result_` 步骤结果 | 总是 |
| inspect_data | 探测前序 JSON 工具结果的结构（路径/类型/样例） | 总是 |
| python_exec | 沙箱执行 Python（六种输入源、产物上传 COS、静态安全扫描） | 总是 |
| todo_write | 增量/覆盖更新当前 run 的 todo | 总是 |
| ask_question | 向用户提问并阻塞等待回填（HITL） | 总是 |
| displayFiles | 把 python_exec 产物展示给用户 | 总是 |
| resolve_async_task / get_async_task | 异步任务完结标记 / 完整记录回读 | 总是 |
| read_attachment / inspect_attachment | 附件读取 / csv 表结构探查 | 总是 |
| create_plan | 计划确认卡片（ReAct 内的轻量计划确认，§10） | `allow_plan`（caller 级三态，§10） |
| load_runtime_code | 加载服务线上代码到 (service, commit) 共享只读工作区并挂检索工具 | `workspace.enabled` |
| web_fetch | 抓取公开网页正文，按预算截断 + resultRef 续读 | `web_fetch.enabled` |
| web_search | SearXNG 网页检索（标题/链接/摘要清单） | `web_search` 启用且已配置 |
| memory_list / memory_read / memory_write | 长期记忆列表/读取/写入 | `memory.enabled` |
| graph_memory_search / graph_memory_write | 图谱记忆检索/写入 | `graph_memory.enabled` |
| delegate_agent | 把子任务委派给子 Agent（隔离子 run，§4） | 子代理启用 |
| send_message | 向已委派子代理发补充消息（父→子 guide 通道） | 同上 |
| wait_agent | 等待一个/多个子 run 到终态并返回状态快照 | 同上 |
| list_tools / list_skills | 轻量索引（已软下线，改为 run 初始化注入 system 前缀） | 不再对模型可见 |

## 4. 多智能体体系

核心模型：**子 Agent = tblLlmAgent 配置 + 独立 ReactRun + 同一个 executeReactLoop**。子 run 有自己的模型上下文、工具/Skill 白名单、token 预算、agentPath（`main/<agent>`）与持久化记录，继承父 run 的 runtimeServices 与 clientHub（HITL 通道共享）。

### 4.1 delegate_agent 委派

- `buildSubAgentRuntimeRequest` 隔离覆盖：不继承父历史、系统提示词换 agent 定义、工具/Skill 快照经 `FilterToolIndexSnapshot/FilterSkillIndexSnapshot` 过滤、`depth+1`（超过 `subagent.max_depth` 默认 2 拒绝）、`tokenBudget = max_tokens_per_run`（递归口径，子 run 消耗原子累加回父 run 的 delegated 列）。
- agent 定义实时解析（管理面板改动下一次委派生效，失败回退 run 快照）；模型不配置则继承父 run 当前模型（含互备后的实际模型）；思考口径父子一致。
- 状态透传：返回结构化 JSON `{agentKey, runId, status, finalResponse}`，status 由子 run 终态映射（completed/error/timeout/cancelled/expired），父模型可区分"完成"与"预算/看门狗耗尽被迫收尾"；失败为软错误，可凭 runId 续跑。
- `continue_run_id` 续跑：校验必须本 run 委派（parent_run_id 匹配防跨 run 窥探）、agent_key 一致、已终态；通过后从该子 run 恢复持久化历史（含压缩摘要）、todo、已加载工具。
- `background=true` 后台委派：立即返回启动回执；监视 goroutine 跑到终态后把完成通知落账本（kind=notification）并入父 run 命令箱，父 run 下一个模型步顶部吸收；父 run 已终态则不落账不入箱（绝不为通知复活 run）。
- 看门狗：`subagent.max_run_seconds` 到点以取消传播终止子 run，终态置 timeout。

### 4.2 内置子代理 profile（`service/agent/builtin.go`）

参照 ZCode"代码内置 + 同名覆盖"模式：4 个内置 profile 以 `builtin:` 前缀 AgentID 物化在代码中不落库，`CallerKey=default`（保留字伪 caller，全 caller 可见）；解析顺序 **DB > 内置**，DB 中同 agent_key 的行即 fork（整体覆盖）：

| profile | 工具白名单 | 只读域 | 定位 |
|---|---|---|---|
| general-purpose | `[]`（继承全部业务工具） | 否 | 自包含独立子任务多步查证/批量处理 |
| researcher | `@readonly` | 是 | 只读调查员，非只读工具被硬拦截 |
| report-writer | `@none`（显式不继承） | 否 | 零业务工具，靠 python_exec 产出报告 |
| code-reader | `@readonly` + 内置技能"代码工作区调查" | 是 | load_runtime_code + `ws_<service>_<commit短哈希>_*` 只读代码检索 |

白名单约定 token：`@none` = 显式为空（区别于空数组=继承全部）；`@readonly` = 仅继承声明了 `config.readOnly` 的工具。内置 profile 均为 MaxSteps 64（DB 自定义默认 8）。

### 4.3 wait_agent / send_message

- `wait_agent`（join 原语）：校验 run_ids ≤10 个且只允许本 run 委派的子 run → 2s 轮询 DB 至全部终态或超时（缺省跟随看门狗，无看门狗 300s，钳位 [1,3600]s）；返回各 run 状态快照而非整体报错；`waiting_client_message/waiting_plan` 映射为 waiting 立即返回。
- `send_message`（父→子定向消息）：定位目标（run_id 或 agent_path，必须本 run 委派且同 session）→ 优先活跃 run；目标软着陆中**拒收**；活跃则落账本（kind=user_input, delivery=guide），子引擎在下个模型步边界消费；发送后复核——子 run 恰好收敛则如实回报失败，定向消息绝不改投会话队列。

### 4.4 只读执行域（三层约束）

1. 工具白名单收敛（模型看到什么：索引快照过滤 + `@readonly` token）；
2. 系统提示词禁止清单（软约束）；
3. **enforceReadOnlyTools 硬拦截**（唯一硬保证）：`readOnlyViolationResult` 在 server/client 两个执行入口拒绝未声明 readOnly 的业务工具。

开关单调收紧：`子 run = 父 run 开关 || agent 定义 read_only`，嵌套委派只紧不松；外层 run 不开启。

## 5. Steering 与队列

用户在 run 进行中插话的完整处理（`steering.go` / `queue_manager.go`）：

- **准入决策**（`steerAdmissionDecision`）：run 处于 running 且无附件且非软着陆 → **guide**（注入当前 run 下一个模型轮）；waiting_*/cancelling、软着陆、带附件 → 队列开启则 **queue**，否则 **reject**。回执事件 `steer_guided / steer_queued / steer_rejected`。
- **guide 注入**：唯一注入点在"整批 tool_result 落库后、下一次模型请求前"（或纯文本 finish 前），同一事务内账本置 guided + 用户消息落库（claim-once）；guide 成为下轮尾部 user 消息，绝不插进 tool_use/tool_result 之间。
- **队列**：`POST /react/queue/list|update|reorder|delete` 管理（重排同步改写账本 seq，写操作与自动晋升互斥）；显式发送走 WS `queue_send`（从账本快照重建请求，claim 在新 run 创建事务内原子完成）；run 正常 finish 后同连接**自动续跑**消费队列（S2），循环至排空。
- **结算纪律**：run 终态统一 `settleRunPendingInputs`——后台通知一律 discarded；guide 仅外层 run 且队列开启时降级排队（`steer_delivery_changed`）；**子 run 的未消费 guide 一律 discarded**；重启后遗留 queued 行作废（session_resumed）。
- 账本：`tblLlmReactPendingInput`（guide/queue 双投递 + notification 机器事件共用，claim-once 晋升）。

## 6. 上下文管理

四层递进（阈值锚定上一轮真实 `lastInputTokens`，冷启动才估算）：

1. **入口检查**（`entry_check.go`）：估算"不可压缩部分"（system + tools schema + 当前输入），超过 `token_trigger` 直接拒绝——压缩救不了不可压缩部分。外层 run 与子 run 委派两处入口共用。
2. **微压缩**（`microcompact.go`）：token 压力达全量阈值 90% 时，把较旧工具结果就地替换为占位文案（带 resultRef 可续读），保留最近 5 组不动；**只改内存不落库**，回放与跨 run 历史按 DB 原文重建。
3. **全量压缩**（`runtime_support.go`）：超阈值 → 当前模型生成语义摘要，保留 system 前缀 + 最近若干轮（按轮次安全边界对齐，不切开 tool_use/tool_result 配对），落 `compact_summary` 消息并记录覆盖范围（回放与后续上下文据此过滤）。三道防线：连续失败熔断（转本地拼接摘要）、rapid-refill 防抖（近 3 轮压缩 2 次仍超 → 转软着陆）、压缩器输入分片（map-reduce 归并）。压缩成功后异步触发记忆维护（§9.2）。
4. **软着陆与预算**（`boundaries.go`）：步数/时间/token 任一临近耗尽进入收尾窗口（只放行只读工具、注入收尾提醒）；run 级 token 预算超 100% 以 `finish_budget_exhausted` 收尾（先回填未执行调用的 tool_result）；wall-clock 超时由 `runDeadlineScope` 兜底。重复工具调用软守卫（参数签名 + streak 计数，只提醒不阻断）。

**上下文分解**（`context_breakdown.go`）：每轮模型调用前估算 token 构成（systemPrompt/messages/mcpTools/systemTools/skills/others 六类）落 run 快照，供前端容量卡片可视化，不参与计费或压缩判定。

## 7. 模型层

### 7.1 连接与模型分离（重构后形态）

- **tblLlmConnection**：`protocol(openai/anthropic) + base_url + api_key（密文）` 的自包含连接，是模型配置主体形态；`tblLlmUserModel.connection_id` 引用连接取凭证与端点（0=自包含模式，key/url 直接填模型上）；解析顺序 caller 精确命中且 route 最长 > 空 caller 全局兜底 > 回退旧 tblLlmApiKey。
- 服务端代理 `GET {base}/models` 拉取模型清单（key 不出后端），自动填能力位；连接检测先验凭证再用最小对话验推理链路。
- `service/llmmodel/schema.go` 声明式参数 schema 下发前端渲染（contextTokens/maxOutputTokens/reasoning 预算/压缩参数等，min/max/step 零硬编码）。

### 7.2 能力目录与运行时门禁（`api/llm/capabilities.go`）

`capability_catalog.json`（go:embed，models.dev 风格）按 `exact` 全等 + `prefixes` 前缀匹配输出 `SupportTools/SupportThinking/SupportVision/ContextTokens/MaxOutputTokens`。消费点：fetch_models 自动填位；**运行时门禁**——声明不支持 thinking 则不下发思考参数、不支持 vision 则拒绝 image/* 载荷；目录可被每模型开关覆盖，未命中保守回退。

URL 归一化（`url.go`）：`ChatCompletionsURL/MessagesURL/ModelsURL` 保证"贴什么路径都能通"（已含最终路径原样返回、以版本段结尾只拼协议路径、否则拼完整 /v1/...）。

### 7.3 模型互备与重试

- **互备**（`model_failover.go`）：仅当所选模型在 `models.available` 且 `mutual_failover=true` 时，available 其余模型追加为候选（未知/临时模型不混入，防配置外切供应商）。同一 step 内"同模型重试 → 切互备"，切换不增加步号、不执行工具；成功备选提升为后续轮次首选；互备轮先收集成功再补发事件（失败不污染前端），半截输出经 `model_fallback` 事件（resetCurrentOutput）提示前端丢弃。
- **重试**（`model_retry.go`）：失败三分类——`retryable`（408/409/429/5xx/流空闲超时/上游中断）/ `fatal`（其余 4xx 客户端类）/ `cancelled`（绝不重试）；退避尊重 Retry-After（≤5min），否则指数退避 + ±30% 抖动。
- 跨厂商兼容：非 claude 模型清空 thinking part 的 Signature；用户模型声明不支持 tools 时该模型轮不下发工具 schema（纯对话收敛）。

### 7.4 思考程度三态（`reasoning.go`）

payload `reasoning: {mode: off|auto|custom, effort: minimal..high, budgetTokens: 0..131072}`，每轮经 `llm.WithReasoning` 注入，由各协议 client 翻译为 `reasoning_effort`（gpt）或 `thinking.budget_tokens`（claude）；off 不请求思考块；能力目录声明不支持 thinking 时门禁参数。子 run 继承父 run 口径。

### 7.5 密钥安全

conf 加载支持 `${VAR}` / `${VAR:-default}` 环境引用（仓库不落明文 key）；DB 密钥 AES-256-GCM 加密落库（`enc:v1:` 前缀，主密钥由 `LLM_SECRET_KEY` 派生，存量明文灰度兼容，有密文但无密钥时解密返回空串不外传）。

## 8. MCP 子系统

三个组件、两个方向：

| 组件 | 方向 | 职责 |
|---|---|---|
| `service/mcpclient` | 出（消费） | 连外部 MCP 服务器，拉取工具同步进 tblLlmTool（tool_type=mcp），run 执行时分发调用 |
| `service/mcpgateway` | 入（供给） | 本服务启用的 http 工具经 MCP Streamable HTTP 对外暴露给外部 MCP 客户端 |
| `mcpserver` + `cmd/repo-mcp` | 自研 stdio 服务器 | 零依赖 JSON-RPC over stdin/stdout 框架 + 只读代码检索工具集，被 mcpclient 拉起 |

### 8.1 mcpclient

- 统一 `Server` 接口（Start/Stop/ListTools/CallTool），三种传输：`kind=repo`（stdio 子进程，可执行文件走适配器白名单，仅 `bin/repo-mcp`）；`kind=http`（手写 Streamable HTTP，按次会话，支持 JSON 与 SSE 响应）；`kind=http_sdk/sdk`（官方 MCP Go SDK）。
- SSRF 防护（`endpoint.go`）：仅 http/https，拒绝环回/私网/链路本地/CGNAT 等保留网段，域名解析后逐一判定（防 DNS rebinding）；`allow_private_endpoint` 仅本机开发。
- 工具同步：toolId `mcp_<server>_<tool>`（绑定 caller 副本加后缀），config 携带 inputSchema/outputSchema/mcpServer/mcpTool；**仅 kind=repo（ws_ 工具族）同步 `readOnly:true`**，其余语义未知保守不标。失联分级：连接失败 → 工具整体 status=0（恢复自动恢复）；服务器下架工具 → 停用名单外行；删除/停用/解绑 → 软删。启动时 `CleanupOrphanedRegistryTools` 兜底软删无来源服务器的孤儿工具（软删行占唯一键，同名重接自动复活）。
- 来源二：DB 注册表（`/react/mcp/*` 管理端点：list/detail/create/update/delete/connect/refresh）+ yaml 静态声明（`mcp.servers`，只读展示）；支持粘贴标准 `mcpServers` JSON（仅 url 形式，command/args 拒绝）。运行中服务器工具变更不自动重同步，需手动刷新。

### 8.2 mcpgateway

- 端点 `/mcp`（独立进程）或 `/react-base-service/mcp`（内嵌），Streamable HTTP 且 `Stateless + JSONResponse`（每请求独立生命周期，天然支持多副本水平扩容）。
- **鉴权**：`Bearer <app_key>:<app_secret>`（常量时间比较，查 tblLlmMcpApp）；失败返回 403 而非 401（避免客户端误走 OAuth 发现）；`X-MCP-User` 仅审计展示。
- **注册与白名单**：工具基础集合（全部启用 http 工具，跨 caller 按名去重）∩ 应用绑定（tblLlmMcpAppTool）——绑定即权限；tools/call 阶段按名复核（防客户端缓存过期）。
- **执行链**：JSON Schema 显式校验（缺必填字段附参数 description 追问）→ dispatch（GET/DELETE 转 query，其余 JSON body；可透传调用方 Cookie）→ 上游响应信封归一 → 审计。
- **结构化输出**：成功同时返回文本与 StructuredContent；outputSchema 声明 `x-output-projection: true` 时按 schema 裁剪上游响应并把字段 description 渲染成说明文本；最终按 outputSchema 校验结果结构。
- **审计**：`tools/call` 全量切面，异步批量落 tblLlmMcpCallLog（4096 队列满则丢弃告警、100 条/批、最长 1s flush；只插不改）。
- 管理面：`/react/mcpapp/*`（应用凭证 CRUD/重置密钥/白名单/审计查询）+ `/api/manage/*` 兼容层（适配旧 Vue3 管理台，X-Admin-Token 静态令牌）。

### 8.3 repo-mcp 与代码工作区

- `repotools/` 只读仓库访问：ast-grep 风格结构化搜索（pattern 元变量 `$NAME`/`$$$NAME`）、go/ast 声明感知检索、路径全部限制在 REPO_ROOT 内（含符号链接逃逸校验）。
- `cmd/repo-mcp` 注册 9 个工具：list_repositories / list_files / read_file / search_code / search_pattern / find_symbol / get_file_symbols / find_references / get_repo_map。
- `service/workspace` 四件套：静态 resolver 白名单（service+env → repo_url+ref）→ bare mirror 缓存（clone --mirror / fetch --prune，mirror 级互斥防 .lock 冲突）→ **(service, commit) 粒度共享 worktree + repo MCP（引用计数，多 run/caller 复用一份）** → 动态挂载只读 repo MCP（工具名 `ws_<service>_<commit8>_<tool>`，带 commit 天然唯一）；run 终态 ReleaseRun 减引用：caller 归零摘其工具副本、总归零才回收 worktree 与 MCP。安全面：service/env/ref 正则白名单、git 子命令白名单、不经 shell。
- 模型入口：`load_runtime_code` Meta Tool；code-reader 内置子代理的标准工作流。

## 9. 长期记忆

双层模型（`docs/memory.md` 蓝本）：resident 常驻层（全文注入）+ detached 按需层（目录索引注入，memory_read 按需读取）。V2 类型化增量：`memory_type`（preference/fact/event/procedure）、`confidence`（0-1）、`importance`（1-5，注入排序）。

### 9.1 数据与写核心

- `tblLlmMemoryItem`：原子事实，逻辑唯一键 `(owner_type, owner_key, item_key)`——item_key 为内容规范化 SHA-256 前 16 位指纹（幂等去重）；`version` 乐观锁；`tags.locked` 只读保护。
- `tblLlmMemoryRevision`：不可变修订流水（action/before/after/reason/source），回滚 = 用旧快照反向插入新修订。
- 作用域：`caller`（公共底座）/ `caller_user`（用户级覆盖同名条目）。
- **`ApplyMutation` 唯一写入口**：归一化 → 校验 → 敏感拦截（凭证/JWT/私钥/身份证/手机号正则）→ 事务写（含流水）→ 打点。模型工具（source=model）、管理面（admin）、reflection、extractor 四类写入方全部经此，审计链无旁门。容量守门（resident 条数上限 / detached 软上限）。

### 9.2 提取与反思（compact_end 后触发，二者互斥，均不阻塞主 run）

- **extractor（结构化流水线，优先）**：确定性预过滤（置信度/指纹去重/条数上限）→ 非工具 LLM 调用抽候选（强制纯 JSON，鲁棒解析）→ resolver 冲突消解：候选召回相似存量 → 单次 LLM 批量判定 ADD/UPDATE/SUPERSEDE/SKIP → **代码裁决落库**（时间/归属/限额全由代码执行，幻觉 ID 按 SKIP 收敛；被取代事实进修订快照不物理删除）。
- **reflection（agentic 备选路径）**：派生 `session_type=reflection` 的异步受限子 run（MaxSteps=12，工具集仅 memory 三工具，五阶段提示词 Investigate→Extract→Update→Review→Commit）；安全阀：每 run 写次数上限、locked 只读、失败静默重试一次。
- 冷却与防自触发：session 维度进程内冷却表。

### 9.3 注入与工具

- run 初始化：`<memory>` 块（preference 不论层级全文注入；其余 resident 全文、detached 目录）+ `<graph_memory>` 块（按本次输入检索相关事实）；空记忆整块不出现（token 零增量）。
- 模型侧：memory_list / memory_read / memory_write（受 ExecutionProfile.AllowMemory 控制）。
- 管理面：`/react/memory/*`（list/create/update/delete/revisions/rollback）。

### 9.4 graphmemory（Layer2）

指向外部 Graphiti FastAPI 服务的瘦 HTTP 客户端（/search、/messages、/healthcheck），不镜像图数据。scope：基座两级作用域映射为 Graphiti `group_id`（caller 级组做公共底座，userScope 开启时追加用户组；组名清洗 + 哈希后缀防碰撞），服务端强制注入组、模型入参不暴露组概念。`graph_memory_search/write` 两工具；run 初始化按本次输入检索渲染 `<graph_memory>` 块——增强不是依赖，失败/超时/空结果一律空串绝不阻断 run。

## 10. Plan Runtime（时序编排执行器）

用户主动选择 `executionMode=plan` 时启用；入口分流在 controller 层（依赖方向 plan → react，react 不反向 import）。

```
submit_plan（schema-constrained 虚拟工具，非 ReAct 不执行工具，max 3 次尝试）
  → 计划版本+步骤持久化（线性依赖，stepType: AGENT / USER_INPUT / USER_ACTION）
  → executor 循环：claimStep 事务 CAS（并发 Resume 只有一个成功）→ 分派
       ├─ AGENT 步骤 → RunScopedStep：派生隔离 Scoped ReactRun（agentPath="plan/<stepKey>"，
       │    不继承 outer 历史，继承工具/Skill/记忆/Agent 能力快照，独立超时默认 600s）
       ├─ USER_INPUT / USER_ACTION → 暂停：createWait + outer run 置 waiting_plan
       │    （goroutine 正常退出不挂死，Resume 重建上下文续跑）
       └─ 全部完成 → Finalizer（复用模型互备但不暴露工具）汇总生成最终答复写回 outer run
```

- 失败/取消级联：剩余 PENDING 步骤批量置 CANCELLED，不留孤儿；步骤超时（区别于用户取消）换新 Attempt 重试，自动重试耗尽后允许手工 Retry（同事务复位外层 run）/ Skip（仅非 required）/ Cancel。
- 前端视图：`plan_view_update`（状态变化推送 PlanPublicView）/ `plan_step_event`（步骤 run 事件包装）；HTTP `POST /react/plan_execution/{detail,events,resume,retry,skip,cancel}` + WS `plan_resume/plan_retry/plan_skip/plan_cancel`。
- 与旧 `create_plan` Meta Tool 的关系：后者仍是普通 ReAct 模式里的"计划确认卡片"（生成计划 → 前端确认 → 新 run 按计划执行，进程内存态），受 `allow_plan` 控制；Plan Runtime 才是真执行器。
- **allow_plan 三态闸门**（caller 级）：`tblLlmCaller.allow_plan` 显式 true/false 覆盖全局 `llm.react.allow_plan`（默认开），未配置跟随全局；执行档案装配与 `executionMode=plan` 显式入口闸门共用同一口径。

## 11. Bundle（Agent 插件包）

参照 OpenHands plugin loader 的"安装即注入多类资源"：一个包含 `manifest + agents/*.md + skills/<name>/SKILL.md + .mcp.json` 的目录打包，安装时展开写入各注册表。

- **来源白名单**：source（git URL 或本地路径）必须命中 `bundle.allowed_source_prefixes` 前缀（空列表拒绝一切）；git 来源走 mirror 裸仓缓存 + ref 钉 commit + 临时 worktree 检出（git 子命令白名单、参数正则、超时看门狗、纯内存解析）。
- **安装**：重装同名先卸载还原；逐资源应用（agents/skills 走 Markdown frontmatter upsert、.mcp.json 仅支持 url 形式 HTTP MCP），每资源即时落 `tblLlmBundleResource` 清单行（含覆盖前整行快照）；任一失败自动卸载清理，不留半装状态。
- **卸载**：按资源清单逆序回滚——快照为空（新建）软删，非空（覆盖）按快照整行恢复。
- 端点：`POST /bundle/{install,browse,uninstall,list}`。

## 12. 定时触发工作流（service/workflow）

cron 无人值守调度 ReAct run。总开关 `llm.workflow.enabled`：关闭时不启动调度器也不挂管理接口（空转降级，与历史版本行为一致）。设计蓝本 `docs/定时触发工作流实现方案.md`。

- **定义与调度**：`tblLlmWorkflow`（workflow_key 唯一；cron 表达式 + 时区 + 目标 prompt + 模型/maxSteps/超时 + webhook + 规则判级正则 `risk_patterns`）；进程内 cron 调度器单例（robfig/cron），定义变更经 manager 单点 SyncRegister 重注册内存 entry；cron 着火按 `planned_fire_at` 防重键落历史行，重启不补跑、不重复触发（手动触发该键为 NULL，不受防重约束）。
- **执行**（`executeWorkflowRun`，与 memory_reflection 同构的 headless 链路）：`session_type=scheduled` 无人值守会话内跑完整 ReAct run——无前端连接、禁用 client 工具与 ask_question（等待人工输入即挂死）；run 身份 = 定义 `user_name`，空则系统账号 `workflow` 兜底（引擎要求 run 必须归属用户，审计/工具白名单按用户解析）；wall-clock 超时与 react 引擎内注册/停机机制双保险。
- **裁判外环**（`runJudgeLoop` + `service/judge`）：completed 后审阅 transcript 与任务目标，语义判定「达成与否 + 风险等级」，未达成自动注入追问、同会话续跑再判（上限 N 轮，追问轮共享同一超时预算）；judge 是纯结构包（transcript + 目标 → 判定 + 追问建议，LLM 经 Invoker 接口注入），不依赖 react 引擎与模型层；判定器任何故障 fail-open——保留规则判级、不追问、不改终态。
- **双判级**：语义判级（judge 输出 none/low/high）+ 规则判级（`risk_patterns` 正则命中 run 结论），结果落 run 历史 `risk_level`，供通知分级与面板筛选。
- **报告产物化**：run 终态后自动发现会话内报告产物（tblLlmReactArtifact），随 run 历史关联并在通知中携带清单，面板可溯源。
- **连环失败熔断**：最近 threshold 条 run 全为 failed/timeout → 自动停用定义 + 摘除调度 entry + 熔断告警（防故障风暴）；连续计数跨停用窗口——重启用后首败立即再熔断，运维验证先手动 dispatch 成功一次（成功即清零）。
- **webhook 通知**：企业微信/飞书通用格式按 URL 域名识别；failed / timeout / 高风险 / 判未达成 / 熔断恒通知；completed 且无风险受 `notify_only_on_risk` 控制（只告警不报平安）。
- **管理面**：REST `POST /react/workflow`（建）、`GET /react/workflow/list`、`PATCH /react/workflow/:key`（改/启停）、`DELETE /react/workflow/:key`、`POST /react/workflow/:key/dispatch`（手动触发）、`GET /react/workflow/:key/runs`（执行历史）；自包含管理面板 `GET /react/workflow/admin`（数据经带登录态的管理接口拉取）。

## 13. 周边能力

- **会话管理**：sessionId 锁定复用（校验归属五元组 userName/callerKey/routeValues/type 一致）；同会话并发 run 互斥（Steering 开启后转为插话准入）。端点：`POST /react/session/list` 分页（type/keyword 过滤）、`POST /react/session/events` 历史回放、`POST /react/session/delete` **硬删级联**（Plan 五表/工具结果/排队账本/反馈/产物/异步任务/消息/run/session 单事务，活跃 run 拒绝）、`POST /react/session/fork` 复制式分叉、`POST /react/session/rename` 重命名。
- **会话分叉（fork）**：截断点之前的历史按 **run 边界对齐**复制成一个全新普通会话——分叉出的会话走既有历史装配/回放/压缩路径零改动，原会话硬删不影响分叉。截断点两种给法：`throughRunId + inclusive`（run 级，前端轮次入口；inclusive=true 带着该轮继续，false 回到该轮提问前，缺省 true）或 `throughMessageId`（消息级，命中 run 起始用户输入该轮起丢弃、否则整轮保留），二者恰好提供一个；delegate 子 run 随父复制并重写 parent_run_id，plan/* run 与 Plan 子表剔除。复制纪律：session/run/message 业务 ID 全量重生成、compact `coveredThrough` 游标按映射重写、`created_at` 保序（timeline 主序不变）；TodoState/已加载工具经"最新外层 run 继承"无缝延续；活跃 run 拒绝（行锁 + 并发检查，与 delete 同口径），排队账本等运行态数据（ToolResult/PendingInput/Feedback/Artifact/AsyncTask/Plan）不复制。前端 SDK 在每轮末尾反馈条提供"从这里分叉"入口，分叉成功后自动切到新会话。
- **会话重命名（rename）**：仅标题元数据（去空白 + rune 截断 64 字，与 fork 自定义标题共用上限），活跃 run **不**拦截（与 delete/fork 的数据级联约束刻意区分）；行锁读-归属校验-写同事务，幂等短路（标题未变不写库，避免无谓 bump updated_at 扰动列表排序）。
- **历史回放**：`POST /react/session/events` 把 tblLlmReactMessage 按时间线还原成**与实时 WS 协议同形**的事件流（含 plan_view_update、steer_* 等新事件），直播与回放共用一套解析；内置回放页 `GET /react/replay`。
- **异步任务**：`config.async=true` 工具执行成功后落 tblLlmReactAsyncTask（pending，TTL 7 天，全量快照）→ 后续 run 启动注入 `<async_tasks>` 提醒 → 模型确认结果后 `resolve_async_task` 唯一清除；`service/asynctask` Provider 框架可让第三方调度系统状态自动回写（含租约对账），未注册时纯模型提醒模式。
- **工具大结果与产物**：超过 inline_limit_bytes（或工具级 maxOutputBytes）只回填预览，全文落 tblLlmReactToolResult（resultRef），`read_tool_result` 分片续读；python_exec 产物上传 COS 落 tblLlmReactArtifact，前端经 `GET /react/artifact/:id`（IPS 鉴权）下载。
- **附件**：`POST /api/chat/files/upload`（csv/md/txt ≤50MB，UTF-8/UTF-16 BOM/GB18030）；run payload `attachments` 引用 fileId。
- **web_fetch**：仅公网 http/https；环回/私网/链路本地字面量拒绝 + 重定向逐跳复查（DNS rebinding 留待 egress 代理）；响应 2MB 上限、仅文本类 Content-Type；正文提取（去脚本样式标签 + 实体解码）按预算截断、完整正文走 resultRef；进程内缓存 15 分钟。
- **web_search**：SearXNG JSON API（自建实例免密钥）；结果只含标题/链接/摘要；结构化结果经 meta 旁路给前端（可点击），不进模型上下文。
- **上下文容量看板**：`POST /react/usage/context` 聚合会话最近 run 的容量占用、缓存命中率、六类 token 构成（context_breakdown 数据源）。
- **工具确认门**：`tblLlmTool.permission_mode` 三档——auto / confirm（每次人工确认）/ confirm_risky（入参命中风险正则才确认）；确认流复用 ask_question 等待通道（`tool_confirm_request` 事件 + WS `tool_confirm_answer` 回填）；agent 级 permission_mode 是子 run 内全部工具的下限。
- **内置技能包**：5 条 SKILL.md 经 go:embed 打进二进制（委派任务写作 / 大数据结果处理 / 业务工具两段式调用 / Python 数据分析 / 代码工作区调查）；caller 创建时 + 启动时为全部 caller（含 default）幂等补种，同名行跳过不覆盖用户改动；生效路径与自建技能一致（摘要索引 + get_skill 按需加载 + triggers 关键词触发）。

## 14. 运行时在线配置

`tblLlmRuntimeSetting`（setting_key 单行 JSON）+ `conf/runtime_setting.go`（atomic.Value 快照）：

- 优先级 **DB 覆盖 > custom.yaml > 内置默认**；引擎经 `conf.GetReactRuntimeConfig()` 每次调用应用覆盖快照——"下一个 run / 下一次委派"即生效，运行中 run 不受影响。
- 启动加载一次 + 每 10s TTL 刷新（拉平多实例漂移与人工改库；刷新失败保留旧快照防策略闪断）；面板写入后本进程立即生效。
- 端点：`POST /setting/{subagent,context,memory}/get|update`；响应含 `effective`（与引擎消费同一实现）+ 逐字段 `sources`（override/yaml/default）+ `baseline`（清除覆盖后的回落值）；更新支持 `clearFields`。

## 15. 数据模型（38 张表）

| 域 | 表 | 说明 |
|---|---|---|
| 调用方与注册资源（11） | tblLlmCaller | 调用方（鉴权与资源归属主体；default 为保留字伪作用域） |
| | tblLlmSystemPrompt | 系统提示词（caller+route 前缀匹配） |
| | tblLlmTool / tblLlmToolUserPolicy | 工具（http/client/mcp，含 permission_mode）及用户白名单 |
| | tblLlmSkill | 技能（SKILL.md 正文 + triggers） |
| | tblLlmAgent | 子 Agent 定义（tools_json/read_only/max_steps/max_tokens_per_run） |
| | tblLlmBundle / tblLlmBundleResource | 插件包安装记录 / 资源清单（快照回滚） |
| | tblLlmMcpServer / tblLlmMcpServerCaller | MCP 连接注册表 / 连接-caller 绑定 |
| | tblLlmApiKey | caller 级模型凭证（被 tblLlmConnection 升维替代中） |
| 模型与积分（4） | tblLlmConnection | 模型连接（protocol+base_url+密文 key） |
| | tblLlmUserModel | 用户自定义模型（connection_id 或自包含） |
| | tblLlmUserBaseCredits / tblLlmUserBonusCredits | 月度基础/赠送积分（**未接线**：仅列表展示与管理调整，run 链路无门禁/扣减） |
| ReAct 核心（4） | tblLlmReactSession / tblLlmReactRun / tblLlmReactMessage | 会话 / 运行实例（状态机+token 统计+快照+parent_run_id）/ 上下文消息 |
| | tblLlmReactPendingInput | 插话与通知账本（guide/queue/notification，claim-once） |
| ReAct 周边（5） | tblLlmReactToolResult / tblLlmReactArtifact | 工具大结果 / Python 产物元数据 |
| | tblLlmReactAsyncTask / tblLlmReactRunFeedback / tblLlmChatFileRecord | 异步任务待办 / 轮次反馈 / 附件记录 |
| Plan Runtime（6） | tblLlmPlanExecution / Version / Step / StepAttempt / StepResult / Wait | 执行实例 / 计划版本 / 步骤 / 尝试 / 结果 / 等待请求 |
| 长期记忆（2） | tblLlmMemoryItem / tblLlmMemoryRevision | 双层记忆条目（类型化）/ 不可变修订流水 |
| 运行时设置（1） | tblLlmRuntimeSetting | 在线配置覆盖（subagent/context/memory 三键） |
| MCP 网关（3） | tblLlmMcpApp / tblLlmMcpAppTool / tblLlmMcpCallLog | 应用凭证 / 工具白名单 / 调用审计 |
| 定时工作流（2） | tblLlmWorkflow / tblLlmWorkflowRun | 工作流定义（cron/目标/判级正则/webhook）/ 执行历史（planned_fire_at 防重、终态、风险等级、结论快照） |

完整 DDL 见 `sql/init.sql`（增量迁移：`mcp_gateway_v1.sql`、`plan_runtime_v1.sql`、`runtime_setting_v1.sql`、`memory_v2_upgrade.sql`、`model_config_v1.sql`、`caller_allow_plan_v1.sql`；workflow 两表随 init.sql 全量维护）。

## 16. 关键设计约束

- **消息即上下文**：`tblLlmReactMessage.content_json` 保存真实进入模型的 modelMessage（含 tool_use parts 与 reasoning），UI 旁路数据（toolMeta）单独存放，保证回放与模型上下文一致；thinking 空 signature 块不落库（防严格 provider 422）。
- **串行事件信封**：所有事件统一补 runId/sessionId/seq，经 EventWriter 串行写出；异步场景（反思子 run）用无头 EventWriter，事件照常持久化供回放。
- **依赖方向**：plan → react（经 external_runtime.go 窄接口：PrepareExternalRun / ScopedStep / TextCompletion / StructuredCompletion）；react 不 import service/plan（唯一例外：read_tool_result 按 `plan_result_` 前缀读 Plan 步骤结果，以 session 为授权边界）；HTTP/MCP 工具执行下沉 service/tool；记忆在 service/memory；代码工作区在 service/workspace。
- **收敛优先于报错**：maxSteps 耗尽以完成态 + 终止原因收尾（非 error）；预算耗尽先回填未执行调用的 tool_result；模型互备轮先收集成功再补发；工具/计划失败尽量返回模型可读的软错误让其自纠。
- **claim-once 与行锁**：队列晋升、guide 注入、命令箱吸收、Plan 步骤抢占全部经条件更新原子完成，与并发写互斥；重启不复活（遗留 queued/admitted 行作废）。
- **白名单/只读分层**：索引过滤（模型看到什么）→ 白名单硬检查（能不能加载）→ 只读执行域（能不能执行）→ 软着陆限流（收尾期还能用什么）。
- **WS 保活**：协议层 ping 每 5s + 应用层 heartbeat 事件每 20s；读侧靠 pong 刷新读超时（允许连丢 3 个）实现断连检测；断连原因分类（close_frame/tcp_eof/reset/timeout）。
- **配置与密钥**：yaml 支持 `${VAR}` 环境引用；DB 密钥 AES-256-GCM（`enc:v1:` 前缀）双向灰度兼容。

## 17. 启动与后台任务

`main.go` → `router.Tasks(engine)`（退出挂 `router.StopTasks()`）：

1. `model.ExpireStaleActiveReactRuns(30min)`：清理上一进程遗留活跃 run（同步，先于路由注册）；
2. `go skillService.SeedBundledSkillsForAllCallers()`：内置技能包幂等补种（异步）；
3. `asynctask.Start`：异步任务状态同步框架（无 Provider 空转）；
4. `mcpclient.Bootstrap`：拉起 yaml 静态 MCP 服务器 → 同步工具注册表 → 拉起 DB 登记连接 → 清理孤儿工具；
5. `mcpgateway.StartAuditWriter`（`mcp_server.enabled` 时）：审计异步落库 worker 池；
6. `setting.Bootstrap`：运行时设置覆盖快照加载 + TTL 周期刷新；
7. `workflow.Start`（`llm.workflow.enabled` 时，`router/command.go`）：cron 调度器启动，加载 enabled 定义注册 entry。

退出对称 Shutdown（`workflow.Stop` 先停 cron 着火、再有界等待在跑 run 收敛，超时由 react 停机 expire 兜底）。graphmemory 无启动钩子（瘦客户端按配置运行时实例化）。

`cmd/livetest/` 下 7 个程序对真实模型做全链路实测：steering-guide / steering-queue / steering-waiting / steering-fallback（插话四场景）、queue-manage（队列管理）、send-message（定向消息）、delegate-background（后台委派与通知）。
