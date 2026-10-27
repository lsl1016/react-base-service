# react-base-service 前端复杂任务会话 × 会话重放 测试报告

- **日期**：2026-10-27
- **测试人**：ZCode 自动化测试（浏览器真实操作 playground 前端）
- **代码基线**：main @ `09bb2dc`（feat(mcpclient): 识别 MCP ToolAnnotations.readOnlyHint）
- **服务形态**：本地 `go run`（:8180）+ 容器化依赖（MySQL :3317 / Redis :16379 / Python 沙箱 :18190 / SearXNG :8888，均 healthy）
- **被测会话**：`session_b27c7d2175104bf9adc6da27c3c16f8b`（appKey=demo-app）
  - 父 run：`run_b0481a23bc9646b7a7ebdb5adadb88a7`（19:39:24 – 19:45:48，终态 **error**）
  - 子 run（researcher）：`run_e84fbf4b55c242fba7171c31b99af020`（终态 finished）

---

## 一、结论速览

**核心链路可用**：从 React 前端（playground）发起的 5 步复杂任务（联网检索 → 子代理交叉核验 → 沙箱词频统计 → 长期记忆写入 → 简报汇总）**全部执行成功且产物落库**；会话重放（对话视图 / 多 agent 泳道 / 从头播放）能高保真还原全过程。

**但发现 3 个 P1 级问题**：

1. **python_exec 输入注入链路断裂**——引擎发给沙箱的 multi-transport 信封沙箱不解包，所有带 `inputs` 的脚本首次执行必失败；
2. **run 生命周期与 WebSocket 强耦合**——页面刷新/关闭即把 run 判成 `error: client disconnected`，本次任务 5 步全部完成后 1 秒内被页面自刷新"连坐"成 error 终态；
3. **多 agent 会话 todo 卡片串道**——SDK reducer 用单一全局 todos 状态回写"最近一张完整 todo 卡片"，父 run 后续更新会覆盖子代理卡片内容（实时与回放均受影响）。

---

## 二、环境与准备（全部通过）

| 项 | 结果 | 备注 |
|---|---|---|
| 服务启动 | ✅ | :8180 已运行最新代码（14:24 启动，晚于最后一次提交 13:46）；`/healthz` ok、`/readyz` mysql/redis ok |
| 依赖容器 | ✅ | react-base-mysql / redis / sandbox / searxng 均 Up & healthy |
| 记忆能力 | ✅ | `/setting/memory/get` enabled=true（override） |
| 子 agent 能力 | ✅ | `/setting/subagent/get` enabled=true；builtin 三件套（general-purpose / researcher / report-writer）已注册在 default 作用域 |
| 浏览器检索能力 | ⚠️ | `/setting/web/get` enabled=true 且 configured=true，但 SearXNG 后端引擎大面积不可用（见问题 D-4） |
| 上下文压缩 / 工作区 | ✅ | 均开启 |
| Playground 前端 | ✅ | `/react/playground` 200，WS 连接正常，会话历史正确恢复 |

## 三、测试任务与执行时间线

任务（单条消息，从前端输入框真实键入发送）：

> 请完成一份小型技术调研简报，共 5 步：(1) 用联网搜索查一下智谱 GLM-5.3 模型的发布时间与主要能力点，至少 2 个来源；(2) 委派只读调查子代理 researcher 交叉核验其中一条关键信息并给出证据；(3) 用 python_exec 对检索要点做一次简单词频统计，验证沙箱可用；(4) 用 memory_write 把「GLM-5.3 发布信息」这条关键结论写入长期记忆；(5) 最后汇总成不超过 200 字的中文简报。

