package e2e

import (
	"encoding/json"
	"os"
	react "react-base-service/service/react"
	core "react-base-service/service/react/internal/core"
	"strings"
	"sync"
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/env"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	agentService "react-base-service/service/agent"

	"github.com/gin-gonic/gin"
)

// 复杂委派 e2e：验证子 run 的嵌套 HITL 冒泡与二级委派深度。
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/internal/e2e/ -run TestDelegateComplex -v -timeout 900s
//
// 前置：react-base-mysql（3327）+ 模型 API 可用。

const (
	e2eHitlAgentMD = `---
agent_key: hitl-agent
name: 交互确认代理
description: |
  适用问题：需要向用户确认偏好（如选哪个城市）后才能继续的任务
  不适用：无需用户确认即可完成的任务
max_steps: 3
tools: []
---
你是交互确认代理。收到任务后必须：
1. 第一步调用 ask_question 向用户提问：问题 id 为 city，prompt 为「要查询哪个城市的信息？」，提供两个选项（id: shanghai/label: 上海，id: beijing/label: 北京）；
2. 拿到用户回答后，用一句中文汇报「用户选择了 <城市>，确认完成」，然后结束。
不要调用 ask_question 以外的任何工具。`

	e2ePlannerAgentMD = `---
agent_key: planner-agent
name: 二级委派代理
description: |
  适用问题：需要把算术计算部分进一步委派给回声代理的任务
  不适用：直接回答即可的任务
max_steps: 4
tools: []
---
你是二级委派代理。收到任务后：
1. 立即调用 delegate_agent 把其中的算术计算部分委派给子代理 echo-agent（agent_key: echo-agent），task 必须自包含；
2. 拿到结论后原样转述给委派方。
不要自己计算，不要调用 delegate_agent 与 ask_question 以外的工具。`
)

// setupComplexE2E 幂等初始化配置/MySQL。
func setupComplexE2E(t *testing.T) {
	t.Helper()
	if helpers.MysqlClientLLM == nil {
		env.SetRootPath("../../../..")
		conf.InitConf()
		zlog.InitLog(conf.BasicConf.Log)
		helpers.InitMysql()
		// 连接为进程级单例，不在单测试 Cleanup 关闭（同 delegate_e2e_test.go）。
	}
	if !conf.CustomConf.LLM.React.SubAgent.SubAgentEnabled() {
		t.Fatal("llm.react.subagent.enabled 必须为 true（conf/mount/custom.yaml）")
	}
}

// importE2EAgent 幂等导入一个子 Agent 定义（存在则先删除重建）。
func importE2EAgent(t *testing.T, ctx *gin.Context, markdown string) {
	t.Helper()
	// 从 markdown 提取 agent_key（仅用于幂等清理）。
	agentKey := ""
	if idx := strings.Index(markdown, "agent_key:"); idx >= 0 {
		rest := markdown[idx+len("agent_key:"):]
		if lineEnd := strings.IndexAny(rest, "\r\n"); lineEnd > 0 {
			agentKey = strings.TrimSpace(rest[:lineEnd])
		}
	}
	if agentKey == "" {
		t.Fatal("markdown 缺少 agent_key")
	}
	if existing, err := model.GetAgentByCallerAndAgentKey(ctx, "demo-app", agentKey); err != nil {
		t.Fatalf("查询已有 agent 失败: %v", err)
	} else if existing != nil {
		if _, err := agentService.DeleteAgent(ctx, existing.AgentID); err != nil {
			t.Fatalf("清理旧 agent 失败: %v", err)
		}
	}
	enabled := 1
	if _, err := agentService.ImportFromMarkdown(ctx, &params.ImportAgentReq{
		CallerKey:   "demo-app",
		RouteValues: []string{},
		Status:      &enabled,
		Markdown:    markdown,
	}, "e2e-delegate"); err != nil {
		t.Fatalf("导入子 Agent %s 失败: %v", agentKey, err)
	}
}

// runE2EConversation 发起一次外层 run 并返回结果与收集到的全部事件。
func runE2EConversation(t *testing.T, ctx *gin.Context, prompt string, maxSteps int, writer func(params.ReactEvent) error, readClient react.ClientMessageReader) (*react.RunResult, []params.ReactEvent) {
	t.Helper()
	payload := params.ReactRunPayload{
		CallerKey:   "demo-app",
		RouteValues: []string{},
		Type:        model.ReactSessionTypeChat,
		UserPrompt:  prompt,
		ModelKey:    e2eModelKey(),
		MaxSteps:    maxSteps,
	}
	events := make([]params.ReactEvent, 0, 128)
	result, err := react.RunWithClientReaderContext(ctx, ctx.Request.Context(), payload, "", func(event params.ReactEvent) error {
		if writer != nil {
			if werr := writer(event); werr != nil {
				return werr
			}
		}
		events = append(events, event)
		return nil
	}, readClient)
	if err != nil {
		t.Fatalf("外层 run 失败: %v", err)
	}
	return result, events
}

// findSubRuns 返回指定父 run 的全部子 run。
func findSubRuns(t *testing.T, ctx *gin.Context, sessionID, parentRunID string) []model.ReactRun {
	t.Helper()
	runs, err := model.GetReactRunsBySessionID(ctx, sessionID)
	if err != nil {
		t.Fatalf("查询会话 run 失败: %v", err)
	}
	subRuns := make([]model.ReactRun, 0, 2)
	for _, run := range runs {
		if run.ParentRunID == parentRunID {
			subRuns = append(subRuns, run)
		}
	}
	return subRuns
}

