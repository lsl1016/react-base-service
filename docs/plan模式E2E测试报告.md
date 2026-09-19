# Plan 模式端到端测试报告

> 日期：2026-09-19 ｜ 分支：`feature/plan-runtime`（commit `9447ece`）｜ 测试人：ZCode Agent
> 被测对象：Plan Runtime（`executionMode=plan`）第二种执行范式，含规划、隔离执行、持久化等待/恢复、控制命令与事件协议
> 关联文档：[Plan 模块功能文档](system/plan.md) ｜ 测试驱动：`tests/plan-e2e/`

## 1. 测试目的与范围

用**真实模型 + 真实工具**驱动 Plan 模式跑复杂任务，验证：

1. 规划 → 逐步隔离执行 → 总结写回的完整链路（含工具充分调用）；
2. `USER_INPUT` / `USER_ACTION` 持久化等待与双通道恢复（WS `plan_resume` / HTTP resume）；
3. 取消 / 拒绝的级联语义与并发互斥；
4. 六张表全量持久化、事件协议、detail/events 管理接口、会话回放；
5. 边界与异常行为（超时、断连、非法计划输出、运行中控制命令）。

不在本次范围：自动 Replan、并行步骤、DAG、重启恢复 Worker（V1 显式不做）。

## 2. 测试环境

| 项 | 值 |
|---|---|
| 服务 | 本地 `go build` 直跑，`:8180`（依赖全容器化） |
| 依赖容器 | `react-base-mysql`(mysql:8.0, :3317)、`react-base-redis`(redis:7, :16379)、`react-base-sandbox`(python, :18190)，全程 healthy |
| 模型 | `claude/glm-4.6`（bigmodel Anthropic 网关，`mutual_failover: false`） |
| Caller | `demo-app`（匿名鉴权 `X-User-Name: plan-e2e-tester`） |
| 业务工具 | 9 个 MCP 检索工具（`repo_*`，stdio 适配器，镜像库含 adk-go / trpc-agent-go）+ `python_exec` 沙箱 + Meta Tools（todo、read_tool_result 等） |
| 已知环境限制 | mcpgw HTTP 网关（18080）未启动，其 6 个 `mcpgw_*` 工具不可用；服务启动时同步失败仅记日志，不影响其余工具注册（体现韧性） |

## 3. 测试方法

自研 Node WS 驱动（`tests/plan-e2e/driver.js` + `run_scenarios.js`）：建立 WebSocket → 发送 `run`（`executionMode: plan`）→ 按场景策略消费 `plan_view_update` / `plan_step_event` 事件流 → 在适当时机注入 `plan_resume` / `plan_cancel` / HTTP 控制命令 → 断言终态与副作用。全部原始事件落盘 `tests/plan-e2e/artifacts/`。

| 场景 | 目标 | 任务要点 |
|---|---|---|
| S1 | 复杂正常流 | 仓库检索（repo 工具）+ python_exec 量化统计 + 中文报告，多 AGENT 步骤串联 |
| S2 | USER_INPUT 等待/恢复 | 计划首步 USER_INPUT 询问背景 → WS `plan_resume` → 续跑完成；附等待期并发互斥检查 |
| S3 | USER_ACTION 拒绝 | 高风险动作确认步骤，`approved: false` → 整体取消级联 |
| S4 | 运行中取消 | 首个 AGENT 步骤 RUNNING 时取消（HTTP 通道）；附 WS `plan_cancel` 运行态探针 |
| S5 | HTTP 恢复 + 连接无关性 | USER_INPUT 等待后**关闭 WS**，经 HTTP resume 续跑，轮询 detail 到终态 |
| S6 | WAIT 态互斥复测 | 等待期同会话普通 run（正确探针）应被拒；自定义 responseSchema 校验 |

## 4. 结果总览

| 场景 | 结果 | 耗时 | 说明 |
|---|---|---|---|
| S1 复杂正常流（检索+沙箱计算） | ✅ PASS | 520s | 最终版 3 步全 SUCCEEDED：`repo_get_repo_map`×1 → `python_exec` 统计 → 中文总结；18,599 条步骤事件 |
| S2 USER_INPUT 等待 + WS 恢复 + 互斥 | ✅ PASS | 417s | 3 步全 SUCCEEDED；11,172 条步骤事件流式下发 |
| S3 USER_ACTION 拒绝 → 级联取消 | ✅ PASS | 101s | 步骤态 `SUCCEEDED / CANCELLED / CANCELLED`，Plan=CANCELLED |
| S4 运行中取消（HTTP）+ WS 探针 | ✅ PASS | 45s | HTTP cancel 级联正确；WS `plan_cancel` 运行态被拒（符合控制器约束） |
| S5 HTTP 通道恢复（断开 WS） | ✅ PASS | 301s | WS 关闭后 Plan 续跑至 SUCCEEDED，持久化语义成立 |
| S6 WAIT 态互斥 + 自定义 schema | ✅ PASS | （分段） | 外层 run=`waiting_plan` 时并发 run 被拒（6000100）；Planner 自定义必填字段被 Resume 校验强制；恢复后 Plan SUCCEEDED |

