package core

import (
	"encoding/json"
	"strings"

	model "react-base-service/models/llm"
)

// P2-2 Skill 关键词触发器：run 装配期对用户消息做确定性匹配，
// 命中时把 skill 提示追加到该条用户消息（modelUserMessage，随 persistUserInput 落库，
// 后续轮次上下文回放一致；前端历史展示只读用户原文不受影响）。
// 无 triggers 的 skill 不参与匹配，行为保持「仅摘要索引」。

// SkillTriggerMatchLimit 是单条用户消息最多注入的命中 skill 数，防止宽泛关键词刷屏。
const SkillTriggerMatchLimit = 5

type SkillTriggerMatch struct {
	Skill      model.Skill
	HitKeyword string
}

// MatchSkillTriggers 对用户消息按 skills 的 triggers_json 做子串匹配。
// ASCII 大小写不敏感，CJK 精确子串；按 skills 传入顺序取前 SkillTriggerMatchLimit 个命中项。
func MatchSkillTriggers(userPrompt string, skills []model.Skill) []SkillTriggerMatch {
	prompt := strings.ToLower(strings.TrimSpace(userPrompt))
	if prompt == "" {
		return nil
	}
	var matches []SkillTriggerMatch
	for i := range skills {
		triggers := ParseSkillTriggersJSON(skills[i].TriggersJSON)
		if len(triggers) == 0 {
			continue
		}
		for _, trigger := range triggers {
			if strings.Contains(prompt, strings.ToLower(trigger)) {
				matches = append(matches, SkillTriggerMatch{Skill: skills[i], HitKeyword: trigger})
				break
			}
		}
		if len(matches) >= SkillTriggerMatchLimit {
			break
		}
	}
	return matches
}

// RenderSkillTriggerHint 渲染追加到用户消息末尾的命中提示块；
// 与 renderSkillIndexSummary 同款约定：摘要仅用于判断，执行前必须 get_skill 拿完整说明。
func RenderSkillTriggerHint(matches []SkillTriggerMatch) string {
	if len(matches) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<skill-trigger-hint>\n")
	builder.WriteString("以下 Skill 的触发关键词命中了本轮用户输入；如适合当前任务，请调用 get_skill 获取完整说明后执行，不要仅凭以下摘要执行：\n")
	for _, match := range matches {
		builder.WriteString("- skillId: ")
		builder.WriteString(match.Skill.SkillID)
		builder.WriteString("\n  name: ")
		builder.WriteString(match.Skill.Name)
		if description := strings.TrimSpace(match.Skill.Description); description != "" {
			builder.WriteString("\n  description: ")
			builder.WriteString(description)
		}
		builder.WriteString("\n  命中关键词: ")
		builder.WriteString(match.HitKeyword)
		builder.WriteString("\n")
	}
	builder.WriteString("</skill-trigger-hint>")
	return builder.String()
}

// ParseSkillTriggersJSON 解析 skill 的 triggers_json 字段，非法 JSON 静默返回 nil。
func ParseSkillTriggersJSON(triggersJSON string) []string {
	triggersJSON = strings.TrimSpace(triggersJSON)
	if triggersJSON == "" {
		return nil
	}
	var triggers []string
	if err := json.Unmarshal([]byte(triggersJSON), &triggers); err != nil {
		return nil
	}
	return triggers
}
