# QQ 机器人 Adapter 详细设计（NapCat + OneBot 11）

> 定位：本文是 [多端服务形态接入方案](./多端服务形态接入方案.md) §4.5「QQ 机器人（adapter 形态）」的**落地实现设计**，覆盖：协议端选型、模块布局、身份与会话模型、消息协议映射表、会话/连接状态机、下行文本处理管线、配置、Compose 接入、实施排期。
> 配套阅读：[integration-guide.md](./integration-guide.md)（基座 WS/HTTP 协议）、[定时触发工作流实现方案.md](./定时触发工作流实现方案.md)（通知触达，P2 复用）。
> 状态：设计稿（未实施）。文中 NapCat 侧字段以部署时的 NapCat 版本文档为准；基座侧协议均已对照代码核实。

---

## 1. 结论与范围

**路线**：协议端用 **NapCat**（QQNT 协议端，实现 OneBot 11 事实标准），Adapter 用 Go 实现为基座仓库内的**独立二进制**（`cmd/qqbot`），Compose 内与基座同网部署。官方 QQ 开放平台 API 作为后续生产化选项——本设计的「QQ 平台对接层」收敛在 `onebot.go` 单文件内，未来可平行新增 `qqofficial.go` 而不动翻译层。

**MVP 范围（P0）**：
- 私聊全量触发；群聊 @ 机器人 / 引用机器人消息触发；
- 纯文本收发、markdown 降级、长回复分片；
- 群聊共享会话（scope=group）+ `/new` `/cancel` `/help` `/status` 命令；
- ask_question 降级为编号选项问答。

**明确不做（本期）**：图片入模（基座附件上传仅支持 csv/md/txt）、Plan 模式（qq-bot caller 提示词不引导，见 §10）、client 工具（该 caller 不注册即不可见）、流式逐条播报（QQ 无输入态，聚合后一次发送）。

---

## 2. 总体架构与模块布局

```text
                    QQ 服务器
                       ▲ QQNT 协议
        ┌──────────────┴───────────────┐
        │  NapCat（docker，独立容器）     │  OneBot 11 正向 WS :3001
        └──────────────┬───────────────┘
                       ▼ 事件上行 / 动作下行
        ┌──────────────────────────────┐
        │  qqbot Adapter（cmd/qqbot）    │  会话 FSM · 协议翻译 · 文本管线
        └───────┬──────────────┬───────┘
        WS :8080│react/ws      │ HTTP（附件上传/产物下载）
                ▼              ▼
        ┌──────────────────────────────┐
        │  react-base-service（基座）    │
        └──────────────────────────────┘
```

模块布局（「独立启动器 + service 包」惯例；**不依赖 MySQL**，配置/日志/WS 即可启动）：

```text
cmd/qqbot/main.go                # 启动器：读配置 → 起 NapCat WS 客户端 → 起基座连接池
service/qqbot/
  config.go                      # conf/mount/qqbot.yaml 加载与校验
  onebot.go                      # OneBot 11 客户端：事件收包、action 发送 + echo 匹配、重连
  baseclient.go                  # 基座 WS 连接池（lease 模型）+ HTTP 客户端（附件/产物）
  conversation.go                # 会话 FSM、conversationKey→sessionId 映射、命令解析
  inbound.go                     # QQ 消息段解析 → 触发判定 → ReactRunPayload 翻译
  outbound.go                    # 基座事件 → QQ 消息（聚合、markdown 降级、分片、节流）
  media.go                       # 图片/文件/产物搬运（下载、base64、upload_*_file）
deploy/compose/napcat/config/    # NapCat 配置持久化（登录态 + onebot11_<QQ>.json）
deploy/compose/conf/mount/qqbot.yaml   # 容器侧 adapter 配置（本机开发用 conf/mount/qqbot.yaml）
Dockerfile                       # +1 行：go build ./cmd/qqbot
```

会话映射（conversationKey → sessionId）MVP 存**内存**：adapter 重启后映射丢失，后续消息将新建会话（代价：丢上下文，不丢数据——基座历史仍在，可回放）。P1 可持久化到基座 Redis（复用现有连接）。

---

## 3. 身份与会话模型

