# react-base-service（ReAct Agent 基座服务）

**可直接调用的 ReAct Agent 基座服务**：提供 ReAct 运行时及其完整周边能力（会话管理、历史回放、异步任务、工具产物、轮次反馈、附件），内置系统提示词装配、两段式工具加载（get_tool → execute_tool）、Skill 摘要注入与注册调用模式，开箱即用。

## 能力清单

| 能力 | 说明 |
|---|---|
| ReAct 运行时 | WebSocket 单入口 `/react-base-service/react/ws`，模型流式输出 + 工具循环 + 上下文自动压缩 + 模型互备 |
| 多 Agent 委派 | Agent 注册表（`/agent/*`，支持 Markdown 导入）；`delegate_agent` 委派子 ReactRun 执行：父子历史/token 预算隔离、事件按 agentPath 归流、支持同轮并行委派 |
| 计划确认 | `create_plan` Meta Tool：复杂任务先提交分步计划，前端计划卡片 + 「开始任务」确认后按计划执行（进度走 todo）；`llm.react.allow_plan: false` 可关闭（未配置默认开启） |
| 会话管理 | 会话隐式创建/复用（事务加锁 + 归属校验）、会话列表、并发 run 互斥、软删状态；启动期自动清理重启残留的陈旧活跃 run |
| 历史回放 | 持久化消息还原为与实时协议同形的事件流（`/react/session/events`），内置回放页面 `/react/replay` |
| 工具体系 | Business Tool 注册（http/client/mcp 三类）、两段式加载、输入 Schema 校验、白名单、异步提交型工具；MCP 客户端双来源：`custom.yaml` `mcp` 段静态声明 + 「MCP 连接管理」接口动态登记（`tblLlmMcpServer`，支持粘贴 mcpServers JSON、请求头透传、连接测试、启停与工具清单同步），工具自动进注册表供 ReAct 运行时使用 |
| Skill 体系 | Skill 注册 + 摘要索引注入 system 前缀 + get_skill 按需加载完整说明；支持 `default` 默认作用域 |
| 长期记忆 | 跨会话记忆按 owner 作用域读写（memory_write 等 Meta Tool）、run 启动分层注入、修订审计与回滚；管理面 `/react/memory/*` |
| 图谱记忆 | Graphiti 时序事实图谱（Layer2）：run 启动事实注入 + `graph_memory_search` / `graph_memory_write` Meta Tool，外部 REST 存储 |
| 代码工作区 | 按 run 分配 git worktree（bare mirror 缓存 + 静态解析 + commit 锁定），挂载只读 repo MCP 检索工具（`ws_*` 系列）；运行视图 `/react/workspace/active` |
| Bundle 插件包 | manifest + agents + skills + .mcp.json 打包安装，展开写入注册表并记录快照，卸载按快照逆序回滚（`/react/bundle/*`） |
| HITL 交互 | Client Tool（浏览器执行）、ask_question 补充信息、危险工具二次确认（tool_confirm），统一经 clientMessageHub 按 toolUseId 认领回包 |
| 系统提示词 | 按 callerKey + routeValues 前缀匹配解析，多条由通用到具体拼接；支持 `default` 默认作用域（全 caller 共享，拼接在最前） |
| 异步任务 | 提交快照落库、`<async_tasks>` 提醒注入、resolve/get 闭环工具、Provider 状态同步框架（可插拔） |
| 产物与附件 | python_exec 沙箱执行 + COS 产物下载（支持本地目录存储模式）；csv/md/txt 附件上传与引用 |
| 轮次反馈 | run 级点赞/点踩与问题反馈，会话维度回显 |
| 模型管理 | 用户自定义模型（modelHash 直引）、模型白名单、积分 |
| 默认作用域 | 工具/系统提示词/skill 可挂在保留伪 caller `default` 下，全部 caller 的请求自动合并解析；管理面板三类资源支持 全部/默认/各 caller（按平台分组）筛选，新建跟随筛选落到目标作用域 |
| 内置页面 | playground 联调页（`/react/playground`）、回放页（`/react/replay`）、TypeScript SDK |

## 快速开始

**方式一：Docker Compose 一键起**（推荐试用）

```bash
# 1. 填入 LLM API key
vim deploy/compose/conf/mount/api.yaml

# 2. 一键起全套（MySQL 建库 + Redis + python 沙箱 + 服务）
docker compose up -d --build

# 3. 访问 playground
open http://127.0.0.1:8080/react-base-service/react/playground
```

**方式二：日常开发（推荐：依赖容器化 + 服务本地直跑）**

改代码不需要重新打包镜像——依赖在容器里，前后端本地 `go run` 直跑（web 前端是 embed 静态资源，无独立构建步骤）：

