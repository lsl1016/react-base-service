package resolver

import (
	"fmt"
	"strings"

	model "react-base-service/models/llm"
	"react-base-service/service/memory/extractor"
)

// ResolutionSystemPrompt 消解调用的 system 消息：强制纯 JSON 输出。
const ResolutionSystemPrompt = "你是长期记忆的冲突消解器。你只输出一个 JSON 对象，不输出解释、markdown 代码块或其他任何文本。"

// buildResolutionPrompt 组装消解 user prompt：候选 C 编号 + 存量 M 编号（真实 itemID）+ 判定规则
// （借鉴 mem0 的四动作与 Graphiti 的反误杀规则）+ 输出 JSON schema。
func buildResolutionPrompt(candidates []extractor.Candidate, existing []model.MemoryItem) string {
	var sb strings.Builder
	sb.WriteString("<memory_resolution_task>\n")
	sb.WriteString("对下方每条「候选记忆」，结合「存量记忆」判断处置动作，按「输出格式」返回 JSON。\n\n")

	sb.WriteString("## 判定规则\n")
	sb.WriteString("1. skip：候选与某条存量语义等价且没有新信息。\n")
	sb.WriteString("2. update：语义等价但候选补充了新细节、信息量更大 → mergedContent 合并双方信息量，target 指向该存量条目。\n")
	sb.WriteString("3. supersede：候选与存量矛盾或取代它（同主体的状态变化，如偏好改变、方案更换、数据更新）→ mergedContent 为候选的新内容，target 指向被取代条目。\n")
	sb.WriteString("4. add：全新信息，或与任何存量都无关 → target 留空。\n")
	sb.WriteString("5. 数字、日期、限定词有差异的事实不得判为重复——这类差异恰恰是信息，应按 update/supersede/add 处理。\n")
	sb.WriteString("6. 拿不准 → add。\n")
	sb.WriteString("7. 每个候选必须恰好输出一条决策；update/supersede 必填 mergedContent；skip/update/supersede 必填 target。\n\n")

	sb.WriteString("## 候选记忆\n")
	for i, candidate := range candidates {
		fmt.Fprintf(&sb, "- C%d [%s|conf %.2f] %s :: %s\n",
			i+1, candidate.Type, candidate.Confidence, candidate.Title, candidate.Content)
	}
	sb.WriteString("\n## 存量记忆（target 填 M 后面的数字）\n")
	if len(existing) == 0 {
		sb.WriteString("（无）\n")
	}
	for _, item := range existing {
		fmt.Fprintf(&sb, "- M%d [%s] %s :: %s\n",
			item.ID, item.MemoryType, item.Title, extractor.TruncateRunes(strings.TrimSpace(item.Content), 120))
	}

	sb.WriteString("\n## 输出格式\n")
	sb.WriteString(`{"decisions": [{"candidate": "C1", "decision": "add|update|supersede|skip", "target": "M12", "mergedContent": "合并后的内容", "reason": "一句话依据"}]}`)
	sb.WriteString("\n</memory_resolution_task>")
	return sb.String()
}
