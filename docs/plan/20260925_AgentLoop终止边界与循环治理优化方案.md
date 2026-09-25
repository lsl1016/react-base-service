# Agent Loop 循环治理优化方案 —— 从 maxSteps 硬上限到终止边界体系

| | |
|---|---|
| 日期 | 2026-09-25 |
| 状态 | 待评审 |
| 参照系 | 上级目录 `ZCode` 仓库（`apps/zcode-cli`）的 Agent Loop 实现 |
| 适用范围 | `react-base-service` ReAct Runtime（`service/react`） |
| 关联文档 | `docs/plan/长期记忆优化.md`、`docs/plan/长期记忆V2实施计划-Phase1-2.md`（Phase 3 与其共享 compact 触发点） |

---

## 0. TL;DR

ZCode Agent Loop 最关键的设计不是某个功能，而是一条宪法级原则（`ZCode/apps/zcode-cli/AGENTS.md:11`）：

> 核心 agent loop 默认面向可持续运行的复杂任务设计，**不用 tool call 次数做硬停止**。资源与安全边界应由 token/context limit 自动 compact、用户取消、权限拒绝、工具超时、输出截断、provider retry 上限等**明确条件**承担。

本项目的 ReAct 循环（`service/react/engine.go:100`）目前恰好相反：以 `for step := 0; step < maxSteps; step++` 的次数硬上限作为主终止机制，且耗尽后直接进入 error 态（`engine.go:171`），没有软着陆；同时又缺少 ZCode 那套真正的边界（无 provider 重试、无 run 级超时与预算、无 compact 失败熔断）。

本方案分 5 个 Phase：

1. **Phase 1（P0）**：终止边界体系 —— maxSteps 降级为"防御性最后闸门"，新增软着陆收尾轮、重复调用软守卫、run 级 wall-clock/token 预算、输出 token 续写、交互等待超时；
2. **Phase 2（P0）**：模型调用韧性 —— 失败分类器 + 指数退避重试 + Retry-After 尊重 + 与互备协同（当前除互备外零重试）；
3. **Phase 3（P1）**：Context 治理 —— 真实窗口接入、usage 锚定、microcompact、压缩器修复、compact 失败熔断、prompt cache 前缀保护；
4. **Phase 4（P1）**：工具执行治理 —— 统一超时配置、并发调度声明、MCP stdio 超时隔离；
5. **Phase 5（P2）**：取消与权限语义归一 —— 取消不是错误、拒绝即回灌、确认等待超时。

核心取舍：**用"明确条件 + 软性守卫"替代"次数恐慌"**。次数阈值只能做观测与提醒（注入提醒让模型自己收束），永远不做主终止开关。

---

## 1. 为什么"次数硬上限"是错误的终止机制

长任务天然是长工具链：Read → Grep → Edit → Bash → 测试失败 → 再 Edit → 再测试……工具调用次数完全不能说明 Agent 是否进入死循环——一个正常执行的重构任务可能轻松超过 50 次工具调用，而一个死循环可能在第 3 次就出现（同一个 Read 反复调用）。次数上限的代价是：

- **误杀长任务**：上限设小了，正常任务被腰斩；上限设大了（如当前 `custom.yaml:19` 的 `max_steps: 500`），等效于没有上限，死循环烧钱直到 500 轮；
- **错误语义**：耗尽后直接 `ErrorReactRunFailed("exceed maxSteps")`（`engine.go:171`），用户看到的是一次"失败"，而实际上模型可能已经完成了 90% 的工作，只差一个总结；
- **掩盖真问题**：没有重试、没有超时治理时，"加次数"是唯一止血手段，于是默认值 8（`conf/config.go:30`）被临时调到 500 再也回不去。

正确的思路是把循环的终止交给一组**语义明确的边界**，每个边界对应一种真实的失败/结束模式，并让可恢复的边界优先"回灌模型自纠"而不是终止循环。

---

## 2. ZCode 终止边界体系精读

### 2.1 九大终止边界

主循环 `runRegularTurnLoop`（`ZCode/apps/zcode-cli/packages/core/src/runtime/methods/turn-loop.ts:43`）就是一个 `while(true)`，全程**没有任何** modelStepCount / toolCallCount 上限判断。终止只能由以下边界触发：

