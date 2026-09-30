package core

// 内置 Meta Tool 名称常量（Runtime 稳定工具协议）。
// 名称与 service/tool/reservedToolNames 保持一致（业务工具注册不允许同名，react 门面有同步测试兜底）。
const (
	MetaToolListTools      = "list_tools"
	MetaToolGetTool        = "get_tool"
	MetaToolExecuteTool    = "execute_tool"
	MetaToolListSkills     = "list_skills"
	MetaToolGetSkill       = "get_skill"
	MetaToolReadToolResult = "read_tool_result"
	MetaToolInspectData    = "inspect_data"
	MetaToolPythonExec     = "python_exec"
	MetaToolTodoWrite      = "todo_write"
	MetaToolAskQuestion    = "ask_question"
	MetaToolDisplayFiles   = "displayFiles"
	// MetaToolResolveAsyncTask 标记异步任务已完结，是 pending 提醒的唯一清除入口（TTL 过期兜底）。
	MetaToolResolveAsyncTask = "resolve_async_task"
	// MetaToolGetAsyncTask 回读异步任务完整记录，用于提醒内容被截断时取全量提交参数/响应。
	MetaToolGetAsyncTask = "get_async_task"
	// MetaToolReadAttachment 读取上传附件文本内容到上下文（理解类任务）。
	MetaToolReadAttachment = "read_attachment"
	// MetaToolInspectAttachment 探查 csv 附件的表结构（写分析代码前的准备）。
	MetaToolInspectAttachment = "inspect_attachment"
	// MetaToolCreatePlan 提交分步执行计划，等待用户在前端确认后执行（第一期：确认交互闭环）。
	MetaToolCreatePlan = "create_plan"
	// MetaToolLoadRuntimeCode 加载服务线上代码到本 run 专属工作区并挂载只读检索工具（P2-1）。
	MetaToolLoadRuntimeCode = "load_runtime_code"
	// MetaToolDelegateAgent 把子任务委派给注册表中的专家子 Agent（隔离子 run 执行，结果回填父循环）。
	// 描述按 caller 可见 agent 清单动态渲染，主 LLM 由此"发现"子代理（OH TaskToolSet 模式）。
	MetaToolDelegateAgent = "delegate_agent"
	// MetaToolSendMessage 向已委派的子代理发送补充消息（A2 父→子通道，见 send_message.go；
	// 复用 S1 guide 注入机制，对齐 ZCode messageSink）。
	MetaToolSendMessage = "send_message"
	// MetaToolWaitAgent 等待后台子 Agent 终态并取结果（P1 join 原语）。
	MetaToolWaitAgent = "wait_agent"
	// 长期记忆三工具：memory.enabled 开启时注册（见 memory.go）。
	MetaToolMemoryList  = "memory_list"
	MetaToolMemoryRead  = "memory_read"
	MetaToolMemoryWrite = "memory_write"
	// 时序事实图谱记忆工具：graph_memory.enabled 开启时注册（见 graph_memory.go）。
	MetaToolGraphMemorySearch = "graph_memory_search"
	MetaToolGraphMemoryWrite  = "graph_memory_write"
	// MetaToolWebFetch 抓取网页正文（WP4 网络能力）。
	MetaToolWebFetch = "web_fetch"
	// MetaToolWebSearch 联网检索（WP4 网络能力，SearXNG）。
	MetaToolWebSearch = "web_search"
)

// IsInternalMetaTool 判断工具名是否属于 Runtime 内置 Meta Tool，内置工具不走外部工具注册表。
func IsInternalMetaTool(name string) bool {
	switch name {
	case MetaToolListTools, MetaToolGetTool, MetaToolExecuteTool, MetaToolListSkills, MetaToolGetSkill, MetaToolReadToolResult, MetaToolInspectData, MetaToolPythonExec, MetaToolTodoWrite, MetaToolAskQuestion, MetaToolDisplayFiles, MetaToolResolveAsyncTask, MetaToolGetAsyncTask, MetaToolReadAttachment, MetaToolInspectAttachment, MetaToolCreatePlan, MetaToolDelegateAgent, MetaToolSendMessage, MetaToolLoadRuntimeCode, MetaToolMemoryList, MetaToolMemoryRead, MetaToolMemoryWrite, MetaToolGraphMemorySearch, MetaToolGraphMemoryWrite, MetaToolWebFetch, MetaToolWaitAgent, MetaToolWebSearch:
		return true
	default:
		return false
	}
}
