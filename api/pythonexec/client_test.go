package pythonexec

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"react-base-service/conf"

	"react-base-service/golib/base"
	"github.com/gin-gonic/gin"
)

func TestExecute(t *testing.T) {
	if err := os.MkdirAll("log", 0o755); err != nil {
		t.Fatalf("MkdirAll log failed: %v", err)
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "code_200_success",
			body: `{"msg":"操作成功","code":200,"data":{"exitCode":0,"stdout":"{\"structuredResult\":{\"metrics\":{\"total\":1}},\"artifacts\":[]}","stderr":"","timedOut":false}}`,
		},
		{
			name: "code_0_success",
			body: `{"message":"success","code":0,"data":{"exitCode":0,"stdout":"{\"structuredResult\":{\"metrics\":{\"total\":1}},\"artifacts\":[]}","stderr":"","timedOut":false}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			oldCfg := conf.API.PythonExec
			conf.API.PythonExec = base.ApiClient{
				Domain:  server.URL,
				Timeout: "3s",
			}
			defer func() {
				conf.API.PythonExec = oldCfg
			}()

			resp, err := Execute(context.Background(), &ExecuteRequest{
				LogID:  "log-1",
				Python: "print('ok')",
				Data:   "{}",
			})
			if err != nil {
				t.Fatalf("Execute returned err=%v", err)
			}
			if resp.ExitCode != 0 || resp.TimedOut {
				t.Fatalf("unexpected response: %#v", resp)
			}
			if resp.Stdout == "" {
				t.Fatalf("expected stdout to be populated")
			}
		})
	}
}

// TestExecuteDoesNotForwardCookies 锁定 P0-3：沙箱不需要用户凭证，客户端一律不透传 Cookie。
// 上游请求即便带着 Cookie，也必须只留在引擎侧——请求头无 Cookie、请求体无 cookies 字段。
// 这条断言的价值在于防止后续有人"顺手"把凭证透传加回来。
func TestExecuteDoesNotForwardCookies(t *testing.T) {
	var gotCookieHeader string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookieHeader = r.Header.Get("Cookie")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"success","code":0,"data":{"exitCode":0,"stdout":"ok","stderr":"","timedOut":false}}`))
	}))
	defer server.Close()

	oldCfg := conf.API.PythonExec
	conf.API.PythonExec = base.ApiClient{Domain: server.URL, Timeout: "3s"}
	defer func() { conf.API.PythonExec = oldCfg }()

	// 模拟"上游请求带 Cookie"的真实上下文：修复前它会经 ExecuteRequest.Cookies 流向沙箱
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, pathExecute, nil)
	ginCtx.Request.Header.Set("Cookie", "sid=super-secret")
	ginCtx.Set("logID", "log-1")

	if _, err := Execute(ginCtx, &ExecuteRequest{LogID: "log-1", Python: "print('ok')", Data: "{}"}); err != nil {
		t.Fatalf("Execute returned err=%v", err)
	}
	if gotCookieHeader != "" {
		t.Fatalf("沙箱请求不得携带 Cookie 头，实际收到: %q", gotCookieHeader)
	}
	if strings.Contains(string(gotBody), "cookies") {
		t.Fatalf("沙箱请求体不得包含 cookies 字段，实际: %s", string(gotBody))
	}
}