> S1 首三轮未走通（任务规模超出默认 600s 步骤超时 + §6-P1/P5/P6 叠加），每轮失败原因均被定位并归因；最终版以受控任务规模完整走通，工具链路（repo 检索 / python 沙箱 / read_tool_result）在全部四轮中均工作正常。

**聚合统计**（本次测试会话，2026-09-19 18:00 后）：Plan 12 个（4 SUCCEEDED / 4 CANCELLED / 4 FAILED）；步骤 39 个（18 SUCCEEDED / 3 FAILED / 13 CANCELLED / 5 停留 PENDING，见 §6-P8）；Attempt 28 个（18 SUCCEEDED）；Wait 5 个（4 RESOLVED / 1 CANCELLED）。事件量级：单次复杂 Plan 最高 5 万+条原始事件、9~10 次视图快照。

## 5. 场景详情

### 5.1 S2：USER_INPUT 等待 → WS 恢复（417s）

计划 3 步：`collect_tech_background`(USER_INPUT) → `generate_route_table`(AGENT) → `write_summary_report`(AGENT)。等待态视图携带 `wait_request`（含 request_id 与默认 `answer` 必填 schema）；恢复后后续步骤经 `python_exec` 生成路线表，最终 Finalizer 总结以 assistant 消息落库（会话历史可见完整 markdown 表格）。等待期间同会话普通 run 被拒（该轮探针本身有缺陷，最终以 §5.6 S6 干净复测为准）。

### 5.2 S3：USER_ACTION 拒绝 → 级联取消（101s）

计划含前置清单步骤（AGENT，`python_exec` 生成 10 行模拟清单）+ 确认步骤（USER_ACTION）+ 清理步骤。`approved: false` 提交后：确认步骤与后续步骤全部 CANCELLED、前置已成功步骤保持 SUCCEEDED、Wait 置 CANCELLED 且 `resolved_at` 落库，符合"拒绝即整体取消"设计。

### 5.3 S4：运行中取消（45s）

首个 AGENT 步骤 RUNNING 时经 `POST /plan_execution/cancel` 取消：当前步骤与全部后续 PENDING 步骤置 CANCELLED、外层 run 收敛、客户端收到 `cancelled` 事件。副本探针证实：**run goroutine 活跃期间 WS `plan_cancel` 会被控制器拒绝**（"plan command rejected while a run is active"），运行中取消需走 HTTP 或普通 `cancel` 消息（见 §6-P4 文档建议）。

### 5.4 S5：HTTP 恢复 + 连接无关性（301s）

USER_INPUT 等待后**主动关闭 WS 连接**，再经 `POST /plan_execution/resume` 提交答案：Plan 在无任何 WS 连接的情况下续跑两步 AGENT（含 `python_exec` 容量推演）至 SUCCEEDED，全程事件经持久化消息可由 `/plan_execution/events` 重建。验证"等待与恢复不依赖原 goroutine / 原连接"。

### 5.5 S1：复杂检索任务（四轮尝试，问题归因链）

- **第 1 轮**（双仓库深度对比，5 步）：步骤 1–2 成功（adk-go / trpc-agent-go 结构探查），驱动侧 15 分钟超时提前退出；测试进程结束后步骤 4 三次 Attempt 以 `react client disconnected` 瞬时烧尽 → Plan=FAILED。工具侧正常：`repo_read_file`×13、`repo_search_code`×10、`python_exec`×13、`read_tool_result`×3。
- **第 2 轮**（同任务重跑）：步骤 1–2 成功，步骤 3 于**恰好 600s（默认步骤超时）**被记为 `react run cancelled` → 级联取消整个 Plan（"用户取消了 Plan"）。该步骤单轮并发了 111 个工具调用、全程 `repo_list_files`×391——触发 §6-P1 超时分类竞态。
- **第 3 轮**（精简版，3 步）：步骤 1 成功；步骤 2 两次 Attempt 均在 `python_exec` 入参传递上耗尽各自 600s（`read stream: context deadline exceeded` → 按设计自动重试一次 → 耗尽 FAILED）。此次超时**正确走了 Attempt 失败路径**，与第 2 轮形成对照，进一步锁定 P1 的触发条件。
- **第 4 轮（最终版，✅ 520s）**：任务指令显式约束规模（"只调用一次 repo_get_repo_map""数据直接写成 Python 字面量"）后，3 步全部 SUCCEEDED，工具调用干净收敛（`get_tool` / `repo_get_repo_map`×1 / `read_tool_result` / `python_exec`），Finalizer 输出结构化中文报告并明确复述约束达成情况。