### 3.1 基座身份机制（代码事实）

- 操作人身份来自 WS 握手 HTTP 头 `X-User-Name`（`middleware/auth.go`），**连接级**而非消息级；会话归属校验按 `(userName, callerKey, routeValues, type)`。
- 由此推论：一条基座 WS 连接上的所有 run 必须属于同一 userName。

### 3.2 决策 D1：固定基座身份 + adapter 内自治映射

Adapter 所有基座连接固定 `X-User-Name: qq-bot`；QQ 真实身份不进基座身份体系，而是：

- 由 adapter 维护 `conversationKey → sessionId` 映射（会话隔离由此保证）；
- 说话人身份放进 `llmContext`（随用户消息入模），模型可感知「谁在说」并可点名回复。

理由：① 允许小连接池服务全部用户（否则连接数 = 用户数）；② QQ 侧身份的可信边界本来就在 adapter（allowlist 门禁，见 §11），基座侧多一套用户体系无增益；③ 基座会话列表中所有 QQ 会话归在 `qq-bot` 名下，管理界面天然聚合。

代价：基座侧无法按自然人区分 QQ 会话（都挂在 `qq-bot` 下）——session/list 需靠 llmContext/sessionId 前缀辨别。可接受。

### 3.3 会话映射表

| 场景 | conversationKey | 触发时 llmContext.userId | sessionId 策略 |
|---|---|---|---|
| 私聊 | `p:{user_id}` | 发送者 QQ 号 | 每用户固定 1 个会话 |
| 群聊 scope=group（默认） | `g:{group_id}` | 发送者 QQ 号 + 昵称/群名片 | 每群固定 1 个（多人共享上下文） |
| 群聊 scope=user（可配） | `g:{group_id}:u:{user_id}` | 同上 | 每群每人 1 个（上下文隔离，防污染） |

`/new` 命令丢弃映射、下条消息新建会话。群聊 scope=group 时，模型收到的是多说话人混合上下文，llmContext 中每人带昵称，提示词（§10）要求模型区分说话人。

### 3.4 命令协议

命令仅在消息**通过触发判定**（§5.1）后解析；群聊需 @ 才算，私聊直接识别。

| 命令 | 范围 | 行为 |
|---|---|---|
| `/new` | 全部 | 丢弃当前会话映射，回复「已开启新对话」 |
| `/cancel` | 全部 | 若有活跃 run：发 `cancel` 上行；否则回复「当前没有进行中的任务」 |
| `/status` | 全部 | 回复连接状态 + 当前会话轮数 + 活跃/等待状态 |
| `/help` | 全部 | 回复能力说明与命令列表 |

---

## 4. NapCat 接入

### 4.1 连接模式选型：正向 WS

NapCat 起内置 WS **服务端**（:3001），adapter 作为 WS 客户端拨入。理由：adapter 无需发布端口（compose 内纯出站连接），故障恢复语义简单（client 重连）。反向 WS（NapCat 反拨 adapter）作为备选，不改协议翻译层。

鉴权：onebot11 配置里设 `token`，adapter 连接时带 `Authorization: Bearer <token>` 头（OneBot 11 标准），与 `accessToken` 配置对应。

### 4.2 使用的事件/动作子集

**事件（NapCat → adapter）**，`messagePostFormat: "array"`（段数组，不解析 CQ 码字符串）：

| post_type | 类型 | 用途 |
|---|---|---|
| `message` | `message_type=private` | 私聊消息（触发） |
| `message` | `message_type=group` | 群消息（按 @/引用判定触发） |
| `meta_event` | `lifecycle`（connect） | 连接建立确认 |
| `meta_event` | `heartbeat` | 判活（interval 超时视为断连） |
| `notice` | `group_upload` | 群文件上传（P1：下载→附件入模） |

**消息段（message 数组元素）**：

