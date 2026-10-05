package agent

// 内置子 Agent profile（WP2，参考 ZCode 内置 Explore/general-purpose 的"代码内置 + 同名覆盖"模式，
// 方案 docs/参考ZCode的运行时增强方案.md）：
//
//   - 内置 profile 物化为 model.Agent 值（AgentID 带 builtin: 前缀），不落库；
//   - 解析顺序 DB（caller 作用域 + default 合并）> 内置：同 agent_key 的 DB 行整体覆盖内置，
//     覆盖后完全按 DB 行执行（ZCode profile.ts 教训：不能只按名称套用内置行为）；
//   - 行为唯一来源是解析命中的那份定义，下游 buildSubAgentRuntimeRequest 不感知定义来源；
//   - 内置 researcher 携带 ReadOnly=1（只读执行域），DB fork 可经 read_only 列保留该语义；
//   - 管理治理：DB 同名新建即 fork（覆盖内置），fork 后内置定义对当前 caller 不再参与解析。

import (
	"strings"

	model "react-base-service/models/llm"
	"react-base-service/service/skill"
)

// builtinAgentIDPrefix 是内置 profile 的 AgentID 前缀（delegate 描述据此标注 built-in）。
const builtinAgentIDPrefix = "builtin:"

// AgentSourceBuiltin 是 AgentResp.Source 的内置值（管理面板据此禁用编辑/删除、提供 fork）。
const AgentSourceBuiltin = "builtin"

// Agent 工具白名单的约定 token（tools_json 内与业务工具名并存）。
const (
	// AgentToolRefNone 白名单显式为空：不继承 caller 业务工具（区别于空数组=继承全部的存量语义）。
	AgentToolRefNone = "@none"
	// AgentToolRefReadOnly 只继承声明了 readOnly 的业务工具（只读子代理用）。
	AgentToolRefReadOnly = "@readonly"
)

// IsBuiltinAgent 判断一个 Agent 定义是否为代码内置 profile。
func IsBuiltinAgent(agent model.Agent) bool {
	return strings.HasPrefix(agent.AgentID, builtinAgentIDPrefix)
}

