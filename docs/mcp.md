# MCP 客户端与仓库检索：能力边界

本文说明 react-base-service 内置 MCP 客户端的实现边界、安全模型，`repo` 适配器（代码仓库检索）的能力边界，以及工作区动态挂载的 `ws_` 临时服务器。未列出的能力均视为**未实现**。

> 最近更新：2026-10-05（对照 readOnly 同步 / 工作区共享化 / 孤儿工具兜底清理 / repotools 符号与结构化检索等实现校准）

> 项目同时是 MCP **服务端**（对外网关）：`tblLlmTool` 的 http 工具按 caller 作用域经 `/react-base-service/mcp` 暴露，含 Bearer 应用凭证鉴权、输出投影+字段描述渲染与调用审计——详见 `docs/system/mcp-gateway.md`。

## 1. MCP 客户端实现边界

### 1.1 传输：stdio + Streamable HTTP（手写 / 官方 SDK 双实现）

| 项 | 现状 |
|---|---|
| stdio（子进程 + stdin/stdout JSON-RPC，`kind: repo`） | ✅ 已实现 |
| Streamable HTTP 手写版（`kind: http`） | ✅ 已实现 |
| Streamable HTTP 官方 SDK 版（`kind: http_sdk`，别名 `sdk`，[modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) v1.7.0） | ✅ 已实现 |
| 旧版 HTTP+SSE 双端点 | ❌ 未实现 |

- 三种实现共用同一 `Server` 接口，注册表同步与 `execute_tool` 分发对传输与实现形式无感知；
- `http` 与 `http_sdk` 的取舍：手写版零依赖、代码量小、行为完全可控、**支持自定义 headers**；SDK 版协议兼容性由官方保证（版本协商、SSE 解析、会话管理）、支持全部内容类型（图片/音频/资源的占位降级），但**当前不接收 headers（配置被忽略，仅 `kind: http` 生效）**。二者均按次会话、均走同一 SSRF 端点校验，配置仅 `kind` 不同，可按远端服务器的协议严格程度选择；
- 引入 SDK 使项目 Go 版本要求从 1.23 升至 **1.25**（SDK v1.7.0 的最低要求）；
- stdio 单服务器内请求串行；HTTP **按次会话**（每次操作独立完成 initialize → 操作），无连接池、无状态，代价是每次操作多一次握手往返。

### 1.2 协议方法覆盖

客户端仅发送工具相关方法：`initialize`、`notifications/initialized`、`tools/list`、`tools/call`（客户端不主动发 `ping`；自研 stdio 服务端框架 `mcpserver` 作为被调用方响应 `ping`）。

**不支持**：`resources/*`、`prompts/*`、`sampling/*`、`roots`、`elicitation` 等 MCP 其余能力域。

### 1.3 安全模型（本机受信环境，无内置鉴权）

- **stdio（kind=repo）**：可执行文件路径是代码内字面量（按 GOOS 固定于 `bin/repo-mcp[.exe]`），配置只能选择 `kind` 与注入环境变量，**不接受任意 command/args**（npx/uvx 形式在管理接口与 Bundle 安装侧均被显式拒绝）；
- **HTTP（kind=http / http_sdk）**：端点经 **SSRF 校验**（构造时、Start 时与每次建会话前各校验一次）——仅允许 `http/https`，主机为 IP 字面量时直接判定，为域名时解析后逐一判定；**拒绝环回、私网（RFC1918/ULA）、链路本地、组播、未指定、CGNAT、TEST-NET 及其他保留网段**，域名解析出任一非公网地址即拒绝（缓解 DNS 重绑定）。本机开发/演示环境可用 `mcp.allow_private_endpoint: true` 显式放行（默认 false，生产必须关闭）；
- **身份凭证靠自定义 headers**（如 `Authorization`）：仅 `kind: http` 生效，附加到每个请求，但不可覆盖协议自身管理的头（Host/Content-Type/Content-Length/Accept/Mcp-Session-Id），上限 4KB；`http_sdk` 无 headers 能力，仅应指向无鉴权受信端点；
- **tools/call 超时统一收口**（与 HTTP 工具同口径，`service/tool/runtime.go`）：工具 config `timeout_ms` 优先 → 全局 `react.tool.default_timeout_ms`（默认 60000）→ 配置归一化兜底；HTTP 手写/SDK 版另受 server 级 `timeout_ms`（默认 30s，管理接口归一化上限 300s）约束整个按次会话；stdio 内部往返（initialize/tools/list）固定 60s；
- **stdio 超时不再直接杀子进程**：单次超时后 pump 按 id 分发、丢弃迟到响应防串包；**连续 3 次超时**才判定卡死重启，流断开则下次请求自愈重启；
- stdio 子进程 stderr 透传到服务日志；stdio 生命周期随服务启停（`router.Tasks` / `StopTasks`），HTTP 无常驻资源。

