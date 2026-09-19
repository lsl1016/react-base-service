package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	model "react-base-service/models/llm"
)

func TestRuntimeExecuteHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "sid=abc" {
			t.Fatalf("unexpected cookie header: %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body failed: %v", err)
		}
		if string(body) != `{"value":"demo"}` {
			t.Fatalf("unexpected body: %s", string(body))
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	config, _ := json.Marshal(ToolConfig{
		URL:       server.URL,
		Method:    http.MethodPost,
		TimeoutMs: 1000,
	})
	result, err := DefaultRuntime().Execute(context.Background(), ExecuteRequest{
		Tool: model.Tool{
			Name:     "demo",
			ToolType: ToolTypeHTTP,
			Config:   string(config),
		},
		Input:   json.RawMessage(`{"value":"demo"}`),
		Cookies: map[string]string{"sid": "abc"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != `{"ok":true}` {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestRuntimeRejectsInvalidMCPRoute(t *testing.T) {
	_, err := DefaultRuntime().Execute(context.Background(), ExecuteRequest{
		Tool: model.Tool{
			Name:     "broken-mcp",
			ToolType: ToolTypeMCP,
			Config:   `{"inputSchema":{"type":"object"}}`,
		},
		Input: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatalf("invalid MCP route should fail")
	}
}
