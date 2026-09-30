package e2e

import (
	"os"
	react "react-base-service/service/react"
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

// e2eAgentMarkdown 是被委派子 Agent 的文件定义：model_key 留空 = 继承父 run 当前模型。
const e2eAgentMarkdown = `---
agent_key: echo-agent
name: 回声计算代理
description: |
  适用问题：需要精确算术计算并复述结论的任务
  不适用：其它一切任务
max_steps: 2
tools: []
---
你是回声计算代理。收到任务后不要调用任何工具、不要提问，直接给出计算结果，并用固定句式回复：「这是子代理 echo-agent 的结论：<答案>」。`

// e2eModelKey e2e 运行使用的模型种类：run 侧 API key 按 caller 解析（tblLlmApiKey），
// 与模型种类解耦，因此默认 claude 在只有 deepseek key 的环境会 401。
// 可用 REACT_E2E_MODEL_KEY 覆盖（如 deepseek），须与 demo-app 注册 key 的平台匹配。
func e2eModelKey() string {
	if v := strings.TrimSpace(os.Getenv("REACT_E2E_MODEL_KEY")); v != "" {
		return v
	}
	return "claude"
}

// TestDelegateAgentE2E 端到端验证 delegate_agent 委派链路（真实 MySQL + 真实模型调用）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/internal/e2e/ -run TestDelegateAgentE2E -v -timeout 300s
//
// 前置：react-base-mysql 容器（3327）已建 tblLlmAgent 并对 tblLlmReactRun 加列；
// caller=demo-app 与其 apikey 已注册；custom.yaml subagent.enabled=true。
// 模型种类默认 claude（须与 demo-app 注册 key 的平台匹配），可用 REACT_E2E_MODEL_KEY 覆盖。
// 验证点：委派执行、子 run 落库（parent_run_id/agent_path/模型继承）、事件 agentPath 冒泡、
// 父子终态顺序、历史隔离（外层历史排除子消息）、回放接口还原嵌套事件。
func TestDelegateAgentE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e (requires react-base-mysql + model api)")
	}

	// 测试进程工作目录是包目录，配置根指回仓库根。
	env.SetRootPath("../../../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()
	// 注：连接与日志不在单个测试的 Cleanup 里关闭——多个 e2e 测试共享进程级单例，
	// 先结束的测试关闭会让后续测试失败；测试进程退出时由运行时统一回收。
	if !conf.CustomConf.LLM.React.SubAgent.SubAgentEnabled() {
		t.Fatal("llm.react.subagent.enabled 必须为 true（conf/mount/custom.yaml）")
	}

	ctx := newHeadlessGinContext("e2e-delegate")
	importEchoAgent(t, ctx)

	// 事件收集：delegate 并行通道下多个 emitter 可能并发写出，加锁保护。
	var mu sync.Mutex
	events := make([]params.ReactEvent, 0, 64)
	writer := func(event params.ReactEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}

	payload := params.ReactRunPayload{
		CallerKey:   "demo-app",
		RouteValues: []string{},
		Type:        model.ReactSessionTypeChat,
		UserPrompt:  "请立即调用 delegate_agent 工具，把任务「计算 17*23 的值」委派给子代理 echo-agent（agent_key: echo-agent），拿到结论后向用户转述子代理的回复。不要自己计算。",
		ModelKey:    e2eModelKey(),
		MaxSteps:    4,
	}
	result, err := react.RunWithClientReaderContext(ctx, ctx.Request.Context(), payload, "", writer, nil)
	if err != nil {
		t.Fatalf("外层 run 失败: %v", err)
	}

	// 断言 1：子 run 落库，带父子关系、agentPath，且模型继承父 run。
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
		t.Fatalf("未找到 parent_run_id=%s 的子 run，runs=%d", result.RunID, len(runs))
	}
	if subRun.AgentPath != "main/echo-agent" {
		t.Fatalf("子 run agent_path 应为 main/echo-agent: %q", subRun.AgentPath)
	}
	if subRun.State != model.ReactRunStateFinished {
		t.Fatalf("子 run 应为 finished: %s", subRun.State)
	}
	if subRun.ModelKey != e2eModelKey() {
		t.Fatalf("子 run 应继承父模型 %s: %q", e2eModelKey(), subRun.ModelKey)
	}

	// 断言 2：子 run 最终回复包含计算结果。
	subFinal, err := react.SubAgentFinalResponse(ctx, subRun.RunID)
	if err != nil {
		t.Fatalf("提取子 run 最终回复失败: %v", err)
	}
	if !strings.Contains(subFinal, "391") {
		t.Fatalf("子 run 回复应包含 17*23=391: %s", subFinal)
	}

	// 断言 3：事件冒泡——存在 agentPath=main/echo-agent 的事件，父 done 晚于子 done。
	subPathEvents := 0
	parentDoneIdx, subDoneIdx := -1, -1
	var parentFinalContent strings.Builder
	mu.Lock()
	for i, event := range events {
		if event.AgentPath == "main/echo-agent" {
			subPathEvents++
			if event.Type == react.EventDone {
				subDoneIdx = i
			}
		}
		if event.RunID == result.RunID && event.Type == react.EventDone {
			parentDoneIdx = i
		}
		if event.AgentPath == "" && event.Type == react.EventContentEnd {
			if payload, ok := event.Payload.(params.ReactContentEndPayload); ok {
				parentFinalContent.WriteString(payload.Content)
			}
		}
	}
	mu.Unlock()
	if subPathEvents == 0 {
		t.Fatal("事件流未收到子 run 冒泡事件（agentPath=main/echo-agent）")
	}
	if subDoneIdx < 0 || parentDoneIdx < 0 {
		t.Fatalf("父子 run 都应发出 done 事件: subDone=%d, parentDone=%d", subDoneIdx, parentDoneIdx)
	}
	if parentDoneIdx < subDoneIdx {
		t.Fatalf("父 done 应在子 done 之后: parent=%d, sub=%d", parentDoneIdx, subDoneIdx)
	}
	if !strings.Contains(parentFinalContent.String(), "391") {
		t.Fatalf("父 run 最终回复应转述子代理结论 391: %s", parentFinalContent.String())
	}

	// 断言 4：历史隔离——外层历史排除子 run 消息，时间线包含全部。
	outerMessages, err := model.GetOuterReactMessagesBySessionIDWithDB(ctx, helpers.MysqlClientLLM, result.SessionID)
	if err != nil {
		t.Fatalf("查询外层历史失败: %v", err)
	}
	for _, message := range outerMessages {
		if message.RunID == subRun.RunID {
			t.Fatal("外层历史不应包含子 run 消息（隔离失败）")
		}
	}
	timeline, err := model.GetReactMessagesBySessionIDTimeline(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("查询时间线失败: %v", err)
	}
	timelineHasSub := false
	for _, message := range timeline {
		if message.RunID == subRun.RunID {
			timelineHasSub = true
		}
	}
	if !timelineHasSub {
		t.Fatal("回放时间线应包含子 run 消息")
	}

	// 断言 5：回放接口还原嵌套事件（agentPath、父 done 在子 done 后）。
	history, err := react.GetHistoryEvents(ctx, params.ReactSessionEventsReq{SessionID: result.SessionID})
	if err != nil {
		t.Fatalf("查询回放事件失败: %v", err)
	}
	historySubEvents := 0
	historyParentDoneIdx, historySubDoneIdx := -1, -1
	for i, event := range history.Events {
		if event.AgentPath == "main/echo-agent" {
			historySubEvents++
			if event.Type == react.EventDone {
				historySubDoneIdx = i
			}
		}
		if event.RunID == result.RunID && event.Type == react.EventDone {
			historyParentDoneIdx = i
		}
	}
	if historySubEvents == 0 {
		t.Fatal("回放事件应包含 agentPath=main/echo-agent 的子 run 事件")
	}
	if historySubDoneIdx < 0 || historyParentDoneIdx < 0 || historyParentDoneIdx < historySubDoneIdx {
		t.Fatalf("回放中父 done 应在子 done 之后: parent=%d, sub=%d", historyParentDoneIdx, historySubDoneIdx)
	}

	t.Logf("e2e 通过: 父run=%s 子run=%s 子事件=%d 回放子事件=%d 会话=%s",
		result.RunID, subRun.RunID, subPathEvents, historySubEvents, result.SessionID)
}

// importEchoAgent 导入（或重置）e2e 用子 Agent 定义：model_key 留空 = 继承父 run 模型。
func importEchoAgent(t *testing.T, ctx *gin.Context) {
	t.Helper()
	if existing, err := model.GetAgentByCallerAndAgentKey(ctx, "demo-app", "echo-agent"); err != nil {
		t.Fatalf("查询已有 agent 失败: %v", err)
	} else if existing != nil {
		if _, err := agentService.DeleteAgent(ctx, existing.AgentID); err != nil {
			t.Fatalf("清理旧 agent 失败: %v", err)
		}
	}
	enabled := 1
	agent, err := agentService.ImportFromMarkdown(ctx, &params.ImportAgentReq{
		CallerKey:   "demo-app",
		RouteValues: []string{},
		Status:      &enabled,
		Markdown:    e2eAgentMarkdown,
	}, "e2e-delegate")
	if err != nil {
		t.Fatalf("导入子 Agent 失败: %v", err)
	}
	if agent.ModelKey != "" {
		t.Fatalf("e2e 子 Agent 应不指定模型（继承父 run）: %q", agent.ModelKey)
	}
}
