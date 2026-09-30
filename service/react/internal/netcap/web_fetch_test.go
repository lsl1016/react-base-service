package netcap

import (
	"strings"
	"testing"
)

// TestIsPrivateHost：web_fetch 的内网/环回字面量拒绝。
func TestIsPrivateHost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.9", "169.254.1.1", "0.0.0.0", "::1"} {
		if !isPrivateHost(host) {
			t.Fatalf("%s 应判为内网/环回", host)
		}
	}
	for _, host := range []string{"example.com", "8.8.8.8", "docs.example.org"} {
		if isPrivateHost(host) {
			t.Fatalf("%s 不应判为内网", host)
		}
	}
}

// TestWebFetchContentExtraction：HTML 去脚本/样式/标签与实体解码；文本类原样返回。
func TestWebFetchContentExtraction(t *testing.T) {
	html := `<html><head><style>body{color:red}</style><script>var x=1;</script></head>` +
		`<body><h1>标题</h1><p>第一段&amp;符号</p><p>第二段</p><!-- 注释 --></body></html>`
	got := extractWebFetchContent("text/html; charset=utf-8", html)
	for _, banned := range []string{"var x=1", "color:red", "<p>", "注释"} {
		if strings.Contains(got, banned) {
			t.Fatalf("提取结果不应包含 %q: %q", banned, got)
		}
	}
	for _, want := range []string{"标题", "第一段&符号", "第二段"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提取结果缺少 %q: %q", want, got)
		}
	}
	if plain := extractWebFetchContent("application/json", `{"k":1}`); plain != `{"k":1}` {
		t.Fatalf("JSON 应原样返回: %q", plain)
	}
}
