# ZCode 插件创建器与技能创建器调研记录

- **调研日期**：2026-10-13
- **调研方式**：在本机直接阅读 ZCode 官方插件缓存中两个创建器的全部源文件（`~/.zcode/cli/plugins/cache/zcode-plugins-official/` 下的 `skill-creator/0.1.0` 与 `plugin-creator/0.1.1`，含 SKILL.md、references、scripts、plugin.json），所有结论均出自源文件原文。
- **调研目的**：摸清 ZCode「让 agent 自己创建技能/插件」这两个能力的实现机制，为本项目（react-base-service）落地同类能力提供参考。落地见 [plan/20261013_技能与插件创建器实现方案.md](plan/20261013_技能与插件创建器实现方案.md)。

---

## 0. TL;DR

1. **两个创建器没有任何专用后端引擎**。skill-creator 就是一个纯 SKILL.md（约 160 行，无脚本无代码）；plugin-creator 是一个普通插件 = SKILL.md（六阶段工作流）+ 2 份格式规范参考 + 4 个 Node 脚手架脚本。这是**自举（self-hosting）设计**：plugin-creator 本身是一个插件，skill-creator 本身是一个技能。
2. 创建器能力的本质是**把"宿主平台的格式规范 + 创作方法论 + 交付流程"沉淀成写给模型看的工作流知识**，重活全部由宿主已有机制承担（文件读写工具、技能加载、插件市场安装）。
3. 三条贯穿始终的边界设计：**agent 只产源文件不碰安装**（手动交接）、**本地 dev 市场作为测试分发通道**、**四态诚实汇报**（已生成 / 已校验 / 已安装 / 已试用，未验证必须明说 pending）。
4. 对本项目的直接启示：我们已有 Skill 注册表、Bundle 插件包（快照回滚）、内置技能包（go:embed）、危险工具确认——地基比 ZCode 场景更全，缺的只是两份"创建器技能"正文 + 一个 bundle 校验端点。

---

## 1. 总体形态：创建器 = 最普通的技能/插件

ZCode 的插件结构是 `.zcode-plugin/plugin.json`（name/version/description/author + `skills`、`commands`、`hooks`、`mcpServers` 可选组件声明），技能结构是 `<name>/SKILL.md`（YAML frontmatter 的 name + description，正文 markdown）。

两个创建器完全遵循这套格式，没有任何"创建器专用"的加载路径或 API：

```text
skill-creator/0.1.0/                     plugin-creator/0.1.1/
├── .zcode-plugin/plugin.json            ├── .zcode-plugin/plugin.json
└── skills/skill-creator/                └── skills/plugin-creator/
    └── SKILL.md        ← 全部实现           ├── SKILL.md          ← 六阶段工作流
                                         ├── references/
                                         │   ├── plugin-json-spec.md       ← manifest/市场清单格式
                                         │   └── installing-and-updating.md ← UI 安装/更新步骤
                                         └── scripts/
                                             ├── create-basic-plugin.mjs     ← 脚手架 CLI
                                             ├── scaffold-files.mjs          ← 骨架文件映射（纯函数）
                                             ├── marketplace-files.mjs       ← 市场清单模板
                                             ├── upsert-dev-marketplace.mjs  ← 幂等更新本地 dev 市场
                                             └── validate-plugin.mjs         ← 静态校验（可选调官方 CLI）
```

**新能力 = 新数据文件，宿主零改动**。这是"引擎 + 数据分离"在创建器上的延续：创建器对宿主的全部依赖只有三样——通用文件读写工具、技能/插件的加载发现机制、用户手动操作的安装 UI。

## 2. skill-creator 解剖：一份纯提示工程的"创作方法论"

SKILL.md 正文教了四类知识：

### 2.1 格式与发现规则（宿主知识）

- 技能目录优先级：`<project>/.zcode/skills` > `<project>/.agents/skills` > `~/.zcode/skills` > `~/.agents/skills`；**新建默认放 `.agents/skills/`**（跨工具标准位），`.zcode/skills` 用来做同名覆盖。
- frontmatter 必填 `name`（kebab-case，1–64 字符，必须与目录名一致）和 `description`；其余字段极少用。
- 目录可选附件：`references/`（按需读的文档）、`scripts/`（可执行助手）、`assets/`（模板/夹具）。

