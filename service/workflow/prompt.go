package workflow

import (
	"fmt"
	"strings"
	"time"

	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// renderWorkflowPrompt 渲染无人值守 run 的用户提示词：占位符替换 + 无人值守纪律前缀
//（+ 报告产物要求，report.prompt_enabled 时）。纪律前缀与 scheduled 执行档案（禁 client
// 工具/ask_question）双重设防，档案是硬闸门，提示词让模型主动规避无意义的等待类尝试。
func renderWorkflowPrompt(wf *model.Workflow) string {
	var sb strings.Builder
	sb.WriteString("<scheduled_unattended_run>\n")
	sb.WriteString("本任务由定时调度器发起，为无人值守运行，没有用户在前端等待：\n")
	sb.WriteString("- 禁止调用 ask_question 或任何需要人工确认/回填的工具（没有人在应答，等待只会耗尽运行时限）。\n")
	sb.WriteString("- 信息不足时基于现有信息继续推进，并在结论中说明假设与未验证项。\n")
	sb.WriteString("- 不要提交需要人工接续的后台异步任务。\n")
	sb.WriteString("- 完成后输出一段结论性总结（做了什么 / 发现了什么 / 风险与建议）；该总结将作为本次运行结论用于归档与通知。\n")
	if conf.CustomConf.LLM.Workflow.Report.PromptEnabled {
		sb.WriteString(renderReportDiscipline())
	}
	sb.WriteString("</scheduled_unattended_run>\n\n")
	sb.WriteString(replaceWorkflowPlaceholders(wf.Prompt, wf))
	return sb.String()
}

// renderReportDiscipline 渲染报告产物化要求段（二期）：完整报告落 markdown 产物，
// 经 python_exec 保存后走 COS artifact 链路，通知附带下载链接。
func renderReportDiscipline() string {
	fileName := "report-" + time.Now().Format("20060102-150405") + ".md"
	return fmt.Sprintf(
		"- 任务收尾时，把完整报告整理为 markdown 文件并用 python_exec 保存（文件名 %s）："+
			"报告包含检查项清单、各项结果、发现的风险（按级别排列）与处理建议。"+
			"报告正文与最终总结可以一致，总结里说明报告文件名即可。\n", fileName)
}

// renderJudgeFollowUpPrompt 渲染裁判外环的追问消息（二期）：注入原会话驱动续跑，
// 强调在原有进度上继续而非重头再来。
func renderJudgeFollowUpPrompt(followUp string, round int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<scheduled_judge_followup round=\"%d\">\n", round)
	sb.WriteString("无人值守质量复审认为任务目标尚未完全达成，请继续完成：\n")
	sb.WriteString("- 裁判意见：" + strings.TrimSpace(followUp) + "\n")
	sb.WriteString("- 在本次会话已有进度的基础上继续（不要重头再来、不要重复已完成的检查）。\n")
	sb.WriteString("- 完成后重新给出完整结论总结；若报告文件已产出，更新它。\n")
	sb.WriteString("</scheduled_judge_followup>")
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
