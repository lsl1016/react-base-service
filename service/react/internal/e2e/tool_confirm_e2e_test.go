package e2e

import (
	"encoding/json"
	"os"
	react "react-base-service/service/react"
	"strings"
	"testing"

	"react-base-service/components/params"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"
)

// P2-3 危险操作确认 e2e（REACT_DELEGATE_E2E=1，真实 MySQL + glm-4.6 + mcp 网关）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/internal/e2e/ -run TestToolConfirm -v -timeout 600s
//
// 验证：confirm 工具的拒绝/允许两条路径、agent 级收紧、tool_confirm_request 事件冒泡。

const e2eConfirmRateToolID = "mcp_mcpgw_exchange_rate"

// setToolPermissionMode 直接调整网关同步工具的确认模式（MCP 工具 config 由连接管理，走列更新）。
func setToolPermissionMode(t *testing.T, toolID, mode string) {
	t.Helper()
	if err := model.UpdateToolByToolID(newHeadlessGinContext("e2e-confirm"), toolID, map[string]interface{}{"permission_mode": mode}); err != nil {
		t.Fatalf("更新工具权限模式失败: %v", err)
	}
}

// confirmAnswerSimulator 构造模拟前端：收到 tool_confirm_request 后按策略回 tool_confirm_answer。
func confirmAnswerSimulator(approve bool) (writer func(params.ReactEvent) error, readClient func() (params.ReactWSMessage, error), requested *[]params.ReactToolConfirmRequestPayload) {
	answerCh := make(chan params.ReactWSMessage, 4)
	requests := make([]params.ReactToolConfirmRequestPayload, 0, 2)
	writer = func(event params.ReactEvent) error {
		if event.Type != react.EventToolConfirmRequest {
			return nil
		}
		payload, ok := event.Payload.(params.ReactToolConfirmRequestPayload)
		if !ok {
			return nil
		}
		requests = append(requests, payload)
		content, _ := json.Marshal(map[string]any{
			"toolUseId": payload.ToolUseID,
			"approved":  approve,
			"reason":    map[bool]string{true: "e2e 允许", false: "e2e 拒绝"}[approve],
		})
		answerCh <- params.ReactWSMessage{Type: react.EventToolConfirmAnswer, Payload: content}
		return nil
	}
	readClient = func() (params.ReactWSMessage, error) {
		msg, ok := <-answerCh
		if !ok {
			return params.ReactWSMessage{}, react.ErrReactClientDisconnected
		}
		return msg, nil
	}
	return writer, readClient, &requests
}

// TestToolConfirmRejectE2E 拒绝路径：confirm 工具被拒后回填「用户拒绝」，模型转述失败原因而非数据。
func TestToolConfirmRejectE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	setToolPermissionMode(t, e2eConfirmRateToolID, toolService.ToolPermissionConfirm)
	t.Cleanup(func() { setToolPermissionMode(t, e2eConfirmRateToolID, toolService.ToolPermissionAuto) })

	ctx := newHeadlessGinContext("e2e-confirm-reject")
	importE2EAgent(t, ctx, e2eFinanceAgentMD)

	writer, readClient, _ := confirmAnswerSimulator(false)
	result, events := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「查询 1 USD 兑换 CNY 的最新汇率」委派给子代理 finance-agent（agent_key: finance-agent）。如果子代理报告工具被用户拒绝，请如实转述「用户拒绝了汇率查询，未获取到数据」，不要编造数字。",
		6, writer, readClient)

	// 确认请求确实发出（事件带 agentPath 冒泡）。
	confirmEvents := 0
	for _, event := range events {
		if event.Type == react.EventToolConfirmRequest {
			confirmEvents++
			if event.AgentPath != "main/finance-agent" {
				t.Fatalf("确认请求应带 agentPath=main/finance-agent 冒泡: %q", event.AgentPath)
			}
		}
	}
	if confirmEvents == 0 {
		t.Fatal("未收到 tool_confirm_request 事件")
	}

	// 子 run 最终回复：包含拒绝语义、不含真实汇率数据。
	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 || subRuns[0].AgentPath != "main/finance-agent" {
		t.Fatalf("子 run 不符: %v", subRunPaths(subRuns))
	}
	if subRuns[0].State != model.ReactRunStateFinished {
		t.Fatalf("子 run 应正常收敛: %s", subRuns[0].State)
	}
	subFinal := runFinalContent(t, ctx, subRuns[0].RunID)
	if !strings.Contains(subFinal, "拒绝") {
		t.Fatalf("子 run 回复应包含拒绝语义: %s", subFinal)
	}
	if strings.Contains(subFinal, "6.7") {
		t.Fatalf("拒绝后不应出现真实汇率数据: %s", subFinal)
	}
	// 工具结果为 IsError（rejected）。
	messages, _ := model.GetReactMessagesByRunID(ctx, subRuns[0].RunID)
	rejectedSeen := false
	for _, message := range messages {
		if message.MessageType == model.ReactMessageTypeToolResult && strings.Contains(message.ContentJSON, "用户拒绝") {
			rejectedSeen = true
		}
	}
	if !rejectedSeen {
		t.Fatal("子 run 历史应包含「用户拒绝」的工具结果")
	}
	t.Logf("拒绝路径通过: 确认请求=%d, 子run回复=%s", confirmEvents, subFinal)
}