// BuiltinAgentProfiles 返回全部内置子 Agent 定义（调用方不应修改返回值内容）。
func BuiltinAgentProfiles() []model.Agent {
	return []model.Agent{
		{
			AgentID:  builtinAgentIDPrefix + "general-purpose",
			AgentKey: "general-purpose",
			Name:     "通用子代理",
			Description: "通用专家子代理，适合把一个自包含的独立子任务（多步查证、批量处理、耗时分析）委派出去隔离执行。" +
				"继承当前 caller 全部可见业务工具；不适用：需要只读保证的纯调查任务（用 researcher）。",
			SystemPrompt: "你是一个通用专家子代理，独立完成父任务委派给你的自包含子任务：" +
				"围绕 task 目标推进，不要重述任务或反问背景（父任务不会回应）；" +
				"按需使用可见业务工具与数据分析能力，关键结论要有工具结果佐证；" +
				"最终回复直接给出结论与证据（引用具体数据/resultRef），执行失败或信息不足时明确说明缺什么、已验证到哪一步。",
			ToolsJSON:    "[]",
			SkillsJSON:   "[]",
			MaxSteps:     64,
			CallerKey:    model.DefaultCallerKey,
			RouteValues:  "[]",
			Status:       1,
			ReadOnly:     0,
			PermissionMode: model.AgentPermissionModeInherit,
		},
		{
			AgentID:  builtinAgentIDPrefix + "researcher",
			AgentKey: "researcher",
			Name:     "只读调查员",
			Description: "只读调查子代理（类似 ZCode Explore）：在只读执行域内查证事实、检索数据、汇总证据，" +
				"非只读业务工具会被硬拦截，适合资料收集、问题定位、多源交叉核验。" +
				"不适用：任何需要写操作、提交变更或执行动作的任务。",
			SystemPrompt: "你是一个只读调查子代理。你的唯一职责是在只读边界内查证事实并汇总证据：" +
				"只允许使用只读工具（查询、检索、读取类）与网页抓取；任何写操作、提交、执行类工具调用都会被运行时拒绝，" +
				"不要尝试绕过（包括换参数重试同一写工具）。禁止重定向写文件、管道写文件等变体操作。" +
				"产出要求：给出结论 + 关键证据（引用具体数据/resultRef），证据不足时明确列出缺口与建议的后续授权动作，不要臆测。",
			ToolsJSON:    `["` + AgentToolRefReadOnly + `"]`,
			SkillsJSON:   "[]",
			MaxSteps:     64,
			CallerKey:    model.DefaultCallerKey,
			RouteValues:  "[]",
			Status:       1,
			ReadOnly:     1,
			PermissionMode: model.AgentPermissionModeInherit,
		},
		{
			AgentID:  builtinAgentIDPrefix + "report-writer",
			AgentKey: "report-writer",
			Name:     "汇总报告员",
			Description: "汇总产出子代理：把父任务提供的结构化数据（resultRef/JSON）加工为分析报告、图表与展示文件。" +
				"具备 python_exec 数据分析与 displayFiles 文件产出能力；不适用：需要重新查证原始事实的任务（先交给 researcher）。",
			SystemPrompt: "你是一个汇总产出子代理。父任务会通过 task 提供数据引用（resultRef）与产出要求：" +
				"用 inspect_data/python_exec 加工数据（python_exec 的数据输入走 inputs.tool_result 引用，禁止把大对象读进上下文），" +
				"需要产出文件时经 python_exec 的 artifacts 输出并用 displayFiles 展示；" +
				"报告用中文撰写，先结论后论据，图表必须可独立读懂（标题、单位、口径）。" +
				"你没有业务工具，缺数据时在报告中明确说明缺什么，不要编造数据。",
			ToolsJSON:    `["` + AgentToolRefNone + `"]`,
			SkillsJSON:   "[]",
			MaxSteps:     64,
			CallerKey:    model.DefaultCallerKey,
			RouteValues:  "[]",
			Status:       1,
			ReadOnly:     0,
			PermissionMode: model.AgentPermissionModeInherit,
		},
		{
			AgentID:  builtinAgentIDPrefix + "code-reader",
			AgentKey: "code-reader",
			Name:     "只读代码调查员",
			Description: "只读代码调查子代理（ZCode Explore 在服务端工作台的对位）：定位报错代码、核对线上逻辑、解释实现与调用链、梳理近期变更。" +
				"经 load_runtime_code 建立只读代码工作区后，用 ws_<service>_<commit短哈希>_ 符号级/文本检索工具（get_repo_map / find_symbol / search_code / read_file 等，完整名以 load_runtime_code 返回的 tools 为准）调查，" +
				"非只读业务工具被硬拦截。不适用：需要修改代码或执行写操作的任务（产出修改建议而不是改动）。",
			SystemPrompt: "你是一个只读代码调查子代理。职责：在只读代码工作区内定位、核对、解释目标服务的线上实现。\n" +
				"工作流：先调用 load_runtime_code（service 名来自 task，须在 workspace 白名单内）建立工作区——代码会锁定到该环境当前 commit；" +
				"再按返回的 tools 字段取完整工具名，get_tool 加载 ws_<service>_<commit短哈希>_* 检索工具并 execute_tool 执行（工具名带 commit 段，不要凭记忆拼接），检索策略按序：\n" +
				"1. get_repo_map 建立全局结构认知（模块/目录/符号分布）；\n" +
				"2. find_symbol / get_file_symbols 按符号定位（go/ast 声明级，优先于文本搜索）；find_references 找引用方与调用链；\n" +
				"3. search_code / search_pattern 做文本与正则检索（报错文案、配置键、注释）；\n" +
				"4. read_file 精读关键路径，配合 list_files 确认目录结构。\n" +
				"只读纪律：工作区与全部工具均为只读，任何写操作都会被拒绝；修改建议以文字描述 + 文件位置给出，不要尝试改代码。\n" +
				"报告要求：结论先行；每条结论附证据（文件路径:行号 + 关键代码摘录 + 符号名），并说明核对的 commit；" +
				"信息不足时列出已排除的假设与建议的进一步检索方向，不要臆测。",
			ToolsJSON:  `["` + AgentToolRefReadOnly + `"]`,
			SkillsJSON: `["` + skill.BundledSkillCodeInvestigation + `"]`,
			MaxSteps:   64,
			CallerKey:    model.DefaultCallerKey,
			RouteValues:  "[]",
			Status:       1,
			ReadOnly:     1,
			PermissionMode: model.AgentPermissionModeInherit,
		},
	}
}

// builtinAgentByKey 以 agent_key 索引内置 profile。
func builtinAgentByKey() map[string]model.Agent {
	profiles := BuiltinAgentProfiles()
	index := make(map[string]model.Agent, len(profiles))
	for _, profile := range profiles {
		index[profile.AgentKey] = profile
	}
	return index
}

// mergeBuiltinAgents 把未被 DB 行遮蔽（同 agent_key）的内置 profile 追加到可见清单尾部。
// DB 行优先：同名即 fork，内置定义让位；返回顺序保持 DB 行在前、内置在后。
func mergeBuiltinAgents(dbAgents []model.Agent) []model.Agent {
	shadowed := make(map[string]bool, len(dbAgents))
	for _, agent := range dbAgents {
		shadowed[agent.AgentKey] = true
	}
	merged := append([]model.Agent(nil), dbAgents...)
	for _, profile := range BuiltinAgentProfiles() {
		if !shadowed[profile.AgentKey] {
			merged = append(merged, profile)
		}
	}
	return merged
}
