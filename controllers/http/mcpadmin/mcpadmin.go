// Package mcpadmin 是 MCP 网关管理台的接口适配层：把 mcp-server 项目 Vue3 管理台
// （web/mcp-admin/dist，经 go:embed 挂在 /react/mcp-admin）调用的 /api/manage/*
// 协议适配到本项目的融合后端（tblLlmTool http 工具 + tblLlmMcpApp/McpAppTool 凭证授权）。
//
// 字段映射（mcp-server 数据模型 → 融合模型）：
//   - 工具数字 id → tblLlmTool 自增主键；bizTag → caller_key（分组维度）
//   - url/requestConfig/readOnly → config 的 url / {method,timeout_ms,headers} / method==GET
//   - status：管理台 1=停用 2=启用 ↔ 融合表 0/1
//   - app → tblLlmMcpApp（创建默认绑定 default 作用域，绑定调整走 playground「MCP 应用」页）
package mcpadmin

import (
	"encoding/json"
	"fmt"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/base"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	"react-base-service/service/mcpgateway"
	"react-base-service/service/mcpgateway/toolconfig"
	toolService "react-base-service/service/tool"

	"github.com/gin-gonic/gin"
)

// 管理台状态常量（mcp-server 前端约定：status 1=停用 2=启用；readOnly 1=写操作 2=只读）。
const (
	adminStatusDisabled = 1
	adminStatusEnabled  = 2
	adminReadOnlyWrite  = 1
	adminReadOnlyRead   = 2
	// errNoNoAdmin 与前端约定：令牌无效时返回 2001，触发右上角「管理令牌」弹窗。
	errNoNoAdmin = 2001
)

var errNoAdmin = base.Error{ErrNo: errNoNoAdmin, ErrMsg: "管理令牌无效或未配置"}

// Auth 校验管理台静态令牌（X-Admin-Token；X-Admin-User 仅作操作者记录）。
// mcp_server.admin_tokens 配置了白名单时按白名单校验；空列表 = 接受任意非空令牌
// （与 playground 管理面同级的内网联调默认）。
func Auth(ctx *gin.Context) {
	token := strings.TrimSpace(ctx.GetHeader("X-Admin-Token"))
	if token == "" {
		components.RenderJsonFail(ctx, errNoAdmin)
		ctx.Abort()
		return
	}
	if allowlist := conf.CustomConf.MCPServer.AdminTokens; len(allowlist) > 0 {
		matched := false
		for _, candidate := range allowlist {
			if candidate == token {
				matched = true
				break
			}
		}
		if !matched {
			components.RenderJsonFail(ctx, errNoAdmin)
			ctx.Abort()
			return
		}
	}
	ctx.Next()
}

