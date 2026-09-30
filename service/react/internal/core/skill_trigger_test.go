package core

import (
	"strings"
	"testing"

	model "react-base-service/models/llm"
)

func triggerSkill(skillID, name string, triggers ...string) model.Skill {
	return model.Skill{SkillID: skillID, Name: name, TriggersJSON: `["` + strings.Join(triggers, `","`) + `"]`}
}

func TestMatchSkillTriggersHitAndMiss(t *testing.T) {
	skills := []model.Skill{
		triggerSkill("skill_report", "日报生成", "日报", "daily report"),
		{SkillID: "skill_plain", Name: "无触发器"}, // 无 triggers：不参与匹配
		triggerSkill("skill_exch", "汇率查询", "汇率"),
	}

	matches := MatchSkillTriggers("帮我查一下美元汇率", skills)
	if len(matches) != 1 || matches[0].Skill.SkillID != "skill_exch" || matches[0].HitKeyword != "汇率" {
		t.Fatalf("汇率命中结果不符合预期: %+v", matches)
	}

	matches = MatchSkillTriggers("今天天气如何", skills)
	if len(matches) != 0 {
		t.Fatalf("未命中应返回空: %+v", matches)
	}

	matches = MatchSkillTriggers("  Daily Report 请生成 ", skills)
	if len(matches) != 1 || matches[0].Skill.SkillID != "skill_report" {
		t.Fatalf("ASCII 大小写不敏感命中失败: %+v", matches)
	}

	matches = MatchSkillTriggers("", skills)
	if len(matches) != 0 {
		t.Fatalf("空 prompt 应返回空: %+v", matches)
	}
}

func TestMatchSkillTriggersLimitAndOrder(t *testing.T) {
	skills := make([]model.Skill, 0, SkillTriggerMatchLimit+2)
	for i := 0; i < SkillTriggerMatchLimit+2; i++ {
		skills = append(skills, triggerSkill(
			"skill_"+strings.Repeat("a", i+1), "skill"+strings.Repeat("b", i+1), "公共关键词"))
	}
	matches := MatchSkillTriggers("包含公共关键词的消息", skills)
	if len(matches) != SkillTriggerMatchLimit {
		t.Fatalf("命中数应截断到上限 %d: %d", SkillTriggerMatchLimit, len(matches))
	}
	if matches[0].Skill.SkillID != skills[0].SkillID {
		t.Fatalf("应按传入顺序取前 N 个: %+v", matches[0])
	}
}

func TestMatchSkillTriggersMalformedJSON(t *testing.T) {
	skills := []model.Skill{{SkillID: "skill_bad", Name: "坏数据", TriggersJSON: "{not-json"}}
	if matches := MatchSkillTriggers("任意消息", skills); len(matches) != 0 {
		t.Fatalf("非法 triggers_json 应静默跳过: %+v", matches)
	}
}

func TestRenderSkillTriggerHint(t *testing.T) {
	if got := RenderSkillTriggerHint(nil); got != "" {
		t.Fatalf("无命中应返回空串: %q", got)
	}

	matches := []SkillTriggerMatch{{
		Skill:      model.Skill{SkillID: "skill_x", Name: "日报生成", Description: "生成运营日报"},
		HitKeyword: "日报",
	}}
	got := RenderSkillTriggerHint(matches)
	for _, want := range []string{
		"<skill-trigger-hint>", "</skill-trigger-hint>",
		"get_skill", "- skillId: skill_x", "name: 日报生成", "命中关键词: 日报",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("hint 缺少 %q: %s", want, got)
		}
	}
}

func TestParseSkillTriggersJSON(t *testing.T) {
	if got := ParseSkillTriggersJSON(""); got != nil {
		t.Fatalf("空串应返回 nil: %v", got)
	}
	if got := ParseSkillTriggersJSON(`["a","b"]`); len(got) != 2 || got[1] != "b" {
		t.Fatalf("正常解析结果不符合预期: %v", got)
	}
	if got := ParseSkillTriggersJSON(`not-json`); got != nil {
		t.Fatalf("非法 JSON 应返回 nil: %v", got)
	}
}