| 段 type | data 关键字段 | adapter 处理 |
|---|---|---|
| `text` | `text` | 原样拼接 |
| `at` | `qq` | `qq==self_id` → 置位「被 @」；其他（@别人）→ 转文本 `@昵称` |
| `reply` | `id` | 经 `get_msg` 取被引用消息；引用的是 bot 消息 → 置位「引用 bot」；引用文本作为上下文并入 llmContext |
| `image` | `url`、`file` | P0：占位 `[图片]`，url 记入 llmContext.images；P1：经基座附件通道/HTTP 工具入模 |
| `face` | `id` | 转占位 `[表情]` |
| `record` / `video` | — | 占位 `[语音]`/`[视频]`（P0 不处理） |
| `file`（NapCat 扩展） | `file_id`/`name`/`size` | 群聊文件；P1 下载后走 `/api/chat/files/upload` |
| `json` / `forward` | — | 占位 `[卡片]/[合并转发]`，MVP 忽略 |

**动作（adapter → NapCat）**，请求-响应经 `echo`（uuid）匹配，超时 10s：

| action | params | 用途 |
|---|---|---|
| `send_group_msg` | `{group_id, message:[segments]}` | 群回复（首片前带 `{type:"at"}` 指向发送者） |
| `send_private_msg` | `{user_id, message:[segments]}` | 私聊回复 |
| `get_msg` | `{message_id}` | 引用消息还原 |
| `upload_group_file` / `upload_private_file` | `{group_id|user_id, file, name}` | 长代码/产物转文件发送；`file` 支持 `base64://` 与 http URL（NapCat 扩展） |
| `get_login_info` | `{}` | 启动自检（取 self_id，与配置比对） |

### 4.3 上线登录流程

NapCat 首次启动需登录 QQ：宿主机访问 WebUI（compose 映射 :6099）→ 扫码 → 登录态写入挂载卷 `deploy/compose/napcat/config/`，此后容器重启免扫码，可将 6099 端口映射移除。使用**专用小号**（封号风险见 §11）。

---

## 5. 消息协议映射表

### 5.1 上行：QQ → 基座

**触发判定**（`trigger` 配置）：

| 场景 | 判定 | 动作 |
|---|---|---|
| 私聊 | 任意消息 | 触发（命令优先） |
| 群聊 | 段中含 `at(self_id)` **或** `reply` 指向 bot 消息 | 触发；剥离 at 段，剩余文本为 userPrompt |
| 群聊 | 均不满足；若配置 `keywords` 且命中前缀 | 触发（默认不配关键词，防刷屏） |
| 群聊 | 其余 | 忽略（debug 日志） |

**翻译结果**（WS 信封 `{type:"run", payload:{...}}`）：

```jsonc
{
  "callerKey": "qq-bot",
  "routeValues": [],                      // P1 可用 routeValues 区分私聊/群聊提示词
  "type": "chat",
  "userPrompt": "<剥离 @ 后的正文>",
  "sessionId": "session_xxx",             // conversationKey 映射；无则新建并在回执事件记录
  "llmContext": {
    "channel": "qq",
    "user": { "userId": 123456, "nickname": "小明", "groupCard": "小明(产品)" },
    "group": { "groupId": 789, "messageId": 10086, "scope": "group" },
    "quoted": { "text": "...", "byBot": true },   // 引用上下文（有则带）
    "images": ["https://..."],                     // P0 仅告知，不入模
    "textNote": "本条消息来自 QQ 群聊，回复将群发，请控制长度"
  }
}
```

群发场景在 llmContext 注入 `textNote`，配合系统提示词控制回复风格（短、少代码块）。

### 5.2 下行：基座事件 → QQ 消息

基座事件全集逐一处理（未列出的忽略并 debug 日志）：

