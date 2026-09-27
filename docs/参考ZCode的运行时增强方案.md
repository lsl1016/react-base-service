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

---

## 6. 实现说明（2026-09-27，WP1/WP2/WP3 + web_fetch 已落地）

> 全量 go test 通过；前端 web/ 零改动。以下为实际落点与设计取舍，供联调与后续 WP4/WP5 参考。

### 6.1 WP1 统一工具元数据（已落地）

- **注册表**：`service/react/tool_meta.go`——`ToolMeta{ReadOnly, ConcurrentSafe, SideEffect, RiskLevel, TimeoutMs, MaxOutputBytes}` + `metaToolRegistry`，25 个内置工具全量登记（含新增 web_fetch）；`TestToolMetaRegistryCoversInternalTools` 保证 isInternalMetaTool 清单与注册表同步（新增工具漏登记会测试失败）。
- **业务工具声明**：`service/tool/executor.go::ToolConfig` 扩展 `readOnly / riskLevel / maxOutputBytes`（`timeout_ms` 已有）；`businessToolMeta()` 解析，config 非法保守按可写/串行/全局预算。
- **三项接线**：
  1. 只读硬拦截：`runtimeRequest.enforceReadOnlyTools` 开启后，`executeServerTool` / `executeClientTool` 对非 readOnly 业务工具直接回错误结果（`readOnlyViolationResult`）；
  2. 输出预算：`normalizeToolResultWithLimit`，`executeServerTool` 按业务工具 `config.maxOutputBytes` 覆盖全局 InlineLimitBytes（web_fetch 注册表值 20KB）；
  3. 并发调度：`tool_dispatch.go::roundParallelism`——ConcurrentSafe 只读工具同轮并发上限 4；delegate_agent 并行仍严格跟随 `subagent.max_parallel`（默认 1=串行的成本语义保留）。

### 6.2 WP2 内置子智能体（已落地）

- **内置 profile**：`service/agent/builtin.go`——`general-purpose`（继承全部业务工具）/ `researcher`（`@readonly` 白名单 + `ReadOnly=1` 只读执行域）/ `report-writer`（`@none` + python_exec/displayFiles 数据加工）。物化为 `model.Agent` 值，`AgentID` 带 `builtin:` 前缀，不落库。
- **同名覆盖**：`Runtime.FindVisible/Resolve` 合并规则 = DB 行在前 + 未被同 agent_key 遮蔽的内置在后；DB 同名行整体生效（`mergeBuiltinAgents`），engine 其余代码零改动获得覆盖语义。**行为只来自解析命中的定义，无任何按名分支**（ZCode profile.ts 纪律）。
- **白名单 token**：`tools_json` 支持 `@none`（显式不继承业务工具，区别于空数组=继承全部的存量语义）与 `@readonly`（只保留 config.readOnly 业务工具），`FilterToolIndexSnapshot` 签名增加全量工具列表参数（`runtimeRequest.visibleTools` 随 run 传递）。
- **只读执行域单调收紧**：`buildSubAgentRuntimeRequest` 中 `enforceReadOnlyTools = 父run开关 || agent.ReadOnly`——嵌套委派只能继承不能放宽。researcher 的只读保证是三层：白名单收敛 + 系统提示词禁止清单 + ToolMeta 硬拦截（第三层唯一可靠）。
- **表结构**：`tblLlmAgent` 增 `read_only TINYINT DEFAULT 0`（init.sql 建表 + 存量 ALTER 注释），params 的 Create/Update/Resp 均已接入。
- **delegate 描述**：内置项标注 `(built-in)`，token 渲染为「（仅只读业务工具）/（无业务工具）」。
- 顺带修复历史缺口：`delegate_agent / send_message / memory_* / graph_memory_* / load_runtime_code` 此前未加入 `tool.reservedToolNames`（业务工具可与其同名），已补齐并由测试兜底。

### 6.3 WP3 内置技能包（已落地）

- **内容**：`service/skill/bundled-skills/`——manifest.json + 4 个技能（委派任务写作 / 大数据结果处理 / 业务工具两段式调用 / Python 数据分析），正文均从仓库既有调优文案沉淀。
- **引擎+数据分离**：`service/skill/bundled.go`——`go:embed bundled-skills` 打进二进制；`EnsureBundledSkillsForCaller` 幂等导入（同 caller+name 活跃行即跳过，**用户覆盖优先、永不回写**），单条失败只记日志不阻断。
- **两个触发点**：caller 创建（`service/caller/caller.go::RegisterCaller`）+ 服务启动为存量 caller 补种（`main.go` 异步调 `SeedBundledSkillsForAllCallers`，含 default 伪作用域）。生效路径与用户自建技能完全一致（skill 索引注入 + get_skill 按需加载 + triggers 关键词触发）。

