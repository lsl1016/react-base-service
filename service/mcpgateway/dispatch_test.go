package mcpgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"react-base-service/conf"
	"react-base-service/service/mcpgateway/toolconfig"

	"github.com/gin-gonic/gin"
)

func dispatchTestBinding(t *testing.T, name, url, method string) *ToolBinding {
	t.Helper()
	return &ToolBinding{
		Name:    name,
		URL:     url,
		Request: toolConfigForTest(method),
	}
}

func toolConfigForTest(method string) toolconfig.RequestConfig {
	config := toolconfig.RequestConfig{Method: method, TimeoutMS: 2000}
	_ = config.Normalize()
	return config
}

func TestDispatchGenericPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("请求方法为 %s，期望 POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type 为 %q，期望 application/json", r.Header.Get("Content-Type"))
		}
		if cookie, err := r.Cookie("SESSION"); err != nil || cookie.Value != "session" {
			t.Errorf("forwardCookies 配置的 Cookie 未正确透传")
		}
		if cookie, err := r.Cookie("NOT_FORWARDED"); err == nil {
			t.Errorf("未配置的 Cookie 不应透传，实际值为 %q", cookie.Value)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["query"] != "table" {
			t.Errorf("请求体不符合预期: %#v，错误=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errNo":0,"errStr":"","data":{"id":"1"}}`))
	}))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodPost, "http://mcp.local", nil)
	request.AddCookie(&http.Cookie{Name: "SESSION", Value: "session"})
	request.AddCookie(&http.Cookie{Name: "NOT_FORWARDED", Value: "should-not-pass"})
	ctx := &gin.Context{Request: request}

	originalForward := conf.CustomConf.MCPServer.ForwardCookies
	conf.CustomConf.MCPServer.ForwardCookies = []string{"SESSION"}
	defer func() { conf.CustomConf.MCPServer.ForwardCookies = originalForward }()

	binding := dispatchTestBinding(t, "generic_post", server.URL, http.MethodPost)
	result, err := Dispatch(ctx, binding, map[string]interface{}{"query": "table"})
	if err != nil {
		t.Fatalf("通用工具调用失败: %v", err)
	}
	if !result.Success || result.Raw["data"] == nil {
		t.Fatalf("调用结果不符合预期: %#v", result)
	}
}

func TestDispatchRecognizesCommonSuccessFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"errNo 0", `{"errNo":0,"data":{}}`, true},
		{"errNo 1001", `{"errNo":1001,"errStr":"失败","data":{}}`, false},
		{"code 0", `{"code":0,"msg":"ok","data":{}}`, true},
		{"code 500", `{"code":500,"message":"boom","data":{}}`, false},
		{"success false", `{"success":false,"error":"boom"}`, false},
		{"success true", `{"success":true,"data":{}}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			request, _ := http.NewRequest(http.MethodPost, "http://mcp.local", nil)
			result, err := Dispatch(&gin.Context{Request: request}, dispatchTestBinding(t, "generic_status", server.URL, http.MethodPost), nil)
			if err != nil {
				t.Fatalf("调用失败: %v", err)
			}
			if result.Success != test.want {
				t.Fatalf("Success=%v，期望 %v", result.Success, test.want)
			}
		})
	}
}

func TestDispatchDoesNotLimitResponseSize(t *testing.T) {
	content := strings.Repeat("a", (4<<20)+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"content":"` + content + `"}}`))
	}))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodPost, "http://mcp.local", nil)
	ctx := &gin.Context{Request: request}
	result, err := Dispatch(ctx, dispatchTestBinding(t, "large_response", server.URL, http.MethodPost), nil)
	if err != nil {
		t.Fatalf("大响应不应被限制: %v", err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["content"] != content {
		t.Fatal("大响应内容未完整保留")
	}
}

func TestDispatchGenericGetUsesQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "42" {
			t.Errorf("查询参数 id 为 %q，期望 42", r.URL.Query().Get("id"))
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodPost, "http://mcp.local", nil)
	ctx := &gin.Context{Request: request}
	result, err := Dispatch(ctx, dispatchTestBinding(t, "generic_get", server.URL, http.MethodGet), map[string]interface{}{"id": float64(42)})
	if err != nil || !result.Success {
		t.Fatalf("通用工具调用结果=%#v，错误=%v", result, err)
	}
}

func TestDispatchSupportsAnyJSONTopLevelValue(t *testing.T) {
	tests := []struct {
		name string
		body string
		want interface{}
	}{
		{name: "数组", body: `[{"id":"1"}]`, want: []interface{}{map[string]interface{}{"id": "1"}}},
		{name: "字符串", body: `"ok"`, want: "ok"},
		{name: "数字", body: `123.5`, want: float64(123.5)},
		{name: "布尔值", body: `true`, want: true},
		{name: "null", body: `null`, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			request, _ := http.NewRequest(http.MethodPost, "http://mcp.local", nil)
			result, err := Dispatch(&gin.Context{Request: request}, dispatchTestBinding(t, "generic_json", server.URL, http.MethodPost), nil)
			if err != nil {
				t.Fatalf("分发 JSON 顶层值失败: %v", err)
			}
			if !reflect.DeepEqual(result.Data, test.want) || !reflect.DeepEqual(result.Raw["data"], test.want) {
				t.Fatalf("归一化结果不符合预期: %#v", result)
			}
		})
	}
}

func TestDispatchRejectsInvalidJSONResponse(t *testing.T) {
	for _, body := range []string{`not-json`, `{} {}`} {
		_, err := decodeUpstreamJSON([]byte(body))
		if err == nil {
			t.Fatalf("非法 JSON 响应应被拒绝: %q", body)
		}
	}
}