| 基座事件 | 处理 | QQ 侧表现 |
|---|---|---|
| `run`（回执） | 记录 sessionId（新建会话时）、runId | — |
| `thought_start/delta/end` | 忽略（reasoning 不外发） | — |
| `content_delta` | 缓冲聚合 | — |
| `content_end` | 进入文本管线（§7）→ 分片发送 | 群聊首片前置 at 发送者；回复正文 |
| `tool_use_start`（name=ask_question） | 从 `toolInput.questions` 渲染编号选项（§5.3），FSM → waiting_answer | 「请回复编号或直接输入」 |
| `tool_use_start`（其余） | 进度摘要，节流：首个工具调用发送一次，其后 30s 聚合一次 | `[⚙ 正在调用 web_search…]`（可配开关） |
| `tool_use_end`（isError=true） | 拼入最终回复末尾附注 | 「（工具 xx 执行失败：原因）」 |
| `done` | 冲刷缓冲；FSM → idle；若队列有积压消息则立即发起新 run | — |
| `error` | 映射友好文案（`errMsg` 脱敏：只透出可读部分，堆栈/内部地址不出） | 「出错了：……」 |
| `cancelled` | FSM → idle | 「本轮已取消」 |
| `compact_*` / `todo_update` / `model_fallback` | 忽略（日志记录） | — |
| `steer_guided` | 轻提示（可配静默）：「已并入当前任务」 | 群聊插话被注入活跃 run |
| `steer_queued` | 轻提示：「已排队，将在本轮结束后处理」 | run 活跃且不可引导（如 ask_question 等待期） |
| `steer_rejected` | 提示「本轮暂不能接收新消息，稍后再试」（带 reason 日志） | 软着陆/带附件等准入拒绝 |
| `steer_drained` | 忽略（日志） | 排队项被晋升为新 run（S2/S3） |
| `heartbeat` | 忽略 | — |
| `client_tool_use_start` | **不应出现**（qq-bot caller 不注册 client 工具）；防御：自动 cancel + 提示 | 「该能力在 QQ 端不可用，已终止本轮」 |

### 5.3 ask_question 编号选项协议

`ask_question` 服务端工具会阻塞 run 等待 `tool_use_answer` 上行。Adapter 渲染与回填：

```text
【请确认】
1. 导出 CSV
2. 导出 Markdown
3. 都要
回复编号（多选用逗号分隔），或直接输入其他要求。
```

```jsonc
// tool_use_answer 上行
{ "type": "tool_use_answer", "payload": {
  "toolUseId": "<ask_question 的 toolUseId>",
  "content": { "answers": [ { "questionId": "q1",
                "selectedOptionIDs": ["opt_2"], "freeText": "" } ] }
} }
```

解析规则：消息为纯数字/数字逗号组合 → 选中项；其余文本 → `freeText`（自由回答）；`跳过` → `skipped: true`。waiting_answer 状态下收到非答案消息（如 `/cancel`）按命令处理。

---

## 6. 状态机

### 6.1 会话 FSM（每个 conversationKey 一个实例，内存对象）

```text
                       msg(触发) / cmd(/new 除外)
        ┌──────────┐ ──────────────────────► ┌──────────┐
        │   idle   │                         │ running  │◄──── run 进行中
        └──────────┘ ◄────────────────────── └────┬─────┘
             ▲        done / error / cancelled    │
             │                                    │ tool_use_start(ask_question)
             │            ┌────────────────┐      ▼
             └─────────── │ waiting_answer │◄────┘? 不：直接迁移
                answer 回填│                │──► running（回发 tool_use_answer 后）
                          └────────────────┘
```

转移表（事件 × 当前态）：

| 当前态 \ 事件 | 触发消息 | 普通消息 | ask_question 事件 | 答案消息 | 终态事件(done/error/cancelled) | `/cancel` | 超时(max_run_minutes) |
|---|---|---|---|---|---|---|---|
| **idle** | 建 run → running | 视为触发消息同左 | —（防御：日志） | 忽略 | — | 回复「无进行中任务」 | — |
| **running** | — | busy 策略（见下） | → waiting_answer | 忽略 | 发送结果；队列非空则出队建 run → running，否则 → idle | 发 cancel 上行，等终态事件 | 发 cancel + 超时提示 |
| **waiting_answer** | — | 若形如编号 → 视为答案；否则按普通消息入队 | —（已在此态） | 回填 tool_use_answer → running | 清除提问态 → idle | 发 cancel → idle | 发 cancel → idle |

**busy 策略（running 态收到普通消息）**——优先依赖基座 Steering，本地队列退化为兜底：