### 5.6 S6：WAIT 态互斥干净复测 + 自定义 schema

S6 的 Plan 停在 WAIT_USER_INPUT（外层 run=`waiting_plan`）时，用正确探针（sessionId 置于消息顶层）发起同会话普通 run：被拒 `errNo=6000100 当前会话已有运行中的 ReAct run` ✓。随后 HTTP resume：先故意用错误字段被 schema 校验拒绝（Planner 本轮自定义必填字段 `monthly_reading_hours`，非默认 `answer`），换正确字段后恢复成功。

## 6. 发现的问题与观察

### P1【Bug·高】步骤超时的归类取决于截止时刻所处阶段，工具阶段超时会被误判为"用户取消"

- **现象**：S1 第 2 轮步骤 3 于 19:23:49 启动、19:33:49（恰好 600s）Attempt 记为 `react run cancelled`、步骤 CANCELLED（"步骤已取消"）、**整个 Plan 级联 CANCELLED（"用户取消了 Plan"）**，后续 2 个 PENDING 步骤一并取消。
- **对照**：S1 第 3 轮步骤 2 同样 600s 截止，但截止发生在模型流读取中，错误为 `read stream: context deadline exceeded`（DeadlineExceeded）→ **按设计走 Attempt 失败并自动重试**。
- **结论**：`executor.go` 注释（D4：DeadlineExceeded 不属于取消语义）与文档（"超时按 Attempt 失败处理"）描述的是设计意图，但当截止落在**工具执行/衔接阶段**时，中断错误包装为 `context.Canceled`，被 `IsCancellation()`（`errors.Is(err, context.Canceled)`）误吞为用户取消，绕过了失败重试。
- **建议**：步骤超时统一以 `context.DeadlineExceeded` 归类（在 stepCtx 上用 `context.Cause` 区分超时与主动取消），或在取消分支先排除 stepCtx 超时来源。

### P2【Bug·中】Planner 偶发产出非法 stepKey，直接判死整个 Run（无重试）

约 10 次规划中出现 1 次 `invalid stepKey ""`（模型漏填 stepKey；`submit_plan` 的 JSON Schema pattern 是软约束，落库前由 `validateDraft` 硬校验兜底）→ `planner_failed` → run 立即失败，用户只能整体重发任务。建议：规划失败自动重试 1–2 次（同一用户提示），或在事件中给出可重试的明确指引。

### P3【缺口·中】`timeout_seconds` 不在 Planner Schema 中，重型步骤必撞 600s 默认上限

`submit_plan` 参数只有 stepKey/name/instruction/stepType/dependsOn/maxAttempts/required 等，不含 `timeout_seconds`；步骤默认 600s 且任务侧无法调大。叠加 P1 后，任何单步超过 10 分钟的重型任务（深度检索、大规模计算）必然失败或被误取消。建议将 `timeoutSeconds` 纳入 Planner 可配字段（限定合理区间）。

### P4【文档·低】WS 控制命令的适用窗口未在接口文档中标明

`plan_cancel/retry/skip/resume` 在 run goroutine 活跃期间会被 WS 控制器拒绝（"plan command rejected while a run is active"），运行中取消实际通道是 HTTP `/plan_execution/cancel` 或普通 `cancel` 消息。行为本身合理（防并发 run），建议在 `docs/system/plan.md` 接口清单注明各命令的可用状态窗口。

### P5【设计权衡·低】RUNNING 步骤依赖客户端连接，断连按步骤失败烧尽重试

等待态是持久化的，但步骤执行态依赖 WS 连接：客户端断连 → 当前及后续 Attempt 以 `react client disconnected` 瞬时失败（重试同样立即失败）→ Plan=FAILED。对长任务（数分钟/步骤）而言，一次网络抖动即终结整个 Plan。可考虑将"client disconnected"归类为可暂停状态（复用 Wait 机制），向 Durable Runtime 演进。

