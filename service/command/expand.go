package command

import (
	"fmt"
	"regexp"
	"strings"
)

// positionalArgPattern 匹配模板里的 $1/$2… 位置参数占位符。
var positionalArgPattern = regexp.MustCompile(`\$(\d+)`)

// workflowCommandTemplate /workflow 的展开模板（内置展开型命令）。
// 语义对齐本项目 workflow 基座：cron/manual 触发的无人值守整段 ReAct run（非步骤编排）。
const workflowCommandTemplate = `为以下任务创建并启动一个无人值守 workflow（定时或手动触发的完整自动执行任务）：
$ARGUMENTS

请先用 ask_question 工具向用户确认关键参数（触发方式 cron/manual、cron 表达式、时区、超时、
步数上限、通知 webhook 是否需要），确认后调用 create_workflow 工具提交定义。
create_workflow 成功会返回 workflow_key；如用户要求立即执行，再调用 dispatch_workflow。`

func expandWorkflow(args string) string {
	return strings.ReplaceAll(workflowCommandTemplate, "$ARGUMENTS", args)
}

// ExpandTemplate 展开自定义命令模板（对齐 ZCode 契约层实现）：
//   - $ARGUMENTS 替换为参数原文；$1/$2… 替换为引号感知切分后的位置参数；
//   - 模板未使用任何占位符且参数非空时，参数以「User arguments:」块追加尾部；
//   - 展开后加包装头，防止模板首行被当成普通聊天。
//
// 明确不支持 shell 动态展开（!`cmd` 语法）——服务端基座不开执行面。
func ExpandTemplate(name, template, args string) string {
	body := template
	usedPlaceholder := strings.Contains(body, "$ARGUMENTS") || positionalArgPattern.MatchString(body)
	if strings.Contains(body, "$ARGUMENTS") {
		body = strings.ReplaceAll(body, "$ARGUMENTS", args)
	}
	positional := SplitArgs(args)
	body = positionalArgPattern.ReplaceAllStringFunc(body, func(match string) string {
		index := 0
		if _, err := fmt.Sscanf(match[1:], "%d", &index); err != nil || index < 1 || index > len(positional) {
			return ""
		}
		return positional[index-1]
	})
	if args != "" && !usedPlaceholder {
		body = strings.TrimRight(body, "\n") + "\n\nUser arguments:\n" + args
	}
	return fmt.Sprintf("执行自定义命令 /%s，按以下指令处理：\n\n%s", name, body)
}
