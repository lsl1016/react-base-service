# 服务端 AI 工作台改造方案：react-base-service × OpenHands SDK

> 依据：[openhands-sdk学习.md](./openhands-sdk学习.md)（源码调查）、[服务端AI工作台思路和讨论.md](./服务端AI工作台思路和讨论.md)（产品对话记录）、[multi-agent-orchestration.md](./multi-agent-orchestration.md)（既有 eino/adk-go 调研）。
> 目标：把 react-base-service 从「单 Agent ReAct 基座」升级为「面向数据平台运维场景的服务端 Agent 工作台基座」——支持**子 Agent 定义与委派、Skill 标准化、插件式安装、服务端代码 Workspace、危险操作 HITL**。
> 撰写日期：2026-09-12。

---

## 1. 目标与首个接入场景

对话记录已把产品定位收敛为一句话：**"OpenHands for 数据平台运维"**——不是 Dify 类应用平台，而是 Server-side Agent Workbench：

```text
统一 Agent Runtime + MCP 插件生态 + Skills + Subagent + 生产代码 Workspace + 运维 HITL/权限
```

**首个接入场景（来自对话记录）：数据平台运维排障。** 典型请求：

> "帮我分析 taskId=173821 的 UDA 加速任务为什么失败。"

期望的目标形态：

```text
用户（浏览器工作台）
  └─ main-ops-agent（排障总控：create_plan 拆解 + task 委派 + 汇总根因）
       ├─ ops-agent            → OPS MCP（query_log / metric_query / trace_query）
       ├─ dba-agent            → DBA MCP（query_schema / explain_sql / slow_query）
       ├─ data-platform-agent  → 数据平台 MCP（query_task / query_lineage / query_acceleration）
       └─ code-agent           → Code Search MCP（全局）+ repo workspace（线上代码 grep/read/git blame）

危险操作（kill / ALTER TABLE / 重启 / 重新发布）→ 人工确认（HITL）
```

对照现状，这条链路缺四块：**子 Agent 机制**、**线上代码 → Workspace 链路**、**危险操作确认**、**（远期）插件打包分发**。本文即这四块的落地方案。

---

## 2. 概念映射：OpenHands ↔ react-base-service

先建立两张对照表，看清"哪些已有、哪些是差距"。

### 2.1 已有能力映射（OH 概念 → 基座现状）

| OpenHands 概念 | 基座对应物 | 状态 |
|---|---|---|
| Agent Loop（step 循环） | `service/react/engine.go::executeReactLoop` | ✅ 同构，且有模型互备 |
| Conversation 状态机 | run 状态机（running/waiting_client_message/finished/…）+ MySQL 落库 | ✅ |
| 事件溯源 + 回放同形 | `tblLlmReactMessage` + `/react/session/events` 回放 | ✅（OH 用文件，基座用 MySQL，等价） |
| 等用户输入 = FINISHED 再入 | `waiting_client_message` + WS 上行回填 | ✅（client 工具/ask_question） |
| Tool 注册表 | `tblLlmTool` + 两段式 get_tool/execute_tool | ✅ 且更适合大工具集（摘要索引） |
| MCP 客户端 | `service/mcpclient`（stdio + 两种 HTTP）+ 动态登记 `tblLlmMcpServer` | ✅ 差凭据体系与 list_changed 动态刷新 |
| Skill 摘要注入 + 按需加载 | Skill 注册表 + 索引注入 system 前缀 + `get_skill` | ✅ 同构（OH 的 `<available_skills>` + invoke_skill） |
| 上下文压缩 | `runtime_support.go::maybeCompactContext` | ✅ |
| FinishTool / task_tracker | 计划确认 create_plan + todo_write | ✅ |
| LLM 多 provider | `api/llm`（claude/gpt/minimax） | ✅（无需 litellm） |
| python 沙箱产物 | python_exec + COS 产物 | ✅（OH 无此能力，基座反超） |

### 2.2 差距清单（按依赖顺序）

