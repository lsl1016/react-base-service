package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SDKClient 是基于官方 MCP Go SDK（github.com/modelcontextprotocol/go-sdk）
// 的 Streamable HTTP 客户端实现（kind=http_sdk），与手写版 httpclient 并存，
// 由配置选择；两者实现同一 Server 接口，注册表同步与执行分发无差别。
//
// 与手写版一致的设计：无鉴权（不发送任何凭证头）；按次会话（每次操作
// Connect → 操作 → Close，无常驻连接）；每次建会话前执行 SSRF 端点校验。
// 差异：协议细节（版本协商、SSE 解析、会话管理）交由 SDK 处理。
type SDKClient struct {
	name     string
	endpoint string
	timeout  time.Duration
	// testBypassValidate 仅限包内测试注入：直连 httptest 服务器时绕过 SSRF 校验。
	testBypassValidate bool
}

// NewSDKClient 按配置构造官方 SDK 版客户端并做一次端点校验（fail fast）。
func NewSDKClient(name, endpoint string, timeoutMs int) (*SDKClient, error) {
	if _, err := validateEndpoint(endpoint); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &SDKClient{name: name, endpoint: endpoint, timeout: timeout}, nil
}

// Name 返回服务器名。
func (c *SDKClient) Name() string { return c.name }

// Start 校验端点（无子进程、无常驻连接）。
func (c *SDKClient) Start() error {
	if c.testBypassValidate {
		return nil
	}
	_, err := validateEndpoint(c.endpoint)
	return err
}

// Stop 无操作。
func (c *SDKClient) Stop() error { return nil }

// ListTools 建一次会话完成 initialize + tools/list。
func (c *SDKClient) ListTools() ([]RegistryTool, error) {
	var tools []RegistryTool
	err := c.withSession(func(ctx context.Context, session *mcp.ClientSession) error {
		result, err := session.ListTools(ctx, nil)
		if err != nil {
			return err
		}
		for _, tool := range result.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			tools = append(tools, RegistryTool{
				Server:       c.name,
				Tool:         tool.Name,
				Description:  tool.Description,
				InputSchema:  normalizeSDKSchema(tool.InputSchema),
				OutputSchema: normalizeSDKOutputSchema(tool.OutputSchema),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mcp %s (sdk): %w", c.name, err)
	}
	return tools, nil
}

// CallTool 建一次会话完成 initialize + tools/call。
func (c *SDKClient) CallTool(name string, arguments json.RawMessage, _ time.Duration) (string, error) {
	var content string
	err := c.withSession(func(ctx context.Context, session *mcp.ClientSession) error {
		var args any
		if len(arguments) > 0 {
			_ = json.Unmarshal(arguments, &args)
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return err
		}
		content = flattenSDKContent(result)
		if result.IsError {
			return fmt.Errorf("%s", content)
		}
		return nil
	})
	if err != nil {
		return content, fmt.Errorf("mcp %s (sdk) tools/call %s: %w", c.name, name, err)
	}
	if strings.TrimSpace(content) == "" {
		content = "(tool returned no content)"
	}
	return content, nil
}

// withSession 建立一次性逻辑会话：校验端点 → SDK Connect（内部完成 initialize）→ 操作 → Close。
func (c *SDKClient) withSession(op func(ctx context.Context, session *mcp.ClientSession) error) error {
	if !c.testBypassValidate {
		if _, err := validateEndpoint(c.endpoint); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout+10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "react-base-service", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: c.endpoint,
		HTTPClient: &http.Client{
			Timeout: c.timeout,
		},
		// 按次会话不接收服务端主动推送，关闭常驻 GET 流避免额外开销。
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = session.Close() }()
	return op(ctx, session)
}

// normalizeSDKSchema 把 SDK 返回的 InputSchema（any，通常为 *jsonschema.Schema
// 或 map）统一转成 map[string]any，供注册表 config 的 inputSchema 字段使用。
func normalizeSDKSchema(schema any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil || parsed == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return parsed
}

// normalizeSDKOutputSchema 把 SDK 返回的 OutputSchema（any）转成 map；
// 服务器未声明输出 schema 时返回 nil（config 不写入 outputSchema 字段，
// 与手写客户端解析缺省值的行为一致）。
func normalizeSDKOutputSchema(schema any) map[string]any {
	if schema == nil {
		return nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed) == 0 {
		return nil
	}
	return parsed
}

// flattenSDKContent 把 CallToolResult 的内容列表压成文本：TextContent 取文本，
// 非文本内容（图片/音频/资源）以占位描述替代，避免 base64 撑爆上下文。
func flattenSDKContent(result *mcp.CallToolResult) string {
	texts := make([]string, 0, len(result.Content))
	for _, item := range result.Content {
		switch value := item.(type) {
		case *mcp.TextContent:
			texts = append(texts, value.Text)
		case *mcp.ImageContent:
			texts = append(texts, fmt.Sprintf("[image: mime=%s, %d bytes]", value.MIMEType, len(value.Data)))
		case *mcp.AudioContent:
			texts = append(texts, fmt.Sprintf("[audio: mime=%s, %d bytes]", value.MIMEType, len(value.Data)))
		case *mcp.ResourceLink:
			texts = append(texts, fmt.Sprintf("[resource: %s]", value.URI))
		case *mcp.EmbeddedResource:
			if value.Resource != nil && value.Resource.Text != "" {
				texts = append(texts, value.Resource.Text)
			} else if value.Resource != nil {
				texts = append(texts, fmt.Sprintf("[resource: %s]", value.Resource.URI))
			}
		default:
			texts = append(texts, fmt.Sprintf("[unsupported content: %T]", item))
		}
	}
	return strings.Join(texts, "\n")
}
