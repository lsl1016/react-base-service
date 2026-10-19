# pi-web 与 deepseek-harness 交互/展示设计研究

- 日期：2026-10-12
- 目的：为 [APP接入方工作台设计方案](./20261012_APP接入方工作台设计方案.md)（下称"工作台方案"）提供**交互与展示层**的设计参照。研究范围刻意排除两个项目的 agent 运行时实现本身，聚焦：会话级展示（能力/子agent/tool/轮次的可见性与可点击性）、UI 风格、agent 运行时配置界面（skills/MCP/插件/子agent）。
- 研究对象：
  - **pi-web**：`/Users/admin/Desktop/xm/pi-web`，pi agent 的官方 Web 宿主，Next.js App Router 单页 + 大量 API 路由。
  - **deepseek-harness**（下称 **dsh**）：`/Users/admin/Desktop/xm/deepseek-harness`，DeepSeek 客户端 monorepo（React 18 + Vite + 插件化 client 包），Web/桌面共用。
- 文中路径均相对各自项目根。

---

## 0. 一句话画像

| | pi-web | deepseek-harness |
|---|---|---|
| 定位 | 开发者自用的 agent 驾驶舱 | 对外发布的消费级产品（专业向） |
| 视觉 | **IDE 式高密度工具风**：14px 基准、1px 边框分区、几乎无阴影 | **消费级聊天底子 + IDE 密度的过程区**：气泡/Hero 屏 + 24px 折叠行 |
| 架构哲学 | SSE + 会话 JSONL 重放，React 外流式 store | **事件溯源**：append-only 事件台账，UI 是事件的确定性投影 |
| 最值得抄 | turn 过程折叠、子会话三层打通、配置"写入前预览" | Chat/Trajectory 双视图、DisclosureRow 视觉语法、preset 固化进会话 |

两个项目在"会话里能看清 agent 干了什么"这件事上收敛到了**同一套核心范式**（§1.2），这是本次研究最重要的结论。

---

## 1. 会话级交互与展示

### 1.1 整体布局：三栏，左右可拖宽

两者布局同构：**左栏（会话/工作区树）｜中栏（会话时间线 + 输入区）｜右栏（详情面板，可全宽）**。

- pi-web：`components/AppShell.tsx`（左栏 2131-2150、顶栏 2166-2296、右栏 2636-2757），顶栏 36px，按钮组 `agents / system / tools / 分支 / 统计` 触发下拉面板；右栏 TabBar 只挂载激活的 viewer（文件/终端/子会话）。宽度常量在 `lib/panel-layout.ts`，拖拽 `hooks/useResizablePanel.ts`。
- dsh：`packages/client/ui-layout/src/client/AppFrame.tsx` CSS grid 三栏，拖拽用指针捕获 + rAF 节流；右栏是 "track 而非 box"（可全屏/覆盖中栏），tab 由各插件注册（`packages/client/ui-sidebar-right/src/client/tab-registry.ts`）。

### 1.2 核心范式：turn = 用户消息 + 可折叠过程组 + 最终回答 ⭐

这是两项目最一致、也最值得照抄的设计。一轮对话不再是一串平铺的消息，而是：

```text
┌ 用户消息（右对齐气泡）
│ ┌ ▸ 过程组（默认折叠）：Process details · N messages · M tool calls
│ │   ├ thinking 卡（默认折叠，右侧显示秒数）
│ │   ├ tool 调用卡 ×M（名称+参数摘要+耗时 一行）
│ │   └ 中间文本块
│ └ 最终回答（全宽正文，独立呈现）
└ turn 尾部：复制/分叉/时间/用量 pill
```

- **pi-web**：切分逻辑在 `lib/message-display.ts`——`splitFinalAssistantBlocks`（74-87 行：最后一个非 text/image 块之后的才是 answer，之前全是 process）；`collapsesProcessDetails`（99-103 行：**只有存在真实回答才默认折叠过程**，错误/截断轮次保持展开，否则错误前的文字会被藏住）；组渲染 `ProcessDetailsGroup`（`components/ChatWindow.tsx:194-238`）；流式中的 live tail 不折叠（1192-1199）。切分结果用 WeakMap 缓存保持 memo（`lib/turn-views.ts:31-54`）。
- **dsh**：过程组头 `ProcessGroupHeader`（`packages/client/ui-chat/src/client/chat/ChatGroupSeat.tsx`）显示**活动摘要而非计数**——运行中实时显示"正在读取文件 · path.txt"（`useStableLiveProcessTitle`，最少停留 150ms 防闪烁），结束后变为"已读取文件并修改了文件"（活动类别 top-3 拼接，`contract/step-process.ts:11`）。活动类别枚举含 read/search/write/edit/commands/code/webSearch/**subagents**/plan/questions/tools（`contract/process-groups.ts:2`，中英文案 `locale.ts:8-52`）。turn 级披露 `TurnProcessNodeView.tsx`："已完成，用时 N 秒"（aborted/error 时分别显示"已停止/处理失败"且保持展开）。
- **取舍差异**：pi-web 过程组头给"数量"（N messages · M tool calls），dsh 给"动词摘要"（正在读/写/搜索）。**dsh 的动词摘要对非开发者用户更友好**，建议工作台优先采用，数量可作 hover tooltip。