| # | 缺失能力 | OH 参照物 | 优先级 |
|---|---|---|---|
| 1 | **Agent 作为注册类资源**（name/description/提示词/工具集/skill 集/模型/权限的可配置组合体） | `AgentDefinition` + 注册表 | P1 |
| 2 | **委派工具**：主 Agent 能"看见"并调度子 Agent | `task` 工具（描述动态渲染子代理清单） | P1 |
| 3 | **隔离子 run**：子 Agent 独立历史/迭代上限，结果回填父循环 | `TaskManager` + 子 LocalConversation | P1 |
| 4 | **事件归属标识**：多 Agent 事件流区分"谁产出" | 事件 author/agentPath（eino RunPath / adk-go Branch 同义） | P1 |
| 5 | 嵌套 HITL 路由：子 Agent 的 ask_question/确认冒泡到顶层 | OH：子会话继承 confirmation_policy，确认事件在子会话流 | P1 |
| 6 | Agent 并行执行（同轮多个 task 调用） | OH `tool_concurrency_limit` + 并行 executor | P1 尾/P2 |
| 7 | **服务端代码 Workspace**：线上镜像 → repo@commit → 每 run 隔离工作区 | `clone_repos` + worktree 优化（对话记录明确要求） | P2 |
| 8 | **Skill 文件标准**（AgentSkills 格式/触发器）✅ 已落地（`/skill/import`、`/skill/import_zip`、triggers 装配期注入） | SKILL.md frontmatter + triggers | P2 |
| 9 | 危险操作确认（工具级 permission mode） | confirmation_policy + security_risk | P2 |
| 10 | **插件打包安装**（skill+agent+mcp 一键装）✅ 已落地（`/react/bundle/install`，同名覆盖可卸载回滚） | Plugin manifest（Claude Code 兼容） | P3 |
| 11 | 编排容器（Sequential/Parallel/Loop） | workflow 工具；eino/adk-go 三件套 | P3 |

> 注：#1–#5 与 [multi-agent-orchestration.md](./multi-agent-orchestration.md) 第一期建议**完全一致**——那次对 eino/adk-go 的调研得出的路径（子 Agent 即 Meta Tool + 事件路径 + 历史隔离），正是 OpenHands 生产代码的实际做法（task 工具 + 注册表 + 子会话）。**三个独立来源指向同一条路，可以放心押注。**本方案在此基础上补齐 OpenHands 独有的四块：文件型 Agent 定义、Skill 标准、插件、Workspace。

### 2.3 明确不引入的（及理由）

| 不引入 | 理由 |
|---|---|
| 每会话一个 agent-server 容器（OH 的隔离模型） | 基座是中心化多租户服务，会话隔离靠 DB 行级归属已足够；代码执行隔离用「每 run 独立 worktree 目录 + repo MCP 只读」达成，无需容器编排。未来确需任意 shell 时再评估沙箱扩展 |
| 事件逐文件存储 / fork 事件树 / navigate_to | 与「全量落 MySQL、线性回放」路线冲突；分支对话是伪需求 |
| litellm / RouterLLM | `api/llm` 已多 provider + 互备 |
| fastmcp 依赖 | 已有三种传输实现；只借鉴其凭据联合类型与 list_changed 订阅设计 |
| 完整 Claude Code 插件格式兼容 | P3 只取 manifest 思路做自有格式；生态兼容待真有需求再说 |
| 图引擎 / 平级 transfer | 既有调研已论证（eino 官方否定 + 双轨教训），不再赘述 |

---

## 3. 总体架构（目标态）

```text
                         浏览器工作台（playground / 未来运维台前端）
                                        │ WS /react/ws + HTTP 管理接口
┌───────────────────────────────────────▼───────────────────────────────────────┐
│                          react-base-service（基座）                            │
│                                                                               │
│  注册类资源（tblLlm*，管理面板可配）                                            │
│  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌───────────┐ ┌──────────────────────┐  │
│  │ caller  │ │ tool    │ │ skill   │ │ agent ★新 │ │ agent bundle/plugin  │  │
│  │ prompt  │ │ (含MCP) │ │ ★升级   │ │           │ │ ★P3 打包             │  │
│  └─────────┘ └─────────┘ └─────────┘ └─────┬─────┘ └──────────────────────┘  │
│                                            │ 注册表                           │
│  ReAct 引擎（engine.go 扩展）               ▼                                  │
│  ┌──────────────────────────────────────────────────────────────────┐        │
│  │ 主 run（现有循环） + delegate_agent Meta Tool ★新                  │        │
│  │   ├─ 命中 agent 工具 → spawn 子 run（scoped：独立历史/步数/模型）   │        │
│  │   ├─ 子 run 事件冒泡（带 agentPath）进父 WS 流，不进父 LLM 历史     │        │
│  │   └─ 子 run 最终回复 → 作为工具结果回填父循环                        │        │
│  └──────────────────────────────────────────────────────────────────┘        │
│                                                                               │
│  领域执行层                                                                    │
│  ┌──────────────┐ ┌───────────────────────────────────────────────────┐      │
│  │ mcpclient    │ │ workspace service ★P2新                            │      │
│  │ OPS/DBA/DP/  │ │  image→repo@commit 解析 · git mirror 缓存          │      │
│  │ CodeSearch   │ │  每 run git worktree → repo MCP(REPO_ROOT) 挂载     │      │
│  └──────────────┘ └───────────────────────────────────────────────────┘      │
└───────────────────────────────────────────────────────────────────────────────┘
```

