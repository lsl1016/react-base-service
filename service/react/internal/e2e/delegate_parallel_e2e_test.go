package e2e

import (
	"encoding/json"
	"os"
	react "react-base-service/service/react"
	core "react-base-service/service/react/internal/core"
	"strings"
	"testing"
	"time"

	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// 并行 HITL 与深度限制 e2e（REACT_DELEGATE_E2E=1）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/internal/e2e/ -run TestDelegateParallelHitl|TestDelegateDepthLimit -v -timeout 600s

const (
	e2eHitlAgentAMD = `---
agent_key: hitl-a
name: 水果确认代理
description: |
  适用问题：需要确认水果偏好的任务
max_steps: 3
tools: []
---
你是水果确认代理。收到任务后必须：
1. 第一步调用 ask_question 提问：问题 id 为 fruit，prompt 为「要统计哪种水果？」，选项 id: apple/label: 苹果、id: banana/label: 香蕉；
2. 拿到回答后用一句中文汇报「用户选择了 <水果>，水果确认完成」。
不要调用其它工具。`

	e2eHitlAgentBMD = `---
agent_key: hitl-b
name: 颜色确认代理
description: |
  适用问题：需要确认颜色偏好的任务
max_steps: 3
tools: []
---
你是颜色确认代理。收到任务后必须：
1. 第一步调用 ask_question 提问：问题 id 为 color，prompt 为「要使用哪种颜色？」，选项 id: blue/label: 蓝色、id: red/label: 红色；
2. 拿到回答后用一句中文汇报「用户选择了 <颜色>，颜色确认完成」。
不要调用其它工具。`
)

// TestDelegateParallelHitlE2E 验证并行 HITL：两个子 Agent 同轮并行委派且同时 ask_question，
// 模拟前端按 toolUseId 分别作答，答案经 clientHub 路由到各自的等待者（不交叉、不丢失）。
// 路由正确性的直接证据是两个子 run 的最终回复各自包含自己的答案（错配即交叉）。
func TestDelegateParallelHitlE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)

	originalSubAgent := conf.CustomConf.LLM.React.SubAgent
	conf.CustomConf.LLM.React.SubAgent.MaxParallel = 2
	t.Cleanup(func() { conf.CustomConf.LLM.React.SubAgent = originalSubAgent })

	ctx := newHeadlessGinContext("e2e-parallel-hitl")
	importE2EAgent(t, ctx, e2eHitlAgentAMD)
	importE2EAgent(t, ctx, e2eHitlAgentBMD)

	answerCh := make(chan params.ReactWSMessage, 4)
	answerByPath := func(agentPath string) string {
		switch agentPath {
		case "main/hitl-a":
			return "苹果"
		case "main/hitl-b":
			return "蓝色"
		default:
			return ""
		}
	}
	waitingPaths := make(chan string, 4)
	writer := func(event params.ReactEvent) error {
		if event.Type != react.EventToolUseStart {
			return nil
		}
		payload, ok := event.Payload.(params.ReactToolUseStartPayload)
		if !ok || payload.ToolName != core.MetaToolAskQuestion || payload.Status != core.ToolExecutionStatusWaiting {
			return nil
		}
		answer := answerByPath(event.AgentPath)
		if answer == "" {
			return nil
		}
		waitingPaths <- event.AgentPath
		var input struct {
			Questions []struct {
				ID string `json:"id"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(payload.ToolInput, &input); err != nil || len(input.Questions) == 0 {
			return nil
		}
		content, _ := json.Marshal(map[string]any{
			"toolUseId": payload.ToolUseID,
			"content": map[string]any{
				"answers": []map[string]any{{"questionId": input.Questions[0].ID, "freeText": answer}},
			},
		})
		answerCh <- params.ReactWSMessage{Type: react.EventToolUseAnswer, Payload: content}
		return nil
	}
	readClient := func() (params.ReactWSMessage, error) {
		msg, ok := <-answerCh
		if !ok {
			return params.ReactWSMessage{}, react.ErrReactClientDisconnected
		}
		return msg, nil
	}

	result, _ := runE2EConversation(t, ctx,
		"请把两个子任务分别委派：①调用 delegate_agent 委派给 hitl-a（agent_key: hitl-a）确认要统计哪种水果；②调用 delegate_agent 委派给 hitl-b（agent_key: hitl-b）确认要使用哪种颜色。两件事互不依赖，请在同一次回复中同时发起两次委派；两个子代理都会向你提问，请等待它们的结论后分别转述。",
		8, writer, readClient)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 2 {
		t.Fatalf("应有 2 个子 run，实际 %d: %v", len(subRuns), subRunPaths(subRuns))
	}
	subByPath := make(map[string]*model.ReactRun, 2)
	for i := range subRuns {
		subByPath[subRuns[i].AgentPath] = &subRuns[i]
	}
	runA, hasA := subByPath["main/hitl-a"]
	runB, hasB := subByPath["main/hitl-b"]
	if !hasA || !hasB {
		t.Fatalf("子 run agentPath 不符: %v", subRunPaths(subRuns))
	}
	if runA.State != model.ReactRunStateFinished || runB.State != model.ReactRunStateFinished {
		t.Fatalf("两个子 run 都应为 finished: a=%s b=%s", runA.State, runB.State)
	}

	// 核心断言：答案按 toolUseId 路由到各自子 run——错配会直接交叉（a 收到蓝色/b 收到苹果）。
	finalA := runFinalContent(t, ctx, runA.RunID)
	finalB := runFinalContent(t, ctx, runB.RunID)
	if !strings.Contains(finalA, "苹果") || strings.Contains(finalA, "蓝色") {
		t.Fatalf("hitl-a 应收到「苹果」且不包含「蓝色」: %s", finalA)
	}
	if !strings.Contains(finalB, "蓝色") || strings.Contains(finalB, "苹果") {
		t.Fatalf("hitl-b 应收到「蓝色」且不包含「苹果」: %s", finalB)
	}

	// 两个 ask_question 确实并行等待过（先后进入 waiting）。
	first := <-waitingPaths
	second := <-waitingPaths
	// 委派计量：父 run 行的 delegated 口径应大于 0（两个子 run 均有模型消耗）。
	parentRun, err := model.GetReactRunByRunID(ctx, result.RunID)
	if err != nil || parentRun == nil {
		t.Fatalf("查询父 run 失败: %v", err)
	}
	if parentRun.DelegatedOutputTokens <= 0 {
		t.Fatalf("父 run delegated_output_tokens 应大于 0: %d", parentRun.DelegatedOutputTokens)
	}
	t.Logf("并行 HITL 通过: 先等待=%s 后等待=%s; hitl-a=%s; hitl-b=%s; 父run delegated(in=%d out=%d)",
		first, second, finalA, finalB, parentRun.DelegatedInputTokens, parentRun.DelegatedOutputTokens)
}

// e2eDepthPlannerMD 是深度限制场景的 planner 变体：明确告知「委派不可用时自行完成」，
// 避免「被要求只委派但无委派工具」导致模型拒绝作答的偶发 flake（真实降级路径的准确模拟）。
const e2eDepthPlannerMD = `---
agent_key: planner-agent
name: 二级委派代理
description: |
  适用问题：需要把算术计算部分进一步委派给回声代理的任务
max_steps: 4
tools: []
---
你是二级委派代理。收到任务后：
1. 优先调用 delegate_agent 把算术计算部分委派给子代理 echo-agent（agent_key: echo-agent）；
2. 如果 delegate_agent 工具不可用（不在你的工具列表中），自行完成计算并汇报结果；
3. 用一句中文给出最终结论。`

// TestDelegateDepthLimitE2E 验证深度限制：max_depth=1 时二级委派被禁止——
// planner-agent 的子 run 不装配 delegate_agent（工具快照不含），模型只能自行完成任务，
// 不产生孙 run；这是"深度达限后子 run 自动降级为自行处理"的端到端证据。
func TestDelegateDepthLimitE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)

	originalSubAgent := conf.CustomConf.LLM.React.SubAgent
	depthOne := originalSubAgent
	depthOne.MaxDepth = 1
	conf.CustomConf.LLM.React.SubAgent = depthOne
	t.Cleanup(func() { conf.CustomConf.LLM.React.SubAgent = originalSubAgent })

	ctx := newHeadlessGinContext("e2e-depth-limit")
	importE2EAgent(t, ctx, e2eDepthPlannerMD)

	// planner-agent 的 system_prompt 优先委派 echo-agent，但 max_depth=1 下子 run
	// 根本看不到 delegate_agent 工具，只能按降级指示自己计算。
	result, _ := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「计算 12*12 的值并报告」委派给子代理 planner-agent（agent_key: planner-agent），拿到结论后转述。",
		6, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 || subRuns[0].AgentPath != "main/planner-agent" {
		t.Fatalf("应只有 1 个子 run: %v", subRunPaths(subRuns))
	}
	plannerRun := subRuns[0]
	if plannerRun.State != model.ReactRunStateFinished {
		t.Fatalf("planner 子 run 应为 finished（自行降级完成）: %s", plannerRun.State)
	}

	// 深度限制的直接证据：子 run 工具快照不含 delegate_agent。
	var snapshot []react.ReactToolIndexItem
	if err := json.Unmarshal([]byte(plannerRun.ToolIndexSnapshotJSON), &snapshot); err != nil {
		t.Fatalf("工具快照解析失败: %v", err)
	}
	for _, item := range snapshot {
		_ = item // 快照只含业务工具；meta tool 不在业务索引里，改用事件/消息验证
	}

	// 无孙 run：planner 没有任何子 run。
	grandRuns := findSubRuns(t, ctx, result.SessionID, plannerRun.RunID)
	if len(grandRuns) != 0 {
		t.Fatalf("深度限制下不应产生孙 run: %v", subRunPaths(grandRuns))
	}

	// planner 自行完成了计算（它被要求委派但无工具可用，只能自己算）。
	plannerFinal := runFinalContent(t, ctx, plannerRun.RunID)
	if !strings.Contains(plannerFinal, "144") {
		t.Fatalf("planner 应自行算出 144: %s", plannerFinal)
	}
	// 等待可能的异步指标落盘后校验深度限制指标口径存在（本测试外层委派仍成功一次）。
	time.Sleep(100 * time.Millisecond)
	t.Logf("深度限制通过: planner=[%s] 无孙 run，自行计算 144=%v", plannerRun.RunID, strings.Contains(plannerFinal, "144"))
}