### 1.3 工具调用卡（折叠行）⭐

两项目的工具调用都是**一行折叠卡 + 展开体**，标题行的信息设计高度一致：

| 标题行元素 | pi-web（`components/MessageView.tsx:1140-1307` ToolCallBlock） | dsh（`packages/client/ui-tool/src/client/tool/components/ToolRow.tsx`） |
|---|---|---|
| 图标/名称 | 工具名**绿色等宽**，出错变红；MCP 工具显示为 `server/tool`（`lib/mcp-tool-display.ts`） | 变体图标（bash/read/search/edit…映射表 `tool-call-model.ts:44`）+ **本地化标题**（"读取文件/运行命令"，60+ 工具名映射 `TOOL_TITLE_KEYS:74`） |
| 参数摘要 | 取 `command/path/file_path/pattern/query` 等常见首参数截断 120 字符（`getToolPreview`） | `summary` + `summarySuffix`；文件路径摘要**可点击**打开右侧文件预览（带行号）；diff 类显示 `+N -M` 着色总计（**特意用等宽**，与 diff 正文一致） |
| 状态 | 流式中 `rawInput` 未完显示 "generating tool input…"；失败整卡换皮（红边红底） | 五态 `preparing → running(TextShimmer 微光) → ok / error(摘要变红+结果首行) / stopped(琥珀)`（`tool-call-model.ts:18`） |
| 耗时 | `2.4s / 3m 5s`，<100ms 不显示（`formatToolDuration`） | turn 级用时；trajectory 有四种时间模式（sequence/duration/time/actual） |
| 跳转 | 子代理调用右侧有"打开子会话"机器人图标按钮（1236-1246） | 每行 **Inspect 按钮** → 切 Trajectory 视图并定位该 callId |

**展开体**（互斥优先级，专用 > 通用）：

1. pi-web：apply_patch/diff 类 → 双栏 split diff（`SplitFilesView`，行号 + +/- 着色）；普通结果 → 等宽 pre（maxHeight 400）；**结果图片永远可见、不随卡折叠**（`ResultImages`）；嵌套调用（codemode）→ 卡内子列表（最近 20 条 + "show earlier calls" + 合计成本）。
2. dsh：按工具名注册的 keyed toolview（`ui-tool/src/client/tool/toolviews/`：TerminalBlock 带 ANSI、DiffBlock、ReadBlock 带行号、SearchBlock 命中行、WebBlock、图片画廊、AskQuestionCard、ToolDetails 键值表），兜底通用 **IN/OUT 卡**（参数 JSON + 扁平化结果文本）。
3. 嵌套子调用：dsh `ToolCallTree.tsx` 递归渲染 `subCalls`（父子图防环 + 深度上限）；pi-web `lib/agent-event-wire.ts:51-57` 把嵌套工具事件折叠进父卡。

**展开状态持久化**：pi-web 把展开的 toolCallId 存在 React 外的模块级 Set（`lib/tool-call-expansion.ts`），跨"流式 → 落盘 → 重挂载"保持——长会话里用户手动展开的卡不会因为刷新而收起。

**流式进行中的全局信号**（除卡片自身状态外）：

- pi-web：时间线下方 pulse 呼吸状态行 `Running tools: name（+最后输出行）`（`lib/chat-phase-label.ts`、`lib/tool-execution-progress.ts`）；bash/powershell 的 partial result 实时滚进卡片（`tool_execution_update` 事件，服务端 150ms 合并、背压可丢弃）。
- dsh：`RunningStatus.tsx` 运行中提示 + 已运行时长；品牌鲸鱼动画 `RunningWhaleTail`（reduced-motion 下回落静态线稿）。

### 1.4 子 agent 展示 ⭐

用户点名要"点进去看子 agent"，两项目都给了**多层入口**：

**pi-web 三层：**

