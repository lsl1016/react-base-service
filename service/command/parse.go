package command

import (
	"regexp"
	"strings"
)

// commandNamePattern 命令名合法性（与自定义命令创建校验同源）。
var commandNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// SplitCommand 词法拆分：userPrompt 以 "/" 开头且首 token 是合法命令名时返回 (name, args, true)。
// 只认首 token（与 ZCode filterSlashCommands 同口径），消息中间出现的 / 路径不受影响。
// 不合格的 / 开头短词（含大写、超长、非法字符）返回 false——按普通 prompt 处理。
func SplitCommand(userPrompt string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(userPrompt)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	token := trimmed[1:]
	if idx := strings.IndexAny(token, " \t\n"); idx >= 0 {
		args = strings.TrimSpace(token[idx+1:])
		token = token[:idx]
	}
	if !commandNamePattern.MatchString(token) {
		return "", "", false
	}
	return token, args, true
}

// SplitArgs 引号感知的参数切分（对齐 ZCode splitCustomCommandArguments 语义）：
// 空白分隔，双引号内的空白不切分；未闭合引号按剩余整体处理。
func SplitArgs(args string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	flush := func() {
		if current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		}
	}
	for _, r := range args {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t' || r == '\n'):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return parts
}