| 时刻 | 事件 |
|---|---|
| 19:39:24 | 消息发出，run 启动 |
| 19:40:06 | 模型建立 5 项 todo 清单；并行发起 2 组 web_search + 2 个 get_skill |
| 19:40:18–19:40:28 | 前 4 次 web_search 全部空结果（SearXNG 引擎问题） |
| 19:40:37 | 第 5 次检索命中 8 条，锁定 4 个独立来源 |
| 19:41:07 | `delegate_agent` 委派 researcher 子 run（隔离执行，64 个事件） |
| 19:43:35 | 子代理返回核验报告（结论"属实"，7 条带 URL 引句证据；其中 1 个来源 TLS 超时后重试成功，并识别出 8-14 官宣 vs 8-19 上线的口径差异） |
| 19:44:22 | python_exec 第 1 次失败（输入注入问题，见 D-1）；第 2 次模型自救成功（exitCode=0，词频统计产出） |
| 19:45:2x | memory_write 成功（itemId=2，detached 层，经 `/react/memory/list` 复核可查） |
| 19:45:48 | 最终简报（≤200 字 + 执行摘要表）生成并持久化；**同秒页面自刷新 → WS closeCode 1001 → run 判 error** |

父 run 工具调用序列（共 17 次）：`todo_write ×7、web_search ×5、get_skill ×2、delegate_agent ×1、python_exec ×2、memory_write ×1`。

## 四、问题清单

### D-1【P1·功能】python_exec 输入注入信封不解包，带 inputs 的脚本首次必失败

- **现象**：会话内第一次 python_exec（seq 200）`KeyError: 'keypoints'`；模型第二次运行时在脚本里自证 `input_keys_received: ["inputs", "transport"]`，即 stdin 收到的是信封本身而非解包后的输入容器，最终靠把文本硬编码进脚本兜底完成统计。
- **根因**（三方不一致）：
  - 引擎侧 `service/react/internal/pyexec/python_exec.go:443` `resolvePythonExecData` 组装 multi-transport 信封 `{"transport":"multi","inputs":{alias:descriptor}}`；
  - 沙箱侧 `sandbox/server.py:196` 把 `data` **原样**写入子进程 stdin，全文检索不到任何 transport/inline/cos_ref 处理逻辑（运行容器 /app/server.py 与仓库 md5 一致：`08fed90b…`）；
  - 工具描述（`service/react/meta_tools.go:124`）向模型承诺"`inputs['alias']` 直接可用"。
- **独立探针复现**（绕过模型直接打沙箱 HTTP）：

  ```
  POST /api/pythonexec/execute  data={"transport":"multi","inputs":{"a":{"kind":"inline","value":"hi"}}}
  → stdout: {"keys": ["transport", "inputs"], "inner": ["a"], "a_direct": null}
  ```

  即 `d['a']` 为 null，值藏在 `d['inputs']['a']['value']`。
- **影响**：text / tool_result / raw_json / expr 所有 inline 输入源首调必失败，浪费轮次与 token（本会话多花 1 轮）；attachment（cos_ref）与 http（http_ref）更可疑——沙箱网络硬隔离（P0-1）后是否能取 cos_ref/http_ref 存疑，建议一并排查。
- **建议**：在沙箱侧实现信封解包（inline 就地取值、cos_ref/http_ref 由引擎侧预取后 inline），或引擎对 inline 源直接下发平铺 alias map；补一条"带 inputs 的 python_exec"端到端回归用例。

### D-2【P1·架构】run 生命周期与 WS 连接强耦合，断连即终态 error

- **现象**：本会话 5 步任务全部完成、最终简报已持久化（seq 247 content_end），但同秒页面发生整页刷新（access log 19:45:48 出现完整 GET /playground + 静态资源序列），WS 以 closeCode **1001 (Going Away)** 关闭 → `WriteEvent` 失败 → 引擎判 `ErrReactClientDisconnected` → **DB 终态 `state=error, error_message="client disconnected"`**（已直查 MySQL 复核）。历史会话中 2026-10-19 的 run_0b414747 也以同样方式终态 error，说明非偶发。
- **代码链路**：`service/react/ws.go:341`（写失败包装断连错误）→ `service/react/engine.go:675`（context.Canceled 且无 cancel/timeout cause 时映射为 client disconnected）→ `service/react/runtime.go:276`（run 置 error 终态）。
- **影响**：用户在长任务执行期间关闭/刷新页面、切后台被杀连接、网络抖动超时，都会把一个本可继续（或已经完成）的 run 判死。对主打"会话持久化 + 历史回放"的服务端 Agent 基座，交互式 run 应支持脱连接续跑或至少优雅收尾（与 cron 的 headless 执行能力对齐）。
- **附注**：前端 SDK 的自动重连 + 会话恢复表现良好——页面刷新后立即重连并通过 `/session/events` 恢复全部内容，用户几乎无感；问题只在 run 终态语义。
- **建议**：断连改为 run 级"软脱管"（继续执行、事件照常落库），或至少区分"已完成但写入失败"与"中途断连中止"两种终态。