1. **时间线内工具卡**：子代理就是名为 `Agent` 的工具（`lib/subagent-extension.ts:162-260`），卡片摘要行显示 `profile: description (status)`，右侧按钮一键打开子会话 tab。
2. **右侧面板只读详情** `components/SubagentViewer.tsx`：**刻意用轮询（2s）而非 SSE** 拉子会话 tail——查看器永不唤醒/干扰运行中的子代理（36-45 行注释）；正文复用同一套 MessageView 渲染完整子会话时间线。
3. **顶栏 agents 切换面板** `components/AgentSessionPanel.tsx`：主会话 + 全部子代理列表（running 排前、状态图标、>8 个出搜索框），点击把整个聊天区切到该会话。
4. 侧栏里子会话按 `relation.parentSessionId` 归入主会话 family 树（`lib/session-family.ts`）；后台子代理完成时向父会话注入通知消息并开启新一轮（`pi-web:subagent-notification`）。

**dsh 的目录树：**

1. 根会话头部显示**后代计数按钮**（"N 个子智能体"，有运行中加 StateDot）→ 打开 `CatalogDropdown`（`packages/client/ui-subagent/src/client/SubagentHeaderLineage.tsx:446`）：portal 下拉树，**完整键盘导航**（↑↓←→ Home/End），递归展开子目录；每行显示 label、会话标题、mode（one-shot/continuable）、activity（running/completed，运行中每秒刷新）、**token 总数**（四桶求和）、**精确耗时**（秒级紧凑/悬浮精确双格式）。
2. 点击行 = 作为主会话打开；旁侧按钮 = 右侧栏新 tab 打开（`sidebar-chat/index.tsx` 嵌入**完整 Conversation 组件**，不离开父会话上下文）。
3. 子会话输入区是只读接管模式（一次性/父离线时），父离线但仍在跑时保留 Stop（`SubagentReadOnlyComposer.tsx`）。
4. 多 agent 团队运行有专用 `workflow-run` 节点（`ui-workflow-run/src/client/workflow-definition.ts`：name/status/phases→members）。

> 对工作台的映射：我们 SDK 已有 `AgentLaneView`（顶部 tab 泳道，一次一条），但**没有层级嵌套、没有"从工具卡一键跳子会话"、没有子 agent 汇总目录（token/耗时）**。dsh 的"会话头计数按钮 + 下拉树"是投入产出比最高的补法，数据源 `agentPath`/`delegated_*` 已落库。

### 1.5 能力发现 UI（"这个会话能用什么"）

| 层 | pi-web | dsh |
|---|---|---|
| **装配结果预览** | 顶栏 tools → `ToolDefinitionsPanel`：左栏工具名列表，右栏描述 + **参数表（解析 JSON Schema：类型/必填/枚举/默认值）**；相邻 system 面板显示**最终拼接的 system prompt**（数据来自 `get_state` RPC，休眠会话也可见） | Trajectory 视图读 `request/header` 事件的 `EpochHeader.tools: ToolSchema[]`——**每次请求实际携带的完整工具 schema** |
| 输入区选择 | `/` 斜杠面板（builtin/extension/prompt/skill 四组，键盘导航）；`@` 文件引用补全（支持文件+行号）；工具预设下拉（configured/none/read-only/full…，`lib/tool-presets.ts`） | `/` 技能菜单（`skills/list` RPC，单飞缓存，`modelInvocable:false` 标"仅用户可调用"）；`@` 文件触发器（`ui-input-trigger`） |
| 会话内变更通知 | 无（变更走全局设置） | `ContextInjectionRow`：**"工具已更新 · 新增 N · 移除 M"**（展开列工具名清单）、技能目录注入行（`ContextBody` 的 `CatalogBody` 渲染 `{name, description}` 列表） |
| 全局配置 | Settings 的 skills/agents/plugins/mcp 四区（§3.1） | 设置页插件只读清单 + Agent Preset 分组（§3.2） |

> 工作台方案的"装配快照"（§4.1）对应的就是 pi-web 的 `ToolDefinitionsPanel + SystemPromptPanel` 组合与 dsh 的 `request/header` 回放——**两个项目都把"模型此刻能看到什么"做成了可点击的只读视图，而不是让用户去拼多个配置页**。

### 1.6 双视图：Chat vs Trajectory（dsh 独有）⭐

同一会话、同一事件流，两个视图 tab（`conversation.view` slot）：

- **Chat**：人类可读投影——分组折叠 + 过程摘要头（§1.2/1.3）。
- **Trajectory**：**全量事件台账**——每事件一行（kind：system/user/context/compacted/message/tool/subtool…），30px 行高虚拟化，可搜索（`trajectory-search-index.ts`）、可全部折叠、四种时间模式、每次请求行显示 usage 与**累计 usage**（`TrajectoryView.tsx:213`）。
- 互链：工具卡 Inspect → `ConversationViewRequest{view:'trajectory', focus: callId}` 定位；反向可从台账跳回 chat。

