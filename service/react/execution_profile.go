package react

import "react-base-service/conf"

// ExecutionProfile 描述“某类 Run 允许看到哪些 Runtime 能力”。
//
// 它不是用户权限系统，也不替代 Tool/Agent 自身鉴权；它只控制 ReAct Runtime 是否把某类 Meta Tool
// 装配给模型。例如 reflection Run 可以关闭 delegate_agent、Workspace 和分析 Tool，避免受限执行域
// 越权扩大能力。新增特殊 Run 类型时，应优先通过 ExecutionProfile 收敛能力，而不是在各工具里散落判断。
type ExecutionProfile struct {
	AllowTodo           bool
	AllowPlan           bool
	AllowClientTools    bool
	AllowDynamicTools   bool
	AllowUserQuestion   bool
	AllowAsyncTaskTools bool
	AllowSkills         bool
	AllowMemory         bool
	// AllowGraphMemory 控制 graph_memory_search/graph_memory_write（时序图谱记忆）工具；
	// 外层与子 run 跟随 graph_memory.enabled 配置，reflection 等受限执行域恒关闭。
	AllowGraphMemory bool
	// AllowSubagent 控制 delegate_agent（子 Agent 委派）工具；外层 run 跟随 subagent.enabled 配置，
	// reflection 等受限执行域恒关闭。
	AllowSubagent bool
	// AllowWorkspace 控制 load_runtime_code（P2-1 代码工作区入口）；外层/子 run 跟随
	// workspace.enabled 配置，reflection 等受限执行域恒关闭。
	AllowWorkspace bool
	// AllowAnalysisTools 控制 read_tool_result/inspect_data/python_exec 等分析类内置工具；
	// 主对话默认开启，reflection 等受限执行域关闭。
	AllowAnalysisTools      bool
	InjectAsyncTaskReminder bool
	RestoreOuterHistory     bool
}

// outerExecutionProfile 是普通用户对话/外层 Run 的默认能力集合。
// 配置开关只在这里转换为执行档案，后续 runtimeToolDefinitions 统一按档案决定 Tool 暴露。
func outerExecutionProfile() ExecutionProfile {
	return ExecutionProfile{
		AllowTodo:               true,
		AllowPlan:               conf.CustomConf.LLM.React.AllowPlanEnabled(),
		AllowClientTools:        true,
		AllowDynamicTools:       true,
		AllowUserQuestion:       true,
		AllowAsyncTaskTools:     true,
		AllowSkills:             true,
		AllowMemory:             conf.CustomConf.LLM.React.Memory.MemoryEnabled(),
		AllowGraphMemory:        conf.CustomConf.LLM.React.GraphMemory.GraphMemoryEnabled(),
		AllowSubagent:           conf.CustomConf.LLM.React.SubAgent.SubAgentEnabled(),
		AllowWorkspace:          conf.CustomConf.LLM.React.Workspace.WorkspaceEnabled(),
		AllowAnalysisTools:      true,
		InjectAsyncTaskReminder: true,
		RestoreOuterHistory:     true,
	}
}

// subAgentExecutionProfile 是 delegate_agent 子 Run 的执行档案。
// 子 Agent 与外层 Run 共用大部分能力，但不会注入 Session 级异步任务提醒，因为它的上下文应围绕
// 当前委派任务，而不是继承父会话的后台任务噪音。
//
//
// 但不注入会话级异步任务提醒（子 run 上下文由委派任务主导，与 session 任务无关）。
func subAgentExecutionProfile() ExecutionProfile {
	profile := outerExecutionProfile()
	profile.InjectAsyncTaskReminder = false
	return profile
}