| # | 终止边界 | ZCode 位置（相对 `ZCode/apps/zcode-cli`） | 机制要点 | 恢复语义 |
|---|---|---|---|---|
| 1 | 用户取消 | `packages/core/src/runtime/helpers/turn-errors.ts:59-86` | AbortSignal 贯穿全链路，每个阶段之间 `throwIfTurnAborted` 检查 | **取消不是错误**：发 `TurnComplete(resultType:"cancelled")`，已收到的流式部分持久化快照 |
| 2 | 权限拒绝 | `packages/core/src/tool/executor/permission-flow.ts:128-152` | deny 生成 `PermissionDenied` 错误结果**回灌模型** | 不停循环，模型换路径重试；仅极少数产品级场景（plan 退出被拒）经 turn control 停 turn |
| 3 | Context Limit | `packages/core/src/runtime/methods/turn-model-step.ts:742-803` | provider 抛超窗异常 → 先尝试 **reactive compact**（压缩后重试同一请求） | 可恢复优先；恢复失败才终止 |
| 4 | Compact 失败 | `packages/core/src/runtime/methods/compact-active.ts` + `compact/policy.ts:136-144` | 三层防护：单次重试 3 次 → 连续失败 3 次熔断 → 3 个工具轮内 3 次压缩的 **rapid-refill 熔断**（防止"压缩→立即满→再压缩"抖动） | 分级降级，不是一失败就终止 |
| 5 | Tool Timeout | `packages/core/src/tool/executor/timeout.ts:93-179` | 墙钟 deadline → 工具结果变为失败回灌模型 | **不停循环**，模型可自纠；deadline 可暂停（排队时间不计入，见 2.3） |
| 6 | Provider Retry Limit | `packages/adapters/src/model/retry-policy.ts:13-30` | 默认 10 次重试、指数退避（2s 起，封顶 60s）+ 抖动；**Retry-After 头优先**；失败分类器区分可重试（429/5xx/网络/流断）与不可重试（401/400/超窗） | 重试预算耗尽才冒泡为 turn 错误 |
| 7 | Output Token Limit | `packages/core/src/runtime/methods/turn-output-token-continuation.ts:12-56` | finishReason=`length` 时自动注入"直接续写"指令，最多 3 次 | 耗尽才终止 |
| 8 | 明确 Tool Turn Control | `packages/core/src/tool/executor/turn-control.ts` | 工具结果可携带 `turnControl: { stopTurnAfterResult: true }`，由**业务语义**（终态提交、上限命中）声明"本结果应终止 turn" | 与次数无关，是显式的产品级停止信号 |
| 9 | 模型自然结束 | `packages/core/src/runtime/methods/turn-stop.ts:157-237` | 无工具调用即收敛；收敛前先给 Stop hook / inline guide 一次续跑机会 | 主路径 |

**契约层预留了 `error_max_turns` / `error_max_tool_calls`（`packages/core/src/agent/turn-state.ts:153-156`）但全仓没有任何产生点**——"次数硬停"是被刻意移除的，不是没来得及做。

### 2.2 软性守卫：防死循环不靠次数靠"提醒"

去掉硬上限后，ZCode 用注入提醒（而非阻断）让模型自己收束（`packages/core/src/runtime/helpers/model-anomaly.ts` + `runtime/methods/turn-tool-warnings.ts`）：

- **重复调用检测**：对 `toolName + 稳定序列化(参数)` 做签名，连续 ≥3 次相同签名 → 向上下文注入 system-reminder（"你已用相同参数调用 X 三次，请换一个下一步"），并发 `ModelAnomalyWarning` 事件供 UI 展示；每轮最多注入 3 条；
- **总量预算提醒**：工具调用总数达到阈值 → 注入"Do not keep calling tools reflexively"提醒；
- **只警告不阻断**，新用户输入会重置 streak（新语境即新开始）。

### 2.3 其他值得抄的机制

- **可暂停的 Tool Deadline**（`executor/timeout.ts:19-91`）：工具内部发起的模型请求在准入队列排队时暂停 deadline——"超时守的是 provider 挂了，不是我们自己的队列长"，避免限流被放大成工具超时风暴；
- **Provider usage 锚定计量**（`compact/manual.ts:102-138` + `runtime/methods/compact.ts:313-340`）：以上一条 assistant 的真实 usage 为基线、本地只估算增量，比纯字符估算准得多；
- **Microcompact**（`compact/microcompact.ts`）：token 压力下先做局部清理——旧工具结果替换为 `"[Old tool result content cleared]"` 占位（保留最近 5 组），节省 <256 token 则放弃；这是"全量压缩"之前的第一道减压阀；
- **错误回灌自纠**：一切工具失败（超时/权限拒绝/未知工具/schema 校验失败）都序列化为 tool result 喂回模型，不抛断循环；
- **TurnMachine 双层**：控制流用简单返回值（continue/break），声明式状态机只做不变量校验与可观测对账。

