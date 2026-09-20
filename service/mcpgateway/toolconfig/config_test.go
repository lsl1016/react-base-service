package toolconfig

import "testing"

func TestNormalizeRequestURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"https 地址", "https://api.example.com/tool", false},
		{"http 地址", "http://api.example.com/tool", false},
		{"local 协议不支持", "local://code/search", true},
		{"带片段", "https://api.example.com/tool#frag", true},
		{"带用户信息", "https://user:pass@api.example.com/tool", true},
		{"ftp 协议", "ftp://api.example.com/tool", true},
		{"javascript 伪协议", "javascript:alert(1)", true},
		{"相对路径", "code/search", true},
		{"空字符串", "   ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRequestURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望 %q 被拒绝，实际返回 %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望 %q 通过校验，错误: %v", tc.in, err)
			}
			if got != tc.in {
				t.Fatalf("规范化应保留原地址，输入 %q，输出 %q", tc.in, got)
			}
		})
	}
}

func TestRequestConfigNormalize(t *testing.T) {
	config := RequestConfig{TimeoutMS: 500}
	if err := config.Normalize(); err != nil {
		t.Fatalf("默认 POST 配置应通过校验: %v", err)
	}
	if config.Method != "POST" {
		t.Fatalf("方法缺省应为 POST，实际 %q", config.Method)
	}
	if config.Headers["Content-Type"] != "application/json" || config.Headers["Accept"] != "application/json" {
		t.Fatalf("应自动补 JSON 头: %#v", config.Headers)
	}

	config = RequestConfig{Method: "get", TimeoutMS: 500}
	if err := config.Normalize(); err != nil {
		t.Fatalf("GET 配置应通过校验: %v", err)
	}
	if config.Method != "GET" {
		t.Fatalf("方法应大写归一，实际 %q", config.Method)
	}
	if _, exists := config.Headers["Content-Type"]; exists {
		t.Fatal("GET 不应自动补 Content-Type")
	}

	if err := (&RequestConfig{Method: "TRACE"}).Normalize(); err == nil {
		t.Fatal("白名单外方法应被拒绝")
	}
	if err := (&RequestConfig{TimeoutMS: 10}).Normalize(); err == nil {
		t.Fatal("低于下限的超时应被拒绝")
	}
}

func TestParseRequestConfigRejectsUnknownFields(t *testing.T) {
	if _, err := ParseRequestConfig(`{"method":"POST","timeout_ms":1000,"headers":{},"extra":1}`); err == nil {
		t.Fatal("未知字段应被拒绝")
	}
	if _, err := ParseRequestConfig(""); err == nil {
		t.Fatal("空配置应被拒绝")
	}
	if _, err := ParseRequestConfig(`{"method":"POST","timeout_ms":1000}`); err != nil {
		t.Fatalf("合法配置不应被拒绝: %v", err)
	}
}
