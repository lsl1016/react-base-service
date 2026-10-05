# VSCode 插件接入方案（Chat Participant + Webview 混合形态）

> 定位：本文是基座「IDE 端」接入形态的设计方案——将 react-base-service 以 VSCode 扩展的形式接入开发者工作台，属于 [多端服务形态接入方案](./多端服务形态接入方案.md) 所述多端体系的一个新端形态（与 QQ 机器人 Adapter 同构：端侧薄客户端 + 基座运行时）。
> 配套阅读：[integration-guide.md](./integration-guide.md)（WS/HTTP 协议细节）、[QQ机器人Adapter详细设计.md](./QQ机器人Adapter详细设计.md)（同类端形态落地先例）、[web/sdk/README.md](../web/sdk/README.md)（现有 TS SDK）。
> 状态：设计稿（未实施）。撰写日期：2026-10-05。

---

## 1. 结论与范围

**路线**：插件为**纯 TypeScript 客户端扩展**，基座保持独立部署（本地或远程）不动。推荐**原生 Chat Participant 对话 + Webview 复用现有 SDK UI** 的混合形态，代码放在仓库内新增 `vscode/` 独立工程，连接协议复用 `web/sdk` 拆出的 core 层。

**为什么插件不承载基座**：基座是常驻 Go 进程，依赖 MySQL / Redis / Python 沙箱 / 对象存储，且有会话互斥、后台任务、MCP 连接管理等进程内状态——塞进 VSCode 扩展宿主（Extension Host）既不现实也无意义。插件的职责遵循多端方案的边界定义：**身份、界面、端侧能力执行、触达通道**。

**插件的增量价值**（相对 playground 网页）：

1. 把 `/react-base-service/react/ws` 这条 WS 入口和 HITL 交互带进 VSCode 原生聊天面板；
2. **Client Tool 从「浏览器执行」升级为「VSCode 执行」**（打开文件、diff 预览、终端执行、问题定位）；
3. 把 VSCode 的编辑器上下文（选中代码、当前文件、诊断）作为输入喂给 agent；
4. 把基座的 git worktree 代码工作区（`ws_*` 检索工具）与用户正在编辑的仓库对接。

**MVP 范围（P0）**：

- Chat Participant `@react-agent`：流式对话、工具执行进度、取消、`/plan` `/react` 子命令；
- HITL 三件套（ask_question / tool_confirm / client tool 回包）在 chat 内可交互；
- 配置与连接测试（baseUrl / callerKey / routeValues / 用户名）。

**明确不做（本期）**：基座进程托管与生命周期管理（不打包、不拉起服务）、远程开发容器场景适配、团队级鉴权体系（沿用现状，见 §12 风险）。

## 2. 形态选型

| 形态 | 做法 | 成本 | 功能覆盖 | 体验 |
|---|---|---|---|---|
| A. Webview 套壳 | panel webview 加载现有 `@react-agent-web-sdk` UI | 约 1-2 周 | 全部（HITL / 计划卡片 / 产物 / 回放均现成） | 差：网页嵌入，无编辑器集成 |
| **B. 原生 Chat + Webview 混合（推荐）** | 对话走 Chat Participant，HITL 用原生交互，管理面 / 回放用 webview 复用 | 约 4-6 周 | 全部 + 编辑器集成 | 好：原生体验，吃进 VSCode 上下文 |
| C. 仅注册 MCP Server | 把基座暴露为 MCP server 供 VSCode / Copilot 调用 | 约 1 周 | 仅工具调用：ReAct 循环、会话、记忆、HITL 全部丢失 | 受宿主 chat 支配 |

**选 B 为主线，C 作为补充**：`mcpserver` 已是 stdio JSON-RPC 框架，未来加 HTTP/SSE 传输变体即可把基座工具暴露给任意 MCP 客户端，与 B 不冲突；A 是 B 的子集（B 的 webview 部分就是 A）。

## 3. 总体架构与模块布局

```text
  ┌──────────────────────────────────────────────────┐
  │ VSCode 扩展宿主（Node）                            │
  │                                                  │
  │  Chat Participant @react-agent    Tree View 会话  │
  │        │ 事件流翻译                 │ 列表/重命名/分叉│
  │        ▼                          ▼              │
  │  ┌──────────────┐   ┌──────────────────────┐     │
  │  │ connection/   │   │ webviews/（回放/管理面）│    │
  │  │ SDK core 复用 │   │ 复用 embed 页面/SDK UI │     │
  │  └──────┬───────┘   └──────────┬───────────┘     │
  │         │ clientTools/（VSCode 版执行）           │
  └─────────┼──────────────────────┼─────────────────┘
            │ WS :8180/react-base-service/react/ws
            │ HTTP（会话/附件/产物/注册管理）
            ▼
  ┌──────────────────────────────────────────────────┐
  │        react-base-service（基座，独立部署）          │
  └──────────────────────────────────────────────────┘
```