- **steering 开启（推荐，custom.yaml 配 `steering.enabled` + `steering.queue`）**：直接把消息作为 `run` 上行发到该会话的 lease 连接。可引导时被准入为 guide 注入当前 run（群聊插话补充信息的自然语义）；不可引导（如 ask_question 等待期）时排队，run 结束由服务端 S2 自动续跑消费。回执事件 `steer_guided`/`steer_queued` 映射为轻提示（或静默），`steer_rejected` 提示「稍后再试」。**adapter 不再做 latest-wins 覆盖**——保留全部输入交给服务端 FIFO 账本。
- **steering 关闭（兜底兼容）**：本地队列深度 1、最新覆盖旧（latest-wins），回执提示「正在处理上一条，你的消息已记下」；run 结束时若队列有消息，用同一会话连续发起新 run。
- 群聊 scope=group 下 guide 注入是多人协同一大利器：A 提问后 B 补充约束，B 的话会进 A 的 run 而不是被拒绝。

**超时收割**：每会话定时器，`max_run_minutes`（默认 15）超时自动 cancel 并提示；防止模型循环工具调用无限占用群会话。

### 6.2 基座 WS 连接池（lease 模型）

基座约束（对照代码核实的准确口径，integration-guide §2.1 的旧描述已过时）：

1. **连接层串行执行**（`controllers/http/react/ws.go`）：一条连接同一时刻最多只有一个**执行中**的 run——run 活跃期间（`runMsgCh != nil`）收到第二条 `run` 上行**不会并行开跑**，而是转入 steering 准入（`HandleWSSteer`），回执 `steer_guided`/`steer_queued`/`steer_rejected` 事件；steering 未开启（默认）时保持旧的 `react run is active` 错误。
2. **会话层事务级互斥**（`service/react/runtime.go` `createReactRunContext`）：真正的硬互斥在 session 级——事务内 session 行锁 + `HasActiveReactRun` 检查，**跨连接**也不可能同一会话双 run。
3. **Steering（S1/S2/S3）**：开启后，run 活跃期间的新消息按可引导性分流——running 且纯文本 → guide 注入当前 run（下一个模型步边界生效）；waiting/软着陆/带附件 → 排队（`steer_queued`），run 结束由 S2 自动续跑晋升消费；另有 `queue_send` 上行可显式晋升排队项。

对连接池的含义不变：**一条连接串行执行 run，池大小即并发 run 上限**；且 steer 消息应发到承载该会话活跃 run 的 lease 连接（事件流归一，虽然跨连接准入也能成功——`createReactRunContext` 内同样有准入路径）。Adapter 维护连接池：

```text
conn 状态：free ──lease──► leased(run) ──run 终态──► draining(等事件冲刷) ──► free
                                     │
                                     └──连接错误──► dead → 重建
```

- 池大小 `pool_size`（默认 3）：即同时并发的 run 上限；第 N+1 个会话申请连接时进入等待队列（带 10s 超时），超时回复「当前繁忙，稍后再试」。
- 事件与连接绑定：run 的全部事件只在其 lease 的连接上出现，会话 FSM 监听该连接直到终态。
- 连接空闲超过 `idle_reconnect_sec`（默认 10min）主动重建，避免被中间设备静默断连。
- **协议层 ping/pong**：基座 5s 协议级 ping，Go WS 客户端必须显式回 pong（浏览器自动回，Go 不是）——实现时 `SetPingHandler` 同连接回写 `PongMessage`；应用层 `heartbeat` 事件仅作参考。NapCat 侧同理，以 heartbeat 事件超时判活。

### 6.3 NapCat 连接状态机

```text
down ──dial──► connecting ──on_open──► ready ──连接错误/heartbeat 超时(>60s)──► down
   ▲                                        │ lifecycle(connect) 验 self_id
   └──────────── 退避重连 1s/2s/5s/10s/30s(封顶,加抖动) ◄─────────────────────┘
```

NapCat 断连期间到达的 QQ 消息**天然不丢**（腾讯侧缓存离线消息，重连后推送）；断连期间用户发送且 adapter 未收到的不做补偿。重连成功后回执一条「已恢复」到活跃会话（可选，防「为什么不理我」）。

---

## 7. 下行文本处理管线

```text
content_end(payload.content)
   │
   ▼
[1] Markdown 降级 ──► [2] 长代码块转文件 ──► [3] 分片 ──► [4] 发送节流 ──► QQ
```

**[1] Markdown 降级规则**（QQ 无 markdown 渲染，NTQQ 部分端 markdown 消息需模板权限，MVP 纯文本）：

