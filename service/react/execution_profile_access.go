package react

func (p ExecutionProfile) allowsInternalTool(name string) bool {
	switch name {
	case metaToolReadToolResult, metaToolInspectData, metaToolPythonExec, metaToolDisplayFiles, metaToolReadAttachment, metaToolInspectAttachment:
		return p.AllowAnalysisTools
	case metaToolTodoWrite:
		return p.AllowTodo
	case metaToolCreatePlan:
		return p.AllowPlan
	case metaToolGetTool, metaToolExecuteTool, metaToolListTools:
		return p.AllowDynamicTools
	case metaToolDelegateAgent, metaToolSendMessage, metaToolWaitAgent:
		return p.AllowSubagent
	case metaToolLoadRuntimeCode:
		return p.AllowWorkspace
	case metaToolAskQuestion:
		return p.AllowUserQuestion
	case metaToolResolveAsyncTask, metaToolGetAsyncTask:
		return p.AllowAsyncTaskTools
	case metaToolGetSkill, metaToolListSkills:
		return p.AllowSkills
	case metaToolMemoryList, metaToolMemoryRead, metaToolMemoryWrite:
		return p.AllowMemory
	case metaToolGraphMemorySearch, metaToolGraphMemoryWrite:
		return p.AllowGraphMemory
	case metaToolWebFetch:
		return p.AllowWebFetch
	case metaToolWebSearch:
		return p.AllowWebSearch
	default:
		return false
	}
}