### P6【可用性·低】跨步骤数据经 result_ref 传入 python_exec 的心智负担重

S1 第 3 轮中，模型连续 10 次 `python_exec` 调用都在尝试各种方式把前步结果引用（`{"ref": "result_ref_..."}`）塞进 `inputs`（raw_json / 嵌套 / 解包……），两轮 600s 全部耗在传参诊断上，未能进入实际统计。建议在步骤系统提示或工具说明中给出 result_ref 的明确消费范式（如"先 read_tool_result 取文本，再把数据写进代码字面量"）。

### P7【观察】步骤无工具调用预算，模型易过度探索

S1 第 2 轮的探查步骤单模型轮并发 111 个 `execute_tool`、全程 `repo_list_files`×391（逐目录穷举），直至 600s 截止。任务提示里显式限定调用次数（"不超过 6 次"）后第 3 轮步骤 1 即 158s 完成。建议 Planner/步骤模板默认注入检索预算类指令。

### P8【一致性·低】Plan FAILED 后后续步骤停留 PENDING，与取消路径不对称

三个 FAILED Plan 的后续步骤均停留在 PENDING（如 `quant_stats_table:FAILED | final_summary_cn:PENDING`），而 CANCELLED 路径会级联置终态。公开视图上失败计划会长期挂着"待执行"步骤；手工 Retry 仅针对 FAILED 步骤可用，语义可解释但建议失败时也级联标记后续步骤（如 SKIPPED/NOT_RUN）以保持视图一致。

## 7. 已验证能力清单

| 能力 | 验证方式 | 结果 |
|---|---|---|
| WS `run(executionMode=plan)` 分流与隐式建会话 | 全场景 | ✅ |
| Planner 结构化输出 → 落库 → 视图快照 | 全场景（10 次规划，1 次非法输出见 P2） | ✅ |
| Scoped ReactRun 隔离（`parent_run_id=outer`、`agent_path=plan/<stepKey>`、独立 clientHub） | DB 断言（tblLlmReactRun） | ✅ |
| 步骤内两段式工具加载（get_tool 按描述解析 → execute_tool） | S1 各轮日志（get_tool×26） | ✅ |
| repo 检索工具（list/read/search/repo_map/list_repositories） | 400+ 次调用全部成功 | ✅ |
| python_exec 沙箱执行 | S2/S3/S5 + S1（成功调用均正常返回） | ✅ |
| 前置步骤摘要 + read_tool_result 结果引用 | S1 第 1 轮（read_tool_result×3） | ✅ |
| USER_INPUT 持久化等待（WaitRequest 落库、外层 run=waiting_plan） | S2/S5/S6 + DB | ✅ |
| Resume 响应 Schema 校验（默认 answer / Planner 自定义字段） | S6（错误字段被拒、正确字段通过） | ✅ |
| WS `plan_resume` 续跑 | S2 | ✅ |
| HTTP resume 续跑（原 WS 已关闭） | S5、S6 | ✅ |
| USER_ACTION `approved:false` → 级联取消（含 pending wait） | S3 + DB | ✅ |
| 运行中取消级联（当前+后续步骤、外层收敛、`cancelled` 事件） | S4（HTTP cancel） | ✅ |
| 会话并发互斥（RUNNING 态与 waiting_plan 态均拒绝新 run，6000100） | 独立探针×2（正确构造） | ✅ |
| 超时 → Attempt 失败 → 自动重试（截止落模型流阶段时） | S1 第 3 轮 attempt 1→2 | ✅（但见 P1 竞态） |
| 六表持久化一致性（Version 恒 1 / Step / Attempt 不可变 / Result / Wait resolved_at） | SQL 聚合断言 | ✅ |
| Finalizer 总结写回会话历史（assistant 消息） | S2 会话消息查询 | ✅ |
| `/plan_execution/detail`（视图+Attempt 清单）与 `/plan_execution/events`（Attempt 事件重建） | S1/S5/S6 HTTP 断言 | ✅ |
| 会话回放含 Plan 最终视图快照 | S2 `/react/session/events` | ✅ |
| 多 Plan 并行执行（单服务两活跃 Plan） | S1 与 S2 并行窗口观测 | ✅ |
| MCP 网关不可达时的启动韧性（记日志、跳过、其余工具正常） | mcpgw down 全程 | ✅ |

## 8. 结论与建议