---

## 3. 现状盘点：本项目循环与 ZCode 的差距

### 3.1 现有循环结构

核心循环 `executeReactLoop`（`service/react/engine.go:71-172`），每轮：持久化步号 → `maybeCompactContext` → 组装工具 → `callModelRound`（流式，`service/react/model_failover.go:192`）→ 持久化 assistant → **无工具调用即结束** → `checkTokenBudget`（仅子代理）→ `executeToolCalls`（串行）→ 回灌进入下一轮。

### 3.2 差距对照表

| 终止/治理维度 | ZCode | 本项目现状 | 差距 |
|---|---|---|---|
| 循环终止主机制 | 九大明确边界，无次数上限 | `maxSteps` 次数硬上限（默认 8，线上 500），耗尽即 error | ❌ 机制性差距 |
| 步数耗尽结局 | ——（无此概念） | `ErrorReactRunFailed("exceed maxSteps")`，无软着陆 | ❌ |
| 死循环防护 | 重复签名检测 + 提醒注入 | 无 | ❌ |
| run 级资源边界 | token 预算、时间边界（abort 贯穿） | 外层 run 无 token 预算（仅子代理有，`engine.go:154`）、无 wall-clock 超时 | ❌ |
| 用户取消 | 不是错误，快照持久化 | 已有 `cancelled` 事件与 partial 持久化（`engine.go:117-122`） | ✅ 基本具备 |
| 权限拒绝 | 错误回灌模型，循环继续 | 已有：确认拒绝回填"用户拒绝"工具结果（`tool_confirm.go`） | ✅ 基本具备 |
| 工具超时 | 可暂停 deadline，失败回灌 | 已有超时且回灌；但默认值硬编码分散（HTTP 10s `service/tool/executor.go:106`、MCP 60s `service/tool/runtime.go:53`） | ⚠️ 可用，待统一 |
| Provider 重试 | 10 次指数退避 + Retry-After + 失败分类 | **无**。非 200 直接失败（`api/llm/claude.go:689`），互备是唯一容错且线上关闭（`custom.yaml` `mutual_failover: false`） | ❌ 最大风险点 |
| 流中断恢复 | 流恢复锚点 + 10 次重试 | 流 idle 超时可触发互备（`engine.go:403-424`），互备关闭即无恢复 | ❌ |
| 输出 token 截断 | 自动续写最多 3 次 | Claude 侧 `MaxTokens` 硬编码 4096（`api/llm/claude.go:604-607`），截断即截断 | ❌ |
| Context 压缩 | 策略化 compact + microcompact + 双熔断 + usage 锚定 | 有单级 compact（`runtime_support.go:240`，token_trigger 170k → target 90k），但：压缩器输入被截到 `SummaryLimit*2` rune（`runtime_support.go:491`）信息静默丢失；无熔断；无 microcompact；全链路用 `token_trigger` 冒充 `MaxContextTokens`，模型目录 `max_context_tokens` 未参与 | ⚠️ 有骨架，缺治理 |
| Prompt cache | 投影后统一设 cache-control | 每轮向消息尾部追加合成 user 提醒（todo/异步任务，`engine.go:245-264`），**破坏 provider 缓存前缀** | ❌ |
| 工具并发 | concurrentSafe 声明 + parallelGroups | 全串行（仅 delegate_agent 可并行） | ⚠️ 性能项 |
| MCP stdio 隔离 | —— | 单次 tools/call 超时杀掉整个子进程（`service/mcpclient/client.go:347-349`） | ⚠️ |
| 交互等待 | broker 超时 → PermissionTimeout | ask_question / client tool / 工具确认**无限期阻塞**（`client_hub.go:206`） | ❌ |

---

## 4. Phase 1（P0）：终止边界体系

目标：把循环的"主终止机制"从次数硬上限替换为明确边界，同时保留 maxSteps 作为防御性最后闸门（防实现 bug 导致的失控，而非业务手段）。

### 4.1 软着陆收尾轮（替代"耗尽即报错"）