### D-3【P1·前端/SDK】多 agent 会话 todo 卡片串道：子代理卡片被父任务清单覆盖

- **现象**：重放页两种视图（对话/泳道）中，researcher 子代理的 todo 卡片（标题"创建核验任务清单并开始第一步"，实际只有 3 项：并行抓取来源/补充检索/汇总结论）被渲染为**父任务的 5 项清单且全部"已完成"**；同时父任务自己的首张卡片冻结在中途状态（verify 处理中、后 3 项待处理），不再更新到终态。
- **数据侧无问题**：`/session/events` 里子 run 的 `todo_write/todo_update`（seq 127/129/147/…）内容正确（3 项、各自状态推进正常）——纯渲染层缺陷。
- **根因**：`web/sdk/runtime/event-reducer.ts:1232` 的 `todo_update` 分支维护**单一全局 `state.todos`**，并调用 `syncLatestTodoCardItems`（`event-reducer.ts:336`）把当前全局 todos 回写到**全时间线最后一张 merge=false 的完整 todo 卡片**。子代理卡片（seq 127）成为"最后一张完整卡片"后，父 run 后续 merge 更新（seq 192–240）把父清单写进了子代理卡片。live 事件流走同一 reducer，实时会话同样会串道。
- **建议**：todos 状态与 todo 卡片按 runId/agentPath 隔离；`syncLatestTodoCardItems` 只回写同 run 的卡片。

### D-4【P2·基础设施】web_search 成功率低，拖慢并干扰 agent 任务

- **现象**：SearXNG 直接探测 3 次仅 1 次有结果（20 条），unresponsive_engines 常驻 `brave: too many requests / duckduckgo: CAPTCHA / google cse: timeout`；会话内模型 5 连查前 4 次空结果，靠换关键词第 5 次才命中。
- **影响**：每次空结果浪费一轮模型调用（约 7–10s + token），且模型可能误判"信息不存在"提前放弃（本次靠任务压力坚持重试）。工具返回的引导文案（"建议换关键词、拆短查询或改用英文"）设计得当，是有效的减损措施。
- **建议**：为 SearXNG 配置可用引擎池/自托管引擎或 API 代理；`web_search` 空结果时可附上引擎健康度，帮助模型区分"没有结果"与"引擎不可用"。

### D-5【P2·性能/体验】重放"从头播放"慢且中途长时间冻结，无进度与中断控制

- **现象**：218 个事件播放耗时约 **6–7 分钟**（interval 计算值 55ms×218≈12s，实际超 30 倍）；播放到子代理大段 markdown（委派任务书/核验报告）时出现 **40 秒以上界面完全冻结**（DOM 两次采样 0 变化）；期间按钮禁用、无进度指示、无法暂停或跳到结尾。
- **根因推测**：`playFromStart` 每个事件都 `reducer.applyEvent` + `setState` 触发整棵 SolidJS UI 重建，大 markdown 块的同步渲染进一步放大；`web/react/replay.js` 的播放循环无批处理。
- **建议**：播放分片批处理（如按 run/step 粒度应用）、加进度条与"跳到结尾"（结尾态 `replayEvents` 一次到位，其实已有现成函数）。

### D-6【P2·可观测】回放无法感知 run 终态 error

- **现象**：本次 run DB 终态为 error（client disconnected），但重放页从对话视图完全看不出异常——最终回答正常渲染，没有任何错误标记；泳道视图同样无差。回放消费者（如复盘、审计）会误以为 run 圆满完成。
- **建议**：回放按 run 维度展示终态标记（finished/error/cancelled/timeout），error 时提示错误信息。

