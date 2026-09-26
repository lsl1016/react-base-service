# ZCode 实现调研记录

- **调研日期**：2026-09-26 ~ 2026-09-27
- **调研方式**：在 ZCode 会话中对本机安装的 ZCode 运行时（`D:\ZCode\resources\glm\zcode.cjs`）及其源码仓库（`C:\Users\keke\Desktop\xm\ZCode`）做逆向定位与源码比对，全部结论均经过命令验证，未经验证的推断已标注。
- **调研目的**：摸清 ZCode 的工具（Tools）、子智能体（Subagents）、技能（Skills）三套体系的实现方式，为自己的项目增强提供参考。

---

## 0. TL;DR

1. ZCode 的**全部工具实现、子智能体体系、技能调度机制**都打包在 14.8 MB 的单行压缩文件 `zcode.cjs` 里（"引擎"）；技能正文和插件内容作为数据文件放在旁边 `packages/` 目录，运行时按 manifest 加载（"引擎 + 数据"分离）。
2. Explore 子智能体配 7 个工具：`Bash、Glob、Grep、Read、WebFetch、WebSearch、TodoWrite`，全部只读。
3. 桌面上的 `Desktop\xm\ZCode` 就是 ZCode CLI 的**源码仓库**（`apps/zcode-cli`），工具源码在 `packages/core/src/tool/handlers/`（约 110 个文件），内置智能体源码在 `packages/core/src/subagent/`。安装包里的函数原名与源码能一一对上（推断，未做构建产物哈希比对）。
4. 项目自带 8 个项目级技能（`.agents/skills/`），无任何 MCP 配置（无 `.mcp.json`）。
5. 最值得借鉴的设计：**统一的工具元数据注册模型**（权限/风险/审批/输出预算一体声明）、**WebSearch 委托模型服务端原生能力**、**技能发现约定**（SKILL.md + 目录黑名单 + 深度上限）、**内置智能体允许用户/项目同名覆盖**。

---

## 1. 调研对象与环境

| 对象 | 路径 | 说明 |
|---|---|---|
| 运行时引擎（构建产物） | `D:\ZCode\resources\glm\zcode.cjs` | 14,820,819 字节，单行压缩 CommonJS bundle |
| 引擎旁挂载数据包 | `D:\ZCode\resources\glm\packages\` | bundled-skills、browser-use-plugin、documents-plugin、pdf-plugin、presentations-plugin、image-search-plugin、node-repl-host 等 |
| 源码仓库 | `C:\Users\keke\Desktop\xm\ZCode` | pnpm monorepo，git 仓库（2026-09-21 检出） |
| 用户级插件缓存 | `C:\Users\keke\.zcode\cli\plugins\cache\` | zcode-plugins-official / claude-plugins-official 两个来源 |

---

## 2. 运行时引擎 zcode.cjs

### 2.1 文件概况

- 单行压缩 bundle，变量名被混淆（`wca`、`lea`、`Dda` 这类短名），但打包器通过 `r(fn, "原始函数名")` 调用保留了原始函数名，可据此追溯逻辑。
- 已验证的原始函数名示例：`bashHandler`、`executeBashHandler`、`createBashHandler`、`commandCategory`、`startBashCommandTelemetry`、`finishBashCommandTelemetry`、`shouldWalkSkillDirectoryEntry`、`sessionHasLoadedSkill`、`resolveBundledSkillRoots`、`webSearchHandler`。

### 2.2 工具注册模型（★ 重点借鉴）

每个工具在 bundle 中都有一个完整的注册块，字段如下（以 Bash / Glob 的实际提取内容为证）：

```
name            工具名
description     给模型看的描述
readOnly        是否只读（决定并发与审批默认值）
destructive     是否有破坏性
concurrentSafe  是否可并发调用
timeoutMs       默认超时
maxOutputBytes  输出字节上限
sideEffectScope 副作用范围："none" | "session" | "network" | "system"
riskLevel       风险等级："low" | "medium" | "high"
needsApproval   是否需要用户审批
handler         实现函数引用
formatModelContent / formatPersistedModelContent   模型上下文格式化
inputSchema / outputSchema                          zod schema
runtimeInputSchema / runtimeOutputSchema            运行时校验 schema
validateInput / resolveModelContract / resolveTimeoutBudgetMs 等钩子
permission      { permission, reason, riskLevel, sideEffectScope,
                  needsApproval, patternSources, alwaysAllowPatternSources,
                  denyPriority:"beforeAsk" }