// adminToolView 是管理台的工具行视图（mcp-server McpTool 形状）。
type adminToolView struct {
	ID            int    `json:"id"`
	BizTag        string `json:"bizTag"`
	URL           string `json:"url"`
	RequestConfig string `json:"requestConfig"`
	Name          string `json:"name"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	InputSchema   string `json:"inputSchema"`
	OutputSchema  string `json:"outputSchema"`
	ReadOnly      int    `json:"readOnly"`
	Status        int    `json:"status"`
	Owner         string `json:"owner"`
}

// adminToolConfig 是 tblLlmTool.config 中与网关执行相关的字段。
type adminToolConfig struct {
	URL          string                 `json:"url"`
	Method       string                 `json:"method,omitempty"`
	TimeoutMs    int                    `json:"timeout_ms,omitempty"`
	Headers      map[string]string      `json:"headers,omitempty"`
	InputSchema  map[string]interface{} `json:"inputSchema,omitempty"`
	OutputSchema map[string]interface{} `json:"outputSchema,omitempty"`
}

func parseAdminToolConfig(raw string) (*adminToolConfig, error) {
	cfg := &adminToolConfig{}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("config 为空")
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("config 不是合法 JSON: %w", err)
	}
	return cfg, nil
}

func requestConfigText(cfg *adminToolConfig) string {
	request := toolconfig.RequestConfig{Method: cfg.Method, TimeoutMS: cfg.TimeoutMs, Headers: cfg.Headers}
	_ = request.Normalize()
	data, err := json.Marshal(map[string]interface{}{
		"method":     request.Method,
		"timeout_ms": request.TimeoutMS,
		"headers":    request.Headers,
	})
	if err != nil {
		return "{}"
	}
	return string(data)
}

func schemaText(schema map[string]interface{}) string {
	if schema == nil {
		return ""
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	return string(data)
}

func toolToAdminView(tool model.Tool) adminToolView {
	cfg, err := parseAdminToolConfig(tool.Config)
	if err != nil {
		cfg = &adminToolConfig{}
	}
	readOnly := adminReadOnlyWrite
	if strings.EqualFold(cfg.Method, "GET") {
		readOnly = adminReadOnlyRead
	}
	status := adminStatusDisabled
	if tool.Status == 1 {
		status = adminStatusEnabled
	}
	return adminToolView{
		ID:            int(tool.ID),
		BizTag:        tool.CallerKey,
		URL:           cfg.URL,
		RequestConfig: requestConfigText(cfg),
		Name:          tool.Name,
		Title:         tool.Name,
		Description:   tool.Description,
		InputSchema:   schemaText(cfg.InputSchema),
		OutputSchema:  schemaText(cfg.OutputSchema),
		ReadOnly:      readOnly,
		Status:        status,
		Owner:         tool.CreatedBy,
	}
}

type adminToolListRequest struct {
	EnabledOnly bool   `json:"enabledOnly"`
	BizTag      string `json:"bizTag"`
	Keyword     string `json:"keyword"`
	PageNo      int    `json:"pageNo"`
	PageSize    int    `json:"pageSize"`
}

type adminPageResult struct {
	List     []adminToolView `json:"list"`
	Total    int             `json:"total"`
	PageNo   int             `json:"pageNo"`
	PageSize int             `json:"pageSize"`
}

// ListTools 管理台工具分页列表（bizTag=caller 作用域过滤；keyword 跨名称/描述/URL 检索）。
func ListTools(ctx *gin.Context) {
	handleToolPage(ctx, false)
}

// SearchTools 管理台工具关键词检索（与 list 同构，前端搜索框走这里）。
func SearchTools(ctx *gin.Context) {
	handleToolPage(ctx, true)
}

func handleToolPage(ctx *gin.Context, _ bool) {
	var req adminToolListRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	tools, err := model.ListToolsByType(ctx, "http")
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	views := make([]adminToolView, 0, len(tools))
	for _, tool := range tools {
		if req.EnabledOnly && tool.Status != 1 {
			continue
		}
		if req.BizTag != "" && tool.CallerKey != req.BizTag {
			continue
		}
		if keyword := strings.ToLower(strings.TrimSpace(req.Keyword)); keyword != "" {
			cfg, _ := parseAdminToolConfig(tool.Config)
			haystack := strings.ToLower(tool.Name + " " + tool.Description + " " + tool.CallerKey + " " + cfg.URL)
			if !strings.Contains(haystack, keyword) {
				continue
			}
		}
		views = append(views, toolToAdminView(tool))
	}
	pageNo, pageSize := req.PageNo, req.PageSize
	if pageNo <= 0 {
		pageNo = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	total := len(views)
	start := (pageNo - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	components.RenderJsonSucc(ctx, adminPageResult{List: views[start:end], Total: total, PageNo: pageNo, PageSize: pageSize})
}

type adminToolMutation struct {
	BizTag        string `json:"bizTag"`
	URL           string `json:"url"`
	RequestConfig string `json:"requestConfig"`
	Name          string `json:"name"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	InputSchema   string `json:"inputSchema"`
	OutputSchema  string `json:"outputSchema"`
	ReadOnly      *int   `json:"readOnly"`
	Status        int    `json:"status"`
	Owner         string `json:"owner"`
}

type adminToolUpdate struct {
	ID            int     `json:"id"`
	BizTag        *string `json:"bizTag"`
	URL           *string `json:"url"`
	RequestConfig *string `json:"requestConfig"`
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	InputSchema   *string `json:"inputSchema"`
	OutputSchema  *string `json:"outputSchema"`
	ReadOnly      *int    `json:"readOnly"`
}