工程结构（与 `web/sdk` 同级，CI 增加第三个 job；与 SDK 共享协议类型最方便）：

```text
vscode/
├── package.json          # 扩展清单：chat participant、命令、配置、视图容器
├── src/
│   ├── extension.ts      # 激活入口
│   ├── connection/       # WS 连接管理（复用 SDK core，见 §4）
│   ├── chat/             # Chat Participant：基座事件流 → ChatResponse 翻译层
│   ├── hitl/             # ask_question / tool_confirm / client tool 的 VSCode 交互
│   ├── clientTools/      # VSCode 版 client tool 实现（见 §7）
│   ├── sessions/         # 会话 Tree View：列表、重命名、分叉
│   └── webviews/         # 回放页 / 管理面 webview
```

## 4. 连接层：从 Web SDK 拆出 core

`web/sdk` 内部已是 `protocol / client / session / runtime / storage / ui` 分目录，但入口绑死 SolidJS UI 与浏览器环境（IndexedDB、`browser.ts`）。做法：

- SDK `package.json` 增加 `exports`，拆出 `@react-agent-web-sdk/core` 入口（protocol + client + session，零 DOM 依赖），UI 层照旧；
- 扩展宿主为 Node 环境，WebSocket 用 `ws` 包；storage 换内存实现，扩展侧用 `globalState` 持久化会话索引；
- 该步对现有 SDK 是**纯重构不动行为**，由现有 SDK 测试（`npm run test:run`）保护回归。

兜底选项：不动 SDK，扩展内自带一份 WS 协议 client（协议见 integration-guide）——代价是两处维护，仅在 SDK 拆分受阻时采用。

## 5. 对话通道：Chat Participant

注册 `@react-agent` participant（`vscode.chat.createChatParticipant`，API 已稳定，无需 enabledApiProposals）：

- **上行**：用户消息 → SDK core 建 WS run（callerKey / routeValues / `X-User-Name` 来自配置与 SecretStorage）；
- **下行**：基座事件流 → VSCode ChatResponse 翻译（事件名以 integration-guide 协议为准）：

| 基座事件（功能分类） | VSCode 渲染 |
|---|---|
| 模型流式输出 | `ChatResponseMarkdown`（流式追加） |
| 工具执行中 / 委派进度 | `ChatResponseProgress` |
| `create_plan` 计划卡片 | markdown 步骤列表 + 「开始任务」命令按钮 |
| 产物生成（python_exec / 对象存储） | markdown 链接 + 「下载到工作区」命令按钮 |
| ask_question / tool_confirm | 命令按钮 → 弹层交互（见 §6） |
| run 结束 | `ChatResult` 元数据（sessionId、followup 建议映射跟进项） |

- **slash commands**：participant 自带子命令直接映射执行范式——`/plan`（`executionMode=plan`）、`/react`（默认）；
- **取消**：ChatRequest 中断信号 → run cancel；
- **多 Agent 委派**：agentPath 归流的事件折叠为进度行，不打断主渲染。

## 6. HITL 落地

基座 HITL 统一经 clientMessageHub 按 toolUseId 认领回包，插件侧对应：

- `ask_question` → chat 流内命令按钮 → `window.showInputBox` / `showQuickPick` → 回包；
- `tool_confirm`（危险工具二次确认）→ 命令按钮「允许 / 拒绝」（可配「本会话不再询问」）→ 回包；
- 等待期间 `withProgress` 显示「等待用户输入」——与基座持久化等待机制（Plan 的 USER_INPUT 步骤不依赖原 goroutine）天然兼容，插件重启后凭会话恢复。

## 7. Client Tool 的 VSCode 实现（增值核心）

基座已有 client tool「宿主执行」机制，插件把执行端从浏览器换成 VSCode API。工具走基座 client 类型注册，挂在独立 callerKey（如 `vscode-ext`）下：

