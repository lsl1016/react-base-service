# 子 Agent 委派 P1 实现说明

> 依据：[服务端AI工作台改造方案.md](./服务端AI工作台改造方案.md) §4.1（P1：子 Agent 资源化与委派）。
> 本文记录 P1 的实际落点、语义约定与验收路径，供接入方与前端联调参考。

## 1. 已交付能力

| 能力 | 落点 |
|---|---|
| Agent 注册类资源（表 + CRUD + Markdown 导入） | `models/llm/agent.go`、`service/agent/`、`controllers/http/agent/`、`sql/init.sql`（tblLlmAgent） |
| delegate_agent Meta Tool（描述动态渲染子代理清单） | `service/react/meta_tools.go::runtimeToolDefinitions` + `service/react/delegate.go::delegateAgentToolDefinition` |
| 隔离子 run 执行（独立历史/步数/模型/工具集） | `service/react/delegate.go::executeDelegateAgent / buildSubAgentRuntimeRequest` |
| 事件归属标识 agentPath（实时 + 回放同形） | `params.ReactEvent/ReactHistoryEvent.AgentPath`、`runEventEmitter.agentPath`、`history.go` 回放 |
| run 表父子关系 | `tblLlmReactRun.parent_run_id / agent_path`（init.sql 含存量 ALTER 语句） |
| 嵌套 HITL（子 run ask_question / client tool 冒泡） | 子 run 共享父 `readClient`；父循环阻塞在委派工具执行中，上行消息天然路由到子 run |
| 取消级联 | 子 runCtx 从父 runCtx 派生（`WithCancelCause`），父取消/断线级联取消；子 run 同样登记进取消注册表 |
| 并行委派（P1 尾，默认关） | `engine.go::executeToolCalls` 对 delegate_agent 开并行分支，上限 `subagent.max_parallel` |
| 深度限制 | `subagent.max_depth`（默认 2）：达到上限后子 run 不再装配 delegate_agent，直接调用也会被拒 |

## 2. 配置

```yaml
llm:
  react:
    subagent:
      enabled: true          # 关闭则不注册 delegate_agent、不解析 agent 资源（与历史版本一致）
      max_parallel: 1        # 同轮并行委派上限；>1 时多个子 run 并行执行（默认 1=串行）
      default_max_steps: 8   # agent 未配置 max_steps 时的子 run 步数上限
      max_depth: 2           # 委派嵌套深度上限（子 Agent 再委派），防递归失控
```

开启前需执行 `sql/init.sql` 中 `tblLlmAgent` 建表；存量环境按 init.sql 内注释执行 `tblLlmReactRun` 增列 ALTER。

## 3. Agent 定义语义

- `agent_key`：delegate_agent 入参标识，仅允许字母/数字/`_`/`-`；同一 caller 下唯一（`caller_key=default` 为全 caller 默认作用域，与 skill 同款合并语义）。
- `description`：委派质量的生命线，注入 delegate_agent 工具描述；建议「适用问题类型 + 不适用边界」两段。
- `tools_json`：**空数组 = 继承 caller 全部可见工具**；非空 = 按 name/toolId 白名单过滤。
- `skills_json`：**空数组 = 不注入任何 Skill 索引**（专家子 Agent 行为由 system_prompt 主导）；非空按 name/skillId 过滤。
- `model_key` 为空 = 继承父 run 当前模型（含互备后的实际模型）；`max_steps` 为空 = 取 `subagent.default_max_steps`。
- 子 run 不注入长期记忆（memoryContext 置空）、不注入会话级异步任务提醒、不继承父已加载工具与 todo。
- `permission_mode` 字段已建，`inherit/auto/confirm/confirm_risky` 中后三者待 P2 危险操作确认生效。

### Markdown 导入（文件型 Agent 定义）

`POST /agent/import`，请求体 `{callerKey?, routeValues?, status?, markdown}`；markdown 格式：

