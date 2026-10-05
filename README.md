<p align="center">
  <img src="docs/images/logo.svg" alt="react-base-service" width="760">
</p>

<p align="center">
  <strong>一个面向长任务、工具调用、多 Agent 与无人值守执行的 Go Agent Runtime。</strong>
</p>

<p align="center">
  <a href="https://github.com/lsl1016/react-base-service/actions/workflows/ci.yml"><img src="https://github.com/lsl1016/react-base-service/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/Gin-1.10-008ECF" alt="Gin">
  <img src="https://img.shields.io/badge/MCP-go--sdk-5E5CE6" alt="MCP">
  <img src="https://img.shields.io/badge/MySQL-8.0-4479A1?logo=mysql&logoColor=white" alt="MySQL">
  <img src="https://img.shields.io/badge/Redis-7-D82C20?logo=redis&logoColor=white" alt="Redis">
  <img src="https://img.shields.io/badge/Runtime-ReAct%20%2B%20Plan-4F46E5" alt="Runtime">
</p>

---

**react-base-service** 是一个可独立部署的通用 Agent 基座服务。它不只提供“模型 + Tool Call”循环，而是把一个长期运行 Agent 所需的运行时能力完整落到服务端：**持久化 Session / Run、ReAct、Plan、多 Agent 委派、Steering、Pending Queue、MCP、长期记忆、代码工作区、Python 沙箱、HITL、定时工作流、裁判外环、Artifact、历史回放与可观测性**。

项目的目标不是实现某个业务机器人，而是提供一个稳定的 **Agent Runtime / Control Plane**：业务侧只需要注册 Caller、模型、工具、Skill、Agent 和系统提示词，即可通过 WebSocket、HTTP 或无人值守 Workflow 发起完整 Agent 执行。

> 当前 README 按 2026-10-05 默认分支实现更新。

## ✨ 当前能力

| 能力 | 当前实现 |
|---|---|
| **ReAct Runtime** | 流式模型轮、工具循环、模型重试与互备、上下文自动压缩、resultRef 大结果分页、soft landing；不以 Tool Call 次数作为硬停止条件 |
| **Plan Runtime** | Planner → 持久化 Plan/Step → Scoped ReactRun；支持 USER_INPUT / USER_ACTION 等待，以及 resume / retry / skip / cancel |
| **Multi-Agent** | `delegate_agent`、`wait_agent`、`send_message`、后台委派、子 Run 独立上下文/预算/工具白名单、continue_run_id 续跑 |
| **Steering / Queue** | Agent 执行中支持 guided steering；忙碌时输入进入持久化 Pending Queue，也可立即注入当前 Run |
| **Tool Runtime** | 两段式工具加载：摘要索引 → `get_tool` → `execute_tool`；HTTP / Client / MCP 三类 Business Tool 统一执行 |
| **MCP** | MCP Client + MCP Server Gateway；支持 stdio repo MCP、Streamable HTTP、官方 Go SDK 适配与工具注册同步 |
| **Memory** | 长期记忆分层注入、revision 审计、自动 extractor / resolver；可选 Graphiti 时序事实图谱 |
| **Code Workspace** | `load_runtime_code` 将线上/指定版本代码映射为只读仓库检索工具；工作区按 **(service, commit)** 全局共享并引用计数复用 |
| **HITL** | `ask_question`、危险工具确认、浏览器 Client Tool；按 toolUseId 路由响应 |
| **Python / Artifact** | Python 3.11 沙箱、pandas / numpy / matplotlib、附件读取、产物上传、MinIO/S3/COS/local 存储 |
| **Workflow** | robfig/cron 定时触发无人值守 ReAct Run，支持手动 dispatch、并发闸门、超时、运行历史与 webhook 通知 |
| **Judge Loop** | Workflow 结束后语义判断目标是否达成；未达成可自动追问继续同一 Session，支持风险判级与失败熔断 |
| **Web 能力** | 内置 `web_search`（SearXNG）与 `web_fetch` |
| **SDK / UI** | TypeScript + SolidJS SDK，多 Session 独立 WebSocket / 状态分片，IndexedDB Event Ledger；内置 Playground / Replay / Workflow Admin / MCP Admin |
| **Observability** | `/healthz`、`/readyz`、Prometheus metrics、Loki、Grafana、滚动日志 |