// buildToolConfigJSON 由管理台字段拼装 tblLlmTool.config（method 优先取 requestConfig，
// 缺省按 readOnly 推断：只读=GET，写=POST）。
func buildToolConfigJSON(mutation adminToolMutation) (string, error) {
	request := &toolconfig.RequestConfig{}
	if strings.TrimSpace(mutation.RequestConfig) != "" {
		parsed, err := toolconfig.ParseRequestConfig(mutation.RequestConfig)
		if err != nil {
			return "", fmt.Errorf("requestConfig 无效: %w", err)
		}
		request = parsed
	}
	if request.Method == "" {
		if mutation.ReadOnly != nil && *mutation.ReadOnly == adminReadOnlyRead {
			request.Method = "GET"
		} else {
			request.Method = "POST"
		}
	}
	config := adminToolConfig{
		URL:       strings.TrimSpace(mutation.URL),
		Method:    request.Method,
		TimeoutMs: request.TimeoutMS,
		Headers:   request.Headers,
	}
	if config.TimeoutMs == 0 {
		config.TimeoutMs = 10000
	}
	if _, err := toolconfig.NormalizeRequestURL(config.URL); err != nil {
		return "", err
	}
	if err := (&toolconfig.RequestConfig{Method: config.Method, TimeoutMS: config.TimeoutMs, Headers: config.Headers}).Normalize(); err != nil {
		return "", err
	}
	if raw := strings.TrimSpace(mutation.InputSchema); raw != "" && raw != "{}" {
		var schema map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			return "", fmt.Errorf("inputSchema 不是合法 JSON: %w", err)
		}
		if err := toolconfig.ValidateObjectSchema(raw, false); err != nil {
			return "", fmt.Errorf("inputSchema 无效: %w", err)
		}
		config.InputSchema = schema
	}
	if raw := strings.TrimSpace(mutation.OutputSchema); raw != "" {
		var schema map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			return "", fmt.Errorf("outputSchema 不是合法 JSON: %w", err)
		}
		if err := toolconfig.ValidateOutputSchema(raw, false); err != nil {
			return "", fmt.Errorf("outputSchema 无效: %w", err)
		}
		config.OutputSchema = schema
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type adminBatchCreateRequest struct {
	Tools []adminToolMutation `json:"tools"`
}

// BatchCreateTools 批量注册工具（管理台「新增工具」多草稿提交）。
// bizTag 即归属 caller（缺省 default，须为存活 caller 或保留作用域）；全部条目校验通过后逐条落库。
func BatchCreateTools(ctx *gin.Context) {
	var req adminBatchCreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if len(req.Tools) == 0 || len(req.Tools) > 50 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("单次批量注册 1-50 条"))
		return
	}
	operator := adminOperator(ctx)
	created := make([]gin.H, 0, len(req.Tools))
	for index, mutation := range req.Tools {
		if strings.TrimSpace(mutation.Name) == "" || strings.TrimSpace(mutation.URL) == "" {
			components.RenderJsonFail(ctx, components.ParamInvalidf("第 %d 条缺少工具名称或上游 URL", index+1))
			return
		}
		callerKey := defaultBizTag(mutation)
		if !model.IsReservedCallerKey(callerKey) {
			caller, err := model.GetActiveCallerByKey(ctx, callerKey)
			if err != nil {
				components.RenderJsonFail(ctx, err)
				return
			}
			if caller == nil {
				components.RenderJsonFail(ctx, components.ParamInvalidf("第 %d 条 bizTag（caller）不存在或已停用: %s", index+1, callerKey))
				return
			}
		}
		configJSON, err := buildToolConfigJSON(mutation)
		if err != nil {
			components.RenderJsonFail(ctx, components.ParamInvalidf("工具 %s: %s", mutation.Name, err.Error()))
			return
		}
		description := strings.TrimSpace(mutation.Description)
		if description == "" {
			description = mutation.Name
		}
		registered, err := toolService.RegisterTool(ctx, &params.RegisterToolReq{
			Name:        strings.TrimSpace(mutation.Name),
			Description: description,
			ToolType:    "http",
			CallerKey:   callerKey,
			Config:      json.RawMessage(configJSON),
		}, operator)
		if err != nil {
			components.RenderJsonFail(ctx, components.ParamInvalidf("工具 %s 注册失败: %s", mutation.Name, err.Error()))
			return
		}
		created = append(created, gin.H{"id": registered.ID, "name": registered.Name})
	}
	zlog.Infof(ctx, "[MCPGW.Admin] 批量注册工具: count=%d operator=%s", len(created), operator)
	components.RenderJsonSucc(ctx, gin.H{"list": created})
}