设计总纲（一句话，继承既有调研结论 + OpenHands 佐证）：

> **子 Agent 是注册类配置资源；对主 Agent 呈现为一个 Meta Tool；执行是复用现有引擎的隔离子 run；事件带 agentPath 冒泡；历史默认隔离。**

---

## 4. 分期落地方案

### P1：子 Agent 资源化与委派（核心交付）

#### 4.1.1 Agent 资源表与注册

新增注册类资源 `agent`，与 tool/skill 同构（caller_key + route_values 作用域 + default 合并语义 + 管理面板 CRUD）：

```sql
CREATE TABLE tblLlmAgent (
  id            BIGINT PRIMARY KEY AUTO_INCREMENT,
  agent_key     VARCHAR(64)  NOT NULL,           -- 工具名级标识：dba-agent
  name          VARCHAR(128) NOT NULL,           -- 展示名
  description   TEXT         NOT NULL,           -- 给主 LLM 看的"何时派给谁"（对应 OH when_to_use）
  caller_key    VARCHAR(64) NOT NULL,
  route_values  VARCHAR(256) NOT NULL DEFAULT '[]',
  system_prompt MEDIUMTEXT   NOT NULL,           -- 子 Agent 系统提示词（正文）
  model_key     VARCHAR(32)  NULL,               -- 空 = inherit（继承父 run 当前模型，OH 同款默认）
  model_version VARCHAR(64)  NULL,
  tools_json    VARCHAR(1024) NOT NULL DEFAULT '[]',   -- 工具名列表（复用工具可见性机制）
  skills_json   VARCHAR(1024) NOT NULL DEFAULT '[]',   -- skill 名列表
  max_steps     INT NOT NULL DEFAULT 8,          -- 子 run 步数上限（默认小于主 run）
  permission_mode VARCHAR(16) NOT NULL DEFAULT 'inherit', -- inherit/auto/confirm/confirm_risky（P2 生效）
  status        TINYINT NOT NULL DEFAULT 1,
  created_at/updated_at ...
  UNIQUE KEY uk_caller_agent (caller_key, agent_key)
);
```

要点：

- **description 是委派质量的生命线**（OH 把 `<example>` 触发示例嵌进 description）——建议 description 约定包含"适用问题类型 + 不适用边界"两段；
- `tools_json`/`skills_json` 引用现有注册表，子 run 装配时直接复用 `prepareRuntimeRequest` 的工具/Skill 解析逻辑，只是数据源换成 agent 定义；
- 同时支持**文件定义**（OH 的 md+frontmatter）：提供 `POST /react/agent/import` 上传/粘贴 Markdown（frontmatter = 上表字段，正文 = system_prompt），解析入库。这是"像 AI 工作台那样创建子 Agent"的最直观交互，管理面板做表单、文件导入两种入口。

#### 4.1.2 delegate_agent Meta Tool（委派入口）

在 `meta_tools.go` 注册表新增内置工具（与 get_tool/execute_tool 同级，**不走两段式**——子代理数量少、schema 固定，直接暴露）：

```json
{
  "name": "delegate_agent",
  "description": "把子任务委派给专家子 Agent 执行并等待其结论。\n可用子 Agent：\n<动态渲染：每个 agent_key + description + 可用工具摘要>",
  "parameters": {
    "agent_key": "string  目标子 Agent",
    "task":      "string  完整自包含的任务描述（子 Agent 看不到当前对话，必须包含全部背景）",
    "expect":    "string  期望返回什么（可选）"
  }
}
```

- **描述动态渲染注册表清单**（OH TaskToolSet 模式）：每次 run 装配时查询该 caller 可见的 agent 列表拼进 description——主 LLM 因此"发现"子代理，零额外机制；
- `task` 参数语义要求"自包含"写进描述，因为子 Agent 上下文隔离（eino 官方实证：不共享父历史效果更好）。

#### 4.1.3 隔离子 run 执行

`execute_tool`/Meta 工具执行路径命中 `delegate_agent` 时（新文件 `service/react/delegate.go`）：

