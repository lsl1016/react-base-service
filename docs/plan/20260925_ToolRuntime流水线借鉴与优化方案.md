# Tool Runtime 流水线借鉴与优化方案 —— 对照 ZCode 的调度、安全、闭合与恢复语义

| | |
|---|---|
| 日期 | 2026-09-25 |
| 状态 | Phase 1/2 已实施（2026-09-25，见 `docs/changelog/20260925_v1.0_修复工具结果闭合不变量与tool_call去重.md`）；Phase 3-5 待评审 |
| 参照系 | 上级目录 `ZCode` 仓库（`apps/zcode-cli/packages/core`）的 Tool Runtime 实现 |
| 适用范围 | `react-base-service` ReAct Runtime（`service/react`）+ 工具层（`service/tool`） |
| 关联文档 | `docs/plan/20260925_AgentLoop终止边界与循环治理优化方案.md`（其 Phase 4"工具执行治理"由本方案展开并取代）；`docs/plan/20260925_Session与Steering机制借鉴方案.md`（Session/Steering/命令队列/Subagent 层，其 S1 依赖本方案 Phase 1 的闭合修复） |

---

## 0. TL;DR

对 ZCode Tool Runtime（`packages/core/src/tool/` + `runtime/`）与本项目（`service/react` + `service/tool`）做了代码级对照，结论：

1. **流水线骨架已经同构**。assistant `tool_use` 先落库（`engine.go:171`）→ 执行工具 → `tool_result` 统一落库（`engine.go:213`）→ 下一轮模型请求——这个顺序与 ZCode 完全一致；确认门（`tool_confirm.go`）、大结果外置 resultRef（≈ ZCode resultBudget artifact 策略）、取消/中断回填、软着陆、模型重试只发生在工具执行之前——这些也均已对齐 ZCode 对应设计。
2. **差距集中在四点**：闭合不变量有真实缺口（**P0 bug**）、无并行调度、无 hook 抽象、无调用去重。
3. **一个重要的 reality check**：ZCode ToolScheduler 的依赖图机械（Kahn 拓扑排序 + 环检测 + 层级分组，`tool/scheduler.ts` 共 245 行）真实存在，**但生产接线给每个调用的 `dependsOn` 全是空数组**（`runtime/methods/tools.ts:49`）——每天在干活的是 `canRunParallel` 标志树 + `maxConcurrency=10` 分批。因此本方案的调度改造**从元数据标志位起步，不做依赖图**。

分 5 个 Phase：

1. **Phase 1（P0）**：tool_result 闭合不变量修复 + 中断文案区分"未执行/状态未知"；
2. **Phase 2（P0）**：tool_call id 三点去重；
3. **Phase 3（P1）**：Tool 元数据字段 + 通用分组并行调度（推广 delegate 并行模板）；
4. **Phase 4（P1）**：执行边界再校验固化（defense-in-depth 第二层）；
5. **Phase 5（P1）**：PreToolUse/PostToolUse hook 链（现有内联检查迁入，行为不变）。

核心取舍：**每个 assistant tool_use 必须有配对 tool_result 是结构保证，不靠各错误路径自觉**；并行化先做"标志位 + 并发上限"，依赖图等真实需求出现再启用（ZCode 自己也没用上）。

---

## 1. 逐段对照：ZCode 流水线 vs 本项目现状

ZCode 中一次模型 tool_use 的完整流水线（`packages/core/src/` 内）：

```
模型响应解析提取 tool calls (turn-model-step.ts)
  → assistant tool_use 先进 history (commitAssistantToTurnRequest)
  → 调度: 元数据 → 依赖图(未接线) → 并行分组 (tool/scheduler.ts)
  → 分批执行: 组间串行、组内 Promise.all 上限 10 (tool/executor/batch-runner.ts)
  → 单调用流水线: registry → abort → 归一化+schema 校验 → validateInput
      → resolveInput → PreToolUse hooks → 权限流(permission-flow.ts)
      → handler(带超时) → 输出校验 → 结果序列化/预算
      → PostToolUse hooks → 事件/遥测 (tool/executor/call-runner.ts)
  → tool part 持久化 + tool_result 提交进 history (turn-tools.ts 提交循环)
  → 恢复锚点/账本事件 → 下一模型步
```

对照本项目：

