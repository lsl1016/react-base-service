package llmmodel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// strOrExisting 是连接更新防重的终值推导：请求非空取请求值、否则保持库中现值。
// 防重键（app+路由+接入地址）任一变化时按终值查重，推导错误会漏拦或误拦。
func TestStrOrExisting(t *testing.T) {
	cases := []struct {
		name      string
		requested interface{}
		existing  interface{}
		want      string
	}{
		{"请求非空取请求值", "  https://new.example.com ", "https://old.example.com", "https://new.example.com"},
		{"请求空串保持现值", "", "https://old.example.com", "https://old.example.com"},
		{"请求空白保持现值", "   ", "[]", "[]"},
		{"请求缺失(nil)保持现值", nil, "app-a", "app-a"},
		{"请求缺失(map 无该键)保持现值", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updates := map[string]interface{}{}
			if tc.requested != nil {
				updates["field"] = tc.requested
			}
			if got := strOrExisting(updates["field"], tc.existing); got != tc.want {
				t.Fatalf("strOrExisting(%v, %v) = %q, want %q", tc.requested, tc.existing, got, tc.want)
			}
		})
	}
}

// 模拟 DeepSeek 形态：Anthropic 兼容面挂子路径（/anthropic 下无模型发现接口，404），
// 模型发现仅在 host 根 /v1/models 提供；首选失败应回退命中，且结果去重排序。
func TestFetchUpstreamModelsFallbackToHostRoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-pro"},{"id":"deepseek-flash"},{"id":"deepseek-flash"}]}`))
	}))
	defer srv.Close()

	models, err := fetchUpstreamModels(srv.URL+"/anthropic", "anthropic", "test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"deepseek-flash", "deepseek-v4-pro"}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Fatalf("models = %v, want %v", models, want)
	}
}

// 首选同层级 /models 可达（OpenAI 面 / 根路径形态）时不走回退。
func TestFetchUpstreamModelsPrimaryHit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-4.6"}]}`))
	}))
	defer srv.Close()

	models, err := fetchUpstreamModels(srv.URL, "openai", "test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0] != "glm-4.6" {
		t.Fatalf("models = %v, want [glm-4.6]", models)
	}
}

// 首选与回退都失败时报错，错误信息应携带首选 URL 与状态码。
func TestFetchUpstreamModelsAllFailCarriesPrimaryURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := fetchUpstreamModels(srv.URL+"/anthropic", "anthropic", "test-key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "/anthropic/v1/models") {
		t.Fatalf("error should carry status and primary URL, got: %v", err)
	}
}
