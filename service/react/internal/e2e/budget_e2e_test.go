package e2e

import (
	"os"
	react "react-base-service/service/react"
	"strings"
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/env"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	agentService "react-base-service/service/agent"
)

// e2eBudgetAgentMarkdown 是预算探针子 Agent：max_tokens_per_run=1（任意一轮真实模型调用
// 的 token 都必然超过 1），且被要求先调 get_tool——预算检查点在「还有后续工具轮次」时触发。
const e2eBudgetAgentMarkdown = `---
agent_key: e2e-budget-probe
name: 预算探针代理
description: |
  适用问题：e2e 预算超限验证专用
  不适用：一切真实任务
max_steps: 4
max_tokens_per_run: 1
tools: []
---
你是预算探针代理。收到任务后，必须先调用 get_tool 工具加载任意一个能力（如 list_tools 或 exchange_rate），然后再回答任务。不允许跳过工具调用直接回答。`

// TestSubAgentBudgetE2E 端到端验证 P3 子代理预算（真实 MySQL + 真实模型调用）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/internal/e2e/ -run TestSubAgentBudgetE2E -v -timeout 300s
//
// 验证点：max_tokens_per_run=1 的子 Agent 被委派后，首轮工具轮次触发预算检查——
// 子 run 终态 error 且 error_message 含「预算超限」；父 run 正常收尾，
// 委派工具结果（回填父循环的软错误）含「预算超限」。
func TestSubAgentBudgetE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e (requires react-base-mysql + model api)")
	}

	env.SetRootPath("../../../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()

	ctx := newHeadlessGinContext("e2e-budget")
	if existing, err := model.GetAgentByCallerAndAgentKey(ctx, "demo-app", "e2e-budget-probe"); err != nil {
		t.Fatalf("查询已有 agent 失败: %v", err)
	} else if existing != nil {
		if _, err := agentService.DeleteAgent(ctx, existing.AgentID); err != nil {
			t.Fatalf("清理旧 agent 失败: %v", err)
		}
	}
	enabled := 1
	imported, err := agentService.ImportFromMarkdown(ctx, &params.ImportAgentReq{
		CallerKey:   "demo-app",
		RouteValues: []string{},
		Status:      &enabled,
		Markdown:    e2eBudgetAgentMarkdown,
	}, "e2e-budget")
	if err != nil {
		t.Fatalf("导入预算探针 Agent 失败: %v", err)
	}
	if imported.MaxTokensPerRun != 1 {
		t.Fatalf("frontmatter max_tokens_per_run 应导入为 1: %d", imported.MaxTokensPerRun)
	}
	t.Cleanup(func() {
		if existing, err := model.GetAgentByCallerAndAgentKey(ctx, "demo-app", "e2e-budget-probe"); err == nil && existing != nil {
			_, _ = agentService.DeleteAgent(ctx, existing.AgentID)
		}
	})

	payload := params.ReactRunPayload{
		CallerKey:   "demo-app",
		RouteValues: []string{},
		Type:        model.ReactSessionTypeChat,
		UserPrompt:  "请立即调用 delegate_agent 工具，把任务「回答 ok」委派给子代理 e2e-budget-probe（agent_key: e2e-budget-probe），拿到结论或失败原因后向用户转述。",
		ModelKey:    e2eModelKey(),
		MaxSteps:    4,
	}
	result, err := react.RunWithClientReaderContext(ctx, ctx.Request.Context(), payload, "", func(event params.ReactEvent) error {
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("外层 run 失败: %v", err)
	}

	runs, err := model.GetReactRunsBySessionID(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("查询会话 run 失败: %v", err)
	}
	var subRun *model.ReactRun
	for i := range runs {
		if runs[i].ParentRunID == result.RunID {
			subRun = &runs[i]
		}
	}
	if subRun == nil {
		t.Fatalf("未找到委派子 run")
	}
	if subRun.State != model.ReactRunStateError {
		t.Fatalf("预算超限的子 run 应为 error 态: %s（msg=%s）", subRun.State, subRun.ErrorMessage)
	}
	if !strings.Contains(subRun.ErrorMessage, "预算超限") {
		t.Fatalf("子 run error_message 应含「预算超限」: %s", subRun.ErrorMessage)
	}

	// 委派工具结果以软错误回填父循环：父 run 的 tool_result 消息应包含预算超限原因。
	parentMessages, err := model.GetReactMessagesByRunID(ctx, result.RunID)
	if err != nil {
		t.Fatalf("查询父 run 消息失败: %v", err)
	}
	backfilled := false
	for i := range parentMessages {
		if parentMessages[i].MessageType == model.ReactMessageTypeToolResult && strings.Contains(parentMessages[i].ContentJSON, "预算超限") {
			backfilled = true
			break
		}
	}
	if !backfilled {
		t.Fatal("父 run 的委派工具结果应回填「预算超限」原因")
	}
}
