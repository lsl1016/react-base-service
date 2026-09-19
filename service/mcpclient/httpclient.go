package mcpclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient 实现 MCP Streamable HTTP 传输的客户端（kind=http）。
//
// 按设计文档决策「不做连接池」：每次操作（tools/list / tools/call）独立建一次
// 逻辑会话（initialize → 操作），会话头 Mcp-Session-Id 只在该次操作内使用，
// 操作结束即弃，无状态、无断线重连与失效检测负担。
//
// 安全：每次建会话前都执行端点校验（SSRF 防护，默认拒绝环回/私网/保留地址；
// allowPrivate=true 仅限本机开发环境经 mcp.allow_private_endpoint 显式开启）。
// 自定义 headers 会附加到每个请求（如 Authorization），但不会覆盖协议自身管理的头。
type HTTPClient struct {
	name        string
	endpoint    string
	timeout     time.Duration
	headers     map[string]string
	allowPrivate bool
	// validated 是已通过校验的端点，仅限包内测试注入（绕过 SSRF 校验直连 httptest）；
	// 生产构造路径 NewHTTPClient 不设置，withSession 始终走 validateEndpointOpts。
	validated *url.URL
}

// NewHTTPClient 按配置构造 HTTP 传输客户端并做一次端点校验（fail fast）。
func NewHTTPClient(name, endpoint string, timeoutMs int) (*HTTPClient, error) {
	return NewHTTPClientOpts(name, endpoint, timeoutMs, nil, false)
}

// NewHTTPClientOpts 构造带自定义请求头与端点策略的 HTTP 传输客户端。
func NewHTTPClientOpts(name, endpoint string, timeoutMs int, headers map[string]string, allowPrivate bool) (*HTTPClient, error) {
	if _, err := validateEndpointOpts(endpoint, allowPrivate); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPClient{
		name:         name,
		endpoint:     endpoint,
		timeout:      timeout,
		headers:      normalizeHeaderNames(headers),
		allowPrivate: allowPrivate,
	}, nil
}