**结论**：Plan Runtime 的核心骨架——结构化规划、隔离执行、持久化等待/恢复、级联取消、互斥、六表落库与双通道管理接口——在真实模型与真实工具下**行为与设计文档一致，质量达到可试用水平**；全部 6 个控制流/正常流场景（S1–S6）均通过。主要风险集中在**长步骤的鲁棒性**：默认 600s 超时 + 超时分类竞态（P1）+ 超时不可配（P3）三者叠加，使重型步骤任务（S1 前三轮）难以稳定走通，这是当前最值得优先修复的组合。

**修复优先级建议**：P1（超时分类）> P3（timeout_seconds 可配）> P2（规划失败重试）> P6/P7（步骤提示词改进：result_ref 消费范式与工具预算）> P4/P8（文档补充与终态一致性）> P5（断连暂停，可随 Durable Runtime 二期）。

## 9. 附录：复现方式与工件

```bash
# 依赖容器 + 服务
docker compose up -d mysql redis sandbox && go run main.go    # :8180

# 跑全部场景（S4 起每场景 1–8 分钟，S1 视任务规模 10–30 分钟）
cd tests/plan-e2e && node run_scenarios.js            # 或指定 s1 s2 s3 s4 s5 s6
node mutex_probe.js <sessionId>                       # 独立并发互斥探针
```

- 驱动：`tests/plan-e2e/driver.js`（WS 客户端/HTTP 工具）、`run_scenarios.js`（场景与断言）
- 原始工件：`tests/plan-e2e/artifacts/`（results.json + 每客户端全量事件流；`artifacts-run2/` 为 S1 第 2 轮快照）
- 关键 Plan 记录：S1 最终版=`plan_exec_e69dfa14…`、S1 超时竞态样本=`plan_exec_b373129c…`（步骤 3 attempt 19:23:49→19:33:49 恰 600s）、S2=`plan_exec_0d6ff1a4…`、S3=`plan_exec_c498cac7…`、S4=`plan_exec_adc22c99…`、S5=`plan_exec_8a74af97…`、S6=`plan_exec_2b9ed379…`

### 测试脚本备注

早期两轮"等待期并发互斥"探针存在构造缺陷（sessionId 误置于 payload 而非消息顶层，导致服务端为新会话开跑），相关结论以 `mutex_probe.js` 独立探针（正确构造，两态均被 6000100 拒绝）为准。

## 10. 修复复测（同日）

针对 §6 发现的修复已于同日落地并复测（详见 `docs/system/plan.md` v1.1 与 git 提交）：

| 问题 | 修复 | 复测 |
|---|---|---|
| P1 超时误判为取消 | 以 stepCtx 的 `DeadlineExceeded` 为权威信号区分超时与取消，超时统一走 Attempt 失败（T7 场景：timeoutSeconds=60 + sleep(90)，两次 Attempt 错误均标注「步骤超时（上限 60 秒）」并自动重试，Plan 终态 FAILED 而非 CANCELLED） | ✅ T7 PASS |
| P2 非法 stepKey 判死 | `validateDraft` 按 name/序号确定性修复非法与重复 key；规划失败自动重试（最多 3 次） | ✅ 单元测试 |
| P3 超时不可配 | `timeoutSeconds` 纳入 Planner Schema（60–3600 钳制）并持久化 | ✅ T7（DB 落库 60） |
| P8 失败不级联终态 | FAILED 时后续 PENDING 步骤批量置 CANCELLED；Retry/Skip 同事务复位这些步骤及外层 run（顺带修复了 Retry/Skip 被外层 run error 守卫拒绝的存量问题） | ✅ T7（Retry 后 step_2 回 PENDING） |
| P4 WS 命令窗口未标明 | `docs/system/plan.md` 接口清单加注 | ✅ 文档 |
| P6/P7 提示词 | 步骤提示加入 result_ref 消费范式与工具预算约束 | ✅ 随 S1/S2 回归 |
| P5 断连暂停 | 属 Durable Runtime 二期，未改 | ➖ 留二期 |

回归：S2（等待/恢复 438s）✅、S4（运行中取消，首轮失败为驱动侧竞态——探针 error 事件提前满足终态等待，修正等待条件后复跑）见下表、`go test ./...` 全绿。

| 复测轮 | 场景 | 结果 |
|---|---|---|
| 修复后 | T7 超时链路（P1/P3/P8） | ✅ PASS 166s |
| 修复后 | S2 USER_INPUT 等待 + WS 恢复 | ✅ PASS 438s |
| 修复后 | S4 运行中取消 | ✅ PASS（驱动修正后） |