| ZCode 流水线阶段 | 本项目现状 | 差距评级 |
|---|---|---|
| assistant tool_use 先进 history | ✅ `service/react/engine.go:171`，同序 | 无 |
| ToolScheduler（元数据→分组→并发） | ❌ 全串行，仅 `delegate_agent` 并行（`tool_dispatch.go:31-86`） | **P1 重点（Phase 3）** |
| PreToolUse/PostToolUse Hook | ❌ 检查全部硬编码内联 | P1（Phase 5） |
| Permission/确认门 | ✅ `tool_confirm.go` + clientHub 等待 + 超时拒绝 + 中断回填 | 基本齐 |
| 执行边界再校验（第二层安全） | ⚠️ 有雏形（profile/软着陆/schema 再校验），未成体系 | P1（Phase 4） |
| 结果预算/外置 | ✅ resultRef + 预览（`runtime_support.go:53-146`）≈ ZCode artifact 策略 | 无 |
| tool_result 无条件闭合 | ⚠️ 取消/预算路径有回填，**硬错误路径漏**（见 §2） | **P0 bug（Phase 1）** |
| tool_call id 去重 | ❌ 无（`boundaries.go:235` 重复签名只提醒不拦截） | **P0（Phase 2）** |
| 流式工具执行 + 恢复账本 | ❌ tool call 攒到 Done 才交付 | P2（§6，当前架构下风险不存在） |

已经对齐、**不需要重建**的部分（避免重复投资）：闭合的取消/预算路径（`closeInterruptedToolUse`、`persistClientToolInterruptedResults`、`persistToolConfirmInterrupted`、`persistBudgetExhaustedToolResults`）、`assistant_partial` 不进恢复上下文（≈ ZCode `StreamRecoveryDiscarded` 标记）、压缩切点对齐 turn 边界、模型重试/互备全部前置在工具执行之前（`model_failover.go:188-305`）。

---

## 2. Phase 1（P0）：tool_result 闭合不变量修复

> **已实施（2026-09-25）**：三道防线落地——中断登记制（`tool_closure.go` + 四个中断路径改 `record*`）、引擎单点落库（`engine.go` 错误路径先落库再返回、`checkTokenBudget` 回填）、重建孤儿回填（`reactMessagesToChatMessagesWithRefs` 接入 `backfillOrphanToolResults`）；中断文案区分 not_executed / unknown_execution_state。实施细节与验证见 `docs/changelog/20260925_v1.0_修复工具结果闭合不变量与tool_call去重.md`（开发库存量孤儿 19 个，重建回填后归 0）。下文保留原始设计分析。

### 2.1 ZCode 的铁律

`packages/core/src/runtime/methods/turn-tools.ts:142` 注释原文：

> assistant tool_use 已经进入 history 后，Stop 不能在 tool result 创建前直接抛出。继续把 aborted signal 交给 executor，由现有取消路径为每个 tool call 生成 ToolCancelled result，再由 turn loop 感知 abort。

三重保证：

1. 结果提交循环对**每个** result 无条件提交 `tool_result`（`turn-tools.ts:359`）；
2. 同一调度内某个结果要求停轮后，后续组全部调用回填合成 `ToolCancelled` 结果（`executor/batch-runner.ts:92-127`，文案 `"Tool cancelled because a previous tool result requested a turn stop."`）；
3. 冷恢复时 hydrator 对 pending/running part 回填 `"[Tool execution was interrupted before resume]"`（`agent/session-history-hydrator.ts:45,188`），保证恢复后的历史必然配对。

### 2.2 本项目的缺口（已核实）

- `service/react/engine.go:205-208`：`executeToolCalls` 返回任何错误就 `return err`，**本轮全部 tool_result 不落库**——而 assistant `tool_use` 已在 `engine.go:171` 落库。真实触发源：
  - `tool_dispatch.go:219-221`：`storeResultRef` DB 失败；
  - `tool_confirm.go`：确认等待错误；
  - 串行路径中间某个内部工具的 DB 错误。
- `service/react/tool_dispatch.go:88-100`：串行路径出错即 `return results, err`，出错调用之后的 results 是**零值**（连 ToolUseID 都没有）。
- 后果：run 出错结束后，同 session 下次 `createReactRunContext`（`runtime.go:300`）重建上下文时，`reactMessageToChatMessage`（`engine.go:789`）只过滤 `assistant_partial`，带 tool_use 的 assistant 消息原样进入 provider 请求——**没有配对 tool_result，严格 Provider（Anthropic 系）直接拒绝请求**。

### 2.3 修法：结构保证 + 断言防线

1. `executeToolCalls` / `executeToolCallsSerial` 改为**永远返回完整 results**：错误调用之后/未执行的调用统一回填合成错误结果（带 ToolUseID、`IsError: true`）；
2. `engine.go` 在任何错误 return 之前先走 `persistToolResultMessage`（错误本身仍上抛，run 终态语义不变）；
3. 加一条廉价防线：构建 provider 请求前断言每个 tool_use 有配对 result，发现孤儿就回填（等价 ZCode hydrator 角色）。

