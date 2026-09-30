package core

import (
	"strings"

	llm "react-base-service/api/llm"
)

// ObjectTool 快速构造 object 参数类型的工具定义，供内置工具声明复用。
// 自动并入通用 description 必填参数（内置工具协议约定：每次调用说明正在做什么）。
func ObjectTool(name, description string, properties map[string]interface{}) llm.ToolDefinition {
	mergedProperties := map[string]interface{}{
		"description": StringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
	}
	for key, value := range properties {
		mergedProperties[key] = value
	}
	return llm.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": mergedProperties,
			"required":   []string{"description"},
		},
	}
}

// StringSchema 构造字符串字段 schema，保持内置工具参数声明写法简洁。
func StringSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description}
}

// NumberSchema 构造数字字段 schema，用于 offset、limit 等分页参数声明。
func NumberSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "number", "description": description}
}

// TruncateRunes 去掉首尾空白后按 rune 截断，保证摘要字段不会破坏 UTF-8 内容。
func TruncateRunes(content string, limit int) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

// HeadRunes 直接取原始内容的前 limit 个 rune，不做 TrimSpace。
// 与 TruncateRunes 的区别：TruncateRunes 面向摘要展示（去空白、可读性优先），
// HeadRunes 面向可续读预览——保持与原始内容同一套 rune 坐标，使 read_tool_result
// 能从 len([]rune(preview)) 处无缝续读，避免首尾空白导致的偏移错位。
func HeadRunes(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runeCount := 0
	for byteIndex := range content {
		if runeCount == limit {
			return content[:byteIndex]
		}
		runeCount++
	}
	return content
}