## 🏗️ 系统架构

<p align="center">
  <img src="docs/images/architecture.svg" alt="react-base-service 系统架构" width="1000">
</p>

当前架构可以分为五个部分：

1. **Access Channels**：Web SDK、Playground、业务 Caller、MCP Client、Headless Workflow；
2. **Control Plane**：Caller / Tool / Skill / Agent / Prompt / Model / MCP / Bundle / Runtime Setting / Workflow 管理；
3. **Execution Plane**：ReAct、Plan、多 Agent、Steering、Queue、HITL、Context/Memory、Judge；
4. **Capability Services**：MCP、Workspace、Memory、Tool/Artifact、Web Search/Fetch；
5. **Infrastructure**：LLM Provider、MySQL、Redis、MinIO/S3/COS、Python Sandbox、Prometheus/Loki/Grafana。

更细的运行时链路、状态机和模块职责见 [docs/architecture.md](docs/architecture.md)。

## 🔁 ReAct Runtime

一次普通 Run 的核心链路：

```text
WebSocket / Headless Trigger
        │
        ▼
prepareRuntimeRequest
  ├─ caller / user / model 解析
  ├─ system prompt
  ├─ Tool / Skill / Agent 索引快照
  ├─ Memory / Graph Memory 注入
  └─ ExecutionProfile 能力装配
        │
        ▼
createReactRunContext
  ├─ Session 行锁
  ├─ Run 持久化
  ├─ History 恢复
  └─ Pending Input claim
        │
        ▼
executeReactLoop
  ├─ Runtime Command / Steering
  ├─ Context compact / microcompact
  ├─ Model stream + retry / failover
  ├─ Tool dispatch
  ├─ HITL / Client Tool
  ├─ Multi-Agent delegation
  └─ Finish / timeout / cancel / soft landing
```

这个 Runtime 的一个关键原则是：**Tool Call 数量本身不是“失控”的充分条件**。真正的终止边界来自用户取消、权限拒绝、Context/Token Budget、Wall Clock Timeout、模型错误重试预算、Compact 失败、明确的 Runtime 控制以及模型自然结束。

### 两段式 Business Tool

业务工具不会把全部 JSON Schema 一次性塞进模型上下文，而是：

```text
Tool Summary Index
      │
      ├─ get_tool(toolId | name)
      │      └─ 加载完整 parameters / outputSchema
      │
      └─ execute_tool(...)
             ├─ visibility check
             ├─ fingerprint check
             ├─ JSON Schema validation
             ├─ permission / confirmation
             └─ http / mcp / client execution
```

这样可以在工具数量较多时显著降低固定上下文开销，同时保留运行时可见性、版本指纹和权限校验。

## 📋 Plan Runtime

项目同时支持独立的 `executionMode=plan`：

```text
User Request
   │
   ▼
Planner
   │
   ▼
Plan Version / Steps  ───────────────┐
   │                                 │ durable
   ▼                                 │
Scoped ReactRun per Step             │
   │                                 │
   ├─ automatic step                 │
   ├─ USER_INPUT  ── wait ── resume  │
   └─ USER_ACTION ── wait ── resume  │
   │                                 │
   ▼                                 │
Finalizer ◄───────────────────────────┘
```

Plan 数据完整持久化，因此等待用户期间不依赖原 goroutine 存活。Caller 还可以通过 `allow_plan` 三态开关控制 Plan 能力：

- `NULL`：跟随全局 `llm.react.allow_plan`；
- `0`：该 Caller 强制关闭；
- `1`：该 Caller 强制开启。

ReAct 中的 `create_plan` 计划确认卡片，与独立 Plan Runtime 是两套不同能力。

## 🤝 Multi-Agent 与运行中控制