### 2.4 中断文案区分（ZCode 语义，随本 Phase 一起做）

ZCode 合成结果严格区分两类（`runtime/methods/streaming-tool-synthetic-result.ts:23-52`）：

- **not_executed**："Tool execution was interrupted … before this tool was executed. Treat this tool call as failed and do not retry blindly." —— 模型可放心重试；
- **unknown_execution_state**："… before a result was committed. Side effects may be unknown; inspect current state before retrying." —— 必须先核实状态再决定是否重试。

本项目 `closeInterruptedToolUse` 目前统一回填；对会产生副作用的工具（非 GET 的 HTTP/MCP、写类内部工具）应回填 unknown 语义文案，只读工具回填 not_executed 语义。

**验收**：失败测试先行——模拟 `storeResultRef` 失败与串行中断，断言本轮所有 tool_use 均有落库的 tool_result；恢复场景重建的上下文通过配对断言。

---

## 3. Phase 2（P0）：tool_call id 去重

> **已实施（2026-09-25）**：两层去重落地——`collectLLMStreamWithEmitter` 收集阶段按非空 id 去重，`executeToolCalls` 入口对同轮重复 id 回灌拒绝结果不执行（第三层由收口补全结构自然获得）。下文保留原始设计分析。

ZCode 三层去重：

1. adapter 收流时按 id 去重（`runtime/methods/model.ts:378-421`，注释："runtime 按 id 去重，避免同一次响应内重复执行"）；
2. streaming coordinator 内存 map（`streaming-tool-coordinator.ts:73-101`）；
3. 执行前按 id 过滤已执行结果（`turn-tools.ts:67-69,149-151`）。

本项目零去重：模型/adapter 异常重复投递同 id 调用会重复执行，写类工具即重复副作用。

**修法（两点即可，第三层随 Phase 1 的 results 结构自然获得）**：

1. `collectLLMStreamWithEmitter`（`engine.go:418`）收集 `chunk.ToolCalls` 时按 ID 去重；
2. `executeToolCalls` 入口建本轮 seen-id map，重复 ID 直接回填错误结果（"duplicate tool_call id"）不执行。

**验收**：单测注入重复 id 的 ToolCalls，断言只执行一次。

---

## 4. Phase 3（P1）：Tool 元数据 + 通用分组并行调度

### 4.1 ZCode ToolScheduler 精读结论

`packages/core/src/tool/scheduler.ts`（245 行）关键事实：

- **元数据**（`ToolMetadata`，`tool/types.ts:65-94`）：`readOnly / destructive / concurrentSafe / sideEffectScope("none"|"workspace"|"git"|"network"|"system"|"session"|"userInteraction") / riskLevel / needsApproval / providerVisible / stopTurnOnSuccess` 等。实例：Read=readOnly+concurrentSafe+none；Edit=非并行+workspace；Bash=非并行+system；Agent=readOnly 但 `concurrentSafe:true` 显式放行。
- **并行决策树**（`scheduler.ts:85-103`）：`destructive → false`；`concurrentSafe` 显式声明则按声明；`readOnly → true`；`sideEffectScope === "none" → true`；其余 false。默认只读集合 `READ_ONLY_TOOLS = {Read, Glob, Grep, WebSearch, WebFetch, TodoRead, TodoWrite, AskUserQuestion, Skill}`。
- **依赖图**：Kahn 拓扑排序 + 环检测 + 层级分组（`scheduler.ts:105-202`）**存在但生产未接线**——`runtime/methods/tools.ts:49` 对每个调用传 `dependsOn: []`。批量语义实际是：并行安全的攒一批（上限 `DEFAULT_MAX_CONCURRENCY = 10`），不安全的独占一批，批间串行。
- **批间失败语义**：不 fail-fast（旧的"失败跳过后续组"策略已被注释废弃，`batch-runner.ts:130-156`，因为它会错误截断子代理链）；仅 `stopTurnAfterResult` 触发剩余调用回填 ToolCancelled。执行器永不 reject 整批——每个调用兜底为失败 result。

### 4.2 本项目现状

全串行（`tool_dispatch.go:20-30` 注释明确该语义）；唯一并行是 `delegate_agent`（`:51-80`），其实现（goroutine + semaphore + **原索引回填**）正是 ZCode batch-runner 的语义，是现成模板。

### 4.3 改造步骤

