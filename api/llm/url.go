package llm

import (
	"regexp"
	"strings"
)

// URL 归一化：用户贴进「接入地址」的 base url 形态千差万别——有的带 /v1、/v4、
// /api 版本段，有的直接贴完整 endpoint，有的带尾斜杠。client 侧不再做朴素的
// base + 固定后缀 拼接，统一经此处归一，保证「贴什么都能通」：
//   - 已含最终路径（/chat/completions、/v1/messages、/messages）→ 原样返回；
//   - 已以版本段结尾（/v1、/v4、/v4beta、/api…）→ 只拼协议路径；
//   - 其余 → 拼完整 /v1/... 路径。
// 兼容性约束：api.yaml 现有端点（如 https://open.bigmodel.cn/api/anthropic、
// https://openproxy.example.com/openproxy/rp）归一结果必须与旧的朴素拼接一致。

var versionSegmentPattern = regexp.MustCompile(`(?i)/(v\d+[a-z]*|api)$`)

func trimBaseURL(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

// hasSuffixPath 判断 base 是否已经以 path 结尾（大小写不敏感）。
func hasSuffixPath(base, path string) bool {
	return strings.HasSuffix(strings.ToLower(base), path)
}

// ChatCompletionsURL 归一 OpenAI 兼容协议的 chat completions endpoint。
func ChatCompletionsURL(base string) string {
	base = trimBaseURL(base)
	if base == "" {
		return ""
	}
	if hasSuffixPath(base, "/chat/completions") {
		return base
	}
	if versionSegmentPattern.MatchString(base) {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// MessagesURL 归一 Anthropic 兼容协议的 messages endpoint。
func MessagesURL(base string) string {
	base = trimBaseURL(base)
	if base == "" {
		return ""
	}
	if hasSuffixPath(base, "/messages") {
		return base
	}
	if versionSegmentPattern.MatchString(base) {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

// ModelsURL 归一模型发现接口（GET /models）的 endpoint。
// 两个协议的 /models 与 chat 端点同目录层级，直接复用 chat 路径归一后剥掉协议后缀。
func ModelsURL(base string) string {
	chat := ChatCompletionsURL(base)
	if chat == "" {
		return ""
	}
	return strings.TrimSuffix(chat, "/chat/completions") + "/models"
}
