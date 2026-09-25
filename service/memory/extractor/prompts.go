package extractor

import (
	"fmt"
	"strings"
)

// ExtractionSystemPrompt 抽取调用的 system 消息：强制纯 JSON 输出。
const ExtractionSystemPrompt = "你是长期记忆维护流水线的记忆抽取器。你只输出一个 JSON 对象，不输出解释、markdown 代码块或其他任何文本。"

// buildExtractionPrompt 组装抽取 user prompt：类型定义（when to save / what not to save）+
// 事实纪律（借鉴 mem0/ZCode）+ 素材（摘要/转录/现有清单/观察日期）+ 输出 JSON schema。
func buildExtractionPrompt(input ExtractInput) string {
	maxCandidates := input.MaxCandidates
	if maxCandidates <= 0 {
		maxCandidates = 8
	}
	var sb strings.Builder
	sb.WriteString("<memory_extraction_task>\n")
	sb.WriteString("分析下方「压缩摘要」与「近期对话原文」，判断有哪些值得写入长期记忆的内容，按「输出格式」返回 JSON。\n\n")

	sb.WriteString("## 记忆类型定义\n")
	sb.WriteString("- preference：用户明确表达过的稳定偏好（称呼、语言、输出格式、工作方式）。只有用户亲口表述才算，不得从一次性行为推断。\n")
	sb.WriteString("- fact：关于用户、项目或环境的稳定事实（负责什么项目、系统包含什么模块、技术栈是什么）。\n")
	sb.WriteString("- event：带时间的关键决定或经历（确定采用某方案、完成某里程碑），正文中用「截至 YYYY-MM-DD」标注绝对日期。\n")
	sb.WriteString("- procedure：验证有效的可复用工作方法（处理某类问题的固定步骤）。一次性的任务计划不属于此类。\n\n")

	sb.WriteString("## 不要保存（What NOT to save）\n")
	sb.WriteString("- 一次性任务上下文：本次要改哪个文件、当前报错、临时排障过程。\n")
	sb.WriteString("- 可以从代码、文档、git 历史或系统提示中推导的内容。\n")
	sb.WriteString("- 助手对用户话语的复述（回声内容不得重复抽取）。\n")
	sb.WriteString("- 凭证、密钥、证件号、手机号等敏感信息（写入口径会直接拒绝，写出即失败）。\n\n")

	sb.WriteString("## 事实纪律\n")
	sb.WriteString("- 每条候选是自包含的原子事实，一到三句；保留具体细节（名称、版本、数字、路径），不得泛化。\n")
	sb.WriteString("- 状态变化写明「从什么变成什么」及原因（如：从 X 迁移到 Y，原因是 Z）。\n")
	sb.WriteString("- 相对时间（昨天、上周）转为绝对日期，以「观察日期」为基准。\n")
	sb.WriteString("- 用对话中用户的语言记录。\n")
	sb.WriteString("- 语义与「现有记忆清单」中某条等价且无新信息的，不要再输出该候选。\n")
	sb.WriteString("- 候选与现有记忆相关（同主题演进/矛盾/延续）时，在 similarIds 里填清单中的条目 id；没有相关条目就留空数组。\n")
	fmt.Fprintf(&sb, "- 拿不准就不输出；没有值得沉淀的内容时输出 {\"candidates\": []}。最多输出 %d 条候选。\n\n", maxCandidates)

	if summary := strings.TrimSpace(input.Summary); summary != "" {
		sb.WriteString("## 压缩摘要\n")
		sb.WriteString(summary)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## 近期对话原文（可能截断）\n")
	sb.WriteString(input.Transcript)
	sb.WriteString("\n\n")
	sb.WriteString("## 现有记忆清单\n")
	sb.WriteString(input.Manifest)
	sb.WriteString("\n\n")
	sb.WriteString("## 观察日期\n")
	sb.WriteString(strings.TrimSpace(input.CurrentDate))
	sb.WriteString("\n\n")

	sb.WriteString("## 输出格式\n")
	sb.WriteString(`{"candidates": [{"type": "preference|fact|event|procedure", "title": "≤32字", "content": "一到三句自包含事实", "description": "什么场景需要想起这条", "tags": ["标签"], "confidence": 0.0, "importance": 3, "similarIds": [12], "reason": "为什么值得沉淀"}]}`)
	sb.WriteString("\n</memory_extraction_task>")
	return sb.String()
}