这直接回答"我想看到每一次调用的原始记录"的需求：**折叠 UI 负责"读"，台账 UI 负责"查"**。工作台方案的 Inspector（§4.3）建议直接采用这个双视图结构——我们的事件流已持久化（`tblLlmReactRun` + history 回放），台账视图基本是纯前端工作。

### 1.7 数据模型与流式（UI 视角）

- **pi-web = SSE**：`app/api/agent/[id]/events/route.ts`（`text/event-stream`）；服务端 30s 心跳、512KB 高水位丢可重建事件、16MB 断流重连、live 输出 150ms 合并（`lib/agent-event-stream.ts`）。**流式消息放 React 外的自定义 store**（`lib/streaming-message.ts` createStreamingStore + `hooks/useStreamingSelector.ts` 选择器订阅）——每个 chunk 只重渲染流式组件，不重渲染整个聊天。
- **dsh = WebSocket 多路复用**（Typert RPC over Gateway，`packages/api/gateway/src/client/stream-client.ts`），事件窗口 + **settle 语义**：瞬态流式块 `assistant/live-chunk` 在结算时被 `assistant/message` **原子替换**（`packages/api/session-controller/src/client/contract/events.ts`）——"正在生成"和"最终事实"是两个数据源、一次切换。
- 事件 schema（UI 消费的东西）：pi-web `lib/types.ts`（`ToolCallContent{toolCallId,toolName,input,rawInput}`、`ToolResultMessage{isError,content,details,durationMs,usage}`、`AgentUsage{input,output,cacheRead,cacheWrite,cost}`）；dsh `packages/core/session/src/types.ts:281` SessionEventMap（60+ 事件类型全集在 `known-event-types.ts`）。
- **长会话三板斧**（两项目等价实现）：
  1. 尾部窗口 + 向上翻页（pi-web `lib/chat-lazy-load.ts` 默认 50 条 + 顶部 sentinel；dsh `loadOlder()`/`loadThrough(seq)` **按 turn 对齐分页**）；
  2. 右侧导航轨（pi-web `ChatMinimap` 36px 轨道按 turn 定位 + hover 大纲预览；dsh `TurnNavigator` 用 @tanstack/react-virtual，刻度宽度=状态编码、busy 脉冲、hover 预览）；
  3. 滚动位置按会话记忆 + follow-tail 阅读策略（用户上滚即脱离跟随，出现"回到底部"按钮）。

---

## 2. UI 风格对比

### 2.1 定性与色板

| | pi-web | dsh |
|---|---|---|
| 基准字号 | **14px**，状态/meta 大量 10-12px | 正文 14px（用户可设 10-22px），次级 = 正文−1px |
| 分区手法 | 1px 边框 + 2px accent 指示条，**阴影只给真浮层**（下拉/对话框） | 0.5px 发丝描边画在 box-shadow 里 + 两层柔光（elevation 三件套）；暗色下柔光不可见、由描边承担分离 |
| 色板来源 | `app/globals.css:27-125` CSS 变量，**5 套主题**（light/dark/mist/rose/pine），经 Tailwind v4 `@theme` 映射 | Figma "deep suite" 设计平台导出，**三层 token**：`--dsw-static-*` 原语（neutral-bluish/deepseek/green/red/amber）→ `--dsw-alias-*` 语义（bg-layer-1..3/border-l1..l4/label-primary|secondary|tertiary/state-*）→ `--dsw-specific-*` 组件特化；**暗色 = 同一批别名整体重绑** |
| 关键色 | light：bg `#fff`、accent `#245bce`；dark：bg `#1a1a1a`、accent 降饱和为 `#a4c2f4` | 亮：bg `#fff`、link `rgb(65,118,230)`；暗：bg `rgb(21,21,23)`、link 提亮为 `rgb(122,170,255)`（注释明确为对比度） |
| 语义色 | **不走主题变量，硬编码原色 + rgba 透明度叠层**（绿 `#16a34a`=工具、红三级、琥珀=警告），两种底色通用 | 走 alias token：`state-success green-500 / error red-600(亮)|red-400(暗) / warn amber-500 / idle 中性灰` |
| 圆角 | 卡片 6-8px、气泡 12、composer 14 | 阶梯 token 4/8/12/16/20/28；支持引擎用 `superellipse(1.5)` 统一曲率 |

**共同点**（可直接定为工作台规范）：

