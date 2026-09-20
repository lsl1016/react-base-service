package mcpgateway

import (
	"context"
	"strings"
	"time"

	"react-base-service/golib/zlog"

	"github.com/gin-gonic/gin"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ObserveMiddleware 是 MCP 层的统一切面（自 mcp-server 移植，去掉限流与 Prometheus）：
// 仅对 tools/call 计时并投递审计；其余方法（initialize/tools/list 等元数据查询）直接放行。
func ObserveMiddleware(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		ginCtx, hasGin := GinContextFrom(ctx)
		identity, hasIdentity := AppIdentityFrom(ctx)
		if !hasGin || !hasIdentity || method != "tools/call" {
			return next(ctx, method, req)
		}

		toolName := extractToolName(req)
		start := time.Now()
		result, err := next(ctx, method, req)
		cost := time.Since(start)

		reportToolCall(ginCtx, identity, toolName, cost, result, err)
		return result, err
	}
}

func reportToolCall(ginCtx *gin.Context, identity AppIdentity, toolName string,
	cost time.Duration, result sdk.Result, err error) {
	binding, _ := ginCtx.Get(auditBindingKey)
	resultCode, _ := ginCtx.Get(auditResultCodeKey)
	errorMsg, _ := ginCtx.Get(auditErrorMsgKey)
	responseText, isError := extractResponseText(result)

	entry := AuditEntry{
		RequestID:    zlog.GetRequestID(ginCtx),
		AppKey:       identity.AppKey,
		UserName:     identity.UserName,
		McpMethod:    "tools/call",
		ToolName:     toolName,
		ResponseText: responseText,
		CostMs:       int(cost.Milliseconds()),
		ClientIP:     ginCtx.ClientIP(),
		ClientInfo:   auditClientInfo(ginCtx),
	}
	if b, ok := binding.(*ToolBinding); ok && b != nil {
		entry.ToolName = b.Name
	}
	if code, ok := resultCode.(int); ok {
		entry.ResultCode = code
	}
	if msg, ok := errorMsg.(string); ok {
		entry.ErrorMsg = msg
	}
	if isError {
		if entry.ResultCode == 0 {
			entry.ResultCode = -1
		}
		if entry.ErrorMsg == "" {
			entry.ErrorMsg = responseText
		}
	}
	if err != nil {
		entry.ResultCode = -1
		entry.ErrorMsg = err.Error()
		if entry.ResponseText == "" {
			entry.ResponseText = err.Error()
		}
	}
	if args, ok := ginCtx.Get(auditArgumentsKey); ok {
		if text, ok := args.(string); ok {
			entry.Arguments = text
		}
	}

	SubmitAudit(ginCtx, entry)
}

func extractResponseText(result sdk.Result) (string, bool) {
	callResult, ok := result.(*sdk.CallToolResult)
	if !ok || callResult == nil {
		return "", false
	}

	parts := make([]string, 0, len(callResult.Content))
	for _, content := range callResult.Content {
		text, ok := content.(*sdk.TextContent)
		if !ok || text == nil || text.Text == "" {
			continue
		}
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "\n\n"), callResult.IsError
}

// auditClientInfo 取客户端 User-Agent 摘要（mcp-server 用 Redis 会话信息，这里简化为 UA）。
func auditClientInfo(ginCtx *gin.Context) string {
	return truncateRunes(strings.TrimSpace(ginCtx.GetHeader("User-Agent")), 250)
}

// extractToolName 从请求参数中取出工具名。
func extractToolName(req sdk.Request) string {
	params, ok := req.GetParams().(*sdk.CallToolParamsRaw)
	if !ok || params == nil {
		return ""
	}
	return params.Name
}
