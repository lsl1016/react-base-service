package llm

import "testing"

func TestChatCompletionsURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		{"空串", "", ""},
		{"裸域名拼完整路径", "https://api.openai.com", "https://api.openai.com/v1/chat/completions"},
		{"尾斜杠剥除", "https://api.openai.com/", "https://api.openai.com/v1/chat/completions"},
		{"首尾空白剥除", "  https://api.openai.com  ", "https://api.openai.com/v1/chat/completions"},
		{"v1 结尾只拼协议路径", "https://api.openai.com/v1", "https://api.openai.com/v1/chat/completions"},
		{"智谱 coding v4 结尾", "https://open.bigmodel.cn/api/coding/paas/v4", "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"},
		{"v4 带尾斜杠", "https://open.bigmodel.cn/api/coding/paas/v4/", "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"},
		{"完整 endpoint 原样返回", "https://gw.example.com/v1/chat/completions", "https://gw.example.com/v1/chat/completions"},
		{"api.yaml openproxy 兼容旧拼接", "https://openproxy.example.com/openproxy/rp", "https://openproxy.example.com/openproxy/rp/v1/chat/completions"},
		{"/api 结尾", "https://api.example.com/api", "https://api.example.com/api/chat/completions"},
		{"v4beta 结尾", "https://generativelanguage.googleapis.com/v4beta", "https://generativelanguage.googleapis.com/v4beta/chat/completions"},
		{"大小写不敏感的完整 endpoint", "https://gw.example.com/V1/Chat/Completions", "https://gw.example.com/V1/Chat/Completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChatCompletionsURL(tc.base); got != tc.want {
				t.Fatalf("ChatCompletionsURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

func TestMessagesURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		{"空串", "", ""},
		{"裸域名拼完整路径", "https://api.anthropic.com", "https://api.anthropic.com/v1/messages"},
		{"智谱 anthropic 兼容旧拼接", "https://open.bigmodel.cn/api/anthropic", "https://open.bigmodel.cn/api/anthropic/v1/messages"},
		{"deepseek anthropic 兼容旧拼接", "https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic/v1/messages"},
		{"v1 结尾只拼协议路径", "https://api.anthropic.com/v1", "https://api.anthropic.com/v1/messages"},
		{"已含完整路径原样返回", "https://x.example.com/v1/messages", "https://x.example.com/v1/messages"},
		{"/messages 结尾原样返回", "https://x.example.com/messages", "https://x.example.com/messages"},
		{"尾斜杠剥除", "https://api.anthropic.com/", "https://api.anthropic.com/v1/messages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessagesURL(tc.base); got != tc.want {
				t.Fatalf("MessagesURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

func TestModelsURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		{"空串", "", ""},
		{"裸域名", "https://api.openai.com", "https://api.openai.com/v1/models"},
		{"智谱 coding v4", "https://open.bigmodel.cn/api/coding/paas/v4", "https://open.bigmodel.cn/api/coding/paas/v4/models"},
		{"完整 endpoint 贴入", "https://gw.example.com/v1/chat/completions", "https://gw.example.com/v1/models"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelsURL(tc.base); got != tc.want {
				t.Fatalf("ModelsURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}