1. **双字体规则**：机器产物（工具名/命令/路径/参数/时间戳/统计数字/diff 计数）一律等宽 11-12px；人读内容（正文/标签）比例字体 13-14px。dsh 的等宽栈**刻意不加裸 `monospace` 尾巴**，否则 Windows 中文回退宋体（`base.css:4-5`）。
2. **气泡只给用户消息**：右对齐圆角气泡标记"你说的话"；助手回答全宽无底色；过程内容是嵌入式小卡（1px 边框、极淡语义底色、无阴影）。
3. **状态 = 颜色 × 形状双通道**（pi-web `AgentSessionPanel.tsx:32-68`）：running=蓝+环形 spinner；completed=绿+实心圆打勾；failed=红+圆内 ×；aborted=琥珀+圆内方块。色盲可辨。
4. **暗色适配手法**：品牌/accent 色在暗色下**换档提亮/降饱和**而非原样照搬。

### 2.2 折叠行的视觉语法（dsh DisclosureRow，值得整体移植）

`packages/client/ui-primitives/src/DisclosureRow.tsx` 是全部过程行（工具/思考/上下文注入/压缩标记）的统一骨架：

1. **可折叠暗示 = 图标换 chevron**：静止时显示业务图标，hover 时业务图标淡出、向下 chevron 淡入（双 100ms 透明度过渡）；展开后固定为向上 chevron。整行可点，`role="button"` + `aria-expanded`。
2. **层级文字**：标题与摘要默认 `label-tertiary` 色，hover 升 `secondary`——"安静地待着，靠近才回应"。
3. **运行中**：`running` prop 驱动 TextShimmer 整行微光。
4. **状态由调用方叠加**：`data-state="error|stopped"` 只染摘要色，成功**不着色**（安静）。
5. 标题与摘要之间一枚 aria-hidden 装饰圆点，shimmer 扫过时跟着亮。

pi-web 的等价物：整标题行是 `<button>` + 行尾 10px chevron（展开 `rotate(180deg)`，transition 0.15s）；chevron 是全应用统一的折叠记号；失败时整卡换皮（红边 `rgba(248,113,113,0.45)` + 极淡红底 + 工具名变红）。

### 2.3 动效清单（克制，全部有 prefers-reduced-motion 降级）

| 动效 | 出处 | 用途 |
|---|---|---|
| **TextShimmer** | dsh `ui-primitives/src/TextShimmer.tsx` | "运行中"的标准语言：真实文本保留（可选中可访问），叠一份 inert 副本，105° 渐变 mask 从左向右扫（1.5s、`steps(48,end)` 阶梯化、0.3s 起步延迟）；高亮色亮=30%黑/暗=45%白 |
| pulse 呼吸 | pi-web globals.css | agent 运行状态行 opacity 1↔0.5（1.5s）——不抢焦点的"活着"信号 |
| **spinner 相位同步** | dsh `StateDot.tsx:27-45` | `syncSpinner` 把所有 CSS 动画 startTime 钉到文档时间零——页面上多个加载圈**永远同步旋转** |
| 状态行滑入 | pi-web notice-shelf-in/out | height+opacity+translate 三合一，退场 180ms |
| 统计弹层弹出+高光斜扫 | pi-web session-info-pop | 360ms 弹出 + 620ms 的 44% 宽 accent 斜扫 |
| 拖拽上传三重涟漪 | pi-web drop-ripple | 全应用最"奢侈"的动效，只出现在瞬时手势上 |
| 主题切换圆形扩散 | pi-web View Transitions API | clip-path 扩散擦除 |
| Turn 刻度 busy 脉冲 | dsh TurnNavigator | 跳转加载中刻度 1s 呼吸；刻度宽度本身是状态编码 |
| 逐字符上浮入场 | dsh AgentPresetSeat | preset 名称每字 40ms 淡入，CJK/Latin 双封顶 |
| 鲸鱼尾品牌动画 | dsh RunningWhaleTail | 仅 `no-preference + forced-colors:none` 下启用，否则静态线稿 |

**规范层**：dsh 用三档时长 token（0.1/0.2/0.3s）+ 统一曲线 `cubic-bezier(0.4,0,0.2,1)`；滚动行为在 reduce 下 smooth→auto；字号跟随用户设置整体缩放（`--dsh-content-font-delta`，所有行高/图标用 `calc(base + delta)`）。

### 2.4 主题与防闪烁

- pi-web：`useSyncExternalStore` + localStorage + prefers-color-scheme，`<html data-theme>`；防闪烁 init script 注入在 `app/layout.tsx:68-72`。
- dsh：**服务端把偏好内联进 HTML**——head CSS 先给画布底色（亮 `#fff`/暗 `#151517`），body 内联脚本在 React 挂载前设 `data-ds-dark-theme`（`boot-theme.ts:15-35`）。
- 焦点环：dsh 2px、**仅键盘模态显示**（`html[data-input-modality='pointer']` 时抑制）。