// TestToolConfirmApproveE2E 允许路径：确认通过后工具正常执行并返回真实数据。
func TestToolConfirmApproveE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	setToolPermissionMode(t, e2eConfirmRateToolID, toolService.ToolPermissionConfirm)
	t.Cleanup(func() { setToolPermissionMode(t, e2eConfirmRateToolID, toolService.ToolPermissionAuto) })

	ctx := newHeadlessGinContext("e2e-confirm-approve")
	importE2EAgent(t, ctx, e2eFinanceAgentMD)

	writer, readClient, _ := confirmAnswerSimulator(true)
	result, _ := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「查询 1 USD 兑换 CNY 的最新汇率」委派给子代理 finance-agent（agent_key: finance-agent），拿到结论后转述汇率数字。",
		6, writer, readClient)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 {
		t.Fatalf("子 run 数不符: %v", subRunPaths(subRuns))
	}
	subFinal := runFinalContent(t, ctx, subRuns[0].RunID)
	if !strings.Contains(subFinal, "CNY") && !strings.Contains(subFinal, "汇率") {
		t.Fatalf("允许后应返回真实汇率: %s", subFinal)
	}
	t.Logf("允许路径通过: %s", subFinal)
}

// TestToolConfirmAgentTightenE2E agent 级收紧：工具为 auto，agent 定义 permission_mode=confirm
// 时子 run 内工具执行仍需确认。
func TestToolConfirmAgentTightenE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	// 工具保持 auto——确认只能来自 agent 收紧。
	setToolPermissionMode(t, e2eConfirmRateToolID, toolService.ToolPermissionAuto)

	ctx := newHeadlessGinContext("e2e-confirm-agent")
	importE2EAgent(t, ctx, `---
agent_key: finance-agent
name: 汇率查询代理
description: |
  适用问题：货币汇率查询与简单换算
max_steps: 3
tools:
  - mcpgw_exchange_rate
permission_mode: confirm
---
你是汇率查询代理。收到任务后：
1. 用 get_tool 加载工具 mcpgw_exchange_rate，再通过 execute_tool 查询指定货币对的汇率；
2. 用一句中文汇报「1 <基准货币> = <汇率> <目标货币>」。
不要调用其它工具，不要向用户提问。`)
	t.Cleanup(func() { importE2EAgent(t, ctx, e2eFinanceAgentMD) })

	writer, readClient, _ := confirmAnswerSimulator(true)
	result, events := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「查询 1 USD 兑换 CNY 的最新汇率」委派给子代理 finance-agent（agent_key: finance-agent），拿到结论后转述。",
		6, writer, readClient)

	confirmEvents := 0
	for _, event := range events {
		if event.Type == react.EventToolConfirmRequest {
			confirmEvents++
		}
	}
	if confirmEvents == 0 {
		t.Fatal("agent 级 confirm 收紧应触发确认（工具本身为 auto）")
	}
	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 {
		t.Fatalf("子 run 数不符: %v", subRunPaths(subRuns))
	}
	subFinal := runFinalContent(t, ctx, subRuns[0].RunID)
	if !strings.Contains(subFinal, "CNY") && !strings.Contains(subFinal, "汇率") {
		t.Fatalf("确认通过后应返回真实汇率: %s", subFinal)
	}
	t.Logf("agent 收紧路径通过: 确认请求=%d, %s", confirmEvents, subFinal)
}
