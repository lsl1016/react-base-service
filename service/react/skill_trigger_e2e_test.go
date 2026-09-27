package react

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/env"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	skillService "react-base-service/service/skill"
)

// e2eSkillTriggerMarkdown 是 triggers 探针技能：关键词刻意选不常见词，避免污染其它会话。
const e2eSkillTriggerMarkdown = `---
name: e2e触发器探针技能
description: e2e 验证 triggers 关键词命中注入，与业务无关
triggers:
  - 嗡嗡锤探针词
---
这是 e2e 探针技能的正文。无论收到什么任务，都直接回复「探针技能已加载」，不要调用任何工具。`

// TestSkillTriggerE2E 端到端验证 P2-2 关键词触发器（真实 MySQL + 真实模型调用）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/ -run TestSkillTriggerE2E -v -timeout 300s
//
// 前置：react-base-mysql 容器已对 tblLlmSkill 加 triggers_json/content 列；caller=demo-app 已注册。
// 验证点：导入带 triggers 的 SKILL.md 后——
//   - 命中路径：用户消息含关键词时，落库的 modelMessage 追加了 skill-trigger-hint 块，
//     且用户原文（content 字段，前端历史展示用）不受污染；
//   - 未命中路径：无关消息的 modelMessage 无 hint。
func TestSkillTriggerE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e (requires react-base-mysql + model api)")
	}

	env.SetRootPath("../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()

	ctx := newHeadlessGinContext("e2e-skill-trigger")
	imported, err := skillService.ImportFromMarkdown(ctx, &params.ImportSkillReq{
		CallerKey: "demo-app",
		Markdown:  e2eSkillTriggerMarkdown,
	}, "e2e")
	if err != nil {
		t.Fatalf("导入探针技能失败: %v", err)
	}
	t.Cleanup(func() {
		if err := model.SoftDeleteSkillBySkillID(ctx, imported.SkillID); err != nil {
			t.Logf("清理探针技能失败（不影响断言）: %v", err)
		}
	})

	runAndCollect := func(prompt string) (string, string) {
		t.Helper()
		var events []params.ReactEvent
		result, err := RunWithClientReaderContext(ctx, ctx.Request.Context(), params.ReactRunPayload{
			CallerKey:   "demo-app",
			RouteValues: []string{},
			Type:        model.ReactSessionTypeChat,
			UserPrompt:  prompt,
			ModelKey:    e2eModelKey(),
			MaxSteps:    3,
		}, "", func(event params.ReactEvent) error {
			events = append(events, event)
			return nil
		}, nil)
		if err != nil {
			t.Fatalf("run 失败（prompt=%s）: %v", prompt, err)
		}
		messages, err := model.GetReactMessagesByRunID(ctx, result.RunID)
		if err != nil {
			t.Fatalf("查询 run 消息失败: %v", err)
		}
		for i := range messages {
			if messages[i].MessageType != model.ReactMessageTypeUserInput {
				continue
			}
			var stored struct {
				Content      string `json:"content"`
				ModelMessage struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"modelMessage"`
			}
			if err := json.Unmarshal([]byte(messages[i].ContentJSON), &stored); err != nil {
				t.Fatalf("解析用户消息 content_json 失败: %v", err)
			}
			return stored.Content, stored.ModelMessage.Content
		}
		t.Fatal("未找到 react_user_input 消息")
		return "", ""
	}

	// 命中路径：关键词出现，modelMessage 应带 hint 块，用户原文保持干净。
	userContent, modelContent := runAndCollect("嗡嗡锤探针词：请直接回答 ok，不要调用任何工具。")
	if !strings.Contains(modelContent, "<skill-trigger-hint>") {
		t.Fatalf("命中路径 modelMessage 应包含 skill-trigger-hint 块:\n%s", modelContent)
	}
	for _, want := range []string{"e2e触发器探针技能", "命中关键词: 嗡嗡锤探针词", "get_skill"} {
		if !strings.Contains(modelContent, want) {
			t.Fatalf("命中路径 modelMessage 应包含 %q:\n%s", want, modelContent)
		}
	}
	if strings.Contains(userContent, "skill-trigger-hint") {
		t.Fatalf("用户原文（前端历史展示）不应被 hint 污染:\n%s", userContent)
	}

	// 未命中路径：无关消息不应注入 hint。
	_, modelContent = runAndCollect("请直接回答 1+1 等于几，不要调用任何工具。")
	if strings.Contains(modelContent, "<skill-trigger-hint>") {
		t.Fatalf("未命中路径 modelMessage 不应包含 hint 块:\n%s", modelContent)
	}
}