---

## 3. Agent 运行时配置界面（skills / MCP / 插件 / 子agent）

### 3.1 pi-web：左列表 + 右详情的 Settings 体系

**结构**（`components/SettingsPanel.tsx`）：全屏模态，顶部分区 tab = `general / models / skills / agents / plugins / mcp`（`lib/settings-navigation.ts`）；访问过的分区保持挂载（切 tab 不丢状态）。**所有分区共用一套 split-view 原语**（`components/SettingsUi.tsx`）：`ConfigPanelShell / ConfigSidebar(240px) / ConfigSidebarList / ConfigDetail / ConfigDetailGrid / ConfigField / ConfigFooter / ConfigSaveTarget / ConfigScopeTag / ConfigStatusDot`——每个分区的差异只在"列表怎么分组、详情放什么字段"。

**各分区内容：**

| 分区 | 列表 | 详情要点 |
|---|---|---|
| Skills | 按来源五组（project/skills.sh、project、global/skills.sh、global、path），**组头批量开关**（显示 enabled/total，失败逐条列出）；可更新行右侧 `↑` | scope 标签 + **Visible in prompt / Hidden** 开关（隐藏仍可显式调用）+ 版本区（hash + Check + Update）+ **Instructions 全文渲染**（剥 frontmatter）；安装走 skills.sh 搜索 + scope 二选一 |
| Agents（子agent profile） | 按 scope 分组（project/global/workspace/builtin），被覆盖时 `overridden` 标记 | 顶部全局行：内置子代理总开关 + **Max concurrent(1-32)**；详情字段：name/displayName/description/**systemPrompt(195px textarea)**/**tools 白名单 checkbox 网格**/Load skills/Load extensions/MCP 开关/codemode 三态/model + thinking + maxTurns/inheritContext/runInBackground；**保存目标实时显示落盘文件路径**；Duplicate/Delete/Save(✓ Saved 2s) |
| MCP | 按 scope 分组（全局/项目 mcp.json）+ Code mode 特殊行；状态点 tone（on/off/invalid/failed） | **Test 按钮真连一次**（成功且带工具清单时渲染工具 chip 列表 + read-only/exposure 标签）；URL/Command 自动判 transport；env/headers **只列名不列值**；**安全审计行**（会执行 shell 的字段与引用的主机变量以 chips 列出 + 警告）；exposure 下拉（direct/codemode/model-only…）+ 各工具覆盖计数；OAuth sign-in（授权 URL + 粘贴回调地址输入框） |
| Plugins | 按 scope 分组的包（npm/git/本地路径） | 描述/版本/安装路径 + **包内解析出的资源清单**（extensions 及 loaded/disabled）+ Check/Update/Reload/Remove |

**两个杀手级交互：**

1. **MCP 添加 = 单一粘贴框**（`components/McpAddServer.tsx`）：粘 URL/命令行/`claude mcp add …`/他人 JSON/安装链接，**浏览器端用与服务端同一 importer 即时解析**，然后展示**写入预览**：将保存的 transport/URL/command、env 名单、shell 字段与变量、importer 丢弃了什么、待补密钥输入框、重名建议名。空框给各格式示例一键填充。
2. **配置 → 装配闭环**（三段式）：配置层（Settings 管理"哪些能力存在、写到哪个文件"）→ 选择层（会话内工具预设 `/skill:` 斜杠）→ **装配结果预览层**（顶栏 ToolDefinitionsPanel + SystemPromptPanel，"模型此刻真正收到什么"）。整个配置 UI 的哲学：**每个值在保存前就知道会写到哪、谁在什么时候读到它**。

**生效模型（每面板明确标注）：**

| 变更 | 生效时机 | UI 表达 |
|---|---|---|
| Skills 启停 | 新会话 | 无 reload 按钮 |
| Agents profile / 插件启停 | 已开会话需 Reload | "Reload required" 状态 + Reload session 按钮 |
| MCP 服务器 | 之后创建的会话（会话创建时连接） | 无 reload（注释明确说明该模型） |
| 工具预设/模型/思考级别 | 当前会话即时 | RPC `set_tools`，并作为条目持久进会话日志 |

### 3.2 dsh：preset 固化 + 只读清单/管理面板分离

**Agent Preset（预设 = 一组插件组合 = 这个 agent 的能力包）：**

