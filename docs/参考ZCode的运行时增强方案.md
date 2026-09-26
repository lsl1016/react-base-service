# 参考 ZCode 的运行时增强方案：工具元数据 / 内置智能体 / 内置技能 / 网页能力 / 权限规则

> 依据：[ZCode学习.md](./ZCode学习.md)（2026-09-26 ~ 09-27，对本机 ZCode 运行时 `zcode.cjs` 构建产物逆向 + 源码仓库 `C:\Users\keke\Desktop\xm\ZCode` 的比对调查）；本仓库现状对照来自 `service/react/` 各实现与 [todo/20260926_多智能体编排补齐点与场景.md](./todo/20260926_多智能体编排补齐点与场景.md)。
> 目标：借鉴 ZCode「统一工具元数据注册、内置可覆盖的子智能体、引擎+数据分离的技能包、服务端原生搜索委托、allow/deny 权限规则」五项成熟设计，补齐 react-base-service 运行时对应缺口。本方案只做设计，不含实现。
> 撰写日期：2026-09-27。

---

## 1. 调研结论摘要（借鉴来源）

ZCode 是 CLI 形态的单机 agent 运行时，与本仓库（服务端多租户 React 基座）形态不同，但五项机制设计值得直接借鉴：

1. **统一工具元数据注册模型**：每个工具一次声明 `readOnly / destructive / concurrentSafe / sideEffectScope(none|session|network|system) / riskLevel(low|medium|high) / needsApproval / timeoutMs / maxOutputBytes / permission(含 patternSources 与 deny 优先级) / resultBudget(inline 预算 + artifact 化 + head 预览)`。权限审批、输出裁剪、并发调度全部由声明层驱动，工具实现不写 if-else。
2. **输出预算差异化**：搜索类 20KB / 网页类 100KB / Shell 10MB，按副作用类型分级；超限转 session 级 artifact + head 预览，模型上下文只吃 inline 预算内部分。
3. **只读约束三层落地**：工具清单收敛 + 系统提示词禁止清单（连 `>`、`>>`、`|`、heredoc 写文件都点名）+ 元数据只读标记。单靠提示词不可靠，元数据硬拦截才是底线。
4. **引擎与内容分离**：技能正文不内嵌引擎，作为数据包随发行版分发（manifest + seed 防重复落地 + 多级 rootCandidates）；内置智能体允许用户/项目同名覆盖，且覆盖后"不按名称套用内置行为"。
5. **WebSearch 委托服务端原生能力**：模型能力位 `supportsNativeWebSearch` 校验通过后把 `web_search` 服务端工具配置（max_uses / allowed_domains / blocked_domains）随 API 请求下发，本地零爬虫；WebFetch 则是 URL→markdown→小快模型按 prompt 代答（60s 超时 / 100KB 上限 / 15min 缓存）。

---

## 2. 现状对照（ZCode → react-base-service）

| ZCode 的设计 | 我们已有 | 差距 |
|---|---|---|
| 工具统一元数据注册 | meta tool 在 `service/react/meta_tools.go::internalMetaToolDefinitions()` 散装构造；业务工具只有 `tblLlmTool.permission_mode` + `config.riskPatterns` 两项 | **缺统一声明层**：并发/超时/输出预算/风险标记没有驱动来源 |
| 输出预算 + artifact 化 | `resultRef` 外置 + `read_tool_result` + 全局 `conf.ReactToolResult`（inline 64KB / DB 16MB / 单读 8KB） | 机制齐备，但**预算全局统一，不能按工具差异化** |
| WebSearch 原生委托 / WebFetch 代读 | 无任何 web 工具 | **整块缺失** |
| 内置 Explore / general-purpose 子智能体，同名可覆盖 | `tblLlmAgent` 纯注册资源，空库零智能体 | **缺内置 + 覆盖语义**：空库 caller 的 `delegate_agent` 描述是空清单，委派能力形同虚设 |
| 引擎+数据分离的 bundled-skills | SKILL.md/ZIP 导入 + bundle upsert（`service/skill/skill.go::UpsertFromMarkdownForBundle`）设施齐全 | **缺内容供给**：零出厂技能（`service/caller/caller.go::createDefaultSkill` 的"代码现拼一条 Skill"旧思路已注释停用） |
| patternSources 权限（always-allow / deny，deny 优先于询问） | 三态 `permission_mode`（auto/confirm/confirm_risky）+ 风险正则（`service/react/tool_confirm.go`） | **缺两端**：deny（直接拒绝、不进 HITL）与 allow（免确认） |
| 只读约束三层落地 | 子代理隔离已做到（独立历史/白名单/不注入记忆，`delegate.go::buildSubAgentRuntimeRequest`） | 缺"元数据只读"层，无法做真正的只读型子代理 |
| `run_in_background` / 看门狗 / token 递归归账 | `delegate_agent` 的 `background=true`、`subagent.max_run_seconds`、`accumulateDelegatedTokens` | ✅ 已对齐 |