### 1.4 工具注册与执行链路

```
服务启动 → 拉起子进程 + initialize → tools/list
        → upsert tblLlmTool（toolId=mcp_<server>_<tool>，toolType=mcp，
          config={mcpServer,mcpTool,inputSchema[,outputSchema][,readOnly]}，
          挂 caller_key，route_values="[]"）
模型侧 → 工具索引摘要（system 前缀）→ get_tool 加载参数 → execute_tool
        → 路由到 tools/call → 文本内容回填（大结果自动走 resultRef 分片）
```

边界说明：

- **同步时机有三种**：启动时一次性、手动触发（「MCP 连接」面板**刷新**（`/mcp/refresh`，逐连接重连并同步）、单连接「测试连接」（`/mcp/connect`）或重启服务）、以及**工作区挂载时**（`ws_` 服务器每次 `Load` 都重新同步，见 §3）；
- 每次同步把 `tools/list` 的最新结果落库：更新工具描述与入参/出参 schema（`outputSchema` 仅在服务器声明时写入 config），服务器端已下架的工具标记停用（`status=0`，不软删，重新出现后下次同步自动恢复）；
- **连接检测失败即整体下线**：刷新/测试连接/启动装配任一环节失败，该服务器全部 caller 的注册工具标记停用（`status=0`，不软删），不再进入会话工具索引；恢复连接后重新检测即自动恢复；
- **连接停用/删除则软删**其全部注册工具；软删行在同名服务器重新接入（重新启用/同名重建）时自动复活（唯一键约束下的恢复式写入）；
- **启动兜底清理孤儿**（`CleanupOrphanedRegistryTools`，Bootstrap 末尾执行一次）：工具的 `config.mcpServer` 既不在 yaml 静态声明、也没有启用中的 `tblLlmMcpServer` 记录 → 软删。覆盖 yaml 移除服务器、清理失败的残留，以及服务异常退出遗留的 `ws_` 工具副本（`ws_` 无 DB 行，run 已终态后必成孤儿）；
- **readOnly 标记**：`kind=repo` 的服务器（静态与 `ws_` 动态）同步注册表时自动在工具 config 写 `readOnly:true`（自研只读检索适配器，无外部副作用）；http/http_sdk 工具语义未知，保守不标。消费侧三层：readOnly 工具视为 ConcurrentSafe（同轮并发）+ 无副作用低风险；只读执行域内非 readOnly 工具直接拒绝；`@readonly` 子代理白名单 token 依赖此标记硬执行；
- 工具在注册表中就是普通 Business Tool 行，**可被工具管理面板启停/修改/删除**，也受用户白名单（ToolUserPolicy）约束；
- 同步失败只记日志，不阻断服务启动。

### 1.5 配置（custom.yaml）