- 三个 UI 面（`packages/client/ui-agent-preset/src/client/index.ts` 文件头注释写得很清楚）：**Hero chip**（新会话屏选择，带介绍动效与拒绝横幅）/ **会话头只读标签**（运行中只显示不可改）/ **设置分区**（roster 卡片 + make default + "查看组合"只读浏览器）。
- **生效模型（关键设计）**：preset 在会话创建时**持久化进 SessionHeader.agentPreset**（`core/session/src/types.ts:130`，注释："resume 换组合会重放模型无法执行的历史"）；Host 拒绝给运行中会话换装；空白会话例外可同步（`syncBlankSession`）。**UI 的"提示"就是把控件放在事前位置（Hero），事后只读（头标签）——一个大部分时间不可用的控件就该放在它还能用的屏幕上。**

**插件两个面分离：**

- 设置弹窗内 = **只读清单**（插件卡片：模块名/加载相位 pending|loading|active|failed + StateDot/失败重试 + **Agent Preset 分组视图**："这个预设由哪些插件组成"）。
- 左栏独立管理面板 = 可安装（npm/URL，终端式安装输出）/启停/卸载；操作返回 `applied | restart-required`，后者弹 restart Toast。

**权限预设**：preset = **sandbox 模式 × 审批策略的命名捆绑**（`workspace-write: {sandbox: workspace-write, approval: ask}`…）；三个表面写同一条路径（composer 权限 chip、`/permission` 命令菜单、General 默认值）；**Full access 行带风险确认弹层**；运行中需审批时 ApprovalPanel 接管输入区（Enter 允许/Escape 拒绝）。

**子 agent 设置**：只有全局两张卡——委派上限（maxDepth 递归深度 / maxActiveSubagents 并发）+ **模型白名单**（enabled + allowedModels 精确路由，候选 = Host 模型目录 join 已存路由，目录变更自动刷新）。**没有每实例配置**——实例属性（label/mode）由会话事件携带。

**Skill**：多 provider 注册表（本地目录/插件内嵌/远端），每个 skill 带 `modelInvocable × userInvocable` 调用策略四象限；Web UI 只有发现（`/` 菜单）与预览（chip 打开 SKILL.md 右栏预览），**没有管理界面**。

**MCP**：**没有配置 UI**——Host 侧 yaml 组合配置；用户可见途径 = 工具增删通知行 / 普通工具卡 / Trajectory 的 request/header schema。

**表单基础设施**：所有配置卡共用**暂存式表单** `SettingsFormModel`（"用户输入先 staging，点保存才写；屏上所见=保存所存"，带 revision 乐观锁与冲突标记）；设置变更经 `settings/document-updated` 事件全端失效重读。

### 3.3 对比与取舍

| 维度 | pi-web | dsh | 工作台建议 |
|---|---|---|---|
| 配置形态 | 全功能 CRUD（每分区左列表右详情） | 观测为主 + 少量管理（preset/插件） | 接入方需要 pi-web 式 CRUD（能力装配页是 P1 核心），但**必须配 dsh 式"装配结果只读视图"** |
| 生效语义 | 每面板标注生效时机 + reload 按钮 | preset 固化进会话头，事前选择/事后只读 | 我们的运行时配置（app 级 `/setting/*`）应学 dsh：**快照落 run，事后可回放**——`ToolIndexSnapshotJSON` 等已在做，补 `system_prompt_snapshot` |
| 危险操作 | MCP 安全审计行（shell 字段 chips） | Full access 风险确认弹层 | 两者都抄：能力页标 riskLevel + 危险 preset 确认 |
| 添加 MCP | 单粘贴框 + 写入前预览 ⭐ | 无 UI | **直接抄粘贴框**：接我们的 `/react/mcp/*` 接口，预览层展示将写入的配置 |

---

## 4. 对 APP 工作台的落地映射

结合工作台方案的缺口（§2.3 六项 SDK 缺口、§4 关键设计点）：