**第一步：Tool 元数据字段（行为等价上线）。** 工具是 DB 行（`models/llm/tool.go:16-34`），模型只见 `Name/Description/Parameters`。在 `Tool.Config` JSON（或加列）新增四个字段：`readOnly / destructive / concurrentSafe / sideEffectScope`（枚举 none/workspace/network/system/session/userInteraction，照抄 ZCode）。取值规则：

- HTTP 工具按 method 粗分：GET=readOnly+network；非 GET=network、非并行；
- MCP 工具映射 `annotations.readOnlyHint/destructiveHint/idempotentHint`（ZCode `core/src/mcp/index.ts:102` 同款）；
- 内部元工具硬编码声明（list/get 类=readOnly）；
- **默认值全部取"不并行"**，上线即与现状完全等价，随后灰度放开只读工具。

**第二步：通用分组调度。** 重构 `executeToolCalls` 为三步：按决策树分组（组内并行安全、上限可配默认 10）→ 组内 errgroup 并发、组间串行 → results 按原索引回填（复用 delegate 模板）。对齐 ZCode 批间语义：不 fail-fast（失败调用回填错误 result，剩余调用照跑）；与软着陆联动——某个结果触发收尾后，本轮剩余调用立即回填收尾引导（对齐 `stopTurnAfterResult`，比现在等下一轮拦截更及时）。

**第三步（暂缓）：显式依赖 `dependsOn`。** 接口预留字段，等真实出现"必须先 A 后 B"的工具语义再启用。

**验收**：元数据默认值上线后 e2e 行为与串行等价（结果顺序、副作用顺序不变）；放开只读并行后，同轮多个只读 HTTP 工具并发执行、results 仍按原索引回填；并发上限单测。

---

## 5. Phase 4（P1）：执行边界再校验（defense-in-depth 第二层）

### 5.1 ZCode 两层模型

- **第一层 provider-visible**：注册门控（子会话不注册 cron/offpeak、Explore 白名单 `EXPLORE_AGENT_ALLOWED_TOOLS`）+ **每个 provider 请求前重算** denylist（`turn-loop.ts:109-118`），turn 类型检测三重冗余（显式 ID、queryId 前缀、denylist 兜底）——理由是 retry/resume 可能丢元数据。
- **第二层 execution boundary**：即使模型幻觉调用不可见工具，handler 内再校验（`cron.ts:39-58` `assertNotAutomationTurn` 抛 `PermissionDenied`）。可见性过滤只挡"模型看见"，执行边界挡"真的执行"。

### 5.2 本项目现状与缺口

第一层已齐且更保守（`buildToolDefinitions` 只发元工具、`visibility.go` 白名单、`get_tool` 按需激活）。第二层有雏形但分散：`profile.allowsInternalTool`（`tool_dispatch.go:150`）、软着陆拦截（`:144`）、`execute_tool` schema 再校验（`business_tool.go:159-189`）——能挡能报，但硬编码内联、无统一出口。

补三个具体点：

1. **分发时统一再校验**：`executeToolCall` 入口过一遍 allowlist/denylist（含 run 类型）；`executeLoadedBusinessTool` 复核该工具当前是否仍在白名单（现在只有"定义变化"提示 `prevToolDefFingerprint`，`tool_dispatch.go:158`）；
2. **递归派生防护**：ZCode 明确禁止子代理再派发（`isSubagentDispatchToolName`）且子会话不注册 cron/offpeak；确认本项目子 run 的 `profile.AllowSubagent` 默认关闭或限深；
3. **per-request 重算**：恢复/续跑路径重新应用 turn 类型限制，不信任入口快照。

**验收**：单测覆盖"模型幻觉调用未激活/已下线/非白名单工具"均得到错误 tool_result 而非执行；子 run 内 `delegate_agent` 被拒或限深。

---

## 6. Phase 5（P1）：PreToolUse / PostToolUse hook 链

### 6.1 ZCode 语义

- **PreToolUse**（`executor/hook-flow.ts:16`）：输入归一化后、权限前运行；可 deny（成为带 reason 的错误 tool_result）、可 `updatedInput` 改写（改写后重新归一化+校验）、可注入 `additionalContext`（追加到 tool result 文本）、可把 allow 升级为 ask；
- **PostToolUse**（`hook-flow.ts:125`）：结果序列化后运行；不能阻断，`additionalContext` 追加进模型内容；
- **PermissionRequest** hook 与 UI 确认赛跑（`permission-responder-race.ts`），先到先得。

### 6.2 Go 落地

本项目无任何 hook，前置检查写死在 `executeToolCall`/`executeServerTool`。定义两条链：

