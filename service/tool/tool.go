package tool

import (
	"encoding/json"
	"fmt"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/components/route"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	ToolTypeHTTP   = "http"
	ToolTypeClient = "client"
	ToolTypeMCP    = "mcp"

	maxToolConfigLen = 32 * 1024
)

// reservedToolNames 是 Runtime 内置 Meta Tool 名称，业务工具注册时不允许与其同名，
// 避免工具索引与内置工具混淆。名称与 service/react/meta_tools.go 保持一致（react 包有同步测试兜底）。
var reservedToolNames = map[string]struct{}{
	"list_tools":            {},
	"get_tool":              {},
	"execute_tool":          {},
	"list_skills":           {},
	"get_skill":             {},
	"read_tool_result":      {},
	"inspect_data":          {},
	"python_exec":           {},
	"todo_write":            {},
	"create_plan":           {},
	"ask_question":          {},
	"displayFiles":          {},
	"resolve_async_task":    {},
	"get_async_task":        {},
	"read_attachment":       {},
	"inspect_attachment":    {},
	"web_fetch":             {},
	"delegate_agent":        {},
	"send_message":          {},
	"load_runtime_code":     {},
	"memory_list":           {},
	"memory_read":           {},
	"memory_write":          {},
	"graph_memory_search":   {},
	"graph_memory_write":    {},
	"get_plan_template":     {},
	"start_template_plan":   {},
	"resume_template_plan":  {},
	"submit_step_result":    {},
	"plan_template_builder": {},
}

// IsReservedToolName 判断工具名是否为内置 Meta Tool 保留名。
func IsReservedToolName(name string) bool {
	_, ok := reservedToolNames[strings.TrimSpace(name)]
	return ok
}

// validateToolName 校验注册/更新的工具名，返回归一化后的名称。
func validateToolName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("name 不能为空")
	}
	if IsReservedToolName(trimmed) {
		return "", fmt.Errorf("name 不能与内置工具同名: %s", trimmed)
	}
	return trimmed, nil
}

func NormalizeToolType(toolType string) string {
	return strings.ToLower(strings.TrimSpace(toolType))
}

// 工具级危险操作确认模式（P2-3）：auto=自动执行（默认）、confirm=每次人工确认、
// confirm_risky=入参/工具名命中风险正则才确认。
const (
	ToolPermissionAuto         = "auto"
	ToolPermissionConfirm      = "confirm"
	ToolPermissionConfirmRisky = "confirm_risky"
)

// NormalizePermissionMode 校验并归一化工具级权限模式；空值按 auto。
func NormalizePermissionMode(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "":
		return ToolPermissionAuto, nil
	case ToolPermissionAuto, ToolPermissionConfirm, ToolPermissionConfirmRisky:
		return strings.TrimSpace(mode), nil
	default:
		return "", fmt.Errorf("permissionMode 仅支持 auto/confirm/confirm_risky")
	}
}

func IsAllowedToolType(toolType string) bool {
	switch NormalizeToolType(toolType) {
	case ToolTypeHTTP, ToolTypeClient, ToolTypeMCP:
		return true
	default:
		return false
	}
}

// validateToolConfig 校验 config 是当前工具类型支持的合法 JSON 对象。
func validateToolConfig(raw json.RawMessage, toolType string) error {
	normalizedToolType := NormalizeToolType(toolType)
	if !IsAllowedToolType(normalizedToolType) {
		return fmt.Errorf("toolType 仅支持 http 或 client")
	}
	if len(raw) == 0 {
		return fmt.Errorf("config 不能为空")
	}
	if len(raw) > maxToolConfigLen {
		return fmt.Errorf("config 不能超过 %d 字节", maxToolConfigLen)
	}
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return fmt.Errorf("config 必须是合法的 JSON 对象")
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawFields); err != nil {
		return fmt.Errorf("config 必须是合法的 JSON 对象: %w", err)
	}
	for snake, camel := range map[string]string{"input_schema": "inputSchema", "output_schema": "outputSchema", "frontend_hint": "frontendHint"} {
		if _, ok := rawFields[snake]; ok {
			return fmt.Errorf("config 字段 %q 命名错误，请使用 %q", snake, camel)
		}
	}
	var cfg ToolConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("config 必须是合法的 JSON 对象: %w", err)
	}
	if cfg.AsyncTask != nil {
		if !cfg.Async {
			return fmt.Errorf("config.asyncTask 仅可用于 async=true 的工具")
		}
		if strings.TrimSpace(cfg.AsyncTask.SchedulerType) == "" {
			return fmt.Errorf("config.asyncTask 缺少 schedulerType 字段")
		}
	}
	if normalizedToolType == ToolTypeHTTP {
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("http 工具 config 缺少 url 字段")
		}
		if len(cfg.InputSchema) == 0 {
			return fmt.Errorf("http 工具 config 缺少 inputSchema 字段")
		}
	}
	if normalizedToolType == ToolTypeMCP {
		if strings.TrimSpace(cfg.MCPServer) == "" || strings.TrimSpace(cfg.MCPTool) == "" {
			return fmt.Errorf("mcp 工具 config 缺少 mcpServer/mcpTool 字段")
		}
	}
	return nil
}