```bash
# 依赖容器（一次性，日常开机后已在跑）：MySQL/Redis/python 沙箱
docker compose up -d mysql redis sandbox

# 本地起服务（前台，监听 :8180；改代码后重跑即生效）
go run main.go

# 或直接用脚本：./dev.sh（等价上面两条）| ./dev.sh deps | ./dev.sh stop
```

本地开发约束：

- **依赖只在容器中跑**：MySQL=`127.0.0.1:3317`、Redis=`127.0.0.1:16379`、Python 沙箱=`127.0.0.1:18190`，宿主机端口由仓库根 `.env` 固化（Docker Desktop 端口自动避让不会再导致漂移）
- **本地服务端口 8180**（`conf/mount/config.yaml` 的 `server.address`），与容器版 service（:8080）可并行：容器版当稳定环境、本地版当开发环境
- **不要用 `docker compose up -d --build service` 验证代码改动**——那是发布形态；日常开发一律本地 `go run`
- DB 注册的 MCP 连接（如 mcpgw 网关）本地与容器共用同一注册表，本地启动时自动拉起

**方式三：本机分步运行（不依赖容器）**

```bash
# 1. 建库（MySQL）
mysql -h <host> -u root -p < sql/init.sql

# 2. 配置（编辑 conf/mount 下的四个 yaml，替换占位符）
vim conf/mount/resource.yaml   # MySQL / Redis / COS
vim conf/mount/api.yaml        # LLM 网关 / python-exec 沙箱
vim conf/mount/custom.yaml     # 模型目录、ReAct 运行时

# 3. 构建前端 SDK（playground 页面依赖；不构建则仅 SDK 路由 404）
cd web/sdk && npm i && npm run build && cd ../..

# 4. 启动本地 python 沙箱（可选；不启动则 python_exec 工具不可用）
cd sandbox && SANDBOX_HOST=127.0.0.1 python server.py && cd ..   # 监听 :8190，需 pandas/numpy/matplotlib

# 5. 启动
go run main.go                 # 监听 :8180（conf/mount/config.yaml 可改）
```

**运维端点**：`/healthz`（存活）、`/readyz`（就绪，探测 MySQL/Redis）、`/metrics`（Prometheus 指标：run 数/模型耗时/工具失败率/WS 连接数等）；日志默认落 `log/` 目录并按大小轮转（`config.yaml` 的 `log.*` 可调）。

详细文档：

- [文档总索引](docs/README.md)（含 16 篇模块系统文档 `docs/system/` 与变更记录 `docs/changelog/`）
- [系统架构](docs/architecture.md)
- [接入方式](docs/integration-guide.md)
- [启动与部署](docs/deployment.md)
- [Runtime 模块化](docs/runtime-modularization.md)
- [MCP 客户端与仓库检索能力边界](docs/mcp.md)

## 目录结构

```
├── main.go                  # 入口：PreInit → InitResource → 路由 → 后台任务 → HTTP
├── router/                  # 路由注册（react / 注册类管理接口 / 静态资源）
├── controllers/http/        # HTTP/WS 控制器（react、attachment、caller、agent、skill、tool、apikey、systemprompt、llmmodel、probe）
├── service/react/           # ReAct 运行时核心（引擎、运行时装配、Meta Tool、委派、记忆注入、会话历史、异步任务…）
├── service/                 # 领域服务：tool / skill / agent / memory / graphmemory / workspace / mcpclient / bundle / asynctask / caller / apikey / systemprompt / llmmodel / credits / token / skillchatfile
├── api/llm/                 # LLM 客户端（claude / gpt 兼容 / minimax，流式+工具调用）
├── api/pythonexec/          # python_exec 沙箱客户端
├── models/llm/              # GORM 模型（26 张表，表结构见 sql/init.sql）
├── components/              # 错误码、响应渲染、参数结构、路由前缀、COS、调用方运行时上下文
├── conf/                    # 配置加载 + conf/mount 示例配置（脱敏）
├── middleware/              # 免鉴权中间件（信任 X-User-Name 头，缺省 anonymous）
├── web/                     # playground / replay 页面 + TypeScript SDK（embed 进二进制）
├── sql/init.sql             # 全量建库脚本
└── docs/                    # 受管文档体系（system / changelog / plan）+ 架构、接入、部署与专题文档
```

## 范围与边界

- **只做通用 ReAct 基座**：提供与业务域解耦的 ReAct 运行时和注册管理接口，不内置业务知识库检索与 Plan 自动编排（`create_plan` 仅为计划确认卡片，正式 Plan Runtime 见分支规划）。
- **对外契约保持稳定**：ReAct 引擎主循环、系统提示词装配顺序（systemPrompt + 工具索引摘要 + Skill 索引摘要）、Meta Tool 集合与描述、两段式工具加载与指纹自愈、Skill 注入，以及注册类接口（caller/skill/tool/apikey/system-prompt）的请求响应结构。
- **通用化设计**：HTTP 工具请求头透传不绑定特定 caller；异步任务 Provider 框架默认无内置 Provider，按需注册扩展。