```yaml
mcp:
  caller_key: demo-app        # 工具挂载的调用方；留空或不配 mcp 段 = 不启用
  allow_private_endpoint: false  # SSRF 放行开关；仅本机开发/演示可开
  servers:
    - name: repo              # 字母/数字/_/-，≤32 字符，用于工具名前缀
      kind: repo              # stdio 适配器（可执行文件白名单，env 注入子进程）
      env:
        REPO_ROOT: 'C:/path/to/some/repo'
    - name: remote1           # Streamable HTTP 手写版（支持 headers 自定义鉴权）
      kind: http
      endpoint: 'https://mcp.example.com/mcp'
      headers:
        Authorization: 'Bearer xxx'
      timeout_ms: 30000       # 可选，默认 30000，上限 300000
    - name: remote2           # 官方 MCP Go SDK 版（协议兼容性更强，不支持 headers）
      kind: http_sdk
      endpoint: 'https://mcp2.example.com/mcp'
      timeout_ms: 30000
```

## 2. 代码仓库检索（`repo` 适配器）能力边界

### 2.1 数据源：本机目录；远程仓库经工作区链路

- repo 适配器本身只能读**与本服务同机的本地文件系统目录**（`REPO_ROOT` 指向哪里就读哪里，不限于本项目），无 git clone、无 GitHub/GitLab API、无网络访问；
- **读远程仓库的正途是工作区挂载**（§3）：模型调 `load_runtime_code`，workspace 模块 clone/fetch 到本地 mirror、建 worktree、动态拉起 `ws_` 前缀的 repo 适配器实例；
- 可配置**多个静态 server 实例**各指向一个本地目录，模型将看到 `repo_a_read_file`、`repo_b_read_file` 这样的分组工具。

### 2.2 工具集（9 个）与读取上限

| 工具 | 行为 | 上限 |
|---|---|---|
| `list_repositories` | 列检索根下顶层项目目录 | 顶层清单 |
| `list_files` | 列某目录一级条目 | 200 条（最大 1000） |
| `read_file` | 按行区间读单文件 | 默认 1–200 行、单次 ≤400 行差值；单文件 ≤2MB |
| `search_code` | 全仓大小写不敏感**包含匹配** | 50 条（最大 200）；跳过 >1MB 文件 |
| `search_pattern` | **ast-grep 风格结构化搜索**（Go 语法树）：`$NAME` 匹配单节点、`$$$NAME` 匹配零或多列表项（参数/语句）；同名元变量须文本一致；字符串/注释中的字面量不匹配 | 50 条（最大 200） |
| `find_symbol` | **go/ast 声明级查找**（func/method/type，带 kind/receiver/签名/行区间）：精确 → 子串，无声明匹配才回退词法检索 | 50 条（最大 200） |
| `get_file_symbols` | 单个 .go 文件的顶层符号表 | 单文件 |
| `find_references` | 标识符**整词候选检索**（候选级，非语义验证） | 50 条（最大 200） |
| `get_repo_map` | 全仓文件清单总览；.go 文件带 Aider 式顶层声明摘要 | 300 个（最大 2000）；跳过全部点开头目录（**含 .github**）与 `_test.go` |

明确**不做**：语义级引用查找/重命名（需 LSP/gopls）、非 Go 语言的 AST 级结构化搜索、跨 server 的全局搜索、文件写入。

### 2.3 路径安全

- 相对路径统一在 `REPO_ROOT` 内解析，拒绝 `..` 穿越与绝对路径；
- 符号链接解析后必须仍在根内（防链接逃逸）；**仓库根本身先 Abs + EvalSymlinks 归一化**再参与包含判定（修复根前缀含软链时的误判逃逸）；
- 忽略目录：`.git`、`.idea`、`.vscode`、`node_modules`、`vendor`、`dist`、`build`、`target`、`coverage`、`__pycache__`，以及所有 `.` 开头条目（`get_repo_map` 连 `.github` 也跳过）。

## 3. 工作区代码挂载（`ws_` 动态服务器）