### 6.4 web_fetch（已落地，WP4 的第一步）

- `service/react/web_fetch.go`：URL→下载（2MB 硬上限）→正文提取（HTML 去脚本/样式/标签+实体解码，文本类原样）→按 `max_content_runes`（默认 20000）截断回填，完整正文经 resultRef 分页续读。
- 安全边界：仅 http/https；拒绝环回/私网/链路本地地址**字面量**与 localhost/.internal 后缀（重定向同样校验；DNS rebinding 不在本版防护范围，生产建议 egress 代理）；Content-Type 仅文本类。
- 15 分钟内存缓存（同 URL 零成本重取）；conf 开关 `llm.react.web_fetch.enabled`（默认关，custom.yaml 已加示例）；软着陆窗口放行（只读）。
- 与 ZCode 的差异：ZCode 抓取后用小快模型按 prompt 代答；本版返回提取正文（更通用），LLM 代答形态后续按需叠加。

### 6.5 code-reader 只读代码调查员（2026-09-27 追加，参考 ZCode Explore）

读代码工具链（`load_runtime_code` + 动态挂载的 `ws_<service>_*` 只读检索工具族：get_repo_map / find_symbol / get_file_symbols / find_references / search_code / search_pattern / list_files / read_file，符号类为 go/ast 声明级）此前只能靠 DB 自建 agent 使用，本次收编为第 4 个内置子智能体，并补齐两处执行缺口：

1. **`code-reader` 内置 profile**（`service/agent/builtin.go`）：`@readonly` 业务工具白名单 + `ReadOnly=1` 只读执行域（非只读业务工具硬拦截）；系统提示词写明检索策略顺序（repo_map 全局认知 → 符号级定位优先于文本搜索 → read_file 精读殿后）、只读纪律（修改建议以文字+file:line 给出）与报告格式（结论 + file:line 证据 + commit）。`skills_json` 引用内置技能「代码工作区调查」。
2. **MCP 同步补 readOnly 声明**（`service/mcpclient`）：`ToolConfigJSON` 增加 readOnly 参数——repo kind 适配器（自研只读代码检索服务）的工具同步时自动写 `readOnly:true`，其余 kind 未知语义保守不标。此前 ws_ 工具 config 无 readOnly，`@readonly` 白名单拣不到它们、只读域还会误杀。ws_ 服务器为临时拉起（无 DB 行），kind 判定走客户端可选接口断言（`Client.Kind()`），每次挂载重新同步即刷新存量行。
3. **get_tool 白名单硬执行**（补 WP2 缺口）：原实现只过滤索引快照（模型"看到什么"），`get_tool` 活查询不校验白名单——子代理按名字猜工具仍可激活。新增 `runtimeRequest.agentToolRefs`（子 run 从 agent 定义继承，未声明时继承父 run 限制，单调收紧）+ `agentToolRefAllows()`，`get_tool` 与 `reloadBusinessToolIfUnchanged` 自愈激活路径统一硬检查。**注意这是对存量 DB agent 的行为收紧**：此前配置了 tools_json 白名单的 agent，白名单外工具从"索引里看不到但能猜名加载"变为"激活即拒绝"。
4. **内置技能 +1**：`workspace-code-investigation`（代码工作区调查）——工作区生命周期、8 个检索工具的选用顺序表、两段式调用、只读纪律、报告格式（file:line + commit）；`service/skill/bundled_test.go` 增加 manifest 完整性与技能名同步测试（code-reader 白名单引用的名字改动会在此失败提醒）。

### 6.7 后续待办

- ~~**P1（todo）**：wait_agent join 原语~~ ✅ 已实现（2026-09-27，`service/react/wait_agent.go`，含阻塞委派返回值 status 透传与 P2 描述引导，见 §6.8 与 todo 文档 §P1）；
- ~~**子代理续跑**：delegate_agent 加 `continue_run_id`~~ ✅ 已实现（2026-09-27，见 §6.9）；
- ~~**WP4 剩余**：web_search~~ ✅ 已实现（2026-09-27，SearXNG 适配器形态，见 §6.9；provider 原生路径作为后续演进）；
- **WP5**：config.permissionRules 的 deny/allow 两端（deny 优先于询问）；
- **面板**：内置 agent 可视化列出与一键 fork（当前 fork 方式 = 面板新建同 agent_key 的 DB 行）；agent/skill 面板的 read_only 字段与技能来源展示；
- **借鉴 ZCode profile 的剩余能力面**：`disallowedTools` deny 列表（tools 白名单之外的减法）、Explore 式动态工具面变体（配置驱动换白名单+换提示词）、project 来源 profile 禁止经 frontmatter 提升权限。

