package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	model "react-base-service/models/llm"
	"react-base-service/service/mcpclient"
)

// ExecuteRequest 描述一次后端托管 Business Tool 执行。
// Runtime 层只负责工具协议语义；HTTP/MCP 传输细节统一收口在 service/tool。
type ExecuteRequest struct {
	Tool    model.Tool
	Input   json.RawMessage
	Cookies map[string]string
}

// ExecuteResult 是 Tool Runtime 的统一执行结果。
// StructuredContent/Meta 先作为兼容扩展位保留，后续可承接 MCP structuredContent/outputSchema。
type ExecuteResult struct {
	Content           string
	StructuredContent any
	Meta              map[string]any
}

// Runtime 统一承载服务端 Business Tool 的实际执行。
// 当前支持 HTTP 与 MCP；Client Tool 仍由 React Runtime 负责，因为它依赖 WebSocket/HITL。
type Runtime struct{}

var defaultRuntime = &Runtime{}

// DefaultRuntime 返回进程内默认 Tool Runtime。
func DefaultRuntime() *Runtime {
	return defaultRuntime
}

// Execute 根据工具配置选择 HTTP 或 MCP 执行端。
// 调用取消、超时等错误保持原样向上返回，由 Agent Runtime 统一决定 run/tool 的终态语义。
func (r *Runtime) Execute(ctx context.Context, req ExecuteRequest) (ExecuteResult, error) {
	cfg, err := ParseToolConfig(req.Tool.Config)
	if err != nil {
		return ExecuteResult{}, err
	}

	if NormalizeToolType(req.Tool.ToolType) == ToolTypeMCP || strings.TrimSpace(cfg.MCPServer) != "" {
		if strings.TrimSpace(cfg.MCPServer) == "" || strings.TrimSpace(cfg.MCPTool) == "" {
			return ExecuteResult{}, fmt.Errorf("mcp 工具配置缺少 mcpServer/mcpTool")
		}
		timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		content, err := mcpclient.Call(cfg.MCPServer, cfg.MCPTool, req.Input, timeout)
		if err != nil {
			return ExecuteResult{}, err
		}
		return ExecuteResult{Content: content}, nil
	}

	var input any
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &input)
	}
	content, err := ExecuteHTTPTool(ctx, req.Tool.Config, input, req.Cookies)
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{Content: content}, nil
}