静态 yaml 声明之外，代码仓库还可以**按 run 动态挂载**：模型（主 Agent 或 code-agent 子代理）调用内置 meta tool `load_runtime_code`，由 workspace 模块完成「远程仓库 → 本地 worktree → 临时 repo MCP 服务器」的全链路。详见 `docs/system/workspace.md`，此处列 MCP 侧边界：

- **生命周期是引用计数管理的临时实例，无 DB 行**：Load 时经静态白名单（`llm.react.workspace.resolvers`，service+env → repo_url+ref）解析 → bare mirror `git clone --mirror`/`fetch --prune` → `rev-parse` 定位 commit（40 位哈希校验）→ `git worktree add --detach` → 拉起 repo-mcp 子进程；run 到达终态统一 `ReleaseRun`，caller 计数归零清工具副本，总归零才移除子进程与 worktree；进程重启时首帧全清孤儿目录；
- **(service, commit) 粒度全局共享**：同名同 commit 的 worktree 与子进程全局仅一份，多 run/多 caller 复用同一实例，各自持有带 `__<callerKey>` 后缀的工具副本；热路径零 git 操作、零 MCP 拉起；
- **命名**：`ws_<service>_<commit[:8]>`（受 32 字符 server 名上限约束，service 名须 ≤20 字符）；名字带 commit 天然唯一，避开同名替换语义，多 run 并发加载互不顶替；
- **同步**：每次挂载执行 `SyncServerRegistryScoped`（primary=false），工具写 `tblLlmTool` 但不写 `tblLlmMcpServer`——因此不出现在「MCP 连接」面板；kind 判定不查库，走客户端可选接口断言 `Client.Kind()`；readOnly 标记靠每次挂载重新同步刷新存量行 config；同步失败仅告警不阻断；
- **git 安全面**：service/env 字符白名单 `^[a-zA-Z0-9_-]+$`、ref 白名单 `^[a-zA-Z0-9._/\-]+$`、git 子命令白名单（clone/fetch/worktree/rev-parse）、可执行文件为编译期常量不经 shell、单命令超时 kill；
- **管理视图**：`POST /react-base-service/react/workspace/active` 按实例聚合返回 service/commit/路径/工具数/caller 与 run 计数。

## 4. 已评估、未实现的扩展路径

| 方向 | 说明 | 预估成本 |
|---|---|---|
| git 适配器（`kind: git`） | 启动时按 URL `git clone --depth 1` 到本地缓存（存在则 pull），复用 repotools；私有仓库走 token 环境变量。**大部分场景已被 §3 工作区链路覆盖** | ~100 行 |
| GitHub API 适配器 | `kind: github` 直调 contents/search API，不落本地盘；受 API 限速影响 | ~150 行 |
| ~~HTTP 鉴权~~ | 已实现：`headers`（如 `Authorization`）附加到每个请求，协议自身管理的头除外；**仅 `kind: http` 生效，`http_sdk` 暂不透传**（SDK 客户端构造不接收 headers） | 已上线 |
| `http_sdk` headers 支持 | manager 向 `NewSDKClient` 透传 headers 并透传 4KB/协议头约束 | ~20 行 |
| 会话复用 | HTTP 按次会话改「按 server 常驻会话 + 失效重建」；调用次数高频且 initialize 成为瓶颈时再考虑 | ~60 行 |

接入新 stdio 适配器的步骤：实现工具 → 在 `service/mcpclient` 的 `adapterWhitelist` 注册（可执行路径用字面量，当前仅 `repo` 一项）→ 构建到 `bin/` → `custom.yaml` 增加一段 server 配置。

## 5. MCP 连接管理（动态登记）

HTTP MCP 连接的来源有三：`custom.yaml` 静态声明、管理接口动态登记（`tblLlmMcpServer` 表，name 全局唯一）、**Agent Bundle 安装**（bundle 可携带 `.mcp.json`，安装时展开写入连接表并重连同步，卸载按快照回滚；与 /react/mcp 同款约束，仅 url 形式 HTTP MCP）。playground 右侧「MCP 连接」面板提供同能力的页面操作，**list/detail 同时合并展示 yaml 静态服务器**（`source=yaml`、`serverId=yaml:<name>`，与同名 DB 记录并存两条；对 yaml 来源的 update/delete/connect 请求会拒绝并提示改配置文件）：

