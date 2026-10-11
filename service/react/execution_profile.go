package react

import (
	"react-base-service/conf"
	netcap "react-base-service/service/react/internal/netcap"
)

// ExecutionProfile 类型与 AllowsInternalTool 已下沉 internal/core（execution_profile.go）；
// 本文件保留按运行场景选择档案的工厂函数——它们依赖 conf 开关与 netcap 门控，
// 属于引擎门面的装配逻辑，不下沉。
//
// 外层默认档案见 outerExecutionProfile；delegate 子 run 见 subAgentExecutionProfile；
// reflection 受限域见 memory_reflection.go 的 reflectionExecutionProfile。

// outerExecutionProfile 是普通用户对话/外层 Run 的默认能力集合。
// 配置开关只在这里转换为执行档案，后续 runtimeToolDefinitions 统一按档案决定 Tool 暴露。
// req 携带 app 级 allow_plan 覆盖快照；nil（单测最小状态）按全局配置处理。
func outerExecutionProfile(req *runtimeRequest) ExecutionProfile {
	return ExecutionProfile{
		AllowTodo:           true,
		AllowPlan:           effectiveAllowPlan(appAllowPlanOf(req)),
		AllowClientTools:    true,
		AllowDynamicTools:   true,
		AllowUserQuestion:   true,
		AllowAsyncTaskTools: true,
		AllowSkills:         true,
		// 能力族开关一律全局口径（内置工具不按 app 隔离）：运行时设置（DB 覆盖 > yaml > 默认）
		// × 基础设施可用性，与 internalMetaToolDefinitions 的注册门控同口径。
		AllowMemory:             conf.GetReactRuntimeConfig().Memory.MemoryEnabled(),
		AllowGraphMemory:        conf.CustomConf.LLM.React.GraphMemory.GraphMemoryEndpointConfigured(),
		AllowSubagent:           conf.GetReactRuntimeConfig().SubAgent.SubAgentEnabled(),
		AllowWorkspace:          conf.GetReactRuntimeConfig().Workspace.WorkspaceEnabled() && conf.CustomConf.LLM.React.Workspace.WorkspaceResolversConfigured(),
		AllowWebFetch:           conf.GetReactRuntimeConfig().WebFetch.WebFetchEnabled(),
		AllowWebSearch:          netcap.WebSearchProfileEnabled(),
		AllowAnalysisTools:      true,
		InjectAsyncTaskReminder: true,
		RestoreOuterHistory:     true,
	}
}

// appAllowPlanOf 提取 run 请求携带的 app 级 allow_plan 覆盖（nil req 视为未覆盖）。
func appAllowPlanOf(req *runtimeRequest) *bool {
	if req == nil {
		return nil
	}
	return req.appAllowPlan
}

// effectiveAllowPlan 解析 create_plan/Plan 模式能力的生效值：
// app 显式覆盖（tblLlmApp.allow_plan 三态规整结果）优先，未覆盖跟随全局
// custom.yaml llm.react.allow_plan（默认开）。执行档案与 Plan 入口闸门共用本口径。
func effectiveAllowPlan(override *bool) bool {
	if override != nil {
		return *override
	}
	return conf.CustomConf.LLM.React.AllowPlanEnabled()
}

// subAgentExecutionProfile 是 delegate_agent 子 Run 的执行档案。
// 子 Agent 与外层 Run 共用大部分能力，但不会注入 Session 级异步任务提醒，因为它的上下文应围绕
// 当前委派任务，而不是继承父会话的后台任务噪音。
//
// 但不注入会话级异步任务提醒（子 run 上下文由委派任务主导，与 session 任务无关）。
func subAgentExecutionProfile(req *runtimeRequest) ExecutionProfile {
	profile := outerExecutionProfile(req)
	profile.InjectAsyncTaskReminder = false
	return profile
}

// scheduledExecutionProfile 是定时触发工作流无人值守 run 的执行档案。
// 无前端连接：禁用 client 工具与 ask_question（等待人工回填即挂死，是无人值守 run 的
// 首要失控面）；不注入会话级异步任务提醒（新会话无任务账本，且无人值守不应产出待接续任务）。
// 其余能力沿用 app + user 白名单——巡检类 app 建议只配只读工具。
func scheduledExecutionProfile(req *runtimeRequest) ExecutionProfile {
	profile := outerExecutionProfile(req)
	profile.AllowClientTools = false
	profile.AllowUserQuestion = false
	profile.AllowAsyncTaskTools = false
	profile.InjectAsyncTaskReminder = false
	profile.RestoreOuterHistory = false
	return profile
}
