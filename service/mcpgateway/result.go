package mcpgateway

import (
	"encoding/json"
	"fmt"
	"strings"

	"react-base-service/service/mcpgateway/toolconfig"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// textResult 构造成功返回。
func textResult(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: text},
		},
	}
}

// errorResult 构造错误返回。
//
// 错误文案面向模型而非后端排障：除了说明哪里不满足调用约定，
// 还要给出下一步该怎么补，模型才能据此向用户追问或自行改正。
func errorResult(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{
			&sdk.TextContent{Text: text},
		},
	}
}

func toolCallErrorResult(toolName string, err error) *sdk.CallToolResult {
	if err == nil {
		return errorResult(fmt.Sprintf("调用 %s 失败：未知错误。", toolName))
	}
	return errorResult(fmt.Sprintf("调用 %s 失败：%s", toolName, strings.TrimSpace(err.Error())))
}

// missingParamsError 缺少必填参数时的统一文案。
func missingParamsError(toolName string, missing []string, hint string) *sdk.CallToolResult {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("调用 %s 失败，缺少必填参数: %s。", toolName, strings.Join(missing, ", ")))
	if hint != "" {
		sb.WriteString("\n")
		sb.WriteString(hint)
	}
	return errorResult(sb.String())
}

// upstreamResult 把归一化后的上游响应转换为 MCP 返回（自 mcp-server 移植）。
//
// 成功时保留结构化 JSON（StructuredContent），便于模型从中提取下一跳所需的 ID 等
// 关键标识；启用输出投影时按 outputSchema 裁剪并渲染「结果说明/字段说明/结果」；
// 失败时只回传上游错误文案，不编造补救建议。
func upstreamResult(toolName, outputSchema string, result *UpstreamResult) *sdk.CallToolResult {
	if result == nil {
		return errorResult(fmt.Sprintf("调用 %s 失败：上游未返回任何数据。", toolName))
	}

	if !result.Success {
		msg := strings.TrimSpace(result.Message)
		if msg == "" {
			msg = fmt.Sprintf("上游返回错误码 %d", result.Code)
		}
		return errorResult(fmt.Sprintf("上游调用失败：%s", msg))
	}

	structured := result.Raw
	if structured == nil {
		structured = map[string]interface{}{"data": result.Data}
	}
	projection, enabled, err := projectStructuredOutput(outputSchema, structured)
	if err != nil {
		return errorResult(fmt.Sprintf("调用 %s 成功，但输出投影失败：%s", toolName, err.Error()))
	}
	if enabled {
		projectedObject, ok := projection.Value.(map[string]interface{})
		if !ok {
			return errorResult(fmt.Sprintf("调用 %s 成功，但投影结果不是 JSON 对象。", toolName))
		}
		structured = projectedObject
	}
	if err := validateStructuredOutput(outputSchema, structured); err != nil {
		return errorResult(fmt.Sprintf("调用 %s 成功，但返回结构不符合 outputSchema：%s", toolName, err.Error()))
	}

	payload, err := json.Marshal(structured)
	if err != nil {
		return errorResult(fmt.Sprintf("调用 %s 成功，但结果序列化失败：%s", toolName, err.Error()))
	}
	if len(payload) == 0 || string(payload) == "null" {
		return textResult(fmt.Sprintf("调用 %s 成功，上游未返回数据内容。", toolName))
	}
	contentText := string(payload)
	if enabled {
		contentText = renderProjectedOutput(projection, payload)
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: contentText},
		},
		StructuredContent: structured,
	}
}

func validateStructuredOutput(rawSchema string, structured interface{}) error {
	if strings.TrimSpace(rawSchema) == "" {
		return nil
	}
	_, resolved, err := toolconfig.ParseObjectSchema(rawSchema, true)
	if err != nil {
		return fmt.Errorf("输出定义无效")
	}
	object, ok := structured.(map[string]interface{})
	if !ok {
		data, err := json.Marshal(structured)
		if err != nil {
			return fmt.Errorf("结果无法序列化: %w", err)
		}
		if err := json.Unmarshal(data, &object); err != nil {
			return fmt.Errorf("结果不是 JSON 对象")
		}
	}
	if err := resolved.Validate(object); err != nil {
		return err
	}
	return nil
}