| 语法 | 降级 |
|---|---|
| 标题 `#` | `【标题】` |
| 粗体/斜体 | 去符号保文字 |
| 链接 `[t](u)` | `t: u` |
| 无序列表 | `• ` |
| 表格 | 按 `|` 保留为文本（QQ 等宽显示可接受） |
| 行内代码 | 去反引号 |
| 代码块 | 见 [2] |

**[2] 长代码块**：超 `code_to_file_lines`（默认 40 行）→ 上传为 `snippet_<n>.<ext>` 文件（`upload_group_file`/`upload_private_file`，内容 base64），原位替换为一行「📄 完整代码见文件 snippet_1.py（N 行）」。

**[3] 分片**：`chunk_size`（默认 1500 字符，经验值：QQ 单条约 4500 字节上限，留余量）；优先在 `\n\n` 边界切分，单段超长再按句/硬切；代码块内不切（已被 [2] 处理）。每片序号 `(1/3)`。

**[4] 发送节流**：片间 800ms（防 QQ 风控合并/禁言）；发送失败（retcode≠0）重试 2 次（间隔 2s），仍失败降级为「回复过长，已存 COS：<链接>」；`send_*_msg` 返回 `message_id` 记日志（便于撤回/排障）。

**产物回传**：正文/`tool_use_end.meta` 中出现基座产物链接（`/react/artifact/:id`）→ adapter 经 HTTP 下载（backend 内网直连基座）→ 图片类（png/jpg）转 image 段直接发送；文件类转 `upload_*_file`。链接本体始终以文本附上（供电脑端下载）。

---

## 8. 配置设计（conf/mount/qqbot.yaml）

```yaml
qqbot:
  napcat:
    ws_url: "ws://napcat:3001"        # NapCat OneBot11 正向 WS
    access_token: ""                   # NapCat token（建议设置）
    self_id: 0                         # 机器人 QQ 号；0=启动时 get_login_info 自动获取
    heartbeat_timeout_sec: 60
  base:
    ws_url: "ws://service:8080/react-base-service/react/ws"
    http_url: "http://service:8080/react-base-service"
    user_name: "qq-bot"                # 基座侧固定身份（§3.2）
    caller_key: "qq-bot"
  session:
    group_scope: "group"               # group | user（§3.3）
  trigger:
    group_keywords: []                 # 非 @ 触发关键词，默认空
  policy:
    pool_size: 3
    max_run_minutes: 15
    lease_wait_timeout_sec: 10
    queue_depth: 1                     # 固定 latest-wins，保留字段
    busy_notice: "正在处理上一条，你的消息已记下，稍后处理"
    progress_notice: true              # 工具调用进度摘要开关
    progress_interval_sec: 30
  output:
    chunk_size: 1500
    send_interval_ms: 800
    code_to_file_lines: 40
  security:
    groups_allowlist: []               # 空=不限制；建议生产配置群号白名单
    users_allowlist: []                # 私聊白名单
    per_user_rate: "10/m"              # 每用户频控
```

加载方式与基座一致（`env.LoadConf` 读 `conf/mount/`），支持环境变量覆盖敏感项（`QQBOT_NAPCAT_ACCESS_TOKEN` 等）。

---

## 9. Compose 接入

### 9.1 Dockerfile（builder 阶段追加一行）

```dockerfile
RUN go build -o /usr/local/bin/react-base-service main.go
RUN go build -o /usr/local/bin/repo-mcp ./cmd/repo-mcp
+ RUN go build -o /usr/local/bin/qqbot ./cmd/qqbot
```

### 9.2 docker-compose.yml 追加两个 service