| 接口 | 说明 |
|---|---|
| `POST /react-base-service/react/mcp/list` | 列出 caller 下的连接（含来源 yaml/db、运行状态、最近检测结果、工具数） |
| `POST /react-base-service/react/mcp/detail` | 单个连接详情 + 已同步进注册表的工具清单 |
| `POST /react-base-service/react/mcp/create` | 新增：`rawConfig` 粘贴标准 mcpServers JSON（`{url, headers}`，外层 `servers` key 亦可，可一次多条）或结构化字段（`name/kind/endpoint/headers/timeoutMs/boundCallers/description/status`）；**结构化形态仅支持 `kind: http` 与 `kind: repo`**，`http_sdk` 只能经 yaml 声明 |
| `POST /react-base-service/react/mcp/update` | 更新端点（仅 `kind: http` 可改）/请求头/超时/描述/绑定 caller/启停；更新后自动重连重同步，停用会下线其全部注册工具 |
| `POST /react-base-service/react/mcp/delete` | 删除连接并清理其注册工具 |
| `POST /react-base-service/react/mcp/connect` | 连接测试：拉起客户端 + tools/list + 写回检测结果；失败时该连接全部注册工具下线 |
| `POST /react-base-service/react/mcp/refresh` | 批量刷新：对 caller 下全部启用连接（含 yaml 静态声明）逐台重连重同步，工具描述/入参出参 schema/上下线状态落库，返回逐台结果摘要（source/activeToolCount/skipped；停用连接跳过） |

行为与边界：

- 工具同步：连接成功后 `tools/list` 结果 upsert 进 `tblLlmTool`（`tool_type=mcp`，toolId=`mcp_<server>_<tool>`，绑定 caller 副本追加 `__<callerKey>` 后缀），ReAct 运行时经既有 get_tool/execute_tool 两段式加载使用，引擎无感知；
- **刷新语义**（「MCP 连接」面板刷新按钮 → `/mcp/refresh`）：逐台重连并把工具的描述、入参/出参 schema 与上下线状态写回 `tblLlmTool`——连接正常的恢复/更新工具行；失联的整台下线（`status=0`）；服务器端已下架的单独下线。会话工具索引只取 `status=1`，因此失联服务器的工具不会再被加载；
- 请求头：`headers`（如 `Authorization: Bearer ...`）附加到每个 HTTP 请求，但不允许覆盖协议自身管理的头（Host/Content-Type/Content-Length/Accept/Mcp-Session-Id）；上限 4KB；**仅 `kind: http` 生效**；
- 超时：结构化配置归一化为默认 30000ms、上限 300000ms；
- SSRF：HTTP 端点仍走 `validateEndpoint` 校验（拒绝环回/私网/保留地址）；本机开发/演示环境可用 `mcp.allow_private_endpoint: true` 显式放行（生产必须保持关闭）；
- command/args 形式（npx/uvx）明确拒绝：stdio 仅开放代码内适配器白名单（`kind=repo`）；
- **caller 绑定**：`boundCallers` 把连接绑到属主之外的多个 caller，工具同步到每个绑定 caller 名下（toolId 追加 `__<callerKey>` 后缀避开全局唯一键，工具名不变）；解绑立即清理该 caller 的工具副本；caller 删除时级联清理绑定行；详情/列表返回 `boundCallers`（属主在首位）；
- 服务重启时按表内启用记录自动重连并同步（`mcpclient.Bootstrap` 内 `bootstrapDBServers`，含绑定 caller 展开），随后执行孤儿工具兜底清理（见 §1.4）；
- 停用/删除会软删除注册工具；重新启用/同名重建会复活软删除行（唯一键约束下的恢复式写入）。
