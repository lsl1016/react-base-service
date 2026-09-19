package mcpgateway

import (
	"context"
	"encoding/json"
	"fmt"

	"react-base-service/golib/zlog"
	"react-base-service/service/mcpgateway/toolconfig"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildToolHandler 生成某个工具的执行函数（自 mcp-server 移植）。
//
// 执行顺序：取回 gin 上下文 → 复核工具可见性 → 校验入参 → HTTP 上游分派 → 归一化返回。
// 任何一步失败都返回 IsError 的结果而非协议级错误，
// 因为模型能读懂工具返回的文本并据此改正，而协议级错误在多数客户端里只显示为一个失败图标。
func buildToolHandler(binding ToolBinding) sdk.ToolHandler {
	toolName := binding.Name

	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		ginCtx, ok := GinContextFrom(ctx)
		if !ok {
			return errorResult("服务内部错误：请求上下文丢失。"), nil
		}

		identity, ok := AppIdentityFrom(ctx)
		if !ok {
			return errorResult("服务内部错误：调用方身份缺失，请重新连接 MCP 服务。"), nil
		}

		// tools/list 结果可能被客户端缓存，每次调用都重新确认工具仍可见且已授权
		current, err := LookupTool(ginCtx, identity.AppID, identity.CallerKey, toolName)
		if err != nil {
			zlog.Errorf(ginCtx, "[MCPGW] 工具信息加载失败: tool=%s err=%v", toolName, err)
			return errorResult("服务内部错误：工具信息加载失败。"), nil
		}
		if current == nil {
			zlog.Warnf(ginCtx, "[MCPGW] 工具不可见或已下线: app=%s tool=%s", identity.AppKey, toolName)
			return errorResult(fmt.Sprintf("当前应用无权调用工具 %s，或该工具已下线。", toolName)), nil
		}
		recordToolBinding(ginCtx, current)

		args, result := parseArguments(req, current)
		if result != nil {
			return result, nil
		}
		// 记录服务端补全后的最终请求，而不是模型传入的原始参数。
		ginCtx.Set(auditArgumentsKey, marshalArguments(args))

		upstream, err := Dispatch(ginCtx, current, args)
		if err != nil {
			zlog.Errorf(ginCtx, "[MCPGW] 请求上游失败: tool=%s err=%v", toolName, err)
			return toolCallErrorResult(toolName, err), nil
		}

		recordToolCall(ginCtx, current, upstream)
		return upstreamResult(toolName, current.OutputSchema, upstream), nil
	}
}

// parseArguments 解析并校验工具入参。
// 返回的 *sdk.CallToolResult 非空时表示校验未通过，调用方应直接返回该结果。
func parseArguments(req *sdk.CallToolRequest, binding *ToolBinding) (map[string]interface{}, *sdk.CallToolResult) {
	args := map[string]interface{}{}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, toolCallErrorResult(binding.Name, fmt.Errorf("调用 %s 失败：参数不是合法的 JSON 对象。", binding.Name))
		}
	}

	// schema 存在数据库里，无法交由 SDK 泛型注册自动校验，这里显式校验一次
	if missing, hint, ok := validateAgainstSchema(binding.InputSchema, args); !ok {
		if len(missing) > 0 {
			return nil, missingParamsError(binding.Name, missing, hint)
		}
		return nil, toolCallErrorResult(binding.Name, fmt.Errorf("调用 %s 失败：%s", binding.Name, hint))
	}
	return args, nil
}

// validateAgainstSchema 用工具自带的 JSON Schema 校验入参。
//
// 优先单独识别「缺少必填字段」，因为这是最常见、也最需要引导模型追问用户的情况。
func validateAgainstSchema(rawSchema string, args map[string]interface{}) (missing []string, hint string, ok bool) {
	schema, resolved, err := toolconfig.ParseObjectSchema(rawSchema, true)
	if err != nil {
		return nil, "工具参数定义无效，请联系管理员修复。", false
	}

	for _, field := range schema.Required {
		value, exists := args[field]
		if !exists || value == nil || value == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return missing, buildMissingHint(schema, missing), false
	}

	if err = resolved.Validate(args); err != nil {
		return nil, fmt.Sprintf("参数校验未通过：%s", err.Error()), false
	}
	return nil, "", true
}

func buildMissingHint(schema *jsonschema.Schema, missing []string) string {
	if schema.Properties == nil {
		return ""
	}
	hint := "各参数含义："
	found := false
	for _, field := range missing {
		prop, exists := schema.Properties[field]
		if !exists || prop == nil || prop.Description == "" {
			continue
		}
		hint += fmt.Sprintf("\n- %s: %s", field, prop.Description)
		found = true
	}
	if !found {
		return ""
	}
	return hint
}

// marshalArguments 序列化最终上游入参用于审计留档。
func marshalArguments(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(data)
}