resultBudget    { maxInlineBytes, maxModelBytes, strategy:"artifact",
                  preview:{maxBytes, direction:"head"},
                  artifact:{enabled, retention:"session"} }
timeout         { defaultMs, ... }
```

各工具已验证的元数据取值：

| 工具 | readOnly | concurrentSafe | sideEffectScope | riskLevel | needsApproval | timeoutMs | maxOutputBytes |
|---|---|---|---|---|---|---|---|
| Bash | false | false | system | high | true | 默认值（常量 CB） | 1e7（10MB） |
| Glob | true | true | none | low | false | 3e4（30s） | Tht 常量 |
| Grep | true | true | none | low | false | xvn 常量 | Iht 常量 |
| Read | true | true | none | low | false | 3e4 | i3 常量 |
| TodoWrite | true | false | session | low | false | 3e4 | wJ 常量 |
| WebFetch | true | true | network | medium | **true** | 6e4（60s） | 1e5（100KB） |
| WebSearch | true | true | network | low | false | Kda 常量 | 2e4（20KB） |

权限 patternSources 示例：Bash 按 `"command"` 匹配（支持 always-allow / deny 规则，deny 优先于询问 `denyPriority:"beforeAsk"`）；Read/Glob/Grep 按 `"path"`。

### 2.3 七个工具的实现细节

**工具总表**（从 bundle 内置名称表 `Zpr` 提取，共 30 项）：
`Read, Write, Edit, ApplyPatch, Bash, Glob, Grep, WebFetch, WebSearch, web_search, TodoRead, TodoWrite, GoalRead, ReadSessionContext, AskUserQuestion, SendMessage, RespondToCoordinator, TaskOutput, TaskStop, js, js_reset, js_add_node_module_dir, mcp__node_repl__js, mcp__node_repl__js_reset, mcp__node_repl__js_add_node_module_dir, Agent, Task, Skill, CreateWorkflow, AmendWorkflow, submit_result`

配套的类别映射 `WYi`：Read→file-read；Write/Edit/ApplyPatch→file-write；Bash→shell；Glob/Grep/WebFetch/WebSearch/web_search→search；Todo*→todo；GoalRead→goal；ReadSessionContext→session-context；AskUserQuestion→ask-user-question；SendMessage/RespondToCoordinator→message；TaskOutput/TaskStop→task-control；js*→node-repl。

**Explore 实际配备的 7 个工具**（`fpa` 工厂函数，source:"built-in"）：

```js
tools: ["Bash","Glob","Grep","Read","WebFetch","WebSearch","TodoWrite"]
```

> 注意：会话中 Agent 工具的说明文本只列出 5 个（漏了 Glob/Grep），运行时实际是完整的 7 个。UI 展示的"Explore 7 个工具"与源码一致。

各工具 handler（混淆名 → 原名）：

| 工具 | handler | 实现要点 |
|---|---|---|
| Bash | `wca` → `zpo`（executeBashHandler） | 一整套辅助函数：createBashHandler、commandCategory（命令分类）、start/finishBashCommandTelemetry（遥测）、toBackgroundedBashOutput（后台化输出） |
| Glob | `$ua` | 只读、可并发、30s 超时、免审批 |
| Grep | `Hua` | 同上，只搜文件内容 |
| Read | `lea` | 解构 `file_path/offset/limit/pages` 参数，经 `fileSystemPort`（依赖注入的文件系统端口）读文件；缺端口时抛 ConfigurationError |
| WebFetch | `Dda` | URL 解析与预审批（Oda）→ 网络请求 → 转 markdown → 用小快模型按 prompt 作答；有缓存（`fetched` 判定复用） |
| WebSearch | `Qda` | **要求当前模型 `supportsNativeWebSearch`**，即搜索由模型服务商的服务端原生能力完成，不是本地爬虫；模型缺失时抛 ConfigurationError |
| TodoWrite | `Ppa` | 副作用范围仅 session，只更新会话内任务列表 |

**WebSearch 的服务端委托证据**：bundle 中存在 `case "anthropic.web_search_20260209"` 分支，把 `{type:"web_search_20260209", name:"web_search", max_uses, allowed_domains, blocked_domains, user_location}` 推进 API 请求——小写 `web_search` 是发给模型 API 的服务端工具配置；大写 `WebSearch` 是客户端工具注册（负责校验与转发）。

**Agent 工具**：`name:"Agent"`，handler `xpa`，输入 schema 字段：`description`（3-5 词短描述）、`prompt`、`subagent_type`（可选）、`run_in_background`（可选）。

### 2.4 阅读压缩 bundle 的技巧（复现用）

```bash
# 定位某工具的注册块（看元数据与 handler 引用）
grep -o 'name:"Bash".\{700\}' zcode.cjs | head -1