子 Agent 本质上不是一次函数调用，而是**独立 ReactRun**：

- 独立历史与上下文；
- 独立 Tool / Skill 白名单；
- 独立 token budget 与 max steps；
- 使用 `agentPath` 标识执行路径；
- 可同步委派，也可 `background=true` 后台执行；
- `wait_agent` 等待多个子 Run；
- `send_message` 可向正在运行的子 Agent 补充引导；
- 已终态的子 Run 可通过 `continue_run_id` 继续。

外层 Run 执行期间，用户新输入也不必粗暴中断当前 Agent：

- **Steering**：把新指令注入当前活跃 Turn；
- **Pending Queue**：忙时先持久化排队，当前 Run 完成后自动晋升；
- **立即发送**：队列项也可以主动注入当前执行。

## ⏰ Scheduled Workflow

`service/workflow` 提供服务端无人值守执行：

```text
robfig/cron
    │
    ▼
Workflow Definition
    │
    ├─ unique fire key / caller concurrency gate
    ▼
Headless ReAct Run
    │
    ▼
Judge Outer Loop
    │
    ├─ goal achieved?
    ├─ semantic risk
    └─ optional follow-up in same session
    │
    ▼
Markdown Artifact / Replay / Webhook
    │
    └─ consecutive failure circuit breaker
```

已实现：

- cron 调度与时区；
- 手动 dispatch；
- caller 级并发闸门；
- wall-clock timeout；
- scheduled ExecutionProfile（禁止需要前端交互的能力）；
- 运行历史；
- 语义裁判；
- 未达成自动追问；
- risk pattern + semantic risk 双判级；
- Markdown 报告产物化；
- 连续失败自动停用；
- webhook 通知；
- Workflow Admin 页面。

管理页：

```text
/react-base-service/react/workflow/admin
```

## 🧠 Memory

长期记忆支持两层数据来源：

### Durable Memory

- owner scope；
- resident / index / detached 分层；
- Run 启动自动注入；
- `memory_list / memory_read / memory_write`；
- Revision 审计与回滚；
- compact 后可触发 reflection 或 V2 extractor；
- extractor：候选抽取 → 相似记忆检索 → 冲突消解 → 统一写核心。

### Graph Memory

可选接入 Graphiti：

- Session Run 启动按当前输入检索事实；
- `graph_memory_search`；
- `graph_memory_write`；
- 用于时序事实和关系型长期知识，而不是替代普通 durable memory。

## 🧑‍💻 Code Workspace

`load_runtime_code(service, env)` 用于让 Agent 按指定服务版本加载代码检索能力。

最新实现的共享粒度已经从“每 Run 一个 worktree”改为：

```text
(service, commit)
      │
      ├─ bare mirror
      ├─ shared git worktree
      └─ shared repo-mcp process
             │
             ├─ Run A / Caller A
             ├─ Run B / Caller B
             └─ Run C / Caller A
```

同一 commit 的 worktree 和 repo-mcp 进程只冷启动一次，多 Run / Caller 引用计数共享：

- caller 引用归零：清理该 Caller 的工具副本；
- 总引用归零：回收 worktree + repo MCP；
- server 名包含 commit 短哈希，避免不同快照互相替换；
- mirror fetch 有仓库级互斥；
- 冷启动与 teardown 有 entry 生命周期锁；
- Workspace 本身只挂只读 Repo MCP，避免共享写竞争。

详细设计见 [代码工作区共享化改造方案](docs/todo/20261004_代码工作区共享化改造方案.md)。

## 🔌 MCP

项目同时扮演 **MCP Client** 和 **MCP Server**。

### MCP Client

外部 MCP Server 可以通过配置或管理面接入，并同步为普通 Business Tool：

- `kind=repo`：stdio，主要用于 repo-mcp；
- `kind=http`：Streamable HTTP；
- `kind=http_sdk`：官方 `modelcontextprotocol/go-sdk`。

MCP Tool 进入 `tblLlmTool` 后，与 HTTP Tool 使用完全相同的 `get_tool → execute_tool` Runtime 协议。