### 6.8 wait_agent 等待原语与完成度透传（2026-09-27，todo P1/P2 落地）

- **`wait_agent` Meta Tool**（`service/react/wait_agent.go`）：父循环阻塞在工具执行内，每 2s 轮询目标子 run 状态直至终态/waiting/超时；返回各 run 状态快照 `{"runs":[{runId, agentKey, status, finalResponse?, errorMessage?}]}`，整体永不报错。入参 `run_ids`（≤10 个）+ `timeout_sec`（缺省跟随看门狗上限、未配置看门狗时 300s，显式值钳位 [1,3600] 且不超过看门狗）。安全边界：仅允许等本 run 委派的子 run（校验 `parent_run_id`），他人 runId 一律 not_found。与 delegate/send_message 同域装配（`runtimeToolDefinitions`），状态词汇与后台通知口径对齐。
- **语义要点**：子 run 处于 waiting_*（HITL 等用户输入）不算完成——立即随快照返回（status=waiting + 提示可 send_message 催办），由父模型决定催办或放弃，避免空转；timeout 是快照收场不是错误；等待期间命令箱通知照常投递（父 run 活跃），与快照信息冗余无害。
- **完成度透传**：阻塞委派返回值新增 `status`（completed/error/timeout/cancelled/expired，从子 run 行终态读取）——父模型不再需要从 finalResponse 文本里猜"完成 vs 预算耗尽被迫收尾"。
- **验收**：`wait_agent_test.go` 覆盖状态映射、快照授权（parent 校验）、waiting 语义、timeout 钳位；端到端后台委派→wait_agent 收割沿用环境门控 e2e（与 TestDelegateAgentE2E 同 harness）另行验证。

### 6.9 子代理续跑 continue_run_id 与 web_search（2026-09-27 落地）

**续跑（continue_run_id，与 wait_agent 配对：wait 解决"等结果"，续跑解决"结果不够"）**：

- `delegate_agent` 入参新增 `continue_run_id`（可选）：新子 run 不再从空白开始，而是装载该子 run 的持久化上下文继续执行——历史消息复用外层装载同一转换器（`reactMessagesToChatMessagesWithRefs`，压缩摘要覆盖与孤儿 tool_result 回填天然生效）、todo 状态、已加载业务工具（`prevActiveToolIDs/Defs` 经引擎现有指纹校验恢复，定义已变的仍被丢弃强制重新 get_tool）。`modelUserMessage` 仍是本次 task（继续指令）。
- 校验（`validateContinueRun` 纯函数 + 单测）：run 必须存在、必须是本 run 委派的子 run（parent_run_id 匹配，防跨 run 窥探）、agent_key 必须与原 run 一致（续跑语义是"同一个专家接着干"）、原 run 已到终态——运行中引导 wait_agent、HITL 等待引导 send_message。阻塞与后台（background=true）委派均可续跑。
- 成本语义：每次续跑是新 run、独立计费与步数/token 预算；父 run 自身的步数/tokenBudget 仍约束续跑循环，无服务端自动续跑（决策留给父模型）。
- 工具描述已加引导："子任务已结束但结论不完整（status=error/timeout/cancelled/expired，或 completed 但内容不完整）时，用 continue_run_id=runId 续跑，不要从零重查。"

**web_search（WP4 收口）**：

- **实现形态与方案的分歧**：方案首选 provider 原生搜索（模型能力位 + 服务端工具配置下发），需逐 provider 改 api/llm 协议层且无法本地验证，暂缓；本版为 **SearXNG 适配器**（`service/react/web_search.go`）——conf 配置自建/托管 SearXNG 实例（`web_search.enabled + kind=searxng + base_url`，自建免密钥，托管可配 api_key），模型获得同一检索能力，服务端零爬虫。provider 原生路径（claude 服务端 web_search 工具）作为后续演进，届时按模型能力位注册、本工具不动。
- 工具行为：`web_search(query, max_results?)` → 返回标题/链接/摘要清单（snippet 按 300 rune 截断、整体 6000 rune 上限），引导用 web_fetch 抓取全文；空结果给出换词建议。ToolMeta：只读、可并发、network、30s 超时、8KB 输出预算。
- 门控：conf 开关 + 配置完整性双校验（`webSearchProfileEnabled`），半配置状态不注册也不可执行；softLanding 窗口放行（只读）；`reservedToolNames` 与注册表完整性测试已同步。
- 验收：`web_search_test.go` 覆盖 SearXNG 响应解析（脏数据丢弃/截断）、渲染与超限截断、conf 门控三态；真实检索链路依赖外部实例，按环境门控联调。