**现状**：`engine.go:100` for 循环自然退出后直接 `return components.ErrorReactRunFailed.Sprintf("exceed maxSteps")`（`engine.go:171`）。

**方案**：当步数或任一资源预算即将耗尽时，不直接终止，先进入"收尾模式"：

1. 定义软着陆触发条件（任一命中）：
   - `step == maxSteps - softLandingSteps`（softLandingSteps 默认 1）；
   - 外层 token 预算达到 90%（4.3 节）；
   - run wall-clock 达到 90%（4.4 节）；
2. 触发后向 messages 尾部注入一条**仅本轮生效、不落库**的合成 user 消息（复用现有 `contextMessages` 机制，`engine.go:245-264`），文案：
   > `<system-reminder>本 run 的执行预算即将耗尽（原因：步数/token/时间）。请立即停止开启新的多步工作，基于已有结果给出最终回答；未完成事项请列入"待办"并明确说明。</system-reminder>`
3. 收尾模式最多持续 `softLandingSteps`（默认 2）轮：若模型仍发工具调用，则只允许执行**只读/低风险**工具（复用工具风险分类，`tool_confirm.go:41-51` 的正则思路），写类工具直接返回错误结果"预算耗尽，仅允许只读操作"回灌；
4. 收尾窗口耗尽仍无最终回答 → 才落 `finish_exhausted` 终态（新增 TerminationReason，区别于 error），把已有内容 + 未完成事项作为最终输出，**run 状态为完成（带警告）而非失败**。

**改动点**：`service/react/engine.go`（循环尾部 + 终态）、`service/react/runtime_state.go`（软着陆状态位）、`controllers/ws.go` / 前端事件（可选新增 `soft_landing` 事件）。

### 4.2 软性异常守卫（重复调用检测）

对照 ZCode model-anomaly 机制，在 `executeToolCalls`（`tool_dispatch.go:31`）返回后做签名统计：

- 签名 = `toolName + 参数排序序列化后的 SHA-256 前 16 位`（参数 JSON key 排序后哈希，避免 map 序列化不稳定）；
- 连续相同签名 streak ≥ `anomaly_repeat_threshold`（默认 3）→ 下一轮模型请求前注入合成提醒："你已用完全相同的参数连续调用 X 三次，结果相同。请改变策略或直接向用户求助"；同时在 run 事件流发 `anomaly_warning`（前端可展示）；
- 单轮注入上限 3 条（防提醒本身刷屏）；
- **只提醒不阻断**。用户输入新消息后 streak 清零；
- 观测指标：`react_tool_repeat_streak` Prometheus 直方图，为后续调阈值提供数据。

**改动点**：新增 `service/react/tool_anomaly.go`；`engine.go` 组装消息处挂接。

### 4.3 外层 run token 预算（推广现有子代理机制）

**现状**：`checkTokenBudget`（`engine.go:207`）仅对委派子 run 生效（`tokenBudget = policy.MaxTokensPerRun`，`delegate.go:246`），外层 run 无预算。

**方案**：

- 配置 `react.loop.budget_tokens_per_run`（默认 0 = 不限，保持现网行为）与请求级 `payload.TokenBudget` 覆盖；
- 复用现有递归口径计量（本 run 输入+输出+孙代理 delegated_*，`runtime_state.go` 的原子计数）；
- 达到 90% → 触发 4.1 软着陆；达到 100% → 终止并落 `finish_budget_exhausted` 终态（同 4.1 第 4 步语义）。

### 4.4 run 级 wall-clock 超时

**现状**：`runCtx` 只有 `WithCancelCause`（`runtime.go:219`），全包仅压缩 LLM 调用有 `WithTimeout`。

**方案**：

- 配置 `react.loop.run_timeout_sec`（默认 0 = 不限；建议现网先设 1800 观测）；
- `runCtx = context.WithTimeoutCause(ctx, d, ErrReactRunTimeout)`；`ErrReactRunTimeout` 走取消同款优雅收敛（保存 partial、`cancelled`→ 复用现有取消收口路径，事件类型发 `timeout`）；
- 工具执行与模型调用已透传 ctx，无需逐点改造。

### 4.5 输出 token 上限治理