| 基座侧工具（client 类型注册） | VSCode 侧实现 |
|---|---|
| `open_file` / `read_file` | `workspace.openTextDocument` + `showTextDocument` |
| `apply_diff` | `vscode.diff` 预览 + 一键应用（`workspace.applyEdit`） |
| `run_command` | 集成终端 `sendText` |
| `show_problems` / `goto_line` | problems 面板 / `revealRange` |

Caller 体系保证这些工具只对 vscode caller 可见，不影响 playground / QQ 机器人等其他端。

## 8. 编辑器上下文与代码工作区对接

- Chat request 自带 `#file` `#selection` 引用 → 翻译为基座输入前缀 / 附件；
- 每次请求自动附带轻量上下文（当前文件路径、语言 ID、选区范围）；
- **代码工作区打通**：调用基座 workspace 管理接口，把当前 `workspaceFolder` 的 git remote 注册为检索源，agent 即可经 `ws_*` repo MCP 工具检索用户正在编辑的代码；`(service, commit)` 粒度的工作区共享机制支撑多会话并发复用；
- python_exec 产物：从对象存储（minio / cos / local）下载到工作区 `.agent-artifacts/` 并自动打开。

## 9. 会话管理、回放与管理面

- **Tree View「会话」**：列表、重命名、**分叉**（fork 映射为「从这条消息重新问」）、点击继续对话（激活为当前 chat 上下文）；
- **回放**：webview 加载基座已 embed 的 `/react/replay` 页面（`/react/session/events` 事件流还原，直播与回放同形），几乎零开发；
- **管理面**：MCP 连接管理等复用 embed 页面（webview 指向基座 URL），后续再考虑本地化。

## 10. 配置与密钥

`contributes.configuration`：

| 配置项 | 说明 |
|---|---|
| `reactAgent.baseUrl` | 基座地址（默认 `http://localhost:8180`） |
| `reactAgent.callerKey` | 接入方标识（建议 `vscode-ext`） |
| `reactAgent.routeValues` | 路由值数组 |
| `reactAgent.artifactsDir` | 产物下载目录（默认 `.agent-artifacts`） |

用户名（`X-User-Name`）与未来 apikey 存 `context.secrets`（SecretStorage）；提供「连接测试」命令（探测 `/healthz` + `/readyz`）。

## 11. 打包、发布与 CI

- `vsce package` 产出 `.vsix`；发布 Marketplace，同步 Open VSX；
- CI（`.github/workflows/ci.yml`）增加 vscode job：compile + test + vsce 打包 artifact；
- 扩展与 SDK core 版本对齐基座 WS 协议（协议属对外契约、保持稳定，项目已有承诺）。

## 12. 实施排期

| 里程碑 | 内容 | 预估 |
|---|---|---|
| M1 | SDK 拆 core 入口；扩展骨架 + 配置 + 连接测试；Chat Participant 打通 WS 流式回显与取消 | 1 周 |
| M2 | HITL 三件套在 chat 内可用；计划卡片渲染 | 1 周 |
| M3 | Client Tool VSCode 实现层 + 上下文注入 + 产物下载 | 1-2 周 |
| M4 | 会话树（列表 / 重命名 / 分叉）、回放与管理面 webview | 1 周 |
| M5 | 代码工作区对接、vsce 发布流水线、（可选）HTTP/SSE MCP server 变体 | 持续 |

## 13. 风险与开放问题

| 风险 / 问题 | 说明 | 对策 |
|---|---|---|
| SDK core 拆分动存量代码 | 唯一动存量代码的环节 | 现有 SDK 测试保护回归；兜底为扩展内自带协议 client |
| 免鉴权信任头 | `X-User-Name` 信任头意味着拿到 URL 即可冒充；插件若面向团队分发，安全边界不足 | 基座补 apikey 校验路径（api_key 模块已有体系，属基座侧小改动，需单独立项） |
| Chat 流式 UI 表达力上限 | 复杂富交互（多按钮卡片、文件树）有 API 边界 | 兜底：chat 内放摘要 +「在面板中打开」按钮跳 webview |
| 会话互斥 | 基座会话并发 run 互斥，插件与 playground 同时操作同一会话会冲突 | 插件侧会话激活时提示占用；属端侧职责 |
| 基座版本对齐 | 扩展依赖的接口（workspace 管理、事件类型）与部署版本不一致 | 连接测试时校验基座版本；语义化扩展版本与基座 API 版本映射 |

## 14. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-10-05 | react-base-service 项目组 | 初稿：形态选型、混合架构、连接层拆分方案与实施排期 |
