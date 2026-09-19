package react

import (
	"strings"

	"react-base-service/components"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	"react-base-service/service/mcpgateway"

	"github.com/gin-gonic/gin"
)

// MCP 应用（网关接入凭证）管理接口 —— 创建/启停/重置密钥/删除与调用审计查询。
//
// 应用绑定一个 caller 作用域：该 caller 名下（含 default 通用作用域）启用的 http 类型
// 工具（tblLlmTool）即外部 MCP 客户端经 /mcp 端点可见的工具集合。
// appSecret 仅创建与重置时完整返回一次，列表/详情一律打码。

type mcpAppListRequest struct{}

type mcpAppCreateRequest struct {
	AppName   string `json:"appName"`
	CallerKey string `json:"callerKey"`
	// Status 默认启用；显式传 0 创建即停用。
	Status *int `json:"status"`
}

type mcpAppUpdateRequest struct {
	AppID     string `json:"appId"`
	AppName   string `json:"appName"`
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

// mcpAppView 是应用的返回视图（secret 打码；完整 secret 仅创建/重置响应中出现）。
type mcpAppView struct {
	AppID        string `json:"appId"`
	AppName      string `json:"appName"`
	AppKey       string `json:"appKey"`
	MaskedSecret string `json:"maskedSecret"`
	CallerKey    string `json:"callerKey"`
	Status       int    `json:"status"`
	// Endpoint 是外部 MCP 客户端的接入地址（主服务端点；独立网关二进制为 <host>/mcp）。
	Endpoint   string `json:"endpoint"`
	CreatedBy  string `json:"createdBy"`
	UpdatedBy  string `json:"updatedBy"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

func mcpAppToView(app model.McpApp) mcpAppView {
	return mcpAppView{
		AppID:        app.AppID,
		AppName:      app.AppName,
		AppKey:       app.AppKey,
		MaskedSecret: mcpgateway.MaskSecret(app.AppSecret),
		CallerKey:    app.CallerKey,
		Status:       app.Status,
		Endpoint:     "/react-base-service/mcp",
		CreatedBy:    app.CreatedBy,
		UpdatedBy:    app.UpdatedBy,
		CreatedAt:    app.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:    app.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

// normalizeMcpAppCaller 校验应用绑定的 caller 作用域：存活 caller 或保留作用域。
func normalizeMcpAppCaller(ctx *gin.Context, callerKey string) error {
	callerKey = strings.TrimSpace(callerKey)
	if callerKey == "" {
		return components.ParamInvalidf("callerKey 不能为空（绑定 default 表示全部 caller 的通用工具）")
	}
	if model.IsReservedCallerKey(callerKey) {
		return nil
	}
	caller, err := model.GetActiveCallerByKey(ctx, callerKey)
	if err != nil {
		return err
	}
	if caller == nil {
		return components.ParamInvalidf("caller 不存在或已停用: %s", callerKey)
	}
	return nil
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
		views = append(views, mcpAppToView(app))
	}
	components.RenderJsonSucc(ctx, views)
}

// CreateMcpApp 新增 MCP 应用凭证，生成 appKey/appSecret（secret 完整返回仅此一次）。
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
	if err := normalizeMcpAppCaller(ctx, req.CallerKey); err != nil {
		components.RenderJsonFail(ctx, err)
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
		CallerKey: strings.TrimSpace(req.CallerKey),
		Status:    status,
		CreatedBy: helpers.GetUserName(ctx),
		UpdatedBy: helpers.GetUserName(ctx),
	}
	if err := model.CreateMcpApp(ctx, app); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	zlog.Infof(ctx, "[MCPGW] 新增应用: name=%s callerKey=%s", appName, app.CallerKey)
	view := mcpAppToView(*app)
	components.RenderJsonSucc(ctx, gin.H{"app": view, "appSecret": secret})
}

// UpdateMcpApp 更新 MCP 应用（名称/绑定 caller/启停）。
// @Summary      更新 MCP 应用
// @Description  按 appId 更新应用名称、caller 作用域或启停状态
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
	if strings.TrimSpace(req.CallerKey) != "" {
		if err := normalizeMcpAppCaller(ctx, req.CallerKey); err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		updates["caller_key"] = strings.TrimSpace(req.CallerKey)
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
	components.RenderJsonSucc(ctx, gin.H{"app": mcpAppToView(*refreshed)})
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