```yaml
  # ===== QQ 机器人：NapCat 协议端 =====
  napcat:
    image: mlikiowa/napcat-docker:latest   # 上线前固定具体版本号
    container_name: react-base-napcat
    # 需公网与 QQ 服务器通信 → backend 网可出公网；不与沙箱网接触
    networks:
      - backend
    environment:
      - ACCOUNT=${NAPCAT_ACCOUNT}           # 机器人 QQ 号
      - WEBUI=true                          # 首次扫码登录用，稳定后可关
    ports:
      - "${NAPCAT_WEBUI_HOST_PORT:-6099}:6099"   # 仅登录期需要，之后移除
    volumes:
      # 登录态 + onebot11_<QQ>.json 持久化；重启免扫码
      - ./deploy/compose/napcat/config:/app/napcat/config
    restart: unless-stopped

  # ===== QQ 机器人 Adapter（与 service 共镜像，不同启动命令）=====
  qqbot:
    build:
      context: .
    command: ["/usr/local/bin/qqbot"]
    container_name: react-base-qqbot
    networks:
      - backend            # 出站连 napcat:3001 与 service:8080；不发布任何宿主机端口
    depends_on:
      service:
        condition: service_healthy
    volumes:
      - ./deploy/compose/conf/mount:/usr/local/bin/conf/mount:ro
      - service_logs:/usr/local/bin/log
    restart: unless-stopped
```

### 9.3 NapCat onebot11 配置（随卷预置）

`deploy/compose/napcat/config/onebot11_<QQ号>.json`（字段名以 NapCat 版本为准，WebUI 等价生成）：

```jsonc
{
  "network": {
    "websocketServers": [
      { "name": "react-base", "enable": true, "host": "0.0.0.0", "port": 3001,
        "messagePostFormat": "array", "reportSelfMessage": false,
        "token": "${NAPCAT_TOKEN}", "debug": false }
    ]
  },
  "enableLocalFile2Url": true   // 图片/文件自动转 URL，adapter 才能拿到可下载地址
}
```

### 9.4 首次上线步骤

1. `.env` 填 `NAPCAT_ACCOUNT`、`NAPCAT_TOKEN`；
2. `docker compose up -d --build napcat` → 浏览器开 `http://127.0.0.1:6099`（WebUI token 在 napcat 容器日志）→ 扫码登录；
3. 执行 §10 基座侧一次性引导（caller / apikey / system-prompt）；
4. `docker compose up -d --build qqbot` → 看 qqbot 日志确认 `lifecycle connect` + `get_login_info` 自检通过；
5. 私聊机器人发「你好」验收；稳定后从 compose 移除 6099 端口映射。

### 9.5 网络拓扑说明

- `qqbot` 只加入 `backend` 网（可出公网、可达 service/napcat），**不加入 `sandbox-net`**，不发布宿主机端口——QQ 链路整体不扩大基座攻击面；
- NapCat 与 QQ 服务器的连接走公网，属预期；NapCat 无需访问 mysql/redis（backend 网内可见性同 searxng，可接受）。

---

## 10. 基座侧一次性引导

```bash
BASE=http://127.0.0.1:8080/react-base-service

# 1. Caller（allowPlan=0：纯文本端关闭 create_plan 与 executionMode=plan）
curl -X POST $BASE/caller/register -H 'Content-Type: application/json' -d '{
  "callerKey": "qq-bot", "name": "QQ机器人", "platform": "qq", "allowPlan": 0,
  "description": "NapCat/OneBot11 接入" }'

# 2. API Key（模型凭证，与 playground 同源）
curl -X POST $BASE/apikey/register -H 'Content-Type: application/json' -d '{
  "callerKey": "qq-bot", "routeValues": [], "name": "默认", "apiKey": "<LLM网关key>" }'

# 3. 系统提示词（IM 风格，routeValues 可再按群拆分）
curl -X POST $BASE/system-prompt/register -H 'Content-Type: application/json' -d '{
  "callerKey": "qq-bot", "routeValues": [], "name": "IM提示词", "content": "<见下>" }'
```

提示词要点（正文按需撰写）：① 输出为 IM 短段落，单次回复尽量 < 500 字，重要结论先行；② 尊重 llmContext 中的说话人身份，群聊中可点名回复；③ 不输出 markdown 表格/多级标题（QQ 不渲染）；④ 代码块必须给语言标注且尽量短，长代码交给 adapter 转文件；⑤ 产物/文件给链接即可。

**强烈建议同时开启 Steering**（custom.yaml，默认关闭）：

```yaml
llm:
  react:
    steering:
      enabled: true   # S1：run 活跃期间新消息 guide 注入（steer_guided）
      queue: true     # S2：不可引导时排队，run 结束自动续跑（steer_queued）
```

