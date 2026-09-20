package react

import (
	"sort"
	"strings"

	"react-base-service/components"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	"react-base-service/service/mcpgateway"

	"github.com/gin-gonic/gin"
)

// MCP 应用（网关接入凭证）管理接口 —— 创建/启停/重置密钥/删除、工具绑定与调用审计查询。
//
// 工具是全局基础集合（全部启用的 http 工具，不按 caller 划分）；应用通过
// tblLlmMcpAppTool 白名单绑定工具子集——把不同 app 发给不同业务方，即控制
// 各业务方 MCP 连接可见的工具范围。appSecret 仅创建与重置时完整返回一次，
// 列表/详情一律打码。

type mcpAppListRequest struct{}

type mcpAppCreateRequest struct {
	AppName string `json:"appName"`
	// CallerKey 兼容字段：应用不再绑定 caller，传入即忽略。
	CallerKey string `json:"callerKey"`
	// Status 默认启用；显式传 0 创建即停用。
	Status *int `json:"status"`
}

type mcpAppUpdateRequest struct {
	AppID   string `json:"appId"`
	AppName string `json:"appName"`
	// CallerKey 兼容字段：应用不再绑定 caller，传入即忽略。
	CallerKey string `json:"callerKey"`
	Status    *int   `json:"status"`
}

type mcpAppScopeRequest struct {
	AppID string `json:"appId"`
}