```text
delegate_agent(agent_key, task)
  1. 查 tblLlmAgent（caller 作用域解析）→ 组装子 run 配置：
       systemPrompt = agent.system_prompt + 子代理工具索引摘要 + 子代理 skill 索引摘要
       （复用现有系统提示词装配函数，数据源换掉）
       tools/skills = agent.tools_json/skills_json 解析（白名单机制照旧）
       model = agent.model_key 或继承父 run 当前模型（resolveModel 现有逻辑）
  2. 创建子 run：同 session 落库（新字段见 4.1.4），上下文 = system + 单条 user(task)
       —— 不含父历史、不含父已加载工具（隔离的两大来源）
       步数上限 = agent.max_steps；取消传播 = 父 context 派生
  3. 复用 executeReactLoop 驱动子 run（引擎不改主流程，传入 runContext 变体）
  4. 结束取子 run 最后一条 assistant 消息（OH get_agent_final_response 等价物）
       → normalizeToolResult（超长自动走 resultRef，现有机制）
       → 作为 delegate_agent 的工具结果回填父循环
  5. 子 run 的事件实时冒泡到父 WS（带 agentPath，见下），但消息不进父的 tblLlmReactMessage
```

**并发**：一期先维持基座"同一步工具串行"约束（子 run 本身是阻塞执行，串行语义自然正确）；二期为 delegate_agent 单开并行通道——模型一轮发多个 delegate_agent 调用时并行执行（agent 工具无副作用，安全），上限 `llm.react.subagent.max_parallel`（OH `tool_concurrency_limit` 同义，默认 3）。并行时子事件流按 agentPath 区分，前端分卡片渲染。

**计量**：子 run 的 token 消耗按 run 落库（现有 token 统计机制），父 run 汇总时累加 `delegated` 口径——对应 OH 的 `delegate:{id}` metrics 汇入。

#### 4.1.4 事件协议与数据模型扩展

```sql
ALTER TABLE tblLlmReactRun
  ADD COLUMN parent_run_id VARCHAR(64) NULL,     -- 子 run 指向父
  ADD COLUMN agent_path    VARCHAR(256) NULL;     -- 如 "main/ops-agent"，根 run 为 "main" 或 NULL
```

- 所有 WS 事件信封（`EventWriter`）增加 `agentPath` 字段（缺省 "main"，**旧客户端无感**）；
- 子 run 事件直接进父连接的事件流（OH/AgentTool 的 EmitInternalEvents 模式）：`tool_use_start/end` 带 agentPath 即可区分；
- `/react/session/events` 回放按时间线混排全部 run 的事件（回放与实时同形原则继续成立）；
- 子 run 消息仍在 `tblLlmReactMessage`（带 runId，天然可区分），但**父 run 上下文组装时按 runId/agentPath 过滤排除子消息**。

#### 4.1.5 嵌套 HITL（ask_question 冒泡）

子 run 内 `ask_question`/client 工具进入 waiting 时：

- 子 run 状态置 waiting（现有机制），**父工具执行协程阻塞等待**（delegate_agent 本来就是阻塞语义，天然兼容）；
- waiting 事件冒泡至顶层 WS（带 agentPath），前端问题卡片标注来源子 Agent；
- 用户上行答复按 (runId, toolUseId) 路由到子 run 继续——现有 `tool_use_answer` 协议带 runId 即可，**协议无需大改**；
- 超时/父取消 → 级联取消子 run（复用 Cancel 传播）。

#### 4.1.6 配置开关

```yaml
llm:
  react:
    subagent:
      enabled: true          # 关闭则不注册 delegate_agent、不解析 agent 资源（行为与历史版本一致）
      max_parallel: 3        # 同轮并行委派上限（一期可先 1）
      default_max_steps: 8   # agent 未配置时的子 run 步数上限
      max_depth: 2           # 委派嵌套深度上限（子 Agent 再委派，防递归失控；OH 无此限制，基座多租户需要）
```

**P1 验收**：配置 dba-agent/ops-agent 两个子代理（各挂对应 MCP 工具），在 playground 问一个跨域问题，主 Agent 出计划 → 并行/串行委派 → 前端看到嵌套事件卡片 → 子 Agent ask_question 冒泡作答后继续 → 主 Agent 汇总；回放页完整还原全过程。

### P2：场景纵深——代码 Workspace、Skill 标准、危险操作确认

#### 4.2.1 服务端代码 Workspace（运维场景的胜负手）