- 把 `api/llm/claude.go:604-607` 硬编码的 4096 改为模型目录配置（`ModelVersionLimit` 增加 `max_output_tokens`，未配置时回退现有值），每模型可调；
- 新增 finishReason=`max_tokens` 续写机制（对照 ZCode turn-output-token-continuation）：截断且**无 tool_calls** 时，注入一条不落库的 user 消息"输出被截断，请从中断处直接继续，不要重复已输出内容"，最多 `output_continuation_max`（默认 2）次；拼接两段正文后按正常最终回答收口；若截断发生在 tool_use 装配中（不完整 tool call），不做续写，按现有流错误路径处理；
- 注意：续写消息与 4.1/4.2 的合成消息同走 `contextMessages` 通道，持久化层与回放层需保持"临时消息不落库"的现有约定（`engine.go:245-264`）。

### 4.6 交互等待超时

`client_hub.go:206` 的 `waitClientMessage` 纯阻塞改为带 deadline 的等待：

- 配置 `react.loop.interaction_timeout_sec`（默认 0=不限，建议 600）；覆盖 ask_question、client tool 回包、工具确认三种等待；
- 超时后回填工具错误结果："用户未在限时内响应"回灌模型继续循环（模型可决定换路径或再次询问），并向前端发 `interaction_timeout` 事件；用户之后补答的消息按普通 user 输入进入下一轮（不认领已超时的 toolUseId）。

### 4.7 maxSteps 的最终地位

保留 for 循环形式，但语义改为：**防御性实现 bug 闸门**。默认值提升为 `defaultReactMaxSteps = 200`（或由 run_timeout/预算实际承担边界），配置文档标注"不应作为业务控制手段"；触发即视为异常，发 `anomaly_warning` + error 堆栈日志（因为正常路径应先被软着陆拦截）。

---

## 5. Phase 2（P0）：模型调用韧性

目标：单模型 API 抖动不再导致整个 run 失败。当前是**最大风险点**：非 200 直接失败，唯一容错互备且线上关闭。

### 5.1 失败分类器

新增 `service/llmerr`（或 `api/llm/failure.go`），对所有模型调用错误打标：

| 类别 | 判定 | 处理 |
|---|---|---|
| Retryable-Transport | 网络错误、连接重置、EOF、TLS 握手失败 | 重试 |
| Retryable-HTTP | 408、409、429、500、502、503、504、529 | 退避重试，429 优先 Retry-After |
| Retryable-Stream | `stream_idle_timeout`（`engine.go:424`）、SSE 中断、`upstream_eof`（`engine.go:510`） | 重试（Phase 2 先整轮重试，流级恢复留作后续） |
| Fatal-Client | 400（参数）、401/403（鉴权）、404（模型不存在）、422 | 不重试，直接进入下一候选（互备）或终止 |
| Fatal-Context | 超窗错误（provider 特定 code/文案） | 不重试，转 Phase 3 reactive compact 通道 |
| Cancelled | ctx 取消/断连 | 原样上抛，绝不重试 |

判定用**错误码 + 结构化字段**，不做错误文本模糊匹配（沿用 ZCode "不依赖错误文本做流程判断"原则；provider 特定 code 映射表集中维护）。

### 5.2 重试执行器

在 `callModelRound`（`model_failover.go:192`）内、候选模型切换**之前**加同模型重试内环：

- `react.model.retry_max_attempts`（默认 3）、`retry_base_delay_ms`（默认 1000）、`retry_max_delay_ms`（默认 30000）、jitter ±30%；
- **Retry-After 头优先**且 ≤ 5 分钟视为合理直接采用（解析 429/503 响应头，`api/llm/client.go` 需要把响应头透出）；
- 重试期间持续检查 runCtx 取消（abort-aware sleep，取消立即返回）；
- 每次重试发 `model_retry` 事件（含 attempt/delay/错误类别），前端可见"模型抖动重试中"而非长静默。

### 5.3 与互备协同

现语义"每候选每轮只试一次"改为：**先重试后切换**——同模型重试预算（5.2）耗尽且错误为 Retryable → 切下一候选；Fatal-Client 类不消耗重试预算直接切换。互备开启时每候选均享有完整重试预算；互备关闭时重试预算是唯一韧性。`retryableModelError`（`engine.go:424/438/510`）的判定收敛到 5.1 分类器，消除两套口径。

### 5.4 验收

- 注入故障测试（httptest 假 provider）：429 + Retry-After → 按头等待后成功；连续 500×3 → 切互备或失败；401 → 不重试直接失败；
- 现网观测：`react_model_retry_total{class}` 计数器、重试后成功率。

---

## 6. Phase 3（P1）：Context 治理

### 6.1 真实窗口接入与阈值推导

