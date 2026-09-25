// Package extractor 实现长期记忆 V2 的自动沉淀前半程：从压缩对话中抽取候选记忆并做确定性预过滤。
// 冲突消解见 service/memory/resolver；触发与编排见 service/react/memory_extractor.go。
package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LLMInvoker 抽象一次「system+user → 纯文本」的非工具模型调用；
// 由触发方适配 api/llm 的流式客户端，extractor/resolver 不感知传输细节（单测用 mock 注入）。
type LLMInvoker interface {
	ChatOnce(ctx context.Context, system, user string) (string, error)
}

// ChatTimeout 是单次模型调用（抽取/消解）的超时：转录可达 16k 字符，需长于 compact 摘要的预算。
const ChatTimeout = 120 * time.Second

// ExtractJSON 从模型输出中鲁棒提取一个 JSON 对象：先直接校验，失败后剥 code fence，
// 再失败取首尾大括号之间的子串。全部失败返回 error——与「抽取为空」严格区分（mem0 经验：
// 不能把 LLM 故障静默当成"没有可沉淀内容"）。
func ExtractJSON(text string) (string, error) {
	trimmed := strings.TrimSpace(stripCodeFence(strings.TrimSpace(text)))
	if trimmed == "" {
		return "", fmt.Errorf("模型输出为空")
	}
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") && json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		candidate := trimmed[start : end+1]
		if json.Valid([]byte(candidate)) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("模型输出中未找到合法 JSON 对象")
}

// stripCodeFence 剥掉 markdown code fence（```json ... ```）。
func stripCodeFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	if idx := strings.Index(text, "\n"); idx >= 0 {
		text = text[idx+1:]
	}
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	return strings.TrimSpace(text)
}

// TruncateRunes 按 rune 截断字符串，超限追加省略号；limit<=0 原样返回。
// 仅用于展示预览；落库字段的裁剪必须用 TruncateRunesStrict（省略号会突破长度上限）。
func TruncateRunes(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// TruncateRunesStrict 按 rune 严格截断到 limit（含），不加任何后缀；写核心字段长度约束用。
func TruncateRunesStrict(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
