package router

import (
	"embed"
	"net/http"
	"strings"

	"react-base-service/components"
	"react-base-service/conf"
	"react-base-service/controllers/http/agent"
	"react-base-service/controllers/http/apikey"
	"react-base-service/controllers/http/attachment"
	"react-base-service/controllers/http/caller"
	"react-base-service/controllers/http/llmmodel"
	"react-base-service/controllers/http/mcpadmin"
	"react-base-service/controllers/http/react"
	"react-base-service/controllers/http/skill"
	"react-base-service/controllers/http/setting"
	"react-base-service/controllers/http/systemprompt"
	"react-base-service/controllers/http/tool"
	"react-base-service/helpers"
	"react-base-service/middleware"
	"react-base-service/service/mcpgateway"
	"react-base-service/web"

	m "react-base-service/golib/middleware"
	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

func Http(engine *gin.Engine) {
	// 健康检查与指标挂根路径，不走业务前缀与中间件（K8s/抓取惯例）。
	registerHealthRoutes(engine)
	registerMetricsRoute(engine)
	// MCP 网关管理台（mcp-server 移植的 Vue SPA）：页面走业务前缀，
	// 其构建产物引用根路径 /assets/* 与 /favicon.svg（Vite 绝对路径），一并挂根；
	// SPA 的 axios baseURL 构建期为 /api，接口兼容层同样挂根 /api/manage/*。
	registerMcpAdminRoutes(engine)
	registerMcpAdminAPI(engine)

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

	// MCP 服务端网关（/mcp）：把 tblLlmTool 的 http 工具按 caller 作用域对外暴露，
	// 自带 Bearer appKey:appSecret 鉴权，不依赖 AnonymousAuth；mcp_server.enabled 控制。
	if conf.CustomConf.MCPServer.Enabled {
		gateway := router.Group("/mcp")
		gateway.GET("", mcpgateway.Auth, mcpgateway.Handler())
		gateway.POST("", mcpgateway.Auth, mcpgateway.Handler())
		gateway.DELETE("", mcpgateway.Auth, mcpgateway.Handler())
	}

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
		// 队列管理（S3）：会话排队输入的查询/编辑/重排/删除；显式发送走 WS queue_send
		reactGroup.POST("/queue/list", react.ListQueue)
		reactGroup.POST("/queue/update", react.UpdateQueueItem)
		reactGroup.POST("/queue/reorder", react.ReorderQueue)
		reactGroup.POST("/queue/delete", react.DeleteQueueItem)
		// 上下文容量看板（playground 悬浮卡片）：容量占用/缓存命中率/分类构成
		reactGroup.POST("/usage/context", react.GetUsageContext)
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
		reactGroup.POST("/mcp/refresh", react.RefreshMcpServers)
		// MCP 应用（网关接入凭证）管理（见 controllers/http/react/mcp_app.go）
		reactGroup.POST("/mcpapp/list", react.ListMcpApps)
		reactGroup.POST("/mcpapp/create", react.CreateMcpApp)
		reactGroup.POST("/mcpapp/update", react.UpdateMcpApp)
		reactGroup.POST("/mcpapp/delete", react.DeleteMcpApp)
		reactGroup.POST("/mcpapp/reset_secret", react.ResetMcpAppSecret)
		reactGroup.POST("/mcpapp/logs", react.ListMcpAppLogs)
		reactGroup.POST("/mcpapp/grant_tools", react.GrantMcpAppTools)
		reactGroup.POST("/mcpapp/list_tools", react.ListMcpAppGrantableTools)
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

	// 运行时设置接口（管理面板「运行时配置」：DB 覆盖 yaml，写后本进程立即生效，多实例靠 TTL 拉平）
	settingGroup := router.Group("/setting")
	{
		settingGroup.POST("/subagent/get", setting.GetSubAgentSetting)
		settingGroup.POST("/subagent/update", setting.UpdateSubAgentSetting)
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

const (
	mcpAdminIndexEmbedPath   = "mcp-admin/dist/index.html"
	mcpAdminFaviconEmbedPath = "mcp-admin/dist/favicon.svg"
	mcpAdminAssetsEmbedPath  = "mcp-admin/dist/assets/"
)

// registerMcpAdminRoutes 挂载 MCP 网关管理台（mcp-server 移植的 Vue SPA，hash 路由）：
// 页面在 /react-base-service/react/mcp-admin（playground「MCP 应用」页有入口），
// 构建产物按其绝对路径约定挂根 /assets/* 与 /favicon.svg。
func registerMcpAdminRoutes(engine *gin.Engine) {
	router := engine.Group("/react-base-service")
	router.GET("/react/mcp-admin", serveMcpAdminIndex)
	engine.GET("/favicon.svg", serveMcpAdminFavicon)
	engine.GET("/assets/*path", serveMcpAdminAsset)
}

// registerMcpAdminAPI 挂载管理台的 /api/manage 协议兼容层（SPA 的 axios
// baseURL 构建期固定为 /api，故必须挂根路径；见 controllers/http/mcpadmin）。
func registerMcpAdminAPI(engine *gin.Engine) {
	adminAPI := engine.Group("/api/manage")
	adminAPI.Use(mcpadmin.Auth)
	{
		adminAPI.POST("/tools/list", mcpadmin.ListTools)
		adminAPI.POST("/tools/search", mcpadmin.SearchTools)
		adminAPI.POST("/tools/batchCreate", mcpadmin.BatchCreateTools)
		adminAPI.POST("/tools/batchUpdate", mcpadmin.BatchUpdateTools)
		adminAPI.POST("/tools/batchUpdateStatus", mcpadmin.BatchUpdateToolStatus)
		adminAPI.POST("/app/list", mcpadmin.ListApps)
		adminAPI.POST("/app/create", mcpadmin.CreateApp)
		adminAPI.POST("/app/grantTools", mcpadmin.GrantAppTools)
		adminAPI.POST("/app/updateToolStatus", mcpadmin.UpdateAppToolStatus)
		adminAPI.POST("/app/listTools", mcpadmin.ListAppTools)
	}
}

func serveMcpAdminIndex(ctx *gin.Context) {
	serveEmbedFile(ctx, web.McpAdminFS, mcpAdminIndexEmbedPath, "text/html; charset=utf-8")
}

func serveMcpAdminFavicon(ctx *gin.Context) {
	serveEmbedFile(ctx, web.McpAdminFS, mcpAdminFaviconEmbedPath, "image/svg+xml")
}

func serveMcpAdminAsset(ctx *gin.Context) {
	name := strings.TrimPrefix(ctx.Param("path"), "/")
	if strings.Contains(name, "..") || strings.Contains(name, "\\") {
		ctx.String(http.StatusBadRequest, "bad path")
		return
	}
	contentType := "application/octet-stream"
	switch {
	case strings.HasSuffix(name, ".js"):
		contentType = "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		contentType = "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		contentType = "image/svg+xml"
	}
	serveEmbedFile(ctx, web.McpAdminFS, mcpAdminAssetsEmbedPath+name, contentType)
}

func serveEmbedFile(ctx *gin.Context, fs embed.FS, path, contentType string) {
	data, err := fs.ReadFile(path)
	if err != nil {
		ctx.String(http.StatusNotFound, "not found")
		return
	}
	ctx.Header("Cache-Control", "no-cache")
	ctx.Data(http.StatusOK, contentType, data)
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