func defaultBizTag(mutation adminToolMutation) string {
	tag := strings.TrimSpace(mutation.BizTag)
	if tag == "" {
		return "default"
	}
	return tag
}

type adminBatchUpdateStatusRequest struct {
	IDs    []int `json:"ids"`
	Status int   `json:"status"`
}

// BatchUpdateToolStatus 工具批量上线/下线（管理台 1=下线 2=上线）。
func BatchUpdateToolStatus(ctx *gin.Context) {
	var req adminBatchUpdateStatusRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if req.Status != adminStatusDisabled && req.Status != adminStatusEnabled {
		components.RenderJsonFail(ctx, components.ParamInvalidf("status 只允许 1(停用)/2(启用)"))
		return
	}
	if len(req.IDs) == 0 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("ids 不能为空"))
		return
	}
	tools := fetchToolsByIDs(ctx, req.IDs)
	if len(tools) == 0 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("工具不存在"))
		return
	}
	status := 0
	if req.Status == adminStatusEnabled {
		status = 1
	}
	for _, tool := range tools {
		updates := map[string]interface{}{"status": status, "updated_by": adminOperator(ctx)}
		if err := model.UpdateToolByToolID(ctx, tool.ToolID, updates); err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
	}
	zlog.Infof(ctx, "[MCPGW.Admin] 批量更新工具状态: ids=%v status=%d", req.IDs, req.Status)
	components.RenderJsonSucc(ctx, gin.H{"ids": req.IDs, "status": req.Status, "count": len(tools)})
}

type adminBatchUpdateRequest struct {
	Tools []adminToolUpdate `json:"tools"`
}

// BatchUpdateTools 批量编辑工具（管理台编辑页保存；按数字 id 定位，部分字段更新）。
func BatchUpdateTools(ctx *gin.Context) {
	var req adminBatchUpdateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if len(req.Tools) == 0 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("tools 不能为空"))
		return
	}
	ids := make([]int, 0, len(req.Tools))
	for _, update := range req.Tools {
		ids = append(ids, update.ID)
	}
	tools := fetchToolsByIDs(ctx, ids)
	byID := make(map[uint]model.Tool, len(tools))
	for _, tool := range tools {
		byID[tool.ID] = tool
	}
	updatedViews := make([]adminToolView, 0, len(req.Tools))
	for _, update := range req.Tools {
		if update.ID <= 0 {
			continue
		}
		existing, ok := byID[uint(update.ID)]
		if !ok {
			components.RenderJsonFail(ctx, components.ParamInvalidf("工具不存在: id=%d", update.ID))
			return
		}
		cfg, err := parseAdminToolConfig(existing.Config)
		if err != nil {
			components.RenderJsonFail(ctx, components.ParamInvalidf("工具 %s 现有配置无效: %s", existing.Name, err.Error()))
			return
		}
		if update.URL != nil {
			cfg.URL = strings.TrimSpace(*update.URL)
		}
		if update.RequestConfig != nil {
			parsed, err := toolconfig.ParseRequestConfig(*update.RequestConfig)
			if err != nil {
				components.RenderJsonFail(ctx, components.ParamInvalidf("工具 %s: requestConfig 无效: %s", existing.Name, err.Error()))
				return
			}
			cfg.Method, cfg.TimeoutMs, cfg.Headers = parsed.Method, parsed.TimeoutMS, parsed.Headers
		}
		if update.ReadOnly != nil {
			if *update.ReadOnly == adminReadOnlyRead {
				cfg.Method = "GET"
			} else if cfg.Method == "" || strings.EqualFold(cfg.Method, "GET") {
				cfg.Method = "POST"
			}
		}
		if update.InputSchema != nil {
			if err := json.Unmarshal([]byte(*update.InputSchema), &cfg.InputSchema); err != nil {
				components.RenderJsonFail(ctx, components.ParamInvalidf("工具 %s: inputSchema 不是合法 JSON", existing.Name))
				return
			}
		}
		if update.OutputSchema != nil {
			if strings.TrimSpace(*update.OutputSchema) == "" {
				cfg.OutputSchema = nil
			} else if err := json.Unmarshal([]byte(*update.OutputSchema), &cfg.OutputSchema); err != nil {
				components.RenderJsonFail(ctx, components.ParamInvalidf("工具 %s: outputSchema 不是合法 JSON", existing.Name))
				return
			}
		}
		if _, err := toolconfig.NormalizeRequestURL(cfg.URL); err != nil {
			components.RenderJsonFail(ctx, components.ParamInvalidf("工具 %s: %s", existing.Name, err.Error()))
			return
		}
		configJSON, err := json.Marshal(cfg)
		if err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		updates := map[string]interface{}{"config": string(configJSON), "updated_by": adminOperator(ctx)}
		if update.Name != nil && strings.TrimSpace(*update.Name) != "" {
			updates["name"] = strings.TrimSpace(*update.Name)
		}
		if update.Description != nil {
			updates["description"] = strings.TrimSpace(*update.Description)
		}
		if err := model.UpdateToolByToolID(ctx, existing.ToolID, updates); err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		refreshed, err := model.GetToolByToolID(ctx, existing.ToolID)
		if err != nil || refreshed == nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		updatedViews = append(updatedViews, toolToAdminView(*refreshed))
	}
	zlog.Infof(ctx, "[MCPGW.Admin] 批量编辑工具: count=%d operator=%s", len(updatedViews), adminOperator(ctx))
	components.RenderJsonSucc(ctx, gin.H{"tools": updatedViews})
}