对话记录给过明确结论：不要每次完整 clone，用 **bare mirror + git worktree**。落地为独立模块 `service/workspace`（新）+ 复用现有 repo MCP 适配器：

```text
workspace service
  A. RuntimeSourceResolver（接口，按公司基建实现）
       resolve(service, env) → {repo_url, commit}      # 线上 image digest → commit（对话中的链路）
  B. RepoMirrorCache
       /repo-cache/<repo>.git（bare mirror，定期/按需 git fetch --prune）
  C. WorkspaceAllocator
       allocate(runId, repo, commit) → /workspaces/<runId>/<repo>（git worktree add，秒级、共享对象库）
       release(runId)   → worktree prune + 目录清理（run 结束/过期清理，参照现有 expire 逻辑）
  D. 挂载：为该 run 动态实例化一个 repo MCP server（REPO_ROOT=worktree 路径，
       复用 service/mcpclient stdio 适配器与工具同步），工具名前缀 = repo 名
```

- 工具暴露层面模型看到的就是 `bd_cron_grep / bd_cron_read_file / bd_cron_git_log…`（现有 repo MCP 工具集），**引擎零改造**；
- `resolve + allocate + 挂载`本身包装成一个业务工具（如 `load_runtime_code`，挂在 ops caller 下），供 main-ops-agent 或 code-agent 调用；
- Code Search MCP（全局检索，zoekt/es 后端）走现有动态 MCP 登记接入，与 workspace 检索形成对话记录建议的"两级检索"；
- 安全面：worktree 只读语义靠 repo MCP 工具白名单（现有机制本就无写工具）；mirror 凭据经 caller 级 ApiKey 体系（复用 tblLlmApiKey 或引入 secret 引用，P2 顺手把 MCP 凭据头支持补上——OH 的 SecretStr + `${VAR}` 展开是参照）。

#### 4.2.2 Skill 升级：兼容 AgentSkills 文件标准

现有 Skill 注册表保留为"管理面板形态"，新增文件导入能力（OH `load_skills_from_dir` 对应物）：

- 支持 `SKILL.md`（frontmatter：name/description/triggers + 正文）目录式导入：`POST /react/skill/import`（上传 zip / 粘贴）；
- 触发器三种（OH 同款）：`triggers`（关键词 → 命中时把提示追加到该条用户消息，实现于 run 装配期）；`paths`（路径 → 委派 code-agent 场景后置）；无触发器 → 仅入摘要索引（现有行为）；
- get_skill 按需加载机制不变；SKILL.md 内联命令块（`` !`cmd` ``）**不做**（服务端执行任意命令需沙箱，价值/风险比不划算，python_exec 已覆盖大部分诉求）。

#### 4.2.3 危险操作确认（工具级 permission mode）

对话记录中的运维红线：查询类自动执行，变更类人工确认。落地：

- `tblLlmTool` 增加 `permission_mode`（auto / confirm / confirm_risky）字段，管理面板可配；
- `confirm`：execute_tool 执行前 → run 进入 waiting_client_message（复用现有 client 工具等待通道，上行的不是工具结果而是"允许/拒绝"），事件 `tool_confirm_request`（带 agentPath）→ 用户允许则继续、拒绝则回填"用户拒绝"工具结果（OH UserRejectObservation 对应物）；
- `confirm_risky`：工具结果或参数匹配风险正则（如 `drop|alter|kill|restart|delete from`）时才确认——工具注册时可配 `risk_patterns`；
- 子 Agent 的 permission_mode 默认继承主 run（OH 同款），可独立收紧；
- 不引入 LLM 自评 security_risk（OH 的方案依赖工具 schema 注入自评字段，成本高且不可靠，规则表更适合运维域）。

**P2 验收**：UDA 排障全链路 demo——main-ops-agent 委派 data-platform-agent 查任务 → ops-agent 查日志 → `load_runtime_code` 建 worktree → code-agent 在线上代码 grep/git blame → dba-agent explain → 汇总根因；任一变更类工具触发确认卡片。

### P3：生态与编排（按需启动）

1. **Agent Bundle（插件包）**：manifest.json（name/version/entry）+ `agents/` + `skills/` + `mcp.json` 三类资源打包；`POST /react/bundle/install` 展开写入各注册表（OH plugin loader 的"安装即注入多类资源"语义，同名覆盖策略：bundle > 已有，可回滚）；来源限内部 git（不做公网 marketplace）。
2. **编排容器**：sequential/parallel 作为一种特殊 agent 资源（steps 引用其他 agent_key），parallel 用"泳道事件 + 按时间戳合并"（eino laneEvents）或串行近似——仅当并行委派（P1 尾）不够用时启动。
3. **子代理预算**：agent 级 token 上限，超限终止子 run 并回填原因。
4. **管理面板**：agent 列表/编辑/导入、bundle 安装、workspace 运行视图（活跃 worktree 清单）。