```markdown
---
agent_key: dba-agent
name: DBA 专家
description: |
  适用问题：数据库性能、慢查询、执行计划分析
  不适用：非数据库问题
tools:
  - query_schema
  - explain_sql
skills: []
model_key: ""
max_steps: 6
caller_key: ops-workbench
route_values: []
---
你是一名资深 DBA……（正文即 system_prompt）
```

frontmatter 缺省字段回退请求体（caller_key/routeValues 必须至少一处提供）。

## 4. 管理接口

| 路径 | 说明 |
|---|---|
| `POST /agent/create` / `update` / `delete` / `list` / `detail` | 与 skill/tool 同构的管理 CRUD |
| `POST /agent/import` | Markdown 定义导入 |

## 5. 事件协议扩展（向后兼容）

- 事件信封新增 `agentPath`：外层 run **省略该字段**（旧客户端无感，新客户端按 `main` 渲染）；子 run 事件形如 `"main/ops-agent"`，嵌套再委派继续拼接（`"main/ops-agent/dba-agent"`）。
- 子 run 事件实时冒泡进父 WS 连接的事件流（`tool_use_start/end`、`thought_*`、`content_*`、`done` 等照常），按 `runId + agentPath` 区分卡片。
- `/react/session/events` 回放按真实时间线混排全部 run 的消息（外层与子 run 交错），事件带 agentPath；父 run 终态事件落在其全部消息之后，子 run 终态在切回父 run 时发出。

## 6. 历史隔离（关键不变量）

- 外层 run 装配 LLM 历史时**只读外层 run 的消息**（`GetOuterReactMessagesBySessionIDWithDB` 按 `parent_run_id` 过滤）；子 run 消息只属于子 run 上下文与回放，绝不进入父历史。
- 上一 run 状态继承（todo/已加载工具）只取最近一个**外层 run**。
- 会话摘要（last_run_id/last_message）只由外层 run 收敛时更新。
- 子 run 取消时父 `Cancel` 通过 context cause 级联（`ErrReactRunCancelled` 语义保持）。

## 7. P1 边界收尾（并行 HITL / 计量 / 观测 / 实时生效 / 前端）

P1 自身遗留边界已按参考项目机制收齐：

| 能力 | 实现 | 参照 |
|---|---|---|
| 并行 HITL | `service/react/client_hub.go`：外层 run 级上行消息分发器。pump 是唯一 readClient 消费方，`tool_use_answer`/`client_tool_use_end` 按 toolUseId 投递给声明关心它的等待者；cancel 广播级联取消；可寻址消息未匹配时进有界待领缓冲（答复先于等待者注册到达的竞态）；非可寻址消息在单等待者时保持历史严格语义。`waitAskQuestionAnswer`/`waitClientToolOutput(s)` 统一走 hub，`max_parallel>1` 时多个子 run 并行等待用户输入互不干扰 | adk-go openLongRunningCallIDs 按 ID 分发 + "一轮输入回答多个等待者"；eino Address 寻址的简化（toolUseId 全局唯一） |
| 委派计量 | `tblLlmReactRun` 增 `delegated_input/output_tokens`；子 run 终态时把其消耗（含递归口径）SQL 原子累加进父 run 行（`AccumulateReactRunDelegatedTokens`，并行委派并发安全）；done 事件透出 `delegatedInputTokens/delegatedOutputTokens` | OpenHands `usage_to_metrics["delegate:{id}"]` 回写父级 + combined 汇总进预算 |
| 委派观测 | `react_delegations_total{agent_key,status}`：success/error/cancelled/no_response/agent_not_found/depth_limited，配合既有工具耗时直方图观测委派命中率与误配 | 方案风险表"委派命中率进 /metrics" |
| agent 实时生效 | 委派执行时实时查库解析 agent 定义（`resolveAgentForKey`，caller+default 作用域合并，失败回退 run 快照）；管理面板变更从下一次委派起生效 | OpenHands 文件型定义 + 基座多租户管理面板的折中 |
| SDK/前端 agentPath | 协议 `ReactEvent.agentPath`；reducer 按 (runId,index) 独立归组子 run 步骤；子 run 终态只收敛自己的卡片、不改变外层 status/lastRunStats（否则会误收敛父的 delegate 卡片）；工具卡片与消息块渲染 agentPath 徽章 + 子 Agent 缩进；dist 已重新构建 | adk-go branch 隔离 + "不给等待者看别人的答复" |