> todo 的 **P1（wait_agent join 原语）** 是编排原语缺口，与本方案正交，本文不重复；WP1 落地后顺带给 wait_agent 这类新工具提供统一注册位。

---

## 3. 工作包

### WP1：统一工具元数据注册模型（基础，优先做）

**现状**：每个 meta tool 用 `objectTool()` 现拼声明，工具间无 readOnly/超时/预算/并发安全差异；业务工具的风险信息只服务确认门一处。

**设计**：新增 `ToolMeta` 声明层，meta tool 与业务工具归一到同一模型：

```go
type ToolMeta struct {
    ReadOnly        bool            // 只读（研究型子代理白名单的判定依据）
    ConcurrentSafe  bool            // 同轮多调用是否可并行
    SideEffect      string          // none | session | network | system
    RiskLevel       string          // low | medium | high（UI 徽标 + 确认门默认档）
    TimeoutMs       int             // 0 = 跟随全局 defaultReactToolDefaultTimeoutMs
    MaxOutputBytes  int             // 0 = 跟随全局 InlineLimitBytes
    PermissionRules *PermissionRules // WP5 接入点，nil = 走 permission_mode 三态
}
```

**落点**：

1. `internalMetaToolDefinitions()` 改为查 `map[string]ToolMeta` 注册表（24 个内置工具全量登记）；
2. 业务工具在 `business_tool.go` 装配时从 `tblLlmTool.Config` 解析出同结构——config 加约定字段 `readOnly / riskLevel / timeoutMs / maxOutputBytes`，**不迁移表结构**；
3. 消费方全部由声明驱动，工具实现零 if-else：
   - 确认门：`resolveEffectiveToolPermission` 的输出叠加 RiskLevel 默认档；
   - 输出裁剪：`engine.go` 落 resultRef 前按 per-tool `MaxOutputBytes` 覆盖全局默认（web 类 20~100KB、业务工具维持现状）；
   - 并发调度：`tool_dispatch.go` 同轮工具按 `ConcurrentSafe` 决定并行/串行；
   - WS 事件与工具卡片带 `riskLevel / readOnly`，前端渲染风险徽标；
   - `/react/config/schema` 下发字段声明，面板可编辑。

**验收**：内置 meta tool 全量登记元数据；`execute_tool` 能按业务工具 `readOnly=true` 拒绝写操作（WP2 依赖）；go 全量测试 + 前端 tsc/vitest/build 通过。

### WP2：内置子智能体 + 同名覆盖（依赖 WP1）

**现状**：空库 caller 的 `delegate_agent` 描述里"可用子 Agent"为空，委派能力形同虚设。

**设计**：代码内置 2~3 个 profile，`agentResolver().Resolve` 解析顺序改为 **DB（caller 作用域）> 内置**——同 `agent_key` 的 DB 行覆盖内置，且覆盖后完全按 DB 行执行（ZCode 源码 profile.ts 的教训原文："用户/项目 profile 可以同名覆盖内置 Explore，不能只按名称套用内置行为"）。

| agent_key | 定位 | 工具白名单 |
|---|---|---|
| `general-purpose` | 通用专家，继承父 run 全量工具 | 全量 |
| `researcher` | 只读调查（ZCode Explore 对应物） | `get_tool` / `execute_tool`(仅 readOnly) / `read_tool_result` / `inspect_data` / web 工具(WP4) / `ask_question` |
| `report-writer` | 汇总产出 | `inspect_data` / `python_exec` / `displayFiles` |

**只读三层落地**（ZCode 口径）：

1. 工具白名单收敛（上表）；
2. researcher 系统提示词写明确禁止清单；
3. `execute_tool` 由 ToolMeta.ReadOnly **硬拦截**非只读业务工具——这是唯一可靠的一层，前两层是软约束。

**治理对齐**（延续本仓库"非目标"清单纪律）：内置 profile 在管理面板可见、可 fork 成 DB 行再改，**不绕开 tblLlmAgent 的治理口径**；`delegate_agent` 描述渲染时内置项标注 `(built-in)`。

**验收**：空库新 caller 首轮对话即可委派给 researcher 并被硬拦截写操作；面板 fork 后同名 DB 行生效、内置定义不再参与解析。

### WP3：内置技能包（引擎+数据分离，见效最快）

**现状**：导入/upsert/触发设施全齐（`ImportFromMarkdown` / `ImportFromZip` / `UpsertFromMarkdownForBundle` / `triggersJson` 关键词触发），但零内容。`caller.go` 被注释的 `createDefaultSkill` 是"代码现拼一条 Skill"的旧思路，ZCode 方案证明更好的形态是**数据包携带 + 幂等落地 + 允许覆盖**。

**设计**：

1. 仓库建 `bundled-skills/` 目录，每技能一个目录（`SKILL.md` + 可选 `references/`、`templates/`，ZCode 三件套结构），`go:embed` 打进二进制（与 `web/sdk/dist` 同模式）；
2. manifest（版本号 + 文件清单）+ 落地记录（`tblLlmSkill` 加 `bundled_version` 列，或独立 seed 表）：caller 创建与服务升级时幂等导入，**DB 同名行存在则跳过**（用户覆盖优先，与 WP2 同一语义）；
3. 导入实现直接复用 `UpsertFromMarkdownForBundle`，接近零新代码。