| 工作台模块 | 直接借鉴 | 说明 |
|---|---|---|
| 调试工作台会话页（§4.3） | §1.2 turn 三段式 + §1.3 工具卡 | SDK 现状是"WorkBlock 非 live 默认折叠"但没有**过程组摘要头**（动词式"正在读取文件·path"）；可在宿主层对 `store.state.steps` 做分组派生，或长期改 SDK |
| Inspector 侧栏（§4.3） | §1.6 双视图 | **台账视图（Trajectory 式）是我们事件流的最自然消费**：`loadEvents`/`syncSessionEvents` 已有，加一个每事件一行的虚拟化表格（kind/时间/摘要/usage 累计）即达 dsh 八成功力 |
| 子 agent 泳道（§4.4） | §1.4 | 保留 SDK tab 泳道，补 dsh 式**会话头"N 个子智能体"计数按钮 + 下拉目录**（token/耗时/状态），点击切泳道或开右栏 |
| 能力装配页（P1） | §3.1 | pi-web 的 split-view 原语 + 每资源 scope 标签/状态点/批量开关；MCP 添加抄单粘贴框 |
| 装配快照（§4.1 P0） | §1.5 + §3.1 闭环 | `/app/assembly_preview` 的 UI 形态 = ToolDefinitionsPanel（工具 + 参数 schema 表）+ SystemPromptPanel（拼接结果）二合一 tab |
| 待办收件箱（§4.5） | dsh ApprovalPanel 键盘化 + StateDot | 收件箱行用 StateDot 五态 + Enter/Escape 快捷 |
| 会话观测页（P1） | §1.7 长会话三板斧 + TurnNavigator | 跨用户会话列表 + 点开只读回放（pi-web SubagentViewer 的**轮询不干扰**原则同样适用于观测视图） |
| 视觉规范 | §2 全部 | 等宽/比例双字体、状态颜色×形状双通道、边框分区、折叠行语法、TextShimmer、reduced-motion |

**必抄清单**（投入产出比最高）：turn 过程组折叠 + 动词摘要头；工具卡一行式（本地化标题/参数摘要/耗时/状态）；双视图（chat + 事件台账）；子 agent 目录树；配置写入前预览；DisclosureRow 视觉语法；TextShimmer。

**不建议抄**：pi-web 的无路由单页巨型 AppShell（我们用 Vue SPA + SDK mount）；dsh 的插件化 slot 架构（我们无此基建，直接组件化即可）；两项目的会话 fork/分支树（工作台观测场景非刚需，P2 以后再说）。

---

## 5. 关键文件速查

**pi-web**（`/Users/admin/Desktop/xm/pi-web`）：

- 布局：`components/AppShell.tsx`、`components/SessionSidebar.tsx`、`components/TabBar.tsx`
- 时间线：`components/ChatWindow.tsx`、`components/MessageView.tsx`（ToolCallBlock 1140-1307）、`lib/message-display.ts`、`lib/turn-views.ts`、`components/ChatMinimap.tsx`
- 子代理：`components/SubagentViewer.tsx`、`components/AgentSessionPanel.tsx`、`lib/session-family.ts`、`lib/subagent-extension.ts`
- 能力发现：`components/ToolDefinitionsPanel.tsx`、`components/SystemPromptPanel.tsx`、`components/ChatInput.tsx`（斜杠/@/预设）、`lib/tool-presets.ts`
- 配置：`components/SettingsPanel.tsx`、`components/SettingsUi.tsx`（split-view 原语）、`components/SkillsConfig.tsx`、`components/AgentsConfig.tsx`、`components/McpConfig.tsx`、`components/McpAddServer.tsx`、`components/PluginsConfig.tsx`
- 数据/流式：`lib/types.ts`、`lib/agent-event-stream.ts`、`lib/streaming-message.ts`、`hooks/useAgentSession.ts`
- 风格：`app/globals.css`（5 主题 CSS 变量 + 动效 keyframes）

**deepseek-harness**（`/Users/admin/Desktop/xm/deepseek-harness`）：

- 布局：`packages/client/ui-layout/src/client/AppFrame.tsx`、`packages/client/ui-sidebar-right/src/client/`
- 时间线：`packages/client/ui-chat/src/client/chat/`（ChatView/ChatGroupSeat/MessageItem/AssistantNodeView/ReasoningRow/ContextInjectionRow/TurnProcessNodeView/TurnTailNodeView/TurnNavigator/StatsPills）
- 工具卡：`packages/client/ui-tool/src/client/tool/`（ToolCallTree/ToolRow/toolviews/、models/tool-call-model.ts）
- 子代理：`packages/client/ui-subagent/src/client/`（SubagentHeaderLineage/sidebar-chat/）
- Trajectory：`packages/client/ui-trajectory/src/client/`（TrajectoryView/Table/Toolbar/Timeline/virtual-rows）
- 配置：`packages/client/ui-agent-preset/src/client/`、`packages/client/ui-settings-plugin-inventory/`、`packages/client/ui-plugin-manager/`、`packages/client/ui-settings-subagent/`、`packages/client/ui-permission-presets/`
- 数据/事件：`packages/core/session/src/types.ts`（SessionEventMap:281）、`known-event-types.ts`、`surface.ts`；`packages/client/ui-conversation/src/client/contract/records.ts`（ConversationNode）
- 原语与风格：`packages/client/ui-primitives/src/`（DisclosureRow/StateDot/TextShimmer/DiffBlock/TerminalBlock/JsonTree/icons 188 枚）、`packages/client/ui-theme/src/styles/`（design-platform.css 三层 token）