e2e 验证（`REACT_DELEGATE_E2E=1`，真实 MySQL + glm-4.6 + mcp-server 网关）：
`TestDelegateParallelHitlE2E`（两个子 Agent 并行 ask_question、答案按 toolUseId 路由无交叉、父 run delegated>0）、
`TestDelegateDepthLimitE2E`（max_depth=1 下子 run 不装配 delegate_agent、无孙 run、自动降级自行完成）。

## 8. 已知边界（按方案预留）

- 并行 HITL 的待领缓冲有界（16 条，丢最旧）：前端对同一 toolUseId 重复作答等异常洪峰下，
  最早的未认领答复可能被丢弃，等待方最终由 cancel/断连收敛。
- 委派清单（delegate_agent 工具描述）仍是外层 run 装配快照：新 agent 在下一个外层 run
  才对主 LLM 可见（委派执行时已实时解析定义，见第 7 节）。
- P2（代码 Workspace、AgentSkills 文件标准、危险操作确认）与 P3（Bundle 插件、编排容器）按方案后续推进。


## 9. P2 已落地：危险操作确认（P2-3）与代码工作区（P2-1）

### P2-3 危险操作确认（commit 6b1da61）

- `tblLlmTool.permission_mode`：auto（默认，行为与历史一致）/ confirm / confirm_risky；
  `config.riskPatterns` 自定义风险正则（默认表：drop/alter/truncate/delete from/update set/
  kill/shutdown/restart；匹配目标=工具名（下划线归一空格）+ 序列化入参）。
- 确认门在 executeServerTool（http/mcp）执行前：`tool_confirm_request` 事件（带 agentPath）→
  waiting_client_message → 经 clientHub 按 toolUseId 等 `tool_confirm_answer`；
  拒绝回填「用户拒绝」（IsError，卡片 rejected，指示模型不得重试）。
- agent 级收紧：`tblLlmAgent.permission_mode`（frontmatter 支持）作为子 run 权限下限。
- SDK：确认卡片（允许/拒绝按钮）+ `sendToolConfirmAnswer`。

### P2-1 代码工作区（commit 655715c）

- `llm.react.workspace`：enabled/root_dir/mirror_dir/resolvers（静态白名单：
  service+env → repo_url+ref；真实镜像中心解析后续接入）。
- `service/workspace`：bare mirror 单副本（clone --mirror/fetch --prune）→ 每 run
  git worktree（commit 锁定，绝对路径）→ 动态挂载 `ws_<service>_` 前缀只读 repo MCP 工具
  → run 终态统一释放（工具副本/MCP 客户端/worktree；孤儿目录全清兜底）。
- `load_runtime_code` 内置工具为入口；模型按 get_tool/execute_tool 两段式使用检索工具。
- 安全：service/env/ref 正则校验、git 子命令白名单、常量可执行文件不经 shell、
  只读语义由 repo-mcp 工具集保证。

两者 e2e 均真实链路验证（MySQL + glm-4.6 + mcp 网关 / 真实 git），复跑方式：
`REACT_DELEGATE_E2E=1 go test ./service/react/ -run 'TestToolConfirm|TestWorkspace'`。

### P2-2 Skill 文件标准

- 数据模型：`tblLlmSkill` 加 `triggers_json`（关键词数组，确定性匹配）与 `content`
  （SKILL.md 正文）两列；存量库执行 init.sql「存量增量迁移」注释块的 ALTER。
  `trigger_condition` 保持原语义（给模型读的纯文本），两者并存。
- 导入：`POST /skill/import`（Markdown 粘贴，frontmatter：name/description/triggers +
  正文；caller_key/route_values 缺省回退请求体）与 `POST /skill/import_zip`
  （zip 批量，单文件 ≤1MB、SKILL.md ≤64 个、解压总量 ≤10MB，纯内存读取防 zip 炸弹）。