func runFinalContent(t *testing.T, ctx *gin.Context, runID string) string {
	t.Helper()
	content, err := react.SubAgentFinalResponse(ctx, runID)
	if err != nil {
		t.Fatalf("提取 run %s 最终回复失败: %v", runID, err)
	}
	return content
}

func subRunPaths(runs []model.ReactRun) []string {
	paths := make([]string, 0, len(runs))
	for _, run := range runs {
		paths = append(paths, run.AgentPath)
	}
	return paths
}

// TestDelegateComplexNestedHITL 验证：子 run 内 ask_question 冒泡到共享上行通道，
// 模拟前端按 toolUseId 作答后子 run 继续（协议不区分父子 run）。
func TestDelegateComplexNestedHITL(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	ctx := newHeadlessGinContext("e2e-hitl")
	importE2EAgent(t, ctx, e2eHitlAgentMD)

	answerCh := make(chan params.ReactWSMessage, 1)
	var writerMu sync.Mutex
	askedAgentPaths := make(map[string]bool, 1)
	writer := func(event params.ReactEvent) error {
		if event.Type != react.EventToolUseStart {
			return nil
		}
		payload, ok := event.Payload.(params.ReactToolUseStartPayload)
		if !ok || payload.ToolName != core.MetaToolAskQuestion || payload.Status != core.ToolExecutionStatusWaiting {
			return nil
		}
		writerMu.Lock()
		askedAgentPaths[event.AgentPath] = true
		writerMu.Unlock()
		var input struct {
			Questions []struct {
				ID string `json:"id"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(payload.ToolInput, &input); err != nil || len(input.Questions) == 0 {
			return nil
		}
		// 模拟前端自由输入作答：信封必须带 ask_question 工具调用 id，content 为问题答案
		// （不依赖模型生成的选项 id，走 freeText 更稳）。
		content, _ := json.Marshal(map[string]any{
			"toolUseId": payload.ToolUseID,
			"content": map[string]any{
				"answers": []map[string]any{{"questionId": input.Questions[0].ID, "freeText": "上海"}},
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
		"请调用 delegate_agent 把任务「确认要查询哪个城市的信息」委派给子代理 hitl-agent（agent_key: hitl-agent），它可能会向你提问；拿到它的结论后转述。",
		5, writer, readClient)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 || subRuns[0].AgentPath != "main/hitl-agent" {
		t.Fatalf("子 run 不符: %v", subRunPaths(subRuns))
	}
	subRun := subRuns[0]
	if subRun.State != model.ReactRunStateFinished {
		t.Fatalf("子 run 应为 finished: %s（作答未生效？）", subRun.State)
	}
	if !askedAgentPaths["main/hitl-agent"] {
		t.Fatalf("ask_question 事件应带 agentPath=main/hitl-agent 冒泡: %v", askedAgentPaths)
	}
	subFinal := runFinalContent(t, ctx, subRun.RunID)
	if !strings.Contains(subFinal, "上海") {
		t.Fatalf("子 run 最终回复应包含用户选择的「上海」: %s", subFinal)
	}
	t.Logf("hitl-agent 回复: %s", subFinal)
}

// TestDelegateComplexNestedDepth 验证二级委派：main → planner-agent → echo-agent，
// 孙 run 落库 agentPath=main/planner-agent/echo-agent，三层全部收敛。
func TestDelegateComplexNestedDepth(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	ctx := newHeadlessGinContext("e2e-depth")
	importE2EAgent(t, ctx, e2ePlannerAgentMD)
	importE2EAgent(t, ctx, e2eAgentMarkdown)

	result, _ := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「计算 12*12 的值并报告」委派给子代理 planner-agent（agent_key: planner-agent），拿到结论后原样转述。",
		5, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 || subRuns[0].AgentPath != "main/planner-agent" {
		t.Fatalf("二级委派的中间子 run 不符: %v", subRunPaths(subRuns))
	}
	midRun := subRuns[0]
	if midRun.State != model.ReactRunStateFinished {
		t.Fatalf("中间子 run 应为 finished: %s", midRun.State)
	}

	grandRuns := findSubRuns(t, ctx, result.SessionID, midRun.RunID)
	if len(grandRuns) != 1 || grandRuns[0].AgentPath != "main/planner-agent/echo-agent" {
		t.Fatalf("孙 run 不符: %v", subRunPaths(grandRuns))
	}
	grandRun := grandRuns[0]
	if grandRun.ParentRunID != midRun.RunID || grandRun.State != model.ReactRunStateFinished {
		t.Fatalf("孙 run 状态异常: parent=%s state=%s", grandRun.ParentRunID, grandRun.State)
	}
	if !strings.Contains(runFinalContent(t, ctx, midRun.RunID), "144") {
		t.Fatalf("中间子 run 回复应包含 144: %s", runFinalContent(t, ctx, midRun.RunID))
	}
	t.Logf("二级委派完成: main → %s(转述) → %s(计算 12*12=144)", midRun.RunID, grandRun.RunID)
}