**现状**：全链路用 `token_trigger`(170k) 冒充 `MaxContextTokens`（`engine.go:364/396/546`），模型目录 `max_context_tokens`（`conf/config.go:363`）未参与循环。

**方案**（对照 ZCode `compact/policy.ts:67-88`）：

- `effectiveWindow = max_context_tokens - outputReserve`（outputReserve 默认取该模型 max_output_tokens，最小 8k）；
- `autoCompactThreshold = effectiveWindow - buffer`（buffer 默认 13k）——`token_trigger` 退役为该值的兼容别名，迁移期显式告警"配置的是旧语义"；
- `token_target`（压缩后目标）同样按模型推导，不再固定 90k；
- 入口预检 `checkEntryInputTokens`（`entry_check.go:27`）的拒绝阈值同步改用 autoCompactThreshold。

### 6.2 压缩器修复

- **输入截断问题**：`buildCompactSummaryPrompt` 把待压缩 JSON 截到 `SummaryLimit*2` rune（`runtime_support.go:491`）——超长历史被静默丢弃。改为：待压缩内容超预算时**分片多趟摘要**（map-reduce：每片独立摘要 → 合并摘要），或至少按"最新优先"截断并显式声明丢弃范围写入摘要 prompt（让摘要模型知道缺什么）；
- **失败熔断**：`maybeCompactContext` 失败当前静默继续（下次还会重试）。新增 `consecutiveCompactFailures` 计数，连续 ≥3 次本轮 run 跳过自动压缩（发 `compact_disabled` 事件 + 告警日志），防止每轮在坏模型上重复烧一次压缩调用；
- **rapid-refill 防抖**（对照 ZCode）：若最近 ≤3 个工具轮内已发生 ≥2 次压缩且 token 仍超阈值 → 说明工作集本身就超过压缩后预算，此时不再压缩，直接注入提醒"上下文已达硬上限，请立即总结收尾"并走 4.1 软着陆，避免"压缩→立即满→再压缩"抖动循环。

### 6.3 Microcompact（全量压缩前的第一道减压阀）

- 触发：`lastInputTokens > 0.9 × autoCompactThreshold`（早于全量压缩阈值）；
- 动作：把**较旧的 tool_result 消息**内容替换为占位 `"[旧工具结果已清理，完整内容可通过 read_tool_result 按 ref 读取]"`（本项目天然优势：完整结果本来就在 `tblLlmReactToolResult`，resultRef 引用长期有效），保留最近 `microcompact_keep_recent`（默认 5）组工具轮不动；错误结果与含用户确认语义的结果不清理；
- **持久化注意**：本项目历史每 run 从 DB 重建，占位替换不能破坏回放——方案：DB 消息不动，替换只发生在 run 内存 `state.messages` 中，并在 run 行记录 `microcompact_through`（消息 seq 游标），跨 run 恢复时按游标重放替换；效果等价于 ZCode 的历史替换，但不需要改持久化格式；
- 节省 <256 token（估算）则放弃本次 microcompact。

### 6.4 Prompt cache 前缀保护

**现状**：`contextMessages`（`engine.go:245-264`）每轮把 todo 状态、异步任务提醒作为**新的 user 消息追加到尾部**，导致每轮请求前缀都变化，Anthropic prompt caching（client 已带 beta 头，`claude.go:676-688`）几乎必然失效。

**方案**：

- 静态提醒（system 级约束、todo 提醒格式）移入 system 消息或**第一条** user 消息，内容变更时整体替换该块而非尾部追加；
- 动态提醒（异步任务完成通知）保留尾部追加（这是合法的新信息），但收敛为"仅在内容变化时追加"，避免每轮重复；
- 每轮请求记录 `cache_prefix_stable_len` 观测值（估算），验收标准：连续两轮无压缩/无新提醒时前缀完全一致。

### 6.5 Token 计量锚定

保留现有"真实 usage 为主锚点"设计（`engine.go:194` 已写回 last_input_tokens，优于 ZCode 的本地估算+锚定混合方案）；仅改进冷启动：入口估算改用 `service/token/limiter.go` 之外增加工具 schema 与历史 JSON 的结构化开销常量校准（`service/react/token_estimate.go` 已有雏形），并把冷启动估算与首个真实 usage 的偏差记录为指标，持续校准。

---

## 7. Phase 4（P1）：工具执行治理

### 7.1 统一超时配置