### 2.2 渐进式披露（上下文成本知识）

三层加载模型，并给出工程约束：**元数据（name+description）常驻上下文 → 正文仅在触发时加载（目标 <500 行）→ 附属文件按需读、原则上不限大小**。正文超长时拆 `references/`，由正文指示"什么情况读哪份"。

### 2.3 description 写法（触发可靠性知识）

description 是**主触发信号**，"做什么"和"什么语境该用"都写在这里而不是正文。模型天然倾向**漏触发**，所以描述要写得主动一点（pushy）——给出了对照示例："How to build a dashboard for internal data" 应写成 "Use whenever the user mentions dashboards, data visualization, internal metrics, **even if they don't explicitly say 'dashboard'**"。

### 2.4 创作循环（方法论核心）

```
捕获意图（从当前对话历史提取：用了什么工具、步骤顺序、纠错、输入输出格式）
    → 起草 SKILL.md
    → 设计 2–3 个真实测试提示词（带具体路径/列名/口语化措辞甚至错别字）
    → 新会话逐个试跑（触发 or /skill 强制加载）
    → 和用户一起看结果与 trace
    → 改进 → 循环，直到用户满意或改进不再产生增益
```

改进方法论同样具体：**从反馈泛化**（不要过拟合到几个样例上堆 MUST/NEVER，改框架比重叠约束有效）、**保持精简**（删掉不产生增益的指令）、**解释为什么**（模型理解动因时遵循得更好）、**沉淀重复劳动**（每次试跑都各自重写同一辅助脚本 → 收进 `scripts/`）。

一个精妙的判断信号：如果技能导致模型做大量无用功（反复读同一文件、写一次性脚本、兜圈子），说明技能**过度规定或不清晰**——解法是删减而不是加规则。

### 2.5 更新存量技能的规则

- 保名字身份：更新 `research-helper` 产物仍是 `research-helper`，不造 `-v2`。
- 只读安装位（官方插件缓存）不可改：拷贝到 `~/.agents/skills/<name>/` 编辑，靠发现优先级覆盖原版。
- 同名不同路径 = 两个独立安装，路径即安装身份。

## 3. plugin-creator 解剖：六阶段工作流 + 规范参考 + 脚手架

### 3.1 SKILL.md 六阶段

| 阶段 | 要点 |
|---|---|
| 1 定义目标与来源 | 读工作区既有说明，复用用户已做的选择；默认源码位 `<workspace>/plugins/<slug>/`；**先定义一个代表性输入 + 可观察的预期结果**，兼作冒烟测试与最终验收提示词 |
| 2 创建与实现 | 生成 `.zcode-plugin/plugin.json` + **只声明真正需要的组件**；目录名与 manifest name 必须一致；迭代 = 编辑已实现文件 + 语义化版本自增，**不用 `--force` 重新脚手架** |
| 3 环境内可验证项 | manifest JSON 合法性、声明资源存在性、目录/名称一致性、路径不越界、占位符未残留；有 CLI 时可选跑官方 `zcode plugins validate`——**文件检查不等于 schema 校验或运行时验证**，要如实区分 |
| 4 准备本地测试市场 | 生成/更新 `<workspace>/plugins/marketplace.json`：条目带 manifest 的 name/version/description + 相对路径 source + 展示元数据；**保留无关条目**；市场名从路径哈希派生保持稳定（`dev-<workspace>-<hash>`）；**建清单 ≠ 注册市场** |
| 5 指导用户 UI 操作 | 给出真实绝对路径，指引用户"插件市场 → 添加 → 添加插件市场"粘贴目录 → "个人 → 市场 → 插件 → 安装"；给具体验收提示词与预期结果 |
| 6 如实交付 | 汇报实际路径/ID/版本/待办步骤/已做与未做的校验；**用户没完成 UI 操作前，状态一律"市场待添加、插件待安装、试用待验"**，不得声称成功 |

### 3.2 references：规范即数据

- `plugin-json-spec.md`：manifest 字段全表、`${CLAUDE_PLUGIN_ROOT}` 运行时路径 token、市场清单格式、dev 布局约定、脚手架 CLI 用法。
- `installing-and-updating.md`：源码 / 市场副本 / 安装副本**三者是不同位置**；更新流程 = 改源码 → bump 版本 → 同步市场条目 → 用户**刷新该市场** → 用户在插件详情点**更新**；"源码修改不是热加载"。还给了完整的中文交接话术模板。