func RegisterTool(ctx *gin.Context, req *params.RegisterToolReq, createdBy string) (*model.Tool, error) {
	// caller_key=default 是默认作用域伪 caller（全 caller 可用），不要求真实 caller 存在。
	if !model.IsReservedCallerKey(req.CallerKey) {
		caller, err := model.GetActiveCallerByKey(ctx, req.CallerKey)
		if err != nil {
			return nil, err
		}
		if caller == nil {
			return nil, components.ErrorCallerNotFound.Sprintf(req.CallerKey)
		}
	}

	name, err := validateToolName(req.Name)
	if err != nil {
		return nil, components.ErrorToolRegisterFailed.Sprintf(err.Error())
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		return nil, components.ErrorToolRegisterFailed.Sprintf("description 不能为空")
	}
	if err := validateToolConfig(req.Config, req.ToolType); err != nil {
		return nil, components.ErrorToolRegisterFailed.Sprintf(err.Error())
	}
	normalizedToolType := NormalizeToolType(req.ToolType)
	permissionMode, err := NormalizePermissionMode(req.PermissionMode)
	if err != nil {
		return nil, components.ErrorToolRegisterFailed.Sprintf(err.Error())
	}

	exists, err := model.ExistToolByCallerAndName(ctx, req.CallerKey, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, components.ErrorToolDuplicate.Sprintf(req.CallerKey, name)
	}

	toolID := "tool_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	rv := req.RouteValues
	if rv == nil {
		rv = []string{}
	}
	routeValues, _ := json.Marshal(rv)

	t := &model.Tool{
		ToolID:         toolID,
		Name:           name,
		Description:    description,
		ToolType:       normalizedToolType,
		CallerKey:      req.CallerKey,
		RouteValues:    string(routeValues),
		Config:         string(req.Config),
		PermissionMode: permissionMode,
		Status:         1,
		CreatedBy:      createdBy,
		UpdatedBy:      createdBy,
	}

	if err := model.CreateTool(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func UpdateTool(ctx *gin.Context, req *params.UpdateToolReq, updatedBy string) error {
	existing, err := model.GetToolByToolID(ctx, req.ToolID)
	if err != nil {
		return err
	}
	if existing == nil {
		return components.ErrorToolNotFound.Sprintf(req.ToolID)
	}

	// MCP 工具的 config 与 toolType 由「MCP 连接」同步管理（tools/list upsert 会覆盖），
	// 不接受手工修改；名称/描述/状态等展示字段允许调整（下次同步会被服务端清单覆盖）。
	if existing.ToolType == ToolTypeMCP {
		if len(req.Config) > 0 {
			return components.ErrorToolRegisterFailed.Sprintf("MCP 工具的配置 JSON 由 MCP 连接同步管理，不可手工修改")
		}
		if req.ToolType != "" && NormalizeToolType(req.ToolType) != ToolTypeMCP {
			return components.ErrorToolRegisterFailed.Sprintf("MCP 工具不允许修改 toolType")
		}
	}

	finalToolType := NormalizeToolType(existing.ToolType)
	if req.ToolType != "" {
		finalToolType = NormalizeToolType(req.ToolType)
	}
	if !IsAllowedToolType(finalToolType) {
		return components.ErrorToolRegisterFailed.Sprintf("toolType 仅支持 http 或 client")
	}
	if req.ToolType != "" && NormalizeToolType(existing.ToolType) != finalToolType && len(req.Config) == 0 {
		return components.ErrorToolRegisterFailed.Sprintf("修改 toolType 时必须同时传 config")
	}
	if len(req.Config) > 0 {
		if err := validateToolConfig(req.Config, finalToolType); err != nil {
			return components.ErrorToolRegisterFailed.Sprintf(err.Error())
		}
	}

	updates := make(map[string]interface{})
	if req.Name != "" {
		name, err := validateToolName(req.Name)
		if err != nil {
			return components.ErrorToolRegisterFailed.Sprintf(err.Error())
		}
		if name != existing.Name {
			exists, err := model.ExistToolByCallerAndName(ctx, existing.CallerKey, name)
			if err != nil {
				return err
			}
			if exists {
				return components.ErrorToolDuplicate.Sprintf(existing.CallerKey, name)
			}
		}
		updates["name"] = name
	}
	if req.Description != "" {
		description := strings.TrimSpace(req.Description)
		if description == "" {
			return components.ErrorToolRegisterFailed.Sprintf("description 不能为空")
		}
		updates["description"] = description
	}
	if req.ToolType != "" {
		updates["tool_type"] = finalToolType
	}
	if req.RouteValues != nil {
		updateRv := req.RouteValues
		if len(updateRv) == 0 {
			updateRv = []string{}
		}
		rv, _ := json.Marshal(updateRv)
		updates["route_values"] = string(rv)
	}
	if len(req.Config) > 0 {
		updates["config"] = string(req.Config)
	}
	if req.PermissionMode != nil {
		permissionMode, err := NormalizePermissionMode(*req.PermissionMode)
		if err != nil {
			return components.ErrorToolRegisterFailed.Sprintf(err.Error())
		}
		updates["permission_mode"] = permissionMode
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	updates["updated_by"] = updatedBy
	if len(updates) == 0 {
		return nil
	}
	return model.UpdateToolByToolID(ctx, req.ToolID, updates)
}

func DeleteTool(ctx *gin.Context, toolID string) error {
	existing, err := model.GetToolByToolID(ctx, toolID)
	if err != nil {
		return err
	}
	if existing == nil {
		return components.ErrorToolNotFound.Sprintf(toolID)
	}
	// MCP 工具随连接生命周期管理：删除/停用「MCP 连接」时自动清理，不可单独删除。
	if existing.ToolType == ToolTypeMCP {
		return components.ErrorToolRegisterFailed.Sprintf("MCP 工具由 MCP 连接同步管理，请在「MCP 连接」面板删除或停用对应连接")
	}
	return model.SoftDeleteToolByToolID(ctx, toolID)
}

// GetDetail 根据 toolId 获取工具详情
func GetDetail(ctx *gin.Context, toolID string) (*model.Tool, error) {
	t, err := model.GetToolByToolID(ctx, toolID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, components.ErrorToolNotFound.Sprintf(toolID)
	}
	return t, nil
}

func ListByCallerAndRoute(ctx *gin.Context, callerKey string, routeValues []string) ([]model.Tool, error) {
	// callerKey 为空：管理控制台「全部」视图，跨 caller 列出，忽略路由过滤。
	if strings.TrimSpace(callerKey) == "" {
		return model.ListAllTools(ctx)
	}
	return model.ListToolsByCallerAndExactRoute(ctx, callerKey, route.SerializeRouteValues(routeValues))
}

// ToToolResp 将 model.Tool 转换为 params.ToolResp
func ToToolResp(t *model.Tool) params.ToolResp {
	var routeValues []string
	_ = json.Unmarshal([]byte(t.RouteValues), &routeValues)
	permissionMode := t.PermissionMode
	if strings.TrimSpace(permissionMode) == "" {
		permissionMode = ToolPermissionAuto
	}
	return params.ToolResp{
		ToolID:         t.ToolID,
		Name:           t.Name,
		Description:    t.Description,
		ToolType:       t.ToolType,
		CallerKey:      t.CallerKey,
		RouteValues:    routeValues,
		Config:         json.RawMessage(t.Config),
		PermissionMode: permissionMode,
		Status:         t.Status,
		CreatedAt:      t.CreatedAt.Format("2006-01-02 15:04:05"),
		CreatedBy:      t.CreatedBy,
		UpdatedAt:      t.UpdatedAt.Format("2006-01-02 15:04:05"),
		UpdatedBy:      t.UpdatedBy,
	}
}