// normalizeHeaderNames 归一化请求头名称并剔除协议自身管理的头，防止会话被劫持。
func normalizeHeaderNames(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	managed := map[string]bool{
		"host": true, "content-type": true, "content-length": true,
		"accept": true, "mcp-session-id": true,
	}
	normalized := make(map[string]string, len(headers))
	for key, value := range headers {
		lower := strings.ToLower(strings.TrimSpace(key))
		if managed[lower] || lower == "" {
			continue
		}
		normalized[lower] = value
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// Name 返回服务器名。
func (c *HTTPClient) Name() string { return c.name }

// Start 校验端点（HTTP 传输无子进程，无需拉起）。
func (c *HTTPClient) Start() error {
	_, err := validateEndpointOpts(c.endpoint, c.allowPrivate)
	return err
}

// Stop 无操作（无子进程、无常驻连接）。
func (c *HTTPClient) Stop() error { return nil }

// ListTools 建一次会话完成 initialize + tools/list。
func (c *HTTPClient) ListTools() ([]RegistryTool, error) {
	var tools []RegistryTool
	err := c.withSession(func(t *httpTransport) error {
		result, err := t.roundTrip("tools/list", map[string]any{})
		if err != nil {
			return err
		}
		var payload struct {
			Tools []struct {
				Name         string         `json:"name"`
				Description  string         `json:"description"`
				InputSchema  map[string]any `json:"inputSchema"`
				OutputSchema map[string]any `json:"outputSchema"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(result, &payload); err != nil {
			return fmt.Errorf("tools/list parse: %w", err)
		}
		for _, tool := range payload.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			tools = append(tools, RegistryTool{
				Server:       c.name,
				Tool:         tool.Name,
				Description:  tool.Description,
				InputSchema:  tool.InputSchema,
				OutputSchema: tool.OutputSchema,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", c.name, err)
	}
	return tools, nil
}

// CallTool 建一次会话完成 initialize + tools/call。
func (c *HTTPClient) CallTool(name string, arguments json.RawMessage, _ time.Duration) (string, error) {
	var content string
	err := c.withSession(func(t *httpTransport) error {
		var args any
		if len(arguments) > 0 {
			_ = json.Unmarshal(arguments, &args)
		}
		result, err := t.roundTrip("tools/call", map[string]any{"name": name, "arguments": args})
		if err != nil {
			return err
		}
		var payload struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(result, &payload); err != nil {
			content = string(result)
			return nil
		}
		texts := make([]string, 0, len(payload.Content))
		for _, item := range payload.Content {
			if item.Type == "text" || item.Type == "" {
				texts = append(texts, item.Text)
			}
		}
		content = strings.Join(texts, "\n")
		if payload.IsError {
			return fmt.Errorf("%s", content)
		}
		return nil
	})
	if err != nil {
		return content, fmt.Errorf("mcp %s tools/call %s: %w", c.name, name, err)
	}
	if strings.TrimSpace(content) == "" {
		content = "(tool returned no content)"
	}
	return content, nil
}

// withSession 建立一次性逻辑会话：校验端点 → initialize（捕获会话头）→ 执行操作。
func (c *HTTPClient) withSession(op func(t *httpTransport) error) error {
	u := c.validated
	if u == nil {
		var err error
		u, err = validateEndpointOpts(c.endpoint, c.allowPrivate)
		if err != nil {
			return err
		}
	}
	transport := &httpTransport{
		endpoint: u,
		client:   &http.Client{Timeout: c.timeout},
		headers:  c.headers,
	}
	if err := transport.initialize(); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return op(transport)
}

// httpTransport 承载一次逻辑会话的 JSON-RPC over HTTP 通信；
// 仅在 HTTPClient.withSession 内创建，生命周期为单次操作。
type httpTransport struct {
	endpoint  *url.URL
	client    *http.Client
	headers   map[string]string
	sessionID string
	nextID    int
}

type rpcHTTPRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcHTTPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// initialize 完成 MCP 握手，记录服务端返回的会话头（如有）。
func (t *httpTransport) initialize() error {
	result, err := t.roundTrip("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"clientInfo":      map[string]any{"name": "react-base-service", "version": "1.0"},
	})
	if err != nil {
		return err
	}
	var negotiated struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(result, &negotiated)
	// initialized 通知：服务端通常返回 202，无需读取响应体。
	_ = t.postNotification("notifications/initialized")
	return nil
}

// roundTrip 发送一个带 id 的 JSON-RPC 请求并读取同 id 响应。
// 响应体支持 application/json（单响应）与 text/event-stream（SSE 流）两种形态。
func (t *httpTransport) roundTrip(method string, params any) (json.RawMessage, error) {
	t.nextID++
	id := t.nextID
	resp, err := t.post(rpcHTTPRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(contentType, "text/event-stream"):
		return readSSEResponse(resp.Body, id)
	default:
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return nil, err
		}
		var rpcResp rpcHTTPResponse
		if err := json.Unmarshal(data, &rpcResp); err != nil {
			return nil, fmt.Errorf("json response parse: %w", err)
		}
		return rpcResult(rpcResp, id)
	}
}

// postNotification 发送无 id 的通知（不期望响应体）。
func (t *httpTransport) postNotification(method string) error {
	resp, err := t.post(rpcHTTPRequest{JSONRPC: "2.0", Method: method})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return nil
}

func (t *httpTransport) post(req rpcHTTPRequest) (*http.Response, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, t.endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range t.headers {
		httpReq.Header.Set(key, value)
	}
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	resp, err := t.client.Do(httpReq)
	if resp != nil {
		// 记录服务端下发的会话头，供同会话后续请求携带。
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			t.sessionID = sid
		}
	}
	return resp, err
}

// readSSEResponse 从 SSE 流中读取第一个匹配 id 的 JSON-RPC 响应。
// 按规范忽略 event 名与注释行；endpoint 通知等无关事件自然跳过。
func readSSEResponse(body io.Reader, wantID int) (json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	var data strings.Builder
	flush := func() (json.RawMessage, bool, error) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return nil, false, nil
		}
		var rpcResp rpcHTTPResponse
		if err := json.Unmarshal([]byte(payload), &rpcResp); err != nil {
			return nil, false, nil // 非 JSON 数据块（如注释性 ping 载荷）跳过
		}
		if rpcResp.ID != wantID {
			return nil, false, nil
		}
		result, err := rpcResult(rpcResp, wantID)
		return result, true, err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if result, matched, err := flush(); matched || err != nil {
				return result, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE 注释行（常用作心跳）
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(after, " "))
		}
	}
	if result, matched, err := flush(); matched || err != nil {
		return result, err
	}
	return nil, fmt.Errorf("sse stream ended without response for id %d", wantID)
}

func rpcResult(resp rpcHTTPResponse, wantID int) (json.RawMessage, error) {
	if resp.Error != nil {
		return nil, fmt.Errorf("jsonrpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.ID != wantID {
		return nil, fmt.Errorf("jsonrpc id mismatch: want %d got %d", wantID, resp.ID)
	}
	if len(resp.Result) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return resp.Result, nil
}