规范放 references 而不是塞进 SKILL.md 正文，正是渐进式披露的自我实践。

### 3.3 scripts：脚手架是可选加速器

- `scaffold-files.mjs`：纯函数，输入 name + 组件列表，输出 `Map<相对路径, 文件内容>`——manifest、SKILL.md、command、hooks.json、最小 MCP 握手骨架、`.gitkeep`。**生成的 MCP server 只有握手和空工具列表**，SKILL.md 明确要求"实现真实工具前不得报告集成为可用"。
- `create-basic-plugin.mjs`：写文件的 CLI 包装，默认拒绝覆盖已存在文件。
- `upsert-dev-marketplace.mjs`：幂等更新市场清单，`--marketplace-path` 沿用既有市场名。
- `validate-plugin.mjs`：静态校验，`--cli` 才会调官方校验命令。
- 关键约束：**只在 Node 已可用时使用脚本，绝不要求普通桌面用户装 Node**——没有运行时就退回纯文件工具手写，SKILL.md 给了手写的完整格式样例。

## 4. 三个值得照抄的设计决策

### 4.1 手动交接边界（agent 只产源，不碰安装）

plugin-creator 开篇即声明：**不注册市场、不安装/更新/启用插件、不发布**。agent 的爆炸半径被限制在"工作区里的源码文件 + 市场清单"；注册表的变更权永远在一次用户 UI 操作之后。这同时是安全边界（防 agent 自我修改能力集）和信任边界（用户对装了什么保有完全知情）。

### 4.2 本地 dev 市场：测试分发通道

"发布"与"本地试用"分离。本地目录 + `marketplace.json` + 相对路径 source 构成一个零基础设施的测试市场；稳定命名（路径哈希派生）保证多次迭代不产生重复市场；更新语义（刷新市场 → 更新插件）复用正式分发通道，本地测试通过即具备发布形态。

### 4.3 诚实汇报与版本纪律

- **四态区分**：文件已生成 ≠ 已校验 ≠ 已安装 ≠ 已试用；每份交付必须声明真实所处状态。
- **不夸大**：空 MCP 骨架不得称可用；文件检查不冒充 schema 校验；用户未跑验收例就标"试用待验"。
- **版本纪律**：改源码必 bump manifest 版本并同步市场条目；迭代编辑优先于重新脚手架；保名字身份不造 v2。

## 5. 与本项目的概念对应

| ZCode 概念 | react-base-service 对应物 | 差异要点 |
|---|---|---|
| Skill（SKILL.md 文件 + 目录发现） | `tblLlmSkill` 行：结构化字段 + Content 正文（[models/llm/skill.go](../models/llm/skill.go)） | 我们是 DB 注册表不是文件系统；创建走 `/skill/create`、`/skill/import`（SKILL.md 粘贴导入） |
| description 单通道触发 | `TriggerCondition`（模型语义提示）+ `Triggers`（装配期确定性子串）双通道 | 创建器要分别教两种触发器的写法 |
| 插件（plugin.json + 市场分发） | Bundle：manifest + agents/ + skills/ + .mcp.json，白名单来源安装，快照式卸载回滚（[service/bundle](../service/bundle/)） | 我们没有公网市场，也不需要——白名单 + zip 上传暂存就是"dev 市场" |
| bundled-skills 官方内置包 | `service/skill/bundled-skills/` go:embed + 幂等种子（已有） | 创建器技能的天然分发载体 |
| 手动 UI 安装/启用 | 面板 `/bundle/install` + 技能 `status` 0→1 手动启用 | 边界语义一致：agent 产草稿，人按生效键 |
| validate-plugin.mjs | 需新增 `/react/bundle/validate` 校验端点 | 我们缺的唯一一块后端拼图 |

## 6. 结论

创建器不是"要造的功能"，而是"要写的两份技能"。ZCode 的实现证明：只要宿主具备 注册表 + 安装机制 + 文件/工具通道，创建器就是一层纯知识封装。本项目地基（Skill 双通道触发、Bundle 快照回滚、内置技能包、危险工具确认、app 作用域）比 ZCode 的文件系统场景更完备，落地代价集中在两处——创建器技能正文的提示工程质量，和一个小体量的 bundle 校验端点。