- 新增 `react.tool.default_timeout_ms`（默认 60000），HTTP 工具未配置 `TimeoutMs` 时用统一默认替代硬编码 10s（`service/tool/executor.go:105-107`）、MCP 替代硬编码 60s（`service/tool/runtime.go:53-56`）；工具级 > agent 级 > 全局默认，三层解析；
- 超时结果继续走现有回灌通道（保持"超时不终止循环"语义，与 ZCode 一致，本项目已具备）。

### 7.2 MCP stdio 超时隔离

**现状**：单次 tools/call 超时 `stopLocked()` 杀整个子进程（`client.go:347-349`），下次调用重新拉起+握手；慢工具会反复重启进程。

**方案**：

- stdio JSON-RPC 请求增加 per-request id 关联，超时只放弃该请求（标记该 id 的 future 为超时错误），进程保留；连续 N 次请求级超时或心跳失败才重启进程（`react.mcp.stdio_max_consecutive_timeout`，默认 3）；
- 长期可评估取消互斥锁改并发多路复用（MCP 协议本身支持批量并发请求），此项独立评审，不阻塞本 Phase。

### 7.3 并发调度（可选，性能项）

- 工具定义增加 `concurrent_safe` 声明（DB 列或注册表 meta），只读类工具（检索、inspect、read_tool_result、memory_read）默认 true；
- 同轮多个 concurrent_safe 工具并行执行（errgroup + 信号量 `react.tool.max_concurrency`，默认 4），结果仍按原索引回填；任一非 safe 工具出现则整轮退化为串行（保守策略，与 ZCode 的 parallelGroups 简化版对齐）；
- 现有 delegate_agent 并行通道不受影响。

---

## 8. Phase 5（P2）：取消与权限语义归一

- **取消终态**：现有实现已区分 `cancelled` 事件与 partial 持久化，补齐两点——(a) 断连与主动取消区分事件类型（现都是 `ErrReactClientDisconnected`/`ErrReactRunCancelled`，断连后 run 继续跑完是合理策略，需明确产品语义并文档化）；(b) run 行终态统一枚举 `completed | completed_with_warnings(软着陆/预算) | cancelled | failed | timeout`，回放页按终态渲染；
- **权限拒绝回灌增强**：拒绝结果的文案从"用户拒绝"升级为结构化提示（"用户拒绝了该操作+原因（若提供）+请改用其他方案或向用户说明"），对照 ZCode permission-flow 的 reasonSource 语义；`confirm_risky` 内置正则表（`tool_confirm.go:41-51`）评审扩容（增加 `rm -rf`、`git push --force`、`DROP` 等）；
- **turn control 信号（可选）**：为业务工具增加 `stop_run_on_success` 声明（如"提交结果""发送消息"类终态工具），工具成功即优雅收尾——对照 ZCode `withTerminalToolTurnStop`，把"产品级停止"从模型行为猜测变为工具显式声明。实现上在 `executeToolCalls` 返回后检查该标记，置 `finish(streamResult.Content, "terminal_tool")`。

---

## 9. 显式不做的事

1. **不引入任何形式的"工具调用次数硬停"**作为主机制——次数只出现在：软着陆触发（配合语义提醒）、软守卫提醒、观测指标；
2. **不实现 ZCode 的流级恢复锚点**（`streaming-recovery.ts`，从锚点重开 SSE 流）——本项目模型调用以轮为粒度，Phase 2 的整轮重试收益/成本比更高，流级恢复留待有真实需求证据后再评审；
3. **不做 ZCode 的 TurnMachine 声明式状态机**——Go 侧现有终态枚举 + 事件流已够用，双层对账的工程成本在本项目规模下不划算；
4. **不改两段式工具协议**（get_tool/execute_tool）——虽有额外一轮往返，但跨 run 恢复与指纹自愈已缓解，且改动会波及提示词与前端；
5. **不把 compact 触发点改造与记忆 V2 混做**——Phase 3 只动 `maybeCompactContext` 内部，`compact_end` 触发记忆 reflection 的现有契约（`runtime_support.go:312`）保持不变，与《长期记忆V2实施计划》的 Phase 2 extractor 互斥约定继续生效。

---

## 10. 实施顺序、配置汇总与验收

### 10.1 实施顺序与依赖