### MCP Server Gateway

本服务注册的 HTTP Tool 也可以反向通过 MCP Gateway 暴露给 Claude、Cursor 或其它 MCP Client。

独立进程入口：

```bash
go run ./cmd/mcp-gateway
```

repo 只读检索 MCP：

```bash
go run ./cmd/repo-mcp
```

## 🧪 Python Sandbox 与安全边界

`python_exec` 运行在独立 Python 3.11 Sandbox 中。

发布形态下：

- Sandbox 只连接 Docker `internal` 网络；
- 不发布宿主机端口；
- 不能访问公网；
- 不能访问 MySQL / Redis 所在业务网；
- 非 root；
- 根文件系统只读；
- `/tmp` 使用限额 tmpfs；
- 有 CPU / Memory / Process 限制；
- 每次执行使用独立工作目录并在结束后清理。

静态 AST 检查仍保留，但它只是第一层降噪；真正的隔离边界在容器与网络层。

## 🚀 快速开始

### Docker Compose

最适合直接体验完整能力：

```bash
# 1. 配置模型 API Key
vim deploy/compose/conf/mount/api.yaml

# 2. 启动完整环境
docker compose up -d --build

# 3. 打开 Playground
open http://127.0.0.1:8080/react-base-service/react/playground
```

Compose 默认包含：

- react-base-service
- MySQL 8
- Redis 7
- Python Sandbox
- SearXNG
- MinIO
- Prometheus
- Loki / Promtail
- Grafana

### 本地 Go 开发

依赖走 Docker，Go 服务本地运行：

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml   up -d mysql redis sandbox minio searxng

go run main.go
```

或：

```bash
./dev.sh
```

> `docker-compose.dev.yml` 会为本机开发暴露 Sandbox 端口，因此不要把这个覆盖层用于发布环境。

默认开发端口：

| 服务 | 地址 |
|---|---|
| Go Service | `127.0.0.1:8180` |
| MySQL | `127.0.0.1:3317` |
| Redis | `127.0.0.1:16379` |
| Sandbox（dev override） | `127.0.0.1:18190` |
| SearXNG | `127.0.0.1:8888` |
| MinIO API | `127.0.0.1:9000` |
| MinIO Console | `127.0.0.1:9001` |

## 💻 Web SDK

前端 SDK：`web/sdk`。

```typescript
import { createAgentClient, mountAgentUI } from '@react-agent-web-sdk';

const agent = createAgentClient({
  baseUrl: 'http://localhost:8080',
  callerKey: 'my-app',
  routeValues: ['demo'],
});