QQ 群聊「run 进行中插话」是高频场景，steering 开启后 adapter 的 busy 处理从本地 latest-wins 队列升级为服务端语义（§6.1）。

**Plan 能力关闭（已实现）**：`allow_plan` 支持 caller 级开关——注册 qq-bot caller 时传 `"allowPlan": 0`（或 `/caller/update` 修改）。效果：create_plan 工具对该 caller 不可见 + `executionMode: "plan"` 入口直接拒绝（存量 WAIT 计划的 resume/cancel 不受影响）。不再需要提示词约束规避。

---

## 11. 安全与治理

| 面 | 措施 |
|---|---|
| NapCat 接入鉴权 | onebot11 `token` + adapter 校验事件 `self_id` 与配置一致，防伪造事件 |
| 使用范围 | `groups_allowlist` / `users_allowlist`，生产必配群白名单 |
| 频控 | `per_user_rate`（令牌桶），群聊另加全局发送节流（§7[4]） |
| 基座暴露面 | adapter 不发布端口；基座仍免鉴权，但 QQ 链路不新增公网入口 |
| 提示注入 | QQ 消息正文即用户输入，经基座标准 run 通道；工具白名单按 qq-bot caller 最小化配置 |
| 账号风险 | **NapCat 用普通 QQ 号模拟客户端，存在风控/封号风险**：专用小号、避免高频主动消息、新号先养；风险由使用者自担 |
| 内容合规 | 群聊为半公开场景，qq-bot 提示词加「不生成违规内容」约束；产物链接含签名时注意不要被模型复读到群里 |

---

## 12. 实施排期与测试

**P0（可跑通的最小闭环，预计 2~3 天）**
- [ ] `cmd/qqbot` + `service/qqbot` 骨架、配置加载
- [ ] `onebot.go`：正向 WS、echo 匹配、重连、heartbeat 判活
- [ ] `baseclient.go`：连接池 + ping/pong + 单 run 生命周期
- [ ] `inbound.go`：私聊/群@触发、段解析、ReactRunPayload
- [ ] `outbound.go`：聚合、markdown 降级、分片、节流、at 前置
- [ ] FSM（idle/running/waiting_answer）+ 命令 `/new` `/cancel` `/help` `/status`
- [ ] compose 接入 + Dockerfile 构建 + NapCat 配置模板

**P1（体验补齐）**
- [ ] ask_question 编号选项协议；进度摘要；busy 队列提示
- [ ] 长代码转文件；产物图片/文件回传
- [ ] 群文件/图片入模（下载→`/api/chat/files/upload`→attachments；超类型限制则记 llmContext）
- [ ] 会话映射 Redis 持久化；allowlist/频控

**P2（增值）**
- [ ] 基座通知 webhook 消费：run 完成/巡检结论/定时工作流 → QQ 主动消息（复用定时触发方案）
- [ ] 引用消息续聊（reply 指定历史轮次 → 定位会话）；routeValues 按群差异化提示词
- [ ] prometheus 指标（收发量、run 时长、失败率）接入现有监控栈

**测试策略**：单测覆盖分片器/markdown 降级/FSM 转移表（纯函数）；`tests/` 增加假 OneBot 服务端（WS mock，按事件脚本回放）做 adapter 层 E2E；真实链路用小号 + 测试群手工验收。

---

## 13. 开放问题

1. **图片入模**：附件上传仅 csv/md/txt（≤50MB）。图片若要入模需基座扩展附件类型或加「按 URL 拉取图片并描述」的 http/mcp 工具——影响面在基座，单独立项。
2. **回复时效**：QQ 无流式通道，长 run（>30s）靠进度摘要缓解；若要更好体验，可评估 NTQQ markdown 模板消息（需申请权限）。
3. **群聊上下文污染**：scope=group 默认共享，噪音大群建议 scope=user；是否做「@ 指定历史消息续聊对应会话」待真实使用反馈。
4. **官方 API 迁移**：翻译层已收敛（inbound/outbound 与 onebot.go 单向依赖），迁移官方 WebHook 时仅替换 onebot.go 与事件入口，映射表 §5 结构不变。