# 用描述文本反查定义位置（适合 name 是变量的工具，如 WebFetch/WebSearch）
grep -o '.\{150\}converts the page to markdown.\{250\}' zcode.cjs

# 追踪 handler 实现（箭头函数赋值 + 原始函数名）
grep -o '[,;]wca=.\{300\}' zcode.cjs

# 按特征字符串全盘定位（先找文件）
rg -l "fan-out searches" "C:/Users/keke/.zcode" "D:/ZCode" -g '!plugins/cache/**'
```

---

## 3. 子智能体体系

### 3.1 内置智能体定义

- 内置名称表：`gvr = { GeneralPurpose:"general-purpose", Explore:"Explore" }`。
- `source:"built-in"` 在 bundle 中恰好出现 2 次，对应这两个内置智能体的工厂函数（`fpa` 即 Explore 工厂）。
- Explore 工厂字段：`color:"cyan"`（显式身份色，避免 UI 按名称 hash 上色）、`injectAgentsMd:false`（不注入项目的 AGENTS.md）、`source:"built-in"`、7 个只读工具。
- 模型选择：`dtr` 函数处理 `builtInModelSelectionOverrides` / `builtInModelOverrides` / `builtInThoughtLevelOverrides`，对 `builtin:` 前缀的 providerId 有专门映射。

### 3.2 Explore 系统提示词要点（从 bundle 提取）

> "Explore, a file search and codebase research specialist for ZCode CLI. ... === CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS === ... STRICTLY PROHIBITED from: Creating new files (no Write, touch...); Modifying existing files (no Edit); Deleting (no rm); Moving/copying (no mv/cp); Creating temp files including /tmp; Using redirect operators (>, >>, |) or heredocs to write; Running ANY commands that change system state ... Your strengths: Rapidly finding files using glob patterns; Searching code with regex; Reading and analyzing file contents. Guidelines: Use Read when you know the specific file path; Use Bash ONLY for read-only operations..."

设计要点：把"只读"约束同时落在三层——工具清单（7 个只读工具）+ 系统提示词（明确禁止清单，连重定向和 heredoc 写文件都点名）+ 权限元数据（Glob/Grep/Read 免审批只读）。

### 3.3 用户/插件智能体加载

- 有从磁盘目录读取用户自定义智能体的逻辑（如读目录函数 `eHo`：existsSync → statSync → readdirSync）。
- 插件智能体通过种子路径声明（bundle 中可见 `requiredSeedPaths:["agents/visual-judge.md", "skills/${t}/SKILL.md"]`），即 documents/pdf/presentations/spreadsheets 四个插件各自携带 `agents/visual-judge.md` + 技能目录。
- 源码 `profile.ts` 中有注释："用户/项目 profile 可以同名覆盖内置 Explore，不能只按名称套用内置行为"——同名覆盖时优先级：用户/项目 profile > 内置。

---

## 4. 技能体系

### 4.1 Skill 工具与发现机制

- Skill 工具名称常量 `XKs="Skill"`；会话级判断 `sessionHasLoadedSkill`；工具过滤逻辑按 `metadata.name==="Skill"` 处理。
- 发现机制（bundle 提取）：
  - 文件名常量 `ffe="SKILL.md"`；
  - 目录遍历函数 `shouldWalkSkillDirectoryEntry`：跳过黑名单目录 `["node_modules","dist","build","out","target","vendor","coverage",".cache",".next",".turbo",".venv","__pycache__"]`，跳过隐藏目录（`.system` 有专门处理），深度上限 `UXi=8`；
  - bundle 中共 9 处 `SKILL.md` 引用。
- 技能 frontmatter 的 `description` 字段用于"何时触发"的模型判断（见 §5.2 的 8 个技能描述实例）。

### 4.2 bundled-skills 打包机制（★ 重点借鉴）

bundle 中的常量与约定：

```
Oje = "bundled-skills"                      包名
A$o = "skills"                              技能目录名
P$o = ["skills/${Cb}/SKILL.md", "skills/${Cb}/patterns.md", "skills/${Cb}/examples.md"]
                                            一个技能包的文件清单（Cb=dynamic-workflows）