func fetchToolsByIDs(ctx *gin.Context, ids []int) []model.Tool {
	numeric := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			numeric = append(numeric, uint(id))
		}
	}
	tools, err := model.GetHTTPToolsByIDs(ctx, numeric)
	if err != nil {
		zlog.Errorf(ctx, "[MCPGW.Admin] 查询工具失败: %v", err)
		return nil
	}
	return tools
}

func adminOperator(ctx *gin.Context) string {
	operator := strings.TrimSpace(ctx.GetHeader("X-Admin-User"))
	if operator == "" {
		operator = helpers.GetUserName(ctx)
	}
	return operator
}

// ---- 应用（凭证）兼容层 ----

type adminAppView struct {
	ID         int    `json:"id"`
	AppName    string `json:"appName"`
	AppKey     string `json:"appKey"`
	AppSecret  string `json:"appSecret"`
	Description string `json:"description"`
	Status     int    `json:"status"`
	Owner      string `json:"owner"`
	ToolCount  int64  `json:"toolCount"`
}

type adminAppListRequest struct {
	Keyword  string `json:"keyword"`
	PageNo   int    `json:"pageNo"`
	PageSize int    `json:"pageSize"`
}

// ListApps 管理台应用列表（secret 不回传；description 展示绑定的 caller 作用域）。
func ListApps(ctx *gin.Context) {
	var req adminAppListRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	apps, err := model.ListMcpApps(ctx)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	views := make([]adminAppView, 0, len(apps))
	for _, app := range apps {
		if keyword := strings.TrimSpace(req.Keyword); keyword != "" &&
			!strings.Contains(strings.ToLower(app.AppName+app.AppKey), strings.ToLower(keyword)) {
			continue
		}
		status := adminStatusDisabled
		if app.Status == 1 {
			status = adminStatusEnabled
		}
		scopeTools, _ := model.ListGatewayHTTPToolsByCaller(ctx, app.CallerKey)
		toolCount := int64(len(scopeTools))
		views = append(views, adminAppView{
			ID:          int(app.ID),
			AppName:     app.AppName,
			AppKey:      app.AppKey,
			Description: "caller 作用域：" + app.CallerKey,
			Status:      status,
			Owner:       app.CreatedBy,
			ToolCount:   toolCount,
		})
	}
	components.RenderJsonSucc(ctx, gin.H{"list": views, "total": len(views)})
}