```go
type PreToolUseResult struct {
    Deny             string   // 非空 = 拒绝，作为错误 tool_result 回灌
    RewrittenInput   json.RawMessage // 改写后需重新过 schema 校验
    AdditionalContext string
    UpgradeToConfirm bool
}
type PreToolUse  func(ctx context.Context, in *ToolCallInput) PreToolUseResult
type PostToolUse func(ctx context.Context, out *ToolCallResult) // additionalContext 追加
```

把现有内联检查**原样迁成第一批 hook**（纯重构、行为不变）：软着陆拦截、profile 校验、危险确认门、schema 再校验 → PreToolUse；异常记录、async submit 快照、（未来的）审计/记忆抽取/graph memory → PostToolUse。确认门本身保留在 permission 位置（对应 ZCode permission-flow 的 ask 分支），hook 的 `UpgradeToConfirm` 可升级它。

排在 Phase 3 之后做：这是纯粹的可扩展性投资，调度收益更直接。

---

## 7. P2：流式工具执行与恢复 —— 学语义，不抄机制

**当前架构下最危险场景不存在**：adapter 把 tool call 攒到流结束的 Done chunk 才交付（`api/llm/claude.go:752-876`、`gpt.go`、`minimax.go` 同构），且 `callModelRound` 的 retry/failover 全部发生在本轮工具执行之前——"工具已执行、模型流失败、重试导致重复执行"（ZCode 用 streaming-tool-ledger + RecoveryAnchor + ToolResultCommitted 防的场景）不会发生。

若未来做流式执行（只读工具提前跑、省一轮等待），ZCode 的安全约束必须整套装走：

- 只对 `readOnly && concurrentSafe && !destructive && !needsApproval && sideEffectScope === "none"` 的工具 during-stream 执行（默认模式 `"readOnly"`，`streaming-tool-coordinator.ts:39,339-362`）；
- 流式执行失败**吞掉**、回退到端到流执行（`:89-99`）；
- 恢复按 tool id 去重已执行结果（`turn-tools.ts:67-69,149-151`）；
- 恢复重试有预算上限（`STREAM_RECOVERY_MAX_RETRIES = 10`）、在途工具有 250ms drain 窗口（`STREAMING_TOOL_CANCEL_DRAIN_TIMEOUT_MS`）。

不引入 StreamingToolLedger/RecoveryAnchor 事件账本：ZCode 引入它是因为 parts/事件分属内存与宿主存储两层；本项目 MySQL 消息表已是单一持久权威，双写只增加复杂度。

---

## 8. 不建议照搬清单

| 项 | 理由 |
|---|---|
| 显式依赖图 / DAG 拓扑排序 | ZCode 生产接线 `dependsOn` 全空，真正起作用的是标志树 + 并发上限；ROI 低，接口预留即可 |
| StreamingToolLedger / RecoveryAnchor 事件流 | MySQL 单一持久权威已覆盖其职责（`assistant_partial` 过滤 ≈ `StreamRecoveryDiscarded` 标记） |
| batch_start/batch_complete 批事件流 | per-tool `tool_use_start/end` 粒度对前端够用 |
| 完整结果序列化器（head/tail 截断策略） | resultRef 外置 + 预览已等价其 artifact 策略 |

---

## 9. 落地顺序与依赖关系

```
Phase 1 闭合修复 ──┬──> Phase 2 id 去重 ──> Phase 3 元数据+分组并行 ──> Phase 4 边界再校验 ──> Phase 5 hook 链
（含中断文案区分）  （依赖 Phase1 的 results 结构）  （元数据默认等价上线，灰度放开）
```

| Phase | 优先级 | 触碰文件 | 风险 |
|---|---|---|---|
| 1 闭合不变量（✅ 已实施） | P0 | `service/react/engine.go`、`tool_dispatch.go`、`runtime_support.go`、`tool_closure.go`（新增） | 低（补齐回填路径） |
| 2 id 去重（✅ 已实施） | P0 | `engine.go`、`tool_dispatch.go`、`tool_closure.go` | 低 |
| 3 元数据+调度 | P1 | `models/llm/tool.go`、`service/tool/*`、`tool_dispatch.go` | 中（默认值等价上线控风险） |
| 4 边界再校验 | P1 | `tool_dispatch.go`、`business_tool.go` | 低 |
| 5 hook 链 | P1 | `tool_dispatch.go`、新增 `hooks.go` | 中（纯重构，靠行为不变测试兜底） |

Phase 1 建议先写失败测试（模拟 `storeResultRef` 失败与串行中断，断言本轮 tool_result 全部落库），再修实现。
