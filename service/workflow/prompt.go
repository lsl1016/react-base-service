package workflow

import (
	"strings"
	"time"

	model "react-base-service/models/llm"
)

// renderWorkflowPrompt 渲染无人值守 run 的用户提示词：占位符替换 + 无人值守纪律前缀。
// 纪律前缀与 scheduled 执行档案（禁 client 工具/ask_question）双重设防，档案是硬闸门，
// 提示词让模型主动规避无意义的等待类尝试（方案 §5.2 第 2 点）。
func renderWorkflowPrompt(wf *model.Workflow) string {
	var sb strings.Builder
	sb.WriteString("<scheduled_unattended_run>\n")
	sb.WriteString("本任务由定时调度器发起，为无人值守运行，没有用户在前端等待：\n")
	sb.WriteString("- 禁止调用 ask_question 或任何需要人工确认/回填的工具（没有人在应答，等待只会耗尽运行时限）。\n")
	sb.WriteString("- 信息不足时基于现有信息继续推进，并在结论中说明假设与未验证项。\n")
	sb.WriteString("- 不要提交需要人工接续的后台异步任务。\n")
	sb.WriteString("- 完成后输出一段结论性总结（做了什么 / 发现了什么 / 风险与建议）；该总结将作为本次运行结论用于归档与通知。\n")
	sb.WriteString("</scheduled_unattended_run>\n\n")
	sb.WriteString(replaceWorkflowPlaceholders(wf.Prompt, wf))
	return sb.String()
}

// replaceWorkflowPlaceholders 替换任务提示词中的占位符：
// {{date}}=当日日期、{{datetime}}=当前时刻、{{workflow}}=workflow 名称。
func replaceWorkflowPlaceholders(prompt string, wf *model.Workflow) string {
	now := time.Now()
	replacements := map[string]string{
		"{{date}}":     now.Format("2006-01-02"),
		"{{datetime}}": now.Format("2006-01-02 15:04:05"),
		"{{workflow}}": wf.Name,
	}
	for placeholder, value := range replacements {
		prompt = strings.ReplaceAll(prompt, placeholder, value)
	}
	return prompt
}
