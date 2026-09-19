# Runtime 模块化重构（feature/runtime-modularization）

## 目标

本分支执行第一轮“中等拆分”：

- 保持单仓库、单 Go 服务、单部署单元；
- 不修改 HTTP API、WebSocket Event、数据库表、前端 SDK、Tool 两段式协议；
- 把 `service/react` 从“实现所有能力”调整为“编排 Runtime 能力”；
- 优先建立 Tool / Agent / Memory 的明确代码边界，为后续 Plan、Workspace、Durable Execution 留出稳定扩展点。

## 依赖方向

```text
service/react
   Agent Runtime / ReAct orchestration
          |
          +-- serverToolRuntime -> service/tool
          |                         +-- HTTP
          |                         +-- MCP
          |
          +-- agentRuntime -------> service/agent
          |                         +-- visible agent resolver
          |                         +-- live agent resolver
          |
          +-- memoryRuntime ------> service/memory
                                    +-- scope
                                    +-- merge/render
                                    +-- list/read
                                    +-- mutation
```

约束：

1. `service/react` 仍是唯一 ReAct 执行引擎。
2. 子 Agent 仍创建独立 ReactRun，并复用 `executeReactLoop`。
3. Client Tool 仍留在 `service/react`，因为它依赖 WebSocket/HITL。
4. Tool confirmation、tool event、resultRef、async snapshot 等 Agent Runtime 语义仍留在 `service/react`。
5. HTTP/MCP 的传输细节由 `service/tool.Runtime` 负责。
6. Memory 的 caller/caller_user scope、覆盖合并、上下文渲染、list/read 查询和 mutation 由 `service/memory.Runtime` 负责。

## 本轮改动

### 1. Runtime Services

新增：

- `service/react/runtime_services.go`

统一注入：

- `serverToolRuntime`
- `agentRuntime`
- `memoryRuntime`

`runtimeRequest` 会携带 Runtime Services，子 Agent 通过 `prepareRuntimeRequestWithServices` 继承父 Run 的同一组依赖。

### 2. Tool Runtime

新增：

- `service/tool/runtime.go`
- `service/tool/runtime_test.go`

`service/tool.Runtime.Execute` 统一处理后端托管 Business Tool：

- HTTP Tool
- MCP Tool

统一结果类型：

```go
type ExecuteResult struct {
    Content           string
    StructuredContent any
    Meta              map[string]any
}
```

其中 `StructuredContent` / `Meta` 当前作为扩展位，后续可承接 MCP structuredContent/outputSchema，不在本轮改变现有模型上下文格式。

### 3. Tool Dispatch 文件拆分

新增：

- `service/react/tool_dispatch.go`
- `service/react/business_tool.go`

从 `engine.go` 移出：

- executeToolCalls
- executeToolCall
- delegate 并行调度
- Server Tool orchestration

从 `meta_tools.go` 移出：

- Business Tool index
- get_tool
- execute_tool
- active tool restore
- definition fingerprint
- loaded tool persistence

ReAct 主循环本身不再直接感知 HTTP/MCP 的执行实现。

### 4. Agent Runtime Resolver

新增：

- `service/agent/runtime.go`

负责：

- caller/route 下可见 Agent 查询
- 委派前 Agent 实时解析
- tools/skills 白名单运行期策略
- permissionMode / maxSteps / tokenBudget 运行期归一

仍留在 `service/react/delegate.go`：

- Child ReactRun 创建
- parent_run_id / agent_path
- token accounting
- clientHub
- cancel propagation
- executeReactLoop

避免出现 `react -> agent -> react` 循环依赖。

### 5. Memory Runtime

新增：

- `service/memory/runtime.go`
- `service/memory/runtime_test.go`

下沉能力：

- caller/caller_user scope
- write owner
- scope permission set
- caller_user 覆盖 caller 的 merge
- resident/detached context render
- memory_list 查询与过滤
- memory_read 权限校验与读取
- ApplyMutation Runtime 边界

`service/react/memory.go` 保留 Tool schema、输入解析和 reflection 单 Run 写入额度等 Runtime 语义。

## 明确未改变

本轮不修改：

- ReAct 对外 WebSocket 协议
- Event type / payload
- HTTP API
- tblLlmReactRun / tblLlmReactMessage 等表结构
- MCP 配置格式
- get_tool -> execute_tool 协议
- Memory 表结构和 revision 审计
- Agent 表结构
- parent_run_id / agent_path
- Client Tool / ask_question 的 HITL 协议
- 前端 Agent Lane UI
- Context Compact 行为
- Workspace 行为

## Runtime State 整理

`runtimeRequest` 已按 identity/model/capabilities/execution/conversation 分组；
`reactEngineState` 已按 model/conversation/async/usage 分组，并由统一构造函数初始化。
匿名嵌入保持现有字段读取语义，对执行路径无行为变化。

## 后续建议

下一轮优先级：

1. 将 Skill Runtime 从 `meta_tools.go` 下沉为独立 resolver。
3. 将 Tool Result 统一升级为可表达 MCP `structuredContent` / `outputSchema` 的结构。
4. 为 Agent 引入显式 capability policy：
   - toolPolicy
   - skillPolicy
   - memoryPolicy
   - workspacePolicy
5. 给 Business Tool 增加 side-effect 元数据：
   - read_only
   - write
   - destructive
   - exclusive
   供多 Agent 并发调度使用。
6. Workspace 若演进到 shell/build/container 执行，再考虑独立 Sandbox Service；当前不拆微服务。

## 验证要求

每次提交继续执行现有 CI：

```bash
go build ./...
go vet ./...
go test ./... -count=1
cd web/sdk && npm ci --no-audit --no-fund && npm run build
```

Integration tests (MySQL) 仍保持 workflow_dispatch 手动触发。