type adminAppCreateRequest struct {
	AppName     string `json:"appName"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
}

// CreateApp 新增应用凭证（secret 完整返回仅此一次；绑定 caller 固定 default，
// 如需绑定其它 caller 在 playground「MCP 应用」页修改）。
func CreateApp(ctx *gin.Context) {
	var req adminAppCreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	appName := strings.TrimSpace(req.AppName)
	if appName == "" {
		components.RenderJsonFail(ctx, components.ParamInvalidf("appName 不能为空"))
		return
	}
	existing, err := model.ListMcpApps(ctx)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	for _, app := range existing {
		if app.AppName == appName {
			components.RenderJsonFail(ctx, components.ParamInvalidf("应用名已存在: %s", appName))
			return
		}
	}
	secret := mcpgateway.GenerateAppSecret()
	app := &model.McpApp{
		AppID:     mcpgateway.NewMcpAppID(),
		AppName:   appName,
		AppKey:    mcpgateway.GenerateAppKey(),
		AppSecret: secret,
		CallerKey: "default",
		Status:    1,
		CreatedBy: adminOperator(ctx),
		UpdatedBy: adminOperator(ctx),
	}
	if err := model.CreateMcpApp(ctx, app); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	zlog.Infof(ctx, "[MCPGW.Admin] 新增应用: name=%s operator=%s", appName, adminOperator(ctx))
	components.RenderJsonSucc(ctx, gin.H{"app": adminAppView{
		ID:         int(app.ID),
		AppName:    app.AppName,
		AppKey:     app.AppKey,
		AppSecret:  secret,
		Status:     adminStatusEnabled,
		Owner:      app.CreatedBy,
		ToolCount:  0,
	}})
}

type adminAppGrantRequest struct {
	AppID   int   `json:"appId"`
	ToolIDs []int `json:"toolIds"`
	Status  int   `json:"status"`
}

func fetchAppByNumericID(ctx *gin.Context, id int) *model.McpApp {
	if id <= 0 {
		return nil
	}
	apps, err := model.ListMcpApps(ctx)
	if err != nil {
		return nil
	}
	for _, app := range apps {
		if int(app.ID) == id {
			found := app
			return &found
		}
	}
	return nil
}

// GrantAppTools 保存应用工具勾选（兼容 mcp-server 前端；作用域即权限，无服务端状态）。
func GrantAppTools(ctx *gin.Context) {
	var req adminAppGrantRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if fetchAppByNumericID(ctx, req.AppID) == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %d", req.AppID))
		return
	}
	// 作用域即权限：可见工具由应用绑定的 caller 决定，勾选仅作前端展示，无服务端状态。
	zlog.Infof(ctx, "[MCPGW.Admin] 保存应用工具勾选(无操作,作用域即权限): appId=%d count=%d", req.AppID, len(req.ToolIDs))
	components.RenderJsonSucc(ctx, gin.H{"appId": req.AppID, "toolIds": req.ToolIDs, "count": len(req.ToolIDs)})
}

// UpdateAppToolStatus 应用内工具启停（兼容接口；作用域即权限，实际启停请用工具自身上下线）。
func UpdateAppToolStatus(ctx *gin.Context) {
	var req adminAppGrantRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if req.Status != adminStatusDisabled && req.Status != adminStatusEnabled {
		components.RenderJsonFail(ctx, components.ParamInvalidf("status 只允许 1(停用)/2(启用)"))
		return
	}
	if fetchAppByNumericID(ctx, req.AppID) == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %d", req.AppID))
		return
	}
	// 作用域即权限：无服务端授权状态，停用/启用单工具请用工具自身的上下线。
	components.RenderJsonSucc(ctx, gin.H{"appId": req.AppID, "toolIds": req.ToolIDs, "status": req.Status})
}

type adminAppScopeRequest struct {
	AppID int `json:"appId"`
}

type adminAppToolBinding struct {
	ToolID  int    `json:"toolId"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	Status  int    `json:"status"`
	ReadOnly int   `json:"readOnly"`
}

// ListAppTools 应用可见工具清单（作用域内全部启用 http 工具，恒为已启用状态）。
func ListAppTools(ctx *gin.Context) {
	var req adminAppScopeRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	app := fetchAppByNumericID(ctx, req.AppID)
	if app == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("应用不存在: %d", req.AppID))
		return
	}
	tools, err := model.ListGatewayHTTPToolsByCaller(ctx, app.CallerKey)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	bindings := make([]adminAppToolBinding, 0, len(tools))
	for _, tool := range tools {
		cfg, _ := parseAdminToolConfig(tool.Config)
		readOnly := adminReadOnlyWrite
		if strings.EqualFold(cfg.Method, "GET") {
			readOnly = adminReadOnlyRead
		}
		status := adminStatusEnabled
		bindings = append(bindings, adminAppToolBinding{
			ToolID:   int(tool.ID),
			Name:     tool.Name,
			Title:    tool.Name,
			Status:   status,
			ReadOnly: readOnly,
		})
	}
	components.RenderJsonSucc(ctx, gin.H{"appId": req.AppID, "list": bindings})
}
