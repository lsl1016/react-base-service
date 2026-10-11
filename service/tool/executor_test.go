package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if err := os.MkdirAll("log", 0o755); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestExecuteHTTPToolGET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("got method %s, want GET", r.Method)
		}
		if got := r.URL.Query().Get("env"); got != "pangu" {
			t.Fatalf("got env %q, want %q", got, "pangu")
		}
		if got := r.URL.Query().Get("table"); got != "table_a" {
			t.Fatalf("got table %q, want %q", got, "table_a")
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	configBytes, _ := json.Marshal(ToolConfig{
		URL:       server.URL,
		Method:    http.MethodGet,
		TimeoutMs: 1000,
	})

	output, err := ExecuteHTTPTool(context.Background(), string(configBytes), map[string]interface{}{
		"env":   "pangu",
		"table": "table_a",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != `{"ok":true}` {
		t.Fatalf("got output %q", output)
	}
}

func TestExecuteHTTPToolPOST(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("got method %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body failed: %v", err)
		}
		if string(body) != `{"env":"pangu","table":"table_a"}` && string(body) != `{"table":"table_a","env":"pangu"}` {
			t.Fatalf("unexpected body: %s", string(body))
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	configBytes, _ := json.Marshal(ToolConfig{
		URL:       server.URL,
		Method:    http.MethodPost,
		TimeoutMs: 1000,
	})

	output, err := ExecuteHTTPTool(context.Background(), string(configBytes), map[string]interface{}{
		"env":   "pangu",
		"table": "table_a",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != `{"ok":true}` {
		t.Fatalf("got output %q", output)
	}
}

func TestExecuteHTTPToolUsesConfiguredKnowledgeBaseAppHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Knowledge-Base-App-Key"); got != "pinned_app" {
			t.Fatalf("got app header %q, want pinned_app", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body failed: %v", err)
		}
		if string(body) != `{"appKey":"model_app"}` {
			t.Fatalf("unexpected body: %s", string(body))
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	configBytes, _ := json.Marshal(ToolConfig{
		URL:       server.URL,
		Method:    http.MethodPost,
		TimeoutMs: 1000,
		Headers: map[string]string{
			"X-Knowledge-Base-App-Key": "pinned_app",
		},
	})

	output, err := ExecuteHTTPTool(context.Background(), string(configBytes), map[string]interface{}{
		"appKey": "model_app",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != `{"ok":true}` {
		t.Fatalf("got output %q", output)
	}
}

func TestMaskedHeadersForLog(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer token")
	headers.Set("Cookie", "session=abc123")
	headers.Set("X-Service-Token", "service-token")
	headers.Set("X-Bd-Logid", "logid-123")

	masked := maskedHeadersForLog(headers)

	if got := masked.Get("Authorization"); got != "B**********n" {
		t.Fatalf("got authorization %q", got)
	}
	if got := masked.Get("Cookie"); got != "s************3" {
		t.Fatalf("got cookie %q", got)
	}
	if got := masked.Get("X-Service-Token"); got != "s***********n" {
		t.Fatalf("got x-service-token %q", got)
	}
	if got := masked.Get("X-Bd-Logid"); got != "logid-123" {
		t.Fatalf("got x-bd-logid %q", got)
	}
	if got := headers.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("original authorization changed: %q", got)
	}
}

func TestMaskHeaderValueKeepFirstLast(t *testing.T) {
	cases := map[string]string{
		"":     "",
		"a":    "a",
		"ab":   "ab",
		"abc":  "a*c",
		"abcd": "a**d",
		"中文a":  "中*a",
		"中文ab": "中**b",
	}

	for input, want := range cases {
		if got := maskHeaderValueKeepFirstLast(input); got != want {
			t.Fatalf("input %q: got %q, want %q", input, got, want)
		}
	}
}

func TestParseToolConfigSupportsCamelAndLegacySnakeCase(t *testing.T) {
	cfg, err := ParseToolConfig(`{"inputSchema":{"type":"object","properties":{"keyword":{"type":"string"}}},"outputSchema":{"type":"object"},"frontendHint":"新提示"}`)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.InputSchema["type"] != "object" {
		t.Fatalf("unexpected camel input schema: %#v", cfg.InputSchema)
	}
	if cfg.OutputSchema["type"] != "object" {
		t.Fatalf("unexpected camel output schema: %#v", cfg.OutputSchema)
	}
	if cfg.FrontendHint != "新提示" {
		t.Fatalf("unexpected camel frontend hint: %q", cfg.FrontendHint)
	}

	legacy, err := ParseToolConfig(`{"input_schema":{"type":"object","properties":{"keyword":{"type":"string"}}},"output_schema":{"type":"object"},"frontend_hint":"旧提示"}`)
	if err != nil {
		t.Fatalf("unexpected legacy parse error: %v", err)
	}
	if legacy.InputSchema["type"] != "object" {
		t.Fatalf("unexpected legacy input schema: %#v", legacy.InputSchema)
	}
	if legacy.OutputSchema["type"] != "object" {
		t.Fatalf("unexpected legacy output schema: %#v", legacy.OutputSchema)
	}
	if legacy.FrontendHint != "旧提示" {
		t.Fatalf("unexpected legacy frontend hint: %q", legacy.FrontendHint)
	}
}

func TestParseToolConfigPrefersCamelCase(t *testing.T) {
	cfg, err := ParseToolConfig(`{"inputSchema":{"source":"camel"},"input_schema":{"source":"snake"},"outputSchema":{"source":"camel"},"output_schema":{"source":"snake"},"frontendHint":"camel","frontend_hint":"snake"}`)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.InputSchema["source"] != "camel" {
		t.Fatalf("expected camel input schema, got %#v", cfg.InputSchema)
	}
	if cfg.OutputSchema["source"] != "camel" {
		t.Fatalf("expected camel output schema, got %#v", cfg.OutputSchema)
	}
	if cfg.FrontendHint != "camel" {
		t.Fatalf("expected camel frontend hint, got %q", cfg.FrontendHint)
	}
}