### D-7【P3·数据质量】渠道管理存量中文名乱码

- **现象**：playground 右栏"渠道"表格中 `é£ä¹¦æœºå™¨äºº` / `Telegramæœºå™¨äºº`（应为"飞书机器人"/"Telegram机器人"）。`/gateway/channel/list` API 原样返回乱码，说明是 **2026-10-19 建档时写入即坏**（UTF-8 被按 Latin-1 落库），非展示层问题。
- **建议**：修正存量数据；在渠道创建链路加 UTF-8 校验。

### D-8【P3·待复现】输入编辑器在异常输入路径下丢稿

- **现象**：本次自动化测试中，向 Lexical 编辑器（`web/sdk/ui/editor/RichInputEditor.tsx`）注入多行文本后发生编辑器 DOM 重建，内容只剩第 2 行起的部分文本（首行丢失）；Playwright `fill`/`execCommand` 直改 DOM 均不生效（Lexical 状态机不知情，属预期）。CUA 真实键盘逐字输入单行文本则完全正常。
- **定性**：对自动化工具/RPA 场景不友好是确定的；普通用户是否可触发"输入中编辑器重建导致丢稿"本次未能在纯手工路径复现，**标注待复现**，建议针对"多行粘贴 + 立即发送"做一轮手工回归。
- **附带发现**：发送按钮在两种布局间切换时会从"带文字"变为"纯图标（title=发送）"，对按 accessible name 定位的自动化/无障碍工具不稳定（次要）。

## 五、验证通过项（正面结论）

| 能力 | 验证结果 |
|---|---|
| ReAct 主循环 | ✅ 5 步任务全程无人工干预完成；todo 状态推进正确（数据侧）；H1 分节 + 每步说明的输出结构清晰 |
| 多 agent 委派 | ✅ researcher 子 run 历史/token 隔离执行（64 事件独立 runId）；事件按 agentPath 归流；子代理只读约束在卡片上有标记；委派任务书结构完整（目标/已知信息/边界/期望返回/失败预案） |
| 子代理质量 | ✅ 核验报告区分"官宣 8-14 vs 上线 8-19"、"内部评测 50% vs 公开基准"两个口径陷阱，附 7 条带引句证据，主动列出缺口清单 |
| 联网检索 | ⚠️ 功能可用（第 5 次查询成功、web_fetch 抓取多来源成功），稳定性差（D-4） |
| Python 沙箱 | ⚠️ 执行能力正常（exitCode=0、白名单/超时/产物管道在位），输入注入断裂（D-1） |
| 长期记忆 | ✅ memory_write 落库（itemId=2，detached 层，含口径限定），`/react/memory/list` 可查回 |
| 两段式工具加载 | ✅ `get_skill` 按需加载"委派任务写作"/"Python 数据分析"两个 Skill 并在后续步骤中遵循其规范 |
| 前端韧性 | ✅ 页面刷新后 SDK 自动重连 + 会话恢复，最终答案完整可见 |
| 会话重放-对话视图 | ✅ 高保真还原：HITL 未作答占位、工具卡片（含执行失败标记）、委派任务书、子代理报告、执行摘要表全部在场 |
| 会话重放-泳道视图 | ✅ 主 Agent 28 步 / researcher 8 步正确分道；子代理泳道展示"该泳道收到的任务"与逐工具风险标签（todo 卡片串道除外，D-3） |
| 会话重放-从头播放 | ⚠️ 可用但慢（D-5） |
| 服务稳定性 | ✅ 测试期间（含 6+ 分钟 run）服务无重启、无错误日志，/readyz 持续 ok |

## 六、建议优先级

1. **立即修**：D-1（python_exec 信封解包——每个使用数据注入的任务都在流血）、D-3（todo 卡片按 run 隔离）。
2. **排期修**：D-2（run 与 WS 解耦/终态语义细化，涉及架构决策）、D-5（回放性能）、D-6（回放终态可视化）。
3. **运维/数据**：D-4（SearXNG 引擎池）、D-7（乱码数据修正）、D-8（编辑器丢稿手工回归）。

