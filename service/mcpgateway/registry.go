package mcpgateway

import (
	"encoding/json"

	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	"react-base-service/service/mcpgateway/toolconfig"
	"react-base-service/service/tool"

	"github.com/gin-gonic/gin"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultInputSchema 是工具未声明入参 schema 时的兜底（接受任意对象参数）。
const defaultInputSchema = `{"type":"object","properties":{}}`

// ToolBinding 是注册到 MCP Server 的一个工具及其调用上游所需的全部信息，
// 由 tblLlmTool 的 http 工具行展开而来。
type ToolBinding struct {
	ToolID      string
	Name        string
	Description string
	// URL/Request 是上游 HTTP 调用配置（来自 tool config 的 url/method/headers/timeout_ms）。
	URL     string
	Request toolconfig.RequestConfig
	// InputSchema/OutputSchema 是 config 内两个 schema 的原文（JSON）。
	InputSchema  string
	OutputSchema string
	// ReadOnly 按 HTTP method 推断（GET=只读），供客户端执行前确认提示。
	ReadOnly bool
}

// LoadTools 返回应用绑定的 caller 作用域内全部启用的 http 工具——
// 作用域即权限：应用绑哪个 caller（含 default 通用作用域），就看得到、调得了哪些工具。
func LoadTools(ctx *gin.Context, callerKey string) ([]ToolBinding, error) {
	return loadScopeTools(ctx, callerKey)
}

// LookupTool 在 tools/call 阶段按工具名复核：仍在 caller 作用域内且启用。
//
// tools/list 的结果可能被客户端缓存，工具下线或换绑 caller 后仍可能被调用，
// 因此每次调用都要重新确认。
func LookupTool(ctx *gin.Context, callerKey, name string) (*ToolBinding, error) {
	bindings, err := loadScopeTools(ctx, callerKey)
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		if binding.Name == name {
			return &binding, nil
		}
	}
	return nil, nil
}

// loadScopeTools 加载 caller 作用域（caller 自身 + default 通用）内启用的 http 工具行。
func loadScopeTools(ctx *gin.Context, callerKey string) ([]ToolBinding, error) {
	records, err := model.ListGatewayHTTPToolsByCaller(ctx, callerKey)
	if err != nil {
		return nil, err
	}
	bindings := make([]ToolBinding, 0, len(records))
	for _, record := range records {
		binding, err := buildToolBinding(record)
		if err != nil {
			zlog.Errorf(ctx, "[MCPGW] 跳过工具 %s：%v", record.Name, err)
			continue
		}
		bindings = append(bindings, *binding)
	}
	return bindings, nil
}

// buildToolBinding 把一行 tblLlmTool（tool_type=http）展开成网关工具绑定：
// config 解析出 URL 与请求配置；inputSchema/outputSchema 取原文透传。
func buildToolBinding(record model.Tool) (*ToolBinding, error) {
	cfg, err := tool.ParseToolConfig(record.Config)
	if err != nil {
		return nil, err
	}
	requestURL, err := toolconfig.NormalizeRequestURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	request := toolconfig.RequestConfig{
		Method:    cfg.Method,
		TimeoutMS: cfg.TimeoutMs,
		Headers:   cfg.Headers,
	}
	if err := request.Normalize(); err != nil {
		return nil, err
	}

	var rawCfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(record.Config), &rawCfg); err != nil {
		return nil, err
	}
	inputSchema := defaultInputSchema
	if raw, ok := rawCfg["inputSchema"]; ok && len(raw) > 0 {
		inputSchema = string(raw)
	}
	outputSchema := ""
	if raw, ok := rawCfg["outputSchema"]; ok && len(raw) > 0 {
		outputSchema = string(raw)
	}

	return &ToolBinding{
		ToolID:       record.ToolID,
		Name:         record.Name,
		Description:  record.Description,
		URL:          requestURL,
		Request:      request,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		ReadOnly:     request.Method == "GET",
	}, nil
}

// buildToolDefinition 把工具绑定转换成 MCP 协议的 Tool。
//
// input_schema/output_schema 直接以 json.RawMessage 透传：schema 存在数据库里，
// 编译期无法为每个工具准备 Go 结构体，因此走非泛型的动态注册路径。
func buildToolDefinition(ctx *gin.Context, binding ToolBinding) *sdk.Tool {
	if toolconfig.ValidateObjectSchema(binding.InputSchema, true) != nil {
		zlog.Errorf(ctx, "[MCPGW] 跳过工具 %s：inputSchema 无效", binding.Name)
		return nil
	}
	if toolconfig.ValidateOutputSchema(binding.OutputSchema, false) != nil {
		zlog.Errorf(ctx, "[MCPGW] 跳过工具 %s：outputSchema 无效（含不支持的投影语法）", binding.Name)
		return nil
	}

	toolDefinition := &sdk.Tool{
		Name:        binding.Name,
		Description: binding.Description,
		InputSchema: json.RawMessage(binding.InputSchema),
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint: binding.ReadOnly,
		},
	}
	if binding.OutputSchema != "" {
		toolDefinition.OutputSchema = json.RawMessage(binding.OutputSchema)
	}
	// 写操作显式标注，便于客户端在执行前向用户确认
	if !binding.ReadOnly {
		destructive := true
		toolDefinition.Annotations.DestructiveHint = &destructive
	}
	return toolDefinition
}