---

## 5. 首个场景的完整串联（P2 完成态）

```text
用户：taskId=173821 UDA 加速失败，帮我排查
  │
  ▼ main-ops-agent（caller=ops-workbench）
  ├─ create_plan：①查任务与错误 → ②查日志 → ③定位线上代码 → ④查表配置 → ⑤汇总
  ├─ delegate_agent(data-platform-agent, "查询任务 173821 的执行记录、错误信息、血缘…")
  │     └─ query_task(173821) → service=bd-cron, error="create acceleration failed: invalid partition field"
  ├─ delegate_agent(ops-agent, "查询 bd-cron 12:00-12:30 日志与指标…")
  │     └─ query_log → "invalid partition field" 首现 12:07:32
  ├─ load_runtime_code("bd-cron", "prod")
  │     └─ resolve → repo=bd-cron@83f71ad → worktree 就绪 → bd_cron_* 工具可用
  ├─ delegate_agent(code-agent, "在 bd-cron@83f71ad 中定位 'invalid partition field' 校验逻辑，"
  │                 "给出代码位置、近期变更（git log/blame）")
  │     └─ bd_cron_grep → bd_cron_read_file → bd_cron_git_blame → "校验要求 partitionField 非空，上月新增"
  ├─ delegate_agent(dba-agent, "查表 XXX 的分区配置…")   （并行示例）
  │     └─ query_partition_config → "分区字段 9 月被移除"
  └─ 汇总根因 + 建议（表配置变更与代码校验冲突）→ done
```

---

## 6. 代码落点速查（P1）

| 改动 | 位置 |
|---|---|
| agent 资源模型/服务 | 新 `models/llm/agent.go`、`service/agent/`（参照 service/skill 结构） |
| agent 管理接口 + 导入 | 新 `controllers/http/agent.go`、`router/` 注册 |
| delegate_agent Meta Tool | `service/react/meta_tools.go` 注册 + 新 `service/react/delegate.go` |
| 子 run spawn（复用引擎） | `service/react/runtime.go`（抽出可编程入口，供 WS 与委派两处调用）+ `engine.go` 最小改动 |
| 事件 agentPath | `service/react/ws.go` EventWriter 信封字段 + `history.go` 回放 |
| run 表扩展 | `sql/init.sql` 增量（parent_run_id/agent_path）+ `models/llm` 对应 |
| 并行委派（P1 尾） | `engine.go::executeToolCalls` 对 delegate_agent 类型开并行分支 |
| 配置 | `conf/mount/custom.yaml` → `llm.react.subagent` 段 |

## 7. 风险与对策

| 风险 | 对策 |
|---|---|
| 子 run 阻塞父工具协程，长任务拖住主循环 | 子 run 步数上限 + WS 取消级联；二期可让 delegate_agent 返回 async_task 化句柄（复用现有异步任务框架） |
| 委派描述不清导致主 LLM 滥用/误派 | description 约定"适用/不适用"两段；max_depth 限制；观测指标（委派命中率）进 /metrics |
| worktree 磁盘膨胀 | run 结束即 release + 定时 prune；mirror 单副本共享对象库 |
| 事件协议加字段破坏旧前端 | agentPath 缺省 "main"，信封向后兼容；SDK minor 版本同步 |
| 多子 Agent 并行时 token 计量口径 | 子 run 独立计量 + 父汇总 delegated 口径，报表先行约定 |

## 8. 与既有调研的关系

- [multi-agent-orchestration.md](./multi-agent-orchestration.md)（eino/adk-go）已给出"子 Agent 即 Meta Tool + 事件路径 + 历史隔离"一期路径——本方案 P1 与其**逐条对应并落地**，OpenHands 调查为该路径提供了第三个（也是生产验证最充分的）佐证；
- 该文档的二/三期（编排容器、StateDelta、Mode 体系）并入本方案 P3，触发条件不变；
- 本方案新增的 OH 独有增量：**文件型 Agent 定义与导入、AgentSkills Skill 标准、插件 Bundle、代码 Workspace 链路、工具级确认策略**——这些是多 Agent 编排之外的"工作台化"能力，恰是对话记录中"像 AI 工作台那样装插件、建子 Agent"的直接回应。