---
*复现材料：会话 `session_b27c7d2175104bf9adc6da27c3c16f8b`（appKey=demo-app），重放入口 `/react-base-service/react/replay?sessionId=session_b27c7d2175104bf9adc6da27c3c16f8b&appKey=demo-app`；沙箱探针命令见 D-1。*

---

## 七、修复记录（2026-10-28）

D-1 / D-2 / D-3 已全部修复并验证：

### D-1 修复：python_exec 输入改为引擎侧平铺注入

- 方案：废弃 multi-transport 信封（沙箱网络隔离后 cos_ref/http_ref 本就不可达，且沙箱从未实现解包）。`resolvePythonExecData` 现把六类输入源全部在引擎侧解析为平铺的 `alias→value` JSON，直接作为脚本 stdin，与工具契约一致；`sandbox/server.py` 零改动。
- attachment：引擎按 fileId 校验归属后从 COS 下载、按 charset 解码为 UTF-8 文本内联（单源上限 6MB，超限报错并提示替代路径）；http：新增 `netcap.FetchRawText`（复用 web_fetch 的 SSRF 防护/Content-Type 校验/超时）引擎侧代取（同时落定加固方案 P0-2 的处置方向）；全部输入序列化上限 7MB（沙箱请求体硬限 8MB 内留余量）。
- 工具描述与附件清单提示同步更新（actor 改为后端 + 大小上限说明）。
- 验证：`TestResolvePythonExecDataFlatAliasMap` 等单测锁定平铺契约；E2E（`tests/ws_disconnect_soft_detach.py ... inputs`）真实 run 中脚本从 stdin 正确读到 `inputs['words']`，词频输出 `{"苹果":3,"香蕉":1,"橙子":1}`。

### D-2 修复：断连软脱管，run 生命周期与连接解耦

- 根因（探针实证）：客户端 TCP 断开时 `ctx.Request.Context()` 会被取消（即使连接已 hijack），run 的 context 链挂其上被连坐取消 → 引擎按 `ErrReactClientDisconnected` 终态 error。
- 修复（`controllers/http/react/ws.go`）：① connCtx 改从 `context.Background()` 派生，与 TCP 对端生命周期解耦；② 读侧终结时不再以 disconnect cause 取消 run，改为 `wsEventWriter.Detach()` 把事件写降级为静默丢弃（run 继续执行、消息照常落库，重连方经 `/session/events` 恢复）；③ 依赖前端交互的工具（ask_question/危险确认/Client Tool）因上行通道关闭按既有路径失败收敛；④ 新增 `react_runs_detached_total` 指标量化发生频率。
- 验证：单测 `TestWSEventWriterDetachedDropsWrites`；E2E（同脚本 detach 模式）：python_exec 执行中硬断 TCP（RST、无关闭帧），run 继续跑完 sleep 20s 并收敛 `state=finished`，最终回答「睡眠完成」已持久化可回放。修复前同样场景终态为 `error: client disconnected`（本次测试与 10-19 历史数据均复现）。

### D-3 修复：SDK todo 状态按 runId 隔离

- `web/sdk/runtime/event-reducer.ts`：新增 `todosByRun` 分片；`todo_update` 只回写**同 runId** 的最近一张完整 todo 卡片（`syncLatestTodoCardItemsForRun`），父 run 后续更新不再覆盖子代理卡片；无 runId 的历史事件保持旧行为（全局回写）兼容。
- 验证：新增多 Agent 交错回归单测（父 5 项/子 2 项清单互相更新互不污染），SDK 全量 43 文件 317 测试通过；dist 已重新构建。

### 回归与遗留

- `go vet` 无告警；Go 41 个测试包全绿；SDK 317 passed。
- 遗留观察项（本次未处理）：D-1 中模型仍可能首次写出白名单外库（如 `collections`）被沙箱拒绝，属模型行为+白名单策略问题，非注入链路；D-4~D-8 维持原建议。