type mcpAppLogsRequest struct {
	AppKey   string `json:"appKey"`
	ToolName string `json:"toolName"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

type mcpAppGrantRequest struct {
	AppID   string   `json:"appId"`
	ToolIDs []string `json:"toolIds"`
}

// mcpAppView 是应用的返回视图（secret 打码；完整 secret 仅创建/重置响应中出现）。
type mcpAppView struct {
	AppID        string `json:"appId"`
	AppName      string `json:"appName"`
	AppKey       string `json:"appKey"`
	MaskedSecret string `json:"maskedSecret"`
	Status       int    `json:"status"`
	// GrantedToolCount 是当前绑定白名单内的工具数（含已下线工具；0=连接看不到任何工具）。
	GrantedToolCount int64 `json:"grantedToolCount"`
	// Endpoint 是外部 MCP 客户端的接入地址（主服务端点；独立网关二进制为 <host>/mcp）。
	Endpoint  string `json:"endpoint"`
	CreatedBy string `json:"createdBy"`
	UpdatedBy string `json:"updatedBy"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func mcpAppToView(ctx *gin.Context, app model.McpApp) mcpAppView {
	granted, err := model.CountMcpAppTools(ctx, app.AppID)
	if err != nil {
		zlog.Errorf(ctx, "[MCPGW] 统计应用绑定工具数失败: appId=%s err=%v", app.AppID, err)
		granted = 0
	}
	return mcpAppView{
		AppID:            app.AppID,
		AppName:          app.AppName,
		AppKey:           app.AppKey,
		MaskedSecret:     mcpgateway.MaskSecret(app.AppSecret),
		Status:           app.Status,
		GrantedToolCount: granted,
		Endpoint:         "/react-base-service/mcp",
		CreatedBy:        app.CreatedBy,
		UpdatedBy:        app.UpdatedBy,
		CreatedAt:        app.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:        app.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

// ListMcpApps 列出全部 MCP 应用凭证（secret 打码）。
// @Summary      MCP 应用列表
// @Description  返回全部 MCP 网关接入凭证（secret 打码）
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/list [post]
func ListMcpApps(ctx *gin.Context) {
	apps, err := model.ListMcpApps(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[MCPGW] 查询应用列表失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	views := make([]mcpAppView, 0, len(apps))
	for _, app := range apps {
		views = append(views, mcpAppToView(ctx, app))
	}
	components.RenderJsonSucc(ctx, views)
}

// CreateMcpApp 新增 MCP 应用凭证，生成 appKey/appSecret（secret 完整返回仅此一次）。
// 新应用默认未绑定任何工具（tools/list 为空），创建后用 grant_tools 绑定工具子集。
// @Summary      新增 MCP 应用
// @Description  创建接入凭证（appKey/appSecret），secret 仅本次响应完整返回
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/create [post]
func CreateMcpApp(ctx *gin.Context) {
	var req mcpAppCreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	appName := strings.TrimSpace(req.AppName)
	if appName == "" {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appName 不能为空"))
		return
	}
	if len(appName) > 128 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appName 不能超过 128 字符"))
		return
	}
	status := 1
	if req.Status != nil && *req.Status == 0 {
		status = 0
	}
	if existing, err := model.ListMcpApps(ctx); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	} else {
		for _, app := range existing {
			if app.AppName == appName {
				components.RenderJsonFail(ctx, components.ParamInvalidf("应用名已存在: %s", appName))
				return
			}
		}
	}

	secret := mcpgateway.GenerateAppSecret()
	app := &model.McpApp{
		AppID:     mcpgateway.NewMcpAppID(),
		AppName:   appName,
		AppKey:    mcpgateway.GenerateAppKey(),
		AppSecret: secret,
		Status:    status,
		CreatedBy: helpers.GetUserName(ctx),
		UpdatedBy: helpers.GetUserName(ctx),
	}
	if err := model.CreateMcpApp(ctx, app); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	zlog.Infof(ctx, "[MCPGW] 新增应用: name=%s", appName)
	view := mcpAppToView(ctx, *app)
	components.RenderJsonSucc(ctx, gin.H{"app": view, "appSecret": secret})
}

// UpdateMcpApp 更新 MCP 应用（名称/启停）。工具可见范围走 grant_tools，不再绑定 caller。
// @Summary      更新 MCP 应用
// @Description  按 appId 更新应用名称或启停状态
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/update [post]
func UpdateMcpApp(ctx *gin.Context) {
	var req mcpAppUpdateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	appID := strings.TrimSpace(req.AppID)
	if appID == "" {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appId 不能为空"))
		return
	}
	app, err := model.GetMcpAppByAppID(ctx, appID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if app == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %s", appID))
		return
	}

	updates := map[string]interface{}{"updated_by": helpers.GetUserName(ctx)}
	if appName := strings.TrimSpace(req.AppName); appName != "" {
		apps, err := model.ListMcpApps(ctx)
		if err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		for _, other := range apps {
			if other.AppID != appID && other.AppName == appName {
				components.RenderJsonFail(ctx, components.ParamInvalidf("应用名已存在: %s", appName))
				return
			}
		}
		updates["app_name"] = appName
	}
	if req.Status != nil {
		if *req.Status != 0 && *req.Status != 1 {
			components.RenderJsonFail(ctx, components.ParamInvalidf("status 只允许 0/1"))
			return
		}
		updates["status"] = *req.Status
	}
	if err := model.UpdateMcpAppByAppID(ctx, appID, updates); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	refreshed, err := model.GetMcpAppByAppID(ctx, appID)
	if err != nil || refreshed == nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	zlog.Infof(ctx, "[MCPGW] 更新应用: appId=%s status=%d", appID, refreshed.Status)
	components.RenderJsonSucc(ctx, gin.H{"app": mcpAppToView(ctx, *refreshed)})
}

// DeleteMcpApp 删除 MCP 应用凭证（软删；持有该凭证的客户端立即失联）。
// @Summary      删除 MCP 应用
// @Description  按 appId 软删除应用凭证
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/delete [post]
func DeleteMcpApp(ctx *gin.Context) {
	var req mcpAppScopeRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	appID := strings.TrimSpace(req.AppID)
	if appID == "" {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appId 不能为空"))
		return
	}
	app, err := model.GetMcpAppByAppID(ctx, appID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if app == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %s", appID))
		return
	}
	if err := model.SoftDeleteMcpAppByAppID(ctx, appID); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	zlog.Infof(ctx, "[MCPGW] 删除应用: appId=%s name=%s", appID, app.AppName)
	components.RenderJsonSucc(ctx, gin.H{"appId": appID})
}

// ResetMcpAppSecret 重置应用 secret（旧凭证立即失效；新 secret 仅本次响应完整返回）。
// @Summary      重置 MCP 应用密钥
// @Description  按 appId 重新生成 appSecret，完整值仅本次响应返回
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/reset_secret [post]
func ResetMcpAppSecret(ctx *gin.Context) {
	var req mcpAppScopeRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	appID := strings.TrimSpace(req.AppID)
	if appID == "" {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appId 不能为空"))
		return
	}
	app, err := model.GetMcpAppByAppID(ctx, appID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if app == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %s", appID))
		return
	}
	secret := mcpgateway.GenerateAppSecret()
	if err := model.UpdateMcpAppByAppID(ctx, appID, map[string]interface{}{
		"app_secret": secret,
		"updated_by": helpers.GetUserName(ctx),
	}); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	zlog.Infof(ctx, "[MCPGW] 重置应用密钥: appId=%s name=%s", appID, app.AppName)
	components.RenderJsonSucc(ctx, gin.H{"appId": appID, "appSecret": secret})
}

// ListMcpAppLogs 分页查询 MCP 网关调用审计。
// @Summary      MCP 调用审计
// @Description  按 appKey/toolName 过滤分页返回 tools/call 审计记录
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/logs [post]
func ListMcpAppLogs(ctx *gin.Context) {
	var req mcpAppLogsRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	logs, total, err := model.ListMcpCallLogsPage(ctx, model.McpCallLogFilter{
		AppKey:   strings.TrimSpace(req.AppKey),
		ToolName: strings.TrimSpace(req.ToolName),
	}, (page-1)*pageSize, pageSize)
	if err != nil {
		zlog.Errorf(ctx, "[MCPGW] 查询调用审计失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	type logView struct {
		ID           uint   `json:"id"`
		AppKey       string `json:"appKey"`
		UserName     string `json:"userName"`
		ToolName     string `json:"toolName"`
		Arguments    string `json:"arguments"`
		ResponseText string `json:"responseText"`
		ResultCode   int    `json:"resultCode"`
		ErrorMsg     string `json:"errorMsg"`
		CostMs       int    `json:"costMs"`
		ClientIP     string `json:"clientIP"`
		CreatedAt    string `json:"createdAt"`
	}
	views := make([]logView, 0, len(logs))
	for _, entry := range logs {
		views = append(views, logView{
			ID:           entry.ID,
			AppKey:       entry.AppKey,
			UserName:     entry.UserName,
			ToolName:     entry.ToolName,
			Arguments:    entry.Arguments,
			ResponseText: entry.ResponseText,
			ResultCode:   entry.ResultCode,
			ErrorMsg:     entry.ErrorMsg,
			CostMs:       entry.CostMs,
			ClientIP:     entry.ClientIP,
			CreatedAt:    entry.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	components.RenderJsonSucc(ctx, gin.H{"total": total, "page": page, "pageSize": pageSize, "logs": views})
}

// GrantMcpAppTools 全量替换应用的工具绑定（显式白名单：空清单=清空绑定，应用立即看不到任何工具）。
// @Summary      绑定 MCP 应用工具
// @Description  按 appId 全量替换绑定工具清单（toolIds 为空即清空绑定）
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/grant_tools [post]
func GrantMcpAppTools(ctx *gin.Context) {
	var req mcpAppGrantRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	appID := strings.TrimSpace(req.AppID)
	if appID == "" {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appId 不能为空"))
		return
	}
	app, err := model.GetMcpAppByAppID(ctx, appID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if app == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %s", appID))
		return
	}
	if len(req.ToolIDs) > 200 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("单次绑定不能超过 200 个工具"))
		return
	}
	normalized := make([]string, 0, len(req.ToolIDs))
	seen := make(map[string]bool, len(req.ToolIDs))
	for _, toolID := range req.ToolIDs {
		toolID = strings.TrimSpace(toolID)
		if toolID == "" || seen[toolID] {
			continue
		}
		seen[toolID] = true
		normalized = append(normalized, toolID)
	}
	// 绑定的必须是真实存在的 http 工具行，防止拼错 ID 静默失效
	if len(normalized) > 0 {
		all, err := model.ListToolsByType(ctx, "http")
		if err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		existing := make(map[string]bool, len(all))
		for _, tool := range all {
			existing[tool.ToolID] = true
		}
		for _, toolID := range normalized {
			if !existing[toolID] {
				components.RenderJsonFail(ctx, components.ParamInvalidf("工具不存在或非 http 类型: %s", toolID))
				return
			}
		}
	}
	if err := model.ReplaceMcpAppTools(ctx, appID, normalized, helpers.GetUserName(ctx)); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	granted, _ := model.CountMcpAppTools(ctx, appID)
	zlog.Infof(ctx, "[MCPGW] 更新应用工具绑定: appId=%s name=%s granted=%d", appID, app.AppName, granted)
	components.RenderJsonSucc(ctx, gin.H{"appId": appID, "grantedToolCount": granted})
}

// ListMcpAppGrantableTools 返回可绑定给应用的工具清单（工具基础集合全部 http 工具，
// 含已下线行与当前绑定标记；下线行绑定后要等重新上线才对外可见）。
// @Summary      可绑定工具清单
// @Description  按 appId 返回工具基础集合与当前绑定状态
// @Tags         React
// @Accept       json
// @Produce      json
// @Router       /react/mcpapp/list_tools [post]
func ListMcpAppGrantableTools(ctx *gin.Context) {
	var req mcpAppScopeRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	appID := strings.TrimSpace(req.AppID)
	if appID == "" {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appId 不能为空"))
		return
	}
	app, err := model.GetMcpAppByAppID(ctx, appID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if app == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %s", appID))
		return
	}
	grants, err := model.ListMcpAppToolIDs(ctx, appID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	granted := make(map[string]bool, len(grants))
	for _, toolID := range grants {
		granted[toolID] = true
	}
	tools, err := model.ListToolsByType(ctx, "http")
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	// 与网关同名去重规则一致：按 name/id 排序后同名多行只保留第一条，避免勾选歧义
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Name != tools[j].Name {
			return tools[i].Name < tools[j].Name
		}
		return tools[i].ID < tools[j].ID
	})
	type grantableView struct {
		ToolID      string `json:"toolId"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CallerKey   string `json:"callerKey"`
		Status      int    `json:"status"`
		Granted     bool   `json:"granted"`
	}
	views := make([]grantableView, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if seen[tool.Name] {
			continue
		}
		seen[tool.Name] = true
		views = append(views, grantableView{
			ToolID:      tool.ToolID,
			Name:        tool.Name,
			Description: tool.Description,
			CallerKey:   tool.CallerKey,
			Status:      tool.Status,
			Granted:     granted[tool.ToolID],
		})
	}
	components.RenderJsonSucc(ctx, gin.H{"appId": appID, "tools": views})
}