m9a = ["packages/${Oje}", "../${Oje}", "../../${Oje}", "../../../${Oje}"]
                                            根目录候选（多级向上找，兼容不同打包层级）
R$o = "zcode-bundled-skills/"               前缀
h9a = "${R$o}manifest.json"                 清单文件
HEn = ".zcode-bundled-skills-seed.json"     种子文件（记录已落地内容，防重复解包）
g9a = 1e6                                   1MB 尺寸上限（单包预算）
函数：resolveBundledSkillRoots / findMissingBundledSkillPackPaths /
      resolveFilesystemBundledSkillPackRoot
```

结论：**技能正文不内嵌引擎**，引擎只带"发现 + 校验 + 解包"逻辑；内容放在 `packages/bundled-skills/skills/dynamic-workflows/`（本机实况：SKILL.md、patterns.md、examples.md 三件套结构），由 manifest 与 seed 文件管理落地。

### 4.3 插件系统观察

- 插件清单含：`defaultEnabled`、`author`、`category`、`displayName`、`displayName_i18n`（如 Image Search 插件带 `{"zh-CN":"搜图"}`）、`requiredSeedPaths`、`rootCandidates`（同 bundled-skills 的多级候选）、`version`。
- 种子路径示例（browser-use 插件）：`docs/preview.md`、`docs/recording.md`、`scripts/browser-client.mjs`、`skills/control-browser/SKILL.md`、`skills/web-gui-tester/SKILL.md`——插件可同时携带技能、文档、脚本、智能体。
- 本机插件缓存分两个来源：`zcode-plugins-official`（browser-use、documents、pdf、presentations、video-agent-kit、video2code、github、mimosa、zcode-guide、example-plugin、plugin-creator 等）和 `claude-plugins-official`（superpowers、chrome-devtools、ui-ux-pro-max）——兼容 Claude 插件生态。

---

## 5. 源码仓库调查（C:\Users\keke\Desktop\xm\ZCode）

### 5.1 仓库概况

- pnpm workspace monorepo（`pnpm-workspace.yaml`），Node 版本由 `mise.toml` 锁定，工具链用 oxlint/oxfmt（`.oxlintrc.json`、`.oxfmtrc.json`）、knip（未用依赖检查）、release-it。
- 根目录无 `.zcode`、`.claude`、`.codex`、`.cursor` 目录；有 `.agents/`（技能）和 `.kilo/`（Kilo Code 工具目录，内含 worktree 副本）。
- 给智能体看的项目指令文档四件套：`AGENTS.md`（工作规范）、`CONTEXT.md`（插件商店领域词汇）、`DESIGN.md`（UI 设计规范，30KB）、`architecture-policy.yaml` + `.architecture-baseline.json`（架构策略与基线）。
- `apps/zcode-cli/packages/` 子包清单（16 个）：adapters、bootstrap、browser-use-plugin、cli、contracts、core、debug、dynamic-workflow、dynamic-workflow-runtime、i18n、node-repl-host、shared-types、superpowers-plugin、swift-bridge、telemetry、tui。
- 更上层的产品结构（AGENTS.md 记载）：packages/desktop（Electron main/host/renderer）、packages/web + packages/server（Web 客户端与服务端）、packages/ui（共享 React 组件/hooks/Zustand store）、packages/services（业务服务）、packages/rpc（RPC 框架）、packages/shared（协议与类型）、packages/client（Agent 客户端 SDK）。

### 5.2 项目自带技能（.agents/skills/，8 个）

| 技能 | frontmatter description（摘要） |
|---|---|
| agent-browser | 浏览器自动化 CLI：导航、填表、点击等网页交互 |
| ai-elements | 用 ai-elements 组件搭建 AI 聊天界面（会话、消息、工具展示、输入框等） |
| architecture-governance | 对代码改动应用仓库架构策略：生成有界上下文包、检查模块与分层边界 |
| dep-refs | 检查 TypeScript 导出引用、列出文件导出、验证导出是否未被使用 |
| dogfood | 系统性探索测试 web 应用找 bug 与 UX 问题（"dogfood"/QA/探索性测试时触发） |
| electron | 经 Chrome DevTools Protocol 自动化 Electron 桌面应用（VS Code、Slack 等） |
| feature-boundary-planner | 把 ZCode 行为改动映射到 UI 界面、状态所有者、协议命令、持久化与验证 |
| react-best-practices | React/Next.js 性能优化规范（源自 Vercel Engineering） |

目录结构：每个技能一个目录，含 `SKILL.md` + 可选 `references/`（agent-browser 有 authentication/commands/profiling/proxy-support/session-management/snapshot-refs/video-recording 七篇；ai-elements 有 20+ 篇组件参考）+ 可选 `templates/`（agent-browser 有三个 shell 模板）。

> `.kilo/worktrees/merciful-thumb/.agents/skills/` 是同一套技能在 Kilo Code worktree 里的副本，不是新技能。

### 5.3 源码内的插件技能

- `apps/zcode-cli/packages/browser-use-plugin/skills/`：control-browser、web-gui-tester —— 与本机已启用插件缓存中的同名技能对应，证实"仓库源码 → 构建产物/插件包"的供给关系。
- `superpowers-plugin/`：只有 LICENSE 文件（空壳占位）。
- `adapters/src/skills/`、`contracts/src/skills/`：技能加载与契约的实现代码，不是技能内容。

### 5.4 工具实现源码（★ 重点借鉴）

位置：`apps/zcode-cli/packages/core/src/tool/handlers/`，约 110 个 ts 文件，按工具一族一目录拆到极细：

- 基础工具：`bash.ts`、`glob.ts`、`grep.ts`、`read.ts`（+ `read-text/read-image/read-pdf/read-video.ts`）、`edit.ts`、`write.ts`、`todo.ts`、`webfetch.ts`（+ cache/egress-guard/network/url/content/errors/trace/types 八个配套）、`websearch.ts`（+ results/support）、`agent.ts`、`skill.ts`、`ask-user-question.ts`、`send-message.ts`、`respond-to-coordinator.ts`、`task-output.ts`、`task-stop.ts`、`cron.ts`、`off-peak.ts`、`node-repl.ts`、`plan-mode.ts`、`read-session-context.ts`、`list-models.ts`、`model-reference.ts`、`escalate.ts`、`submit-result.ts`
- Bash 只读判定策略族（20+ 文件）：`bash-readonly-policy.ts` 及按维度拆分的 argv-direct / argv-flags / argv-git / argv-io / flags-git / flags-process / flags-text / flags-file / git-callbacks / git-subcommands-core / git-subcommands-history / multiword-core / multiword-gh / simple-commands / commands / callbacks 等 —— 说明"判断一条 Bash 命令是否只读"是一个独立且庞大的子系统
- Bash 配套：command-parser、command-permission-policy、command-rule-evaluator、cwd-policy、gh-rate-limit、git-runtime-safety、image-output、background-lifecycle、background-policy、output、model-content、metadata、prompt、read-file-sources/state、semantics
- 动态工作流工具族：create-workflow（+ source/graph-bounds/graph-fold/description）、amend-workflow（+ description/resolve/source）、eval-workflow-snippet、get-workflow-run（+ format/roster-output/summary/format-roster）、list-workflow-runs、resume-workflow-run、save-workflow（+ description）、list-saved-workflows、resolve-workflow-question、workflow-* 分析展示文件（analysis-display、drafts、path-source、run-introspection、script-analysis、script-notes、script-path）
- 其他：`tool-perf.ts`（性能）、`generated/`（生成代码）、`saved-workflows/`、`bash-prompt.ts`、`plan-mode-prompts.ts`

后端支撑包：`node-repl-host`（node_repl 工具内核）、`dynamic-workflow` + `dynamic-workflow-runtime`（工作流脚本编译与运行）。

### 5.5 子智能体源码

位置：`apps/zcode-cli/packages/core/src/subagent/`

- `explore.ts`（EXPLORE_AGENT_TYPE）、`general-purpose.ts`（GENERAL_PURPOSE_AGENT_TYPE + buildGeneralPurposeSystemPrompt）、`explore-tools.ts`（formatExploreAllowedToolsForAgentDescription）、`profile.ts`（351 行，createBuiltInExploreAgentProfile / isBuiltInExploreAgentProfile）。
- profile.ts 关键注释（原文）："用户/项目 profile 可以同名覆盖内置 Explore，不能只按名称套用内置行为"。
- profile.ts 还处理：内置智能体的显式身份色（避免 UI 按名称 hash 把 general-purpose 显示为红色）、Agent 工具描述中动态拼入 Explore 允许的工具清单。

### 5.6 MCP 配置

全仓库未找到 `.mcp.json` / `mcp.json` / `.mcp*`（node_modules 与 .git 除外）。项目不依赖 MCP 扩展工具，能力全部来自内置工具与插件。

### 5.7 AGENTS.md 指令体系（★ 重点借鉴）

结构（六章）：核心原则 → 命令与仓库结构 → 实现与验证 → UI 与平台边界 → 进程、协议与远程控制（本次仅读取前 60 行，后半未展开）。

值得借鉴的具体条款：

- **spec 先行**："新增或修改行为前，先更新对应 spec；目录不存在时按需创建"。
- **文档联动**："说明中只保留当前仓库提供的功能、命令和文件；删除功能时同步清理指令和技能中的引用"。
- **调查优先**："定位问题时，未明确要求修改代码就先调查原因……区分已确认原因与待验证假设"。
- **技能挂进工作流**："代码改动使用 `.agents/skills/architecture-governance/SKILL.md`，先运行架构检查，再读取目标模块的受控上下文"。
- **基线检查**：开工前运行 `node scripts/check-workspace-freshness.mjs` 检查基线。
- **验证诚实**："必须执行 `pnpm typecheck` 和 `pnpm lint`，报告真实结果，不将已有失败写成通过"。
- **本地化注释**："修复 bug 时用中文注释说明原因和修复依据"。
- **架构红线**："禁止 UI 直接调用 Repo、Service 引用 Runtime 具体实现、跨域导入实现细节及循环依赖"；跨包导入只用公开入口；平台操作走 `IPlatformService`（packages/shared/src/platform.ts），UI 不直接碰 `window.zcode`；Zustand 状态集中在 packages/ui/src/store/；协议改动同步更新 `packages/shared/src/zcode-protocol/` 并提供严格类型与运行时校验。

常用命令表：`pnpm typecheck / lint(:fix) / fmt:check / dev:desktop / dev:web / verify:pre-push / architecture:check --changed / architecture:context <module-id> / knip / dep:refs --list-exports <file>`。

---

## 6. 可借鉴的设计要点（增强自己项目的建议清单）

1. **统一工具元数据注册模型**：每个工具一次性声明 readOnly / destructive / concurrentSafe / sideEffectScope / riskLevel / needsApproval / timeout / maxOutputBytes / permission（含 patternSources 与 deny 优先级）/ resultBudget（inline 预算 + artifact 化 + head 预览）。权限审批、输出裁剪、并发调度全部由这层声明驱动，工具实现本身不写 if-else。
2. **输出预算与 artifact 化**：超限输出转 artifact（session 级保留）+ head 预览，模型上下文只吃 inline 预算内的部分——上下文膨胀的结构性解法。
3. **搜索类工具委托服务端**：WebSearch 校验模型的 `supportsNativeWebSearch` 后把 `web_search_20260209` 配置推给 API（max_uses / allowed_domains / blocked_domains / user_location），本地零爬虫、合规风险低；WebSearch 与 WebFetch 的 maxOutputBytes（20KB / 100KB）远小于 Bash（10MB），按副作用类型差异化预算。
4. **只读约束三层落地**：工具清单收敛 + 系统提示词禁止清单（连 `>`、`>>`、`|`、heredoc 写文件都点名）+ 元数据只读标记。做"只读子智能体"时照此三层实现，比单靠提示词可靠。
5. **引擎与内容分离**：引擎内只放发现/校验/解包逻辑（manifest + seed 文件 + 多级 rootCandidates + 尺寸预算），技能/插件正文作为数据包随发行版分发，也允许用户目录覆盖。
6. **技能发现约定**：目录里放 `SKILL.md`（frontmatter 带触发用 description）即技能；遍历时跳过 node_modules 等 12 个黑名单目录、深度上限 8；复杂技能用 SKILL.md + references/ + templates/ 三件套组织。
7. **内置智能体可被同名覆盖**：内置 Explore/general-purpose 只提供默认行为，用户/项目 profile 同名时覆盖且"不按名称套用内置行为"——扩展性与确定性兼顾。
8. **子智能体的工程细节**：显式身份色避免 UI 随机着色；`injectAgentsMd:false` 让探索型子智能体不带项目指令；Agent 工具 schema 提供 `run_in_background`。
9. **Bash 只读判定独立成子系统**：按命令解析、git/gh 子命令、多词命令、标志位等维度拆成 20+ 策略文件——如果项目要自动判定命令风险，这是成熟分法。
10. **项目级 `.agents/skills/` + AGENTS.md 分层指令**：技能承载"怎么做"，AGENTS.md 承载"何时用哪个技能 + 验证纪律"，二者互相引用（如 architecture-governance 技能被写进实现与验证条款）。
11. **多形态产品共享一个协议内核**：Electron 桌面 / Web / CLI 共享 packages/shared 的严格类型协议（stdio 通信），UI 经 `IPlatformService` 抽象平台差异。

---

## 7. 附录

### 7.1 关键路径速查

| 内容 | 路径 |
|---|---|
| 引擎 bundle | `D:\ZCode\resources\glm\zcode.cjs` |
| 内置技能包 | `D:\ZCode\resources\glm\packages\bundled-skills\skills\dynamic-workflows\`（SKILL.md + patterns.md + examples.md） |
| 用户插件缓存 | `C:\Users\keke\.zcode\cli\plugins\cache\zcode-plugins-official\` 与 `...\claude-plugins-official\` |
| 源码仓库 | `C:\Users\keke\Desktop\xm\ZCode` |
| 工具 handler 源码 | `...\ZCode\apps\zcode-cli\packages\core\src\tool\handlers\` |
| 子智能体源码 | `...\ZCode\apps\zcode-cli\packages\core\src\subagent\`（explore.ts / general-purpose.ts / explore-tools.ts / profile.ts） |
| 项目技能 | `...\ZCode\.agents\skills\`（8 个） |
| 插件技能源码 | `...\ZCode\apps\zcode-cli\packages\browser-use-plugin\skills\` |
| 项目指令 | `...\ZCode\AGENTS.md`、`CONTEXT.md`、`DESIGN.md`、`architecture-policy.yaml` |

### 7.2 本次会话未覆盖 / 可继续深挖

- AGENTS.md 第 60 行之后的内容（进程、协议与远程控制章节全文）。
- `packages/core/src/tool/handlers/` 各 handler 的逐文件精读（本次只确认了清单与组织方式，未读实现细节）。
- bundled-skills 的 manifest.json 具体格式与 seed 文件写入时机。
- 权限系统（patternSources 匹配、always-allow / deny 规则求值）的完整流程。
- 源码仓库与 `zcode.cjs` 的构建对应关系（函数名能对上，但未做产物哈希/版本验证，属强推断）。

### 7.3 复现命令索引

见 §2.4。核心思路：压缩 bundle 里先按 `name:"X"` 或描述文本定位注册块 → 顺 `handler:xxx` 找函数赋值 → 从 `r(fn,"原名")` 恢复语义；源码仓库则直接按目录模块阅读。