- 冲突语义是**同名覆盖**：同 caller_key+name 未软删行 → 只更新文件形态拥有的字段
  （description/triggers_json/content/status/updated_by，沿用 skill_id，不动 is_default
  与面板维护的注册表字段）；不存在 → 走 CreateSkill 全量校验新建。
  为 P3 Bundle「同名覆盖可回滚」铺路。skill_id 为 UUID 无软删占键问题，无需 agent 式 revive。
- 触发器（run 装配期）：`prepareRuntimeRequest` 构造 modelUserMessage 时对用户消息做
  子串匹配（ASCII 大小写不敏感，命中上限 5 条），命中则把 `<skill-trigger-hint>` 块
  追加到消息末尾。注入语义：进当轮模型请求 + 随 persistUserInput 落库（后续轮次上下文
  回放一致）；前端历史展示只读用户原文（content 字段）不受污染；委派子 run 整体覆写
  modelUserMessage，hint 天然不进入子 run（V1 外层生效）。
- 无 triggers 的 skill 行为不变（仅摘要索引进 system 前缀）；get_skill 按需加载机制不动
  （content 列随全量 JSON 自动带给模型）。
- `paths` 触发器按方案后置（等 code-agent 委派场景）；SKILL.md 内联命令块按方案不做。
- e2e 复跑：`REACT_DELEGATE_E2E=1 go test ./service/react/ -run TestSkillTriggerE2E`
  （命中注入 + 原文不受污染 + 未命中不注入）。

### P3 已落地：子代理预算、Agent Bundle、管理面板

**子代理预算（token 口径）**：`tblLlmAgent.max_tokens_per_run`（0=不限，frontmatter 同名支持）。
口径 = 子 run 输入+输出+委派孙代理 delegated_*（递归）；engine 每轮模型调用后检查，
仅在还有后续工具轮次时终止（已产出最终回答的轮次保留完成态，OH max_budget_per_run 同款语义），
超限错误经委派既有软错误通道回填父循环。

**Agent Bundle 插件包**：布局 OH Claude Code 兼容（`.plugin/plugin.json` 或根 `manifest.json`，
name 强校验 kebab-case）+ `agents/*.md` + `skills/<name>/SKILL.md` + `.mcp.json`（仅 url 形式）。
新表 `tblLlmBundle`/`tblLlmBundleResource`，资源清单记录 previous_state_json 覆盖前整行快照——
卸载按清单逆序回滚（新建软删、覆盖按快照恢复含软删态还原），这是 OH 没有而方案要求的增强。
拉取：git URL 走 mirror 缓存 + rev-parse 钉 commit + 临时 worktree（git 子命令白名单与
workspace 同集）；本地路径直接读目录。来源限 `llm.react.bundle.allowed_source_prefixes`
白名单前缀（空列表拒绝一切）。重装同名字 = 先卸载还原再装新；安装中途失败自动卸载清理。
API：`POST /react/bundle/install | uninstall | list`。

**管理面板**（web/react/index.js 数据驱动扩展）：「子 Agent」标准表格 tab（CRUD + 权限模式/预算字段）；
通用「导入定义」机制（`importPath` 配置，agent 与 skill 面板共用 Markdown 粘贴导入，补齐 P2-2 的
skill 导入 UI 缺口）；「Bundle」卡片 tab（安装表单 + 已装卡片 + 卸载回滚按钮）；「工作区」只读
卡片 tab（`POST /react/workspace/active` 暴露 workspace.Manager 活跃 worktree 快照，run 结束即空）。

e2e 复跑：`REACT_DELEGATE_E2E=1 go test ./service/react/ -run TestSubAgentBudgetE2E`、
`REACT_DELEGATE_E2E=1 go test ./service/bundle/ -run TestBundleInstallUninstallE2E`；
面板已浏览器全链路冒烟（agent 导入、bundle 安装/卸载回滚、工作区活态卡片随 run 释放）。
