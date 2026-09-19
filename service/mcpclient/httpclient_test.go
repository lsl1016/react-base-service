package mcpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestHTTPClient 构造直连 httptest 服务器的 HTTPClient（经 validated 测试缝
// 绕过 SSRF 校验；校验逻辑由 endpoint_test.go 覆盖，生产入口 NewHTTPClient 始终校验）。
func newTestHTTPClient(t *testing.T, server *httptest.Server) *HTTPClient {
	t.Helper()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &HTTPClient{name: "test", endpoint: server.URL, timeout: 5 * time.Second, validated: endpoint}
}

// mcpTestServer 是一个最小 Streamable HTTP MCP 服务器：
// initialize 签发会话头；tools/list 走 JSON 响应；tools/call 走 SSE 流响应；
// 业务请求会记录并断言是否携带了会话头。
type mcpTestServer struct {
	mu            sync.Mutex
	sessionSeen   string // 最近一次业务请求携带的 Mcp-Session-Id
	callHandler   func(args map[string]any) (string, bool)
}

func (s *mcpTestServer) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if req.Method == "initialize" {
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.ProtocolVersion == "" {
			params.ProtocolVersion = "2025-06-18"
		}
		w.Header().Set("Mcp-Session-Id", "sess-fixed-1234")
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{
				"protocolVersion": params.ProtocolVersion, // 回显客户端请求的版本（协商兼容）
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "mcp-test", "version": "0.1.0"},
			},
		})
		return
	}

	// initialized 通知：202 无响应体
	if req.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	s.mu.Lock()
	s.sessionSeen = r.Header.Get("Mcp-Session-Id")
	s.mu.Unlock()

	switch req.Method {
	case "tools/list":
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
			"tools": []map[string]any{
				{"name": "echo", "description": "回显输入", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}, "outputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
				{"name": "boom", "description": "总是失败", "inputSchema": map[string]any{"type": "object"}},
			},
		}})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		text, isErr := s.callHandler(params.Arguments)
		// 用 SSE 流返回，覆盖流式解析路径（含心跳注释行）
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr,
		}})
		fmt.Fprintf(w, ": ping heartbeat\n\n")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
	default:
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	}
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func sessionSeenBy(srv *mcpTestServer) string {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.sessionSeen
}

func TestHTTPClient_ListTools(t *testing.T) {
	srv := &mcpTestServer{}
	server := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer server.Close()

	client := newTestHTTPClient(t, server)
	tools, err := client.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Tool != "echo" || tools[1].Tool != "boom" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[0].InputSchema == nil || tools[0].InputSchema["type"] != "object" {
		t.Fatalf("inputSchema not propagated: %+v", tools[0].InputSchema)
	}
	if tools[0].OutputSchema == nil || tools[0].OutputSchema["type"] != "object" {
		t.Fatalf("outputSchema not propagated: %+v", tools[0].OutputSchema)
	}
	if tools[1].OutputSchema != nil {
		t.Fatalf("服务器未声明 outputSchema 时应为 nil: %+v", tools[1].OutputSchema)
	}
	if sessionSeenBy(srv) != "sess-fixed-1234" {
		t.Fatalf("tools/list 未携带 initialize 签发的会话头: %q", sessionSeenBy(srv))
	}
}

func TestHTTPClient_CallTool_SSE(t *testing.T) {
	srv := &mcpTestServer{callHandler: func(args map[string]any) (string, bool) {
		return fmt.Sprintf("echo:%v", args["text"]), false
	}}
	server := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer server.Close()

	client := newTestHTTPClient(t, server)
	content, err := client.CallTool("echo", json.RawMessage(`{"text":"你好"}`), 0)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if content != "echo:你好" {
		t.Fatalf("unexpected content: %q", content)
	}
	if sessionSeenBy(srv) != "sess-fixed-1234" {
		t.Fatalf("tools/call 未携带会话头: %q", sessionSeenBy(srv))
	}
}

func TestHTTPClient_CallTool_ToolError(t *testing.T) {
	srv := &mcpTestServer{callHandler: func(args map[string]any) (string, bool) {
		return "内部错误：数据库不可用", true
	}}
	server := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer server.Close()

	client := newTestHTTPClient(t, server)
	content, err := client.CallTool("boom", json.RawMessage(`{}`), 0)
	if err == nil {
		t.Fatal("tool isError should surface as error")
	}
	if !strings.Contains(err.Error(), "内部错误") || !strings.Contains(content, "内部错误") {
		t.Fatalf("error content not propagated: err=%v content=%q", err, content)
	}
}

func TestHTTPClient_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()

	client := newTestHTTPClient(t, server)
	if _, err := client.ListTools(); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("http error should propagate, got %v", err)
	}
}
