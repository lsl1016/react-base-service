package core

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
	// AllowWebFetch 控制 web_fetch（WP4 网页抓取）；外层/子 run 跟随 web_fetch.enabled 配置，
	// reflection 等受限执行域恒关闭。
	AllowWebFetch bool
	// AllowWebSearch 控制 web_search（WP4 网页检索）；外层/子 run 跟随 web_search 配置，
	// reflection 等受限执行域恒关闭。
	AllowWebSearch bool
	// AllowAnalysisTools 控制 read_tool_result/inspect_data/python_exec 等分析类内置工具；
	// 主对话默认开启，reflection 等受限执行域关闭。
	AllowAnalysisTools      bool
	InjectAsyncTaskReminder bool
	RestoreOuterHistory     bool
}

// AllowsInternalTool 判断当前档案是否放行某内置 Meta Tool。
func (p ExecutionProfile) AllowsInternalTool(name string) bool {
	switch name {
	case MetaToolReadToolResult, MetaToolInspectData, MetaToolPythonExec, MetaToolDisplayFiles, MetaToolReadAttachment, MetaToolInspectAttachment:
		return p.AllowAnalysisTools
	case MetaToolTodoWrite:
		return p.AllowTodo
	case MetaToolCreatePlan:
		return p.AllowPlan
	case MetaToolGetTool, MetaToolExecuteTool, MetaToolListTools:
		return p.AllowDynamicTools
	case MetaToolDelegateAgent, MetaToolSendMessage, MetaToolWaitAgent:
		return p.AllowSubagent
	case MetaToolLoadRuntimeCode:
		return p.AllowWorkspace
	case MetaToolAskQuestion:
		return p.AllowUserQuestion
	case MetaToolResolveAsyncTask, MetaToolGetAsyncTask:
		return p.AllowAsyncTaskTools
	case MetaToolGetSkill, MetaToolListSkills:
		return p.AllowSkills
	case MetaToolMemoryList, MetaToolMemoryRead, MetaToolMemoryWrite:
		return p.AllowMemory
	case MetaToolGraphMemorySearch, MetaToolGraphMemoryWrite:
		return p.AllowGraphMemory
	case MetaToolWebFetch:
		return p.AllowWebFetch
	case MetaToolWebSearch:
		return p.AllowWebSearch
	default:
		return false
	}
}