| 阶段 | 内容 | 依赖 | 预估 |
|---|---|---|---|
| M1 | Phase 2（重试+分类器）+ 4.5 输出 token 配置化 | 无 | 小 |
| M2 | Phase 1 全部（软着陆/软守卫/预算/超时/交互超时） | M1（预算耗尽时需要重试不误判） | 中 |
| M3 | Phase 3（窗口接入/压缩修复/熔断/microcompact/cache 保护） | M2（软着陆被 rapid-refill 复用） | 中 |
| M4 | Phase 4（超时统一/stdio 隔离/并发） | 无强依赖，可与 M3 并行 | 小-中 |
| M5 | Phase 5（终态归一/turn control） | M2 | 小 |

每个 M 独立灰度：新配置全部默认关闭或取现网等效值，逐项开启。

### 10.2 新增配置项汇总（`conf/config.go` + `custom.yaml`）

```yaml
react:
  loop:
    soft_landing_steps: 2          # 软着陆窗口轮数
    budget_tokens_per_run: 0       # 0=不限
    run_timeout_sec: 0             # 0=不限，建议灰度 1800
    interaction_timeout_sec: 0     # 0=不限，建议 600
    output_continuation_max: 2
    anomaly_repeat_threshold: 3
  model:
    retry_max_attempts: 3
    retry_base_delay_ms: 1000
    retry_max_delay_ms: 30000
  tool:
    default_timeout_ms: 60000
    max_concurrency: 4
  mcp:
    stdio_max_consecutive_timeout: 3
  context:
    output_reserve_tokens: 32768   # 派生 autoCompactThreshold
    microcompact_keep_recent: 5
    compact_failure_breaker: 3
```

### 10.3 验收标准

1. **不误杀**：一个 60+ 工具调用的正常长任务（脚本化回放构造）在不调 maxSteps 的情况下完成；
2. **不死烧**：注入"永远返回相同结果"的假工具，模型在第 3 次重复后收到提醒，且 run 在软着陆/预算边界内以 `completed_with_warnings` 结束而非 500 轮烧穿；
3. **抗抖动**：假 provider 按 429/500/EOF 序列抖动，run 成功率与重试事件符合 5.4 验收；
4. **压缩可靠**：构造 300k 字符历史，压缩摘要包含早期关键信息（分片摘要生效）；连续压缩失败 3 次后停止重试并有告警；
5. **缓存有效**：连续两轮无变化请求的 cache 前缀逐字节一致（日志断言）；
6. **可观测**：新增指标 `react_soft_landing_total`、`react_anomaly_warning_total`、`react_model_retry_total{class}`、`react_compact_failure_streak`、`react_cache_prefix_stable_len` 全部可查询。

### 10.4 风险与回滚

- 软着陆注入的合成消息可能干扰某些弱模型 → 开关 `soft_landing_steps=0` 即回到现行为；
- 重试放大故障期间的请求量 → retry_max_attempts 保持 3（低于 ZCode 的 10），429 场景 Retry-After 优先 + 互备分流；
- microcompact 占位替换与回放不一致 → 游标方案保证 DB 只读不改，回滚 = 关闭开关后游标失效即恢复全量历史；
- 所有 Phase 均不改变持久化 schema（除 run 终态枚举扩列值），无数据迁移。

---

## 附：关键参照文件索引

**ZCode 侧**（相对 `ZCode/apps/zcode-cli`）：
- 设计宪法：`AGENTS.md:11`
- 主循环：`packages/core/src/runtime/methods/turn-loop.ts`、`turn-model-step.ts`、`turn-tools.ts`、`turn-stop.ts`
- 终止边界：`runtime/helpers/turn-errors.ts`、`tool/executor/{permission-flow,timeout,turn-control}.ts`、`runtime/methods/turn-output-token-continuation.ts`
- 重试：`packages/adapters/src/model/{retry-policy,runner-retry,failure-classifier}.ts`
- Compact：`packages/core/src/compact/{policy,microcompact,prompt}.ts`、`runtime/methods/compact-active.ts`
- 软守卫：`runtime/helpers/model-anomaly.ts`

**本项目侧**：
- 循环：`service/react/engine.go:71-172`、`runtime.go:196`、`runtime_state.go:84`
- 压缩：`service/react/runtime_support.go:240-507`
- 工具：`service/react/tool_dispatch.go`、`meta_tools.go`、`business_tool.go`、`service/tool/executor.go`、`service/tool/runtime.go`
- 模型：`api/llm/client.go`、`api/llm/claude.go`、`service/react/model_failover.go`
- 配置：`conf/config.go:30-31`、`conf/mount/custom.yaml`
