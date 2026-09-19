package mcpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestSDKClient 构造直连 httptest 服务器的 SDK 版客户端（经 testBypassValidate
// 测试缝绕过 SSRF 校验；生产入口 NewSDKClient 始终校验）。
func newTestSDKClient(t *testing.T, server *httptest.Server) *SDKClient {
	t.Helper()
	return &SDKClient{name: "sdk-test", endpoint: server.URL, timeout: 5 * time.Second, testBypassValidate: true}
}

func TestSDKClient_RejectsLocalEndpoint(t *testing.T) {
	if _, err := NewSDKClient("local", "http://127.0.0.1:9999/mcp", 0); err == nil {
		t.Fatal("loopback endpoint should be rejected at construction")
	}
	if _, err := NewSDKClient("private", "http://10.0.0.1:8080/mcp", 0); err == nil {
		t.Fatal("private endpoint should be rejected at construction")
	}
}

func TestSDKClient_ListTools(t *testing.T) {
	srv := &mcpTestServer{}
	server := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer server.Close()

	client := newTestSDKClient(t, server)
	tools, err := client.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Tool != "echo" || tools[1].Tool != "boom" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[0].InputSchema == nil || tools[0].InputSchema["type"] != "object" {
		t.Fatalf("inputSchema not normalized: %+v", tools[0].InputSchema)
	}
	if tools[0].OutputSchema == nil || tools[0].OutputSchema["type"] != "object" {
		t.Fatalf("outputSchema not normalized: %+v", tools[0].OutputSchema)
	}
	if tools[1].OutputSchema != nil {
		t.Fatalf("服务器未声明 outputSchema 时应为 nil: %+v", tools[1].OutputSchema)
	}
	if sessionSeenBy(srv) != "sess-fixed-1234" {
		t.Fatalf("业务请求未携带 initialize 签发的会话头: %q", sessionSeenBy(srv))
	}
}

func TestSDKClient_CallTool(t *testing.T) {
	srv := &mcpTestServer{callHandler: func(args map[string]any) (string, bool) {
		return fmt.Sprintf("sdk-echo:%v", args["text"]), false
	}}
	server := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer server.Close()

	client := newTestSDKClient(t, server)
	content, err := client.CallTool("echo", json.RawMessage(`{"text":"hello"}`), 0)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if content != "sdk-echo:hello" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestSDKClient_CallTool_ToolError(t *testing.T) {
	srv := &mcpTestServer{callHandler: func(args map[string]any) (string, bool) {
		return "远端执行失败", true
	}}
	server := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer server.Close()

	client := newTestSDKClient(t, server)
	content, err := client.CallTool("boom", json.RawMessage(`{}`), 0)
	if err == nil {
		t.Fatal("isError should surface as error")
	}
	if !strings.Contains(content, "远端执行失败") {
		t.Fatalf("error content not propagated: %q", content)
	}
}

func TestSDKClient_ServerDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()

	client := newTestSDKClient(t, server)
	if _, err := client.ListTools(); err == nil {
		t.Fatal("server error should propagate")
	}
}