const ui = mountAgentUI(
  document.getElementById('agent-container'),
  agent,
  {
    title: 'AI Assistant',
    showSidebar: true,
    theme: 'dark',
  },
);
```

SDK 当前包含：

- WebSocket streaming；
- 多 Session 独立连接与状态分片；
- IndexedDB Event Ledger；
- Event Reducer；
- Session Manager；
- Client Tool Executor；
- SolidJS UI；
- Markdown / highlight / diff；
- reconnect；
- replay / history 对齐。

详见 [web/sdk/README.md](web/sdk/README.md)。

## 🧰 技术栈

| 层 | 实现 |
|---|---|
| Backend | Go 1.25 · Gin |
| Runtime | 自研 ReAct / Plan Runtime |
| WebSocket | 手写 RFC 6455 握手、帧编解码与 ping-pong |
| LLM | Claude / GPT-compatible / MiniMax |
| MCP | `modelcontextprotocol/go-sdk` + 自研 HTTP / stdio 适配 |
| Persistence | GORM + MySQL 8（当前 `sql/init.sql` 38 张表） |
| Cache / metadata | Redis 7 |
| Object Storage | MinIO / S3 / 腾讯 COS / local |
| Sandbox | Python 3.11 |
| Web search | SearXNG |
| Frontend SDK | TypeScript · SolidJS · Vite |
| Schedule | robfig/cron v3 |
| Metrics | Prometheus |
| Logs | lumberjack · Loki / Promtail |
| Dashboard | Grafana |
| Deploy | Docker Compose |

## 📁 目录结构

```text
.
├── main.go
├── api/
│   ├── llm/                    # Claude / GPT-compatible / MiniMax
│   └── pythonexec/             # Python Sandbox client
├── cmd/
│   ├── mcp-gateway/            # 独立 MCP Gateway
│   ├── repo-mcp/               # 只读代码仓库 MCP
│   └── livetest/               # 真实链路测试客户端
├── components/                 # params / metrics / COS / runtime context
├── controllers/http/           # HTTP / WS / management controllers
├── models/llm/                 # GORM models
├── router/                     # route + startup tasks
├── service/
│   ├── react/                  # ReAct Runtime façade + internal capability domains
│   ├── plan/                   # Durable Plan Runtime
│   ├── workflow/               # Cron / headless runner / notification / breaker
│   ├── judge/                  # Workflow semantic judge
│   ├── agent/                  # Agent registry / builtin profiles
│   ├── tool/                   # Business Tool execution
│   ├── skill/                  # Skill registry
│   ├── memory/                 # Durable long-term memory
│   ├── graphmemory/            # Graphiti adapter
│   ├── workspace/              # Shared code workspace
│   ├── mcpclient/              # MCP client manager
│   ├── mcpgateway/             # MCP server gateway
│   ├── bundle/                 # Agent Bundle install / rollback
│   ├── setting/                # Runtime online settings
│   └── ...                     # caller / model / credits / async task / files
├── sandbox/                    # Python execution service
├── web/
│   ├── react/                  # Playground / Replay / Workflow Admin
│   ├── mcp-admin/
│   └── sdk/
├── sql/init.sql
├── deploy/
└── docs/
```

## 🧪 测试与 CI

```bash
# Go 单元测试
go test ./... -count=1

# 常规静态检查
go vet ./...

# SDK
cd web/sdk
npm run type-check
npm run test:run

# Workspace 真实链路（需要 git / repo-mcp）
go build -o bin/repo-mcp ./cmd/repo-mcp
WORKSPACE_IT=1 go test ./service/workspace/   -run TestWorkspaceSharingIntegration -v -count=1 -timeout 600s
```

GitHub Actions：`.github/workflows/ci.yml`。

近期 Workspace 共享化改造已验证：

- `go build ./...`
- `go test ./... -count=1`
- workspace `-race`
- 真实 repo-mcp + git worktree 集成链路

## 📚 文档导航

- [文档总索引](docs/README.md)
- [系统架构](docs/architecture.md)
- [Runtime 模块化](docs/runtime-modularization.md)
- [接入指南](docs/integration-guide.md)
- [启动与部署](docs/deployment.md)
- [MCP](docs/mcp.md)
- [多 Agent 编排](docs/multi-agent-orchestration.md)
- [Memory](docs/memory.md)
- [Plan Runtime](docs/system/plan.md)
- [定时触发工作流实现方案](docs/定时触发工作流实现方案.md)
- [代码工作区共享化改造](docs/todo/20261004_代码工作区共享化改造方案.md)

## 🧭 项目边界

这个仓库刻意把**通用 Agent Runtime** 与具体业务应用分开：

**属于基座：**

- ReAct / Plan 执行范式；
- Agent / Tool / Skill / Prompt 注册与解析；
- Session / Run / History；
- Multi-Agent / Steering / Queue；
- MCP；
- Memory；
- Workspace；
- HITL；
- Workflow；
- Sandbox / Artifact；
- Runtime Setting / Observability。

**不属于基座：**

- 某个具体业务域的 Agent Prompt；
- 企业知识库内容本身；
- 某个产品固定业务流程；
- 上层应用 UI 的具体业务页面。

因此它更适合作为其它 Agent 产品、平台助手、研发助手、巡检系统和自动化执行平台的服务端 Runtime，而不是一个绑定单一场景的成品机器人。

## 📄 License

仓库当前未附加开源 License，默认保留所有权利。使用或引用前请确认授权范围。
