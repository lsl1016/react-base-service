package router

import (
	"net/http"

	"react-base-service/components"
	"react-base-service/conf"
	"react-base-service/controllers/http/agent"
	"react-base-service/controllers/http/apikey"
	"react-base-service/controllers/http/attachment"
	"react-base-service/controllers/http/caller"
	"react-base-service/controllers/http/llmmodel"
	"react-base-service/controllers/http/react"
	"react-base-service/controllers/http/skill"
	"react-base-service/controllers/http/systemprompt"
	"react-base-service/controllers/http/tool"
	"react-base-service/helpers"
	"react-base-service/middleware"
	"react-base-service/web"

	m "react-base-service/golib/middleware"
	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

func Http(engine *gin.Engine) {
	// 健康检查与指标挂根路径，不走业务前缀与中间件（K8s/抓取惯例）。
	registerHealthRoutes(engine)
	registerMetricsRoute(engine)

	router := engine.Group("/react-base-service")

	router.Use(m.AddField(zlog.String("globalCustomerNotice", "react-base-service")))

	// ReAct SDK / playground / replay 静态资源不需要 IPS 登录，便于外部页面直接加载。
	reactPublicGroup := router.Group("/react")
	{
		reactPublicGroup.GET("/playground", serveReactEmbedPlayground)
		reactPublicGroup.GET("/replay", serveReactEmbedReplay)
		reactPublicGroup.GET("/playground/auth", middleware.AnonymousAuth(), checkReactPlaygroundAccess)
		reactPublicGroup.GET("/ips.js", serveReactAsset("react/ips.js", "application/javascript; charset=utf-8"))
		reactPublicGroup.GET("/index.css", serveReactAsset("react/index.css", "text/css; charset=utf-8"))
		reactPublicGroup.GET("/index.js", serveReactAsset("react/index.js", "application/javascript; charset=utf-8"))
		reactPublicGroup.GET("/replay.css", serveReactAsset("react/replay.css", "text/css; charset=utf-8"))
		reactPublicGroup.GET("/replay.js", serveReactAsset("react/replay.js", "application/javascript; charset=utf-8"))
		reactPublicGroup.GET("/sql-client-tools.js", serveReactAsset("react/sql-client-tools.js", "application/javascript; charset=utf-8"))
		reactPublicGroup.GET("/sdk/0.0.1.js", serveReactSDKAsset("0.0.1.js", "application/javascript; charset=utf-8"))
		reactPublicGroup.GET("/sdk/0.0.1.css", serveReactSDKAsset("0.0.1.css", "text/css; charset=utf-8"))
	}

	router.Use(middleware.AnonymousAuth())

	InitLLMRouter(router)
}

func InitLLMRouter(router *gin.RouterGroup) {
	router.GET("/models", llmmodel.GetModels)

	// ReAct Runtime 单入口
	reactGroup := router.Group("/react")
	{
		reactGroup.GET("/ws", react.WS)
		reactGroup.GET("/models", react.GetModels)
		reactGroup.POST("/session/list", react.ListSessions)
		reactGroup.POST("/session/events", react.GetSessionEvents)
		reactGroup.POST("/async_task/list", react.ListAsyncTasks)
		reactGroup.POST("/plan_execution/detail", react.GetPlanExecutionDetail)
		reactGroup.POST("/plan_execution/events", react.GetPlanStepEvents)
		reactGroup.POST("/plan_execution/resume", react.ResumePlanExecution)
		reactGroup.POST("/plan_execution/retry", react.RetryPlanStep)
		reactGroup.POST("/plan_execution/skip", react.SkipPlanStep)
		reactGroup.POST("/plan_execution/cancel", react.CancelPlanExecution)
		reactGroup.POST("/session/feedback", react.ListSessionFeedback)
		reactGroup.POST("/run/feedback", react.UpdateRunFeedback)
		// Plan 模板管理（TODO 占位实现：内存 CRUD，不落库，见 controllers/http/react/plan_template.go）
		reactGroup.POST("/plan_template/list", react.ListPlanTemplates)
		reactGroup.POST("/plan_template/detail", react.GetPlanTemplateDetail)
		reactGroup.POST("/plan_template/create", react.CreatePlanTemplate)
		reactGroup.POST("/plan_template/update", react.UpdatePlanTemplate)
		// MCP 连接管理（查看/更新/管理 MCP 连接，见 controllers/http/react/mcp_server.go）
		reactGroup.POST("/mcp/list", react.ListMcpServers)
		reactGroup.POST("/mcp/detail", react.GetMcpServerDetail)
		reactGroup.POST("/mcp/create", react.CreateMcpServer)
		reactGroup.POST("/mcp/update", react.UpdateMcpServer)
		reactGroup.POST("/mcp/delete", react.DeleteMcpServer)
		reactGroup.POST("/mcp/connect", react.ConnectMcpServer)
		// Agent Bundle 插件包管理（P3：安装展开写入注册表、卸载回滚，见 controllers/http/react/bundle.go）
		reactGroup.POST("/bundle/install", react.InstallBundle)
		reactGroup.POST("/bundle/uninstall", react.UninstallBundle)
		reactGroup.POST("/bundle/list", react.ListBundles)
		// 代码工作区运行视图（P3：活跃 worktree 清单，只读）
		reactGroup.POST("/workspace/active", react.ListActiveWorkspaces)
		// 长期记忆管理面（P2：审计与管理面，见 controllers/http/react/memory.go；写路径与引擎 memory_write 工具共用写核心）
		reactGroup.POST("/memory/list", react.ListMemories)
		reactGroup.POST("/memory/create", react.CreateMemory)
		reactGroup.POST("/memory/update", react.UpdateMemory)
		reactGroup.POST("/memory/delete", react.DeleteMemory)
		reactGroup.POST("/memory/revisions", react.ListMemoryRevisions)
		reactGroup.POST("/memory/rollback", react.RollbackMemory)
		// python_exec 产物下载：走 IPS 登录态，图片/文件均需鉴权后经本接口读取（COS 私有桶不外暴露）。
		reactGroup.GET("/artifact/:artifactId", react.GetArtifact)
	}

	// ReAct 附件上传：fileId 供 run payload 的 attachments 引用。
	router.POST("/api/chat/files/upload", attachment.UploadChatFile)

	// 模型管理接口
	modelGroup := router.Group("/model")
	{
		modelGroup.GET("/whitelist", llmmodel.GetWhitelist)
		modelGroup.POST("/check-connectivity", llmmodel.CheckConnectivity)
		modelGroup.POST("/create", llmmodel.CreateUserModel)
		modelGroup.POST("/update", llmmodel.UpdateUserModel)
		modelGroup.POST("/delete", llmmodel.DeleteUserModel)
		modelGroup.POST("/list", llmmodel.ListUserModels)
		modelGroup.POST("/detail", llmmodel.GetUserModelDetail)
		// 积分管理接口
		modelGroup.POST("/credits/adjust", llmmodel.AdjustCredits)
	}

	// Caller 管理接口
	callerGroup := router.Group("/caller")
	{
		callerGroup.POST("/register", caller.RegisterCaller)
		callerGroup.POST("/update", caller.UpdateCaller)
		callerGroup.POST("/list", caller.ListCallers)
		callerGroup.POST("/copy_config", caller.CopyConfig)
		callerGroup.POST("/batch_delete", caller.BatchDelete)
	}

	// Skill 管理接口（SKILL.md 文件导入见 /skill/import 与 /skill/import_zip）
	skillGroup := router.Group("/skill")
	{
		skillGroup.POST("/create", skill.CreateSkill)
		skillGroup.POST("/update", skill.UpdateSkill)
		skillGroup.POST("/delete", skill.DeleteSkill)
		skillGroup.POST("/list", skill.ListSkills)
		skillGroup.POST("/detail", skill.GetSkillDetail)
		skillGroup.POST("/import", skill.ImportSkill)
		skillGroup.POST("/import_zip", skill.ImportSkillZip)
	}

	// 子 Agent 管理接口（delegate_agent 委派的注册表；Markdown 导入见 /agent/import）
	agentGroup := router.Group("/agent")
	{
		agentGroup.POST("/create", agent.CreateAgent)
		agentGroup.POST("/update", agent.UpdateAgent)
		agentGroup.POST("/delete", agent.DeleteAgent)
		agentGroup.POST("/list", agent.ListAgents)
		agentGroup.POST("/detail", agent.GetAgentDetail)
		agentGroup.POST("/import", agent.ImportAgent)
	}

	// Tool 管理接口
	toolGroup := router.Group("/tool")
	{
		toolGroup.POST("/register", tool.RegisterTool)
		toolGroup.POST("/update", tool.UpdateTool)
		toolGroup.POST("/delete", tool.DeleteTool)
		toolGroup.POST("/list", tool.ListTools)
		toolGroup.POST("/detail", tool.GetToolDetail)

		toolWhitelistGroup := toolGroup.Group("/whitelist")
		{
			toolWhitelistGroup.POST("/create", tool.CreateToolUserPolicy)
			toolWhitelistGroup.POST("/update", tool.UpdateToolUserPolicy)
			toolWhitelistGroup.POST("/delete", tool.DeleteToolUserPolicy)
			toolWhitelistGroup.POST("/detail", tool.GetToolUserPolicyDetail)
			toolWhitelistGroup.POST("/list", tool.ListToolUserPolicies)
		}
	}

	// API Key 管理接口
	apiKeyGroup := router.Group("/apikey")
	{
		apiKeyGroup.POST("/register", apikey.RegisterApiKey)
		apiKeyGroup.POST("/update", apikey.UpdateApiKey)
		apiKeyGroup.POST("/delete", apikey.DeleteApiKey)
		apiKeyGroup.POST("/list", apikey.ListApiKeys)
		apiKeyGroup.POST("/detail", apikey.GetApiKeyDetail)
	}

	// 系统提示词管理接口
	systemPromptGroup := router.Group("/system-prompt")
	{
		systemPromptGroup.POST("/register", systemprompt.RegisterSystemPrompt)
		systemPromptGroup.POST("/update", systemprompt.UpdateSystemPrompt)
		systemPromptGroup.POST("/delete", systemprompt.DeleteSystemPrompt)
		systemPromptGroup.POST("/list", systemprompt.ListSystemPrompts)
		systemPromptGroup.POST("/detail", systemprompt.GetSystemPromptDetail)
	}
}

const (
	reactIndexEmbedPath  = "react/index.html"
	reactReplayEmbedPath = "react/replay.html"
	reactSDKDistPath     = "sdk/dist/"
)

// isPlaygroundWhitelisted 判断用户是否可访问 playground 页面；白名单为空时不限制。
func isPlaygroundWhitelisted(userName string) bool {
	return conf.IsReactPlaygroundWhitelisted(userName)
}

func serveReactEmbedPlayground(ctx *gin.Context) {
	serveReactEmbedHTML(ctx, reactIndexEmbedPath)
}

func serveReactEmbedReplay(ctx *gin.Context) {
	serveReactEmbedHTML(ctx, reactReplayEmbedPath)
}

func serveReactEmbedHTML(ctx *gin.Context, path string) {
	data, err := web.FS.ReadFile(path)
	if err != nil {
		ctx.String(http.StatusInternalServerError, err.Error())
		return
	}
	ctx.Header("Cache-Control", "no-cache")
	ctx.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

func checkReactPlaygroundAccess(ctx *gin.Context) {
	userName := helpers.GetUserName(ctx)
	if !isPlaygroundWhitelisted(userName) {
		ctx.String(http.StatusForbidden, "无权访问该页面，请联系管理员开通权限")
		return
	}
	components.RenderJsonSucc(ctx, gin.H{"userName": userName})
}

func serveReactSDKAsset(filename string, contentType string) gin.HandlerFunc {
	return serveReactAsset(reactSDKDistPath+filename, contentType)
}

func serveReactAsset(path string, contentType string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		data, err := web.FS.ReadFile(path)
		if err != nil {
			ctx.String(http.StatusNotFound, err.Error())
			return
		}
		ctx.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		ctx.Header("Pragma", "no-cache")
		ctx.Header("Expires", "0")
		ctx.Data(http.StatusOK, contentType, data)
	}
}