**首批内容**（从仓库已有最佳实践沉淀，均有现成调优文案）：

| 技能 | 来源 |
|---|---|
| 委派 task 写作规范（自包含、写明全部已知信息与期望产出） | `delegate.go` 工具描述文案 |
| python_exec 数据分析规范 | `meta_tools.go::pythonExecToolDefinition` 描述与用法约定 |
| 大结果处理规范（何时用 read_tool_result 分页读 / inspect_data 优先） | `read_tool_result` / `inspect_data` 工具描述 |
| 业务工具两段式调用规范（get_tool → execute_tool） | `get_tool` / `execute_tool` 工具描述 |

**验收**：空库起服务，新 caller 自动获得内置技能且出现在 run 装配的 skill 索引；同名自建技能不被覆盖；升级时新版本技能对未覆盖的旧版本生效。

### WP4：网页能力（web_fetch + web_search 双形态）

**设计**：

1. **`web_fetch`**（新 meta tool）：URL→markdown→**小快模型按 prompt 代答**（复用多模型配置，挑便宜模型；ZCode 口径 60s 超时 / 100KB 上限 / 15min 缓存）。ToolMeta 标 `SideEffect=network, RiskLevel=medium`，是否默认暴露由 conf 开关控制（对齐 memory/graph_memory 的开关模式）。
2. **`web_search`**：优先 provider 原生——模型目录（`supports_thinking` 已有先例）加 `supports_native_web_search` 能力位，claude 线把 `web_search` 服务端工具配置（max_uses / allowed_domains / blocked_domains）随请求下发；能力缺失时工具不注册或软提示，**本地不做爬虫**（合规风险与维护成本都不划算）。
3. 两个工具直接进 researcher（WP2）白名单，补齐"调查型子代理"的最后一块。

**验收**：web_fetch 对公开 URL 稳定代答且超限截断符合预算；web_search 在支持原生搜索的模型上生效、不支持的模型上不注册且无报错。

### WP5：权限规则补两端（allow / deny）

**现状**：`permission_mode` 三态 + riskPatterns 只有"是否要问"，没有"直接拒"和"免问"。

**设计**：`tblLlmTool.Config` 扩展：

```json
{
  "permissionRules": {
    "allow": ["(?i)^query_"],
    "deny":  ["(?i)\\bdrop\\b|\\btruncate\\b"]
  }
}
```

求值顺序 **deny → allow → permission_mode 三态**（deny 优先于询问，ZCode `denyPriority:"beforeAsk"` 口径）：deny 命中直接回"策略拒绝"的工具结果（不进 HITL 确认流、不计入确认等待）；allow 命中跳过确认。规则匹配目标沿用现有口径（工具名 + 序列化入参）。落点在 `tool_confirm.go` 确认门前加一层，改动集中；管理面板与 `/react/config/schema` 联动。

**验收**：deny 规则命中不产生 `tool_confirm_request` 事件；allow 规则命中 confirm 模式工具不确认直接执行；规则非法（正则编译失败）时 fail-open 并日志告警，与管理接口校验兜底口径一致。

---

## 4. 优先级与实施顺序

```text
WP1（统一元数据，纯重构无行为变化，一切的基础）
  → WP3（内置技能包：复用现有导入设施，纯内容，最快见效）
    → WP2（内置智能体：吃 WP1 的 ReadOnly 硬拦截）
WP4 / WP5（独立，可随时并行插入）
```

- WP1 与 todo P1（wait_agent）无冲突，且给后续新工具（wait_agent、web_fetch 等）提供统一注册位；
- WP3 建议紧跟 WP1：零机制风险，用户可感知收益最直接；
- WP4 的 web_search 能力位依赖模型目录（`tblLlmUserModel` 能力开关列已就位，加一列即可）。

---

## 5. 非目标（有意不做，防止过度设计）

延续 [todo/20260926_多智能体编排补齐点与场景.md](./todo/20260926_多智能体编排补齐点与场景.md) §四的纪律：

- **不做 Bash 级命令只读判定子系统**：ZCode 用 20+ 策略文件判 Bash 命令风险，是因为它暴露 shell；本仓库只有受控 HTTP/MCP/python 工具，`readOnly` 声明 + riskPatterns 已够。
- **不做运行时动态创建 agent 定义**：ZCode 的 Agent 工具允许临时拼 subagent_type；本仓库 agent 是带工具策略的治理对象（tblLlmAgent），模型凭空造 agent 会绕开治理。若需"清单外的专家"，走受控临时模板方向单独设计。
- **不做本地文件系统工具（Read/Write/Edit/Glob/Grep）**：本仓库是服务端多租户运行时，本地文件语义不成立；代码执行已有 sandbox + `load_runtime_code`（P2-1 代码工作区）承担。
- **不做兄弟智能体点对点消息总线**：兄弟间经父模型中转，可审计可归因（todo §四既有结论，维持）。
