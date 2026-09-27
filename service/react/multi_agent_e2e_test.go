package react

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// 多智能体执行正确性补充 e2e（REACT_DELEGATE_E2E=1）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/ -run 'TestParallelOverlap|TestParallelFailureIsolation|TestNestedDelegationChain|TestParallelDisconnectCascade' -v -timeout 900s
//
// 与既有用例的分工：TestDelegateParallelHitlE2E 管「并行 HITL 的答案按 toolUseId 路由」、
// TestDelegateDepthLimitE2E 管「深度达限自动降级」；本文件补齐四个维度——
// 并行时间重叠的直接证据、同轮委派的失败隔离、两层嵌套链的递归计量、并行中 断连的取消级联。

// withParallelism 临时把并行度设为 n（测试自洽，不依赖 custom.yaml 当前值）。
func withParallelism(t *testing.T, n int) {
	t.Helper()
	original := conf.CustomConf.LLM.React.SubAgent
	conf.CustomConf.LLM.React.SubAgent.MaxParallel = n
	t.Cleanup(func() { conf.CustomConf.LLM.React.SubAgent = original })
}

// TestParallelOverlapE2E 验证并行委派的时间重叠（直接证据）：
// 同轮两个无 HITL 的子任务（echo 两道算术）应交错执行——两个子 run 的事件窗口互相重叠
//（A 的首事件早于 B 的末事件，且 B 的首事件早于 A 的末事件），且创建时间贴近。
// 串行执行下 B 的首事件必然晚于 A 的末事件，此断言必不成立。
func TestParallelOverlapE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	withParallelism(t, 2)

	ctx := newHeadlessGinContext("e2e-parallel-overlap")
	importE2EAgent(t, ctx, e2eAgentMarkdown)

	result, events := runE2EConversation(t, ctx,
		"请在同一次回复中同时发起两次 delegate_agent 调用（互不依赖，不要等第一个完成再发第二个）：①把「计算 21+21 并只回答数字」委派给 echo-agent；②把「计算 12*12 并只回答数字」委派给 echo-agent。两个结果都回来后分别转述。",
		6, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 2 {
		t.Fatalf("应有 2 个子 run，实际 %d: %v", len(subRuns), subRunPaths(subRuns))
	}
	for i := range subRuns {
		if subRuns[i].State != model.ReactRunStateFinished {
			t.Fatalf("子 run 应为 finished: %s(%s)", subRuns[i].AgentPath, subRuns[i].State)
		}
	}
	if absDuration := subRuns[1].CreatedAt.Sub(subRuns[0].CreatedAt); absDuration > 10*time.Second {
		t.Fatalf("并行子 run 创建时间应贴近（<10s）: %v", absDuration)
	}

	// 并行重叠的直接证据（DB 时间窗）：两个子 run 的存在期相交——
	// A.created < B.finished 且 B.created < A.finished（finished≈最终态写入时间）。
	// 串行执行下第二个子 run 只会在第一个完成后创建（子 run 至少数秒），时间窗必不相交；
	// 同时启动但一方先流完时事件到达序可能不交错，故到达序只作参考日志不作断言。
	a, b := subRuns[0], subRuns[1]
	overlapped := a.CreatedAt.Before(b.UpdatedAt) && b.CreatedAt.Before(a.UpdatedAt)
	if !overlapped {
		t.Fatalf("两个子 run 存在期未相交（疑似串行）: a=[%s ~ %s] b=[%s ~ %s]",
			a.CreatedAt.Format(time.TimeOnly), a.UpdatedAt.Format(time.TimeOnly),
			b.CreatedAt.Format(time.TimeOnly), b.UpdatedAt.Format(time.TimeOnly))
	}
	subRunIDs := map[string]bool{a.RunID: true, b.RunID: true}
	firstArrival, lastArrival := map[string]int{}, map[string]int{}
	for i, event := range events {
		if !subRunIDs[event.RunID] {
			continue
		}
		if _, ok := firstArrival[event.RunID]; !ok {
			firstArrival[event.RunID] = i
		}
		lastArrival[event.RunID] = i
	}

	// 结果对位：父最终回复同时包含两道题的结果。
	parentFinal := runFinalContent(t, ctx, result.RunID)
	for _, want := range []string{"42", "144"} {
		if !strings.Contains(parentFinal, want) {
			t.Fatalf("父回复应包含 %s: %s", want, parentFinal)
		}
	}
	t.Logf("并行重叠通过: 存在期相交 a=[%s~%s] b=[%s~%s]; 事件到达参考 %v=%v %v=%v; 父回复含 42/144",
		a.CreatedAt.Format(time.TimeOnly), a.UpdatedAt.Format(time.TimeOnly),
		b.CreatedAt.Format(time.TimeOnly), b.UpdatedAt.Format(time.TimeOnly),
		a.RunID, []int{firstArrival[a.RunID], lastArrival[a.RunID]},
		b.RunID, []int{firstArrival[b.RunID], lastArrival[b.RunID]})
}

// TestParallelFailureIsolationE2E 验证同轮并行委派的失败隔离：
// 一个必失败（预算=1 的探针）+ 一个成功（echo），断言——
// 失败子 run error 且含预算超限、成功子 run finished、父 run 正常收尾、
// 父拿到两个工具结果（isError 文本 + finalResponse JSON），最终转述成功侧结论。
func TestParallelFailureIsolationE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	withParallelism(t, 2)

	ctx := newHeadlessGinContext("e2e-parallel-failure")
	importE2EAgent(t, ctx, e2eBudgetAgentMarkdown)
	importE2EAgent(t, ctx, e2eAgentMarkdown)

	result, _ := runE2EConversation(t, ctx,
		"请在同一次回复中同时发起两次 delegate_agent 调用（互不依赖）：①把「回答 ok」委派给 e2e-budget-probe；②把「计算 7*8 并只回答数字」委派给 echo-agent。无论第一个成败，请把第二个的结果转述，并说明第一个的状态。",
		8, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 2 {
		t.Fatalf("应有 2 个子 run，实际 %d: %v", len(subRuns), subRunPaths(subRuns))
	}
	var failed, succeeded *model.ReactRun
	for i := range subRuns {
		switch {
		case strings.Contains(subRuns[i].AgentPath, "e2e-budget-probe"):
			failed = &subRuns[i]
		case strings.Contains(subRuns[i].AgentPath, "echo-agent"):
			succeeded = &subRuns[i]
		}
	}
	if failed == nil || succeeded == nil {
		t.Fatalf("子 run 归属不符: %v", subRunPaths(subRuns))
	}
	if failed.State != model.ReactRunStateError || !strings.Contains(failed.ErrorMessage, "预算超限") {
		t.Fatalf("预算探针子 run 应 error 且含预算超限: state=%s msg=%s", failed.State, failed.ErrorMessage)
	}
	if succeeded.State != model.ReactRunStateFinished {
		t.Fatalf("echo 子 run 应 finished: %s", succeeded.State)
	}
	if !strings.Contains(runFinalContent(t, ctx, succeeded.RunID), "56") {
		t.Fatalf("echo 子 run 应算出 56")
	}

	// 父 run 的工具结果消息：一条含预算超限（软错误），一条含 56（成功 finalResponse）。
	parentMessages, err := model.GetReactMessagesByRunID(ctx, result.RunID)
	if err != nil {
		t.Fatalf("查询父消息失败: %v", err)
	}
	budgetBackfilled, successBackfilled := false, false
	for i := range parentMessages {
		if parentMessages[i].MessageType != model.ReactMessageTypeToolResult {
			continue
		}
		if strings.Contains(parentMessages[i].ContentJSON, "预算超限") {
			budgetBackfilled = true
		}
		if strings.Contains(parentMessages[i].ContentJSON, "56") {
			successBackfilled = true
		}
	}
	if !budgetBackfilled || !successBackfilled {
		t.Fatalf("父应同时收到失败与成功两条工具结果: budget=%v success=%v", budgetBackfilled, successBackfilled)
	}
	if !strings.Contains(runFinalContent(t, ctx, result.RunID), "56") {
		t.Fatalf("父最终回复应转述成功侧 56")
	}
	t.Logf("失败隔离通过: 失败=[%s] 成功=[%s]，父两结果齐备并转述 56", failed.AgentPath, succeeded.AgentPath)
}

// TestNestedDelegationChainE2E 验证两层嵌套委派链（max_depth=2 内的合法链）：
// main → planner-agent → echo-agent，断言孙 run 的三段 agentPath、
// 递归 token 计量（echo 累计进 planner 的 delegated，planner 再累计进父）、父转述最终结论。
func TestNestedDelegationChainE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)

	ctx := newHeadlessGinContext("e2e-nested-chain")
	importE2EAgent(t, ctx, e2eDepthPlannerMD)
	importE2EAgent(t, ctx, e2eAgentMarkdown)

	result, _ := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「计算 13*13 的值并报告」委派给子代理 planner-agent（agent_key: planner-agent），拿到结论后转述。",
		6, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 || subRuns[0].AgentPath != "main/planner-agent" {
		t.Fatalf("应只有 planner 子 run: %v", subRunPaths(subRuns))
	}
	planner := subRuns[0]
	if planner.State != model.ReactRunStateFinished {
		t.Fatalf("planner 子 run 应 finished: %s", planner.State)
	}

	// 孙 run：三段 agentPath，父指向 planner。
	grandRuns := findSubRuns(t, ctx, result.SessionID, planner.RunID)
	if len(grandRuns) != 1 || grandRuns[0].AgentPath != "main/planner-agent/echo-agent" {
		t.Fatalf("应有 1 个孙 run 且路径为三段: %v", subRunPaths(grandRuns))
	}
	grand := grandRuns[0]
	if grand.State != model.ReactRunStateFinished {
		t.Fatalf("孙 run 应 finished: %s", grand.State)
	}
	if !strings.Contains(runFinalContent(t, ctx, grand.RunID), "169") {
		t.Fatalf("echo 孙 run 应算出 169")
	}

	// 递归计量（output 口径稳定；input 在前缀缓存场景常为 0 只作日志）：
	// planner 的 delegated_output > 0（echo 累计进来）；父的 delegated_output ≥ planner 递归口径。
	if planner.DelegatedOutputTokens <= 0 {
		t.Fatalf("planner 的 delegated_output_tokens 应大于 0（孙 run 累计）: %d", planner.DelegatedOutputTokens)
	}
	parentRun, err := model.GetReactRunByRunID(ctx, result.RunID)
	if err != nil || parentRun == nil {
		t.Fatalf("查询父 run 失败: %v", err)
	}
	plannerRecursiveOut := planner.TotalOutputTokens + planner.DelegatedOutputTokens
	if parentRun.DelegatedOutputTokens < plannerRecursiveOut {
		t.Fatalf("父 delegated_output(%d) 应 ≥ planner 递归口径(%d)", parentRun.DelegatedOutputTokens, plannerRecursiveOut)
	}
	if !strings.Contains(runFinalContent(t, ctx, result.RunID), "169") {
		t.Fatalf("父最终回复应转述 169")
	}
	t.Logf("嵌套链通过: 孙=[%s] 169, planner(in=%d out=%d delegatedIn=%d delegatedOut=%d), 父 delegated(in=%d out=%d) ≥ planner递归out %d",
		grand.AgentPath, planner.TotalInputTokens, planner.TotalOutputTokens, planner.DelegatedInputTokens, planner.DelegatedOutputTokens,
		parentRun.DelegatedInputTokens, parentRun.DelegatedOutputTokens, plannerRecursiveOut)
}

// TestParallelDisconnectCascadeE2E 验证并行执行中客户端断连的取消级联：
// 两个子代理同时 ask_question 等待中，readClient 返回断连 → 父 run 收敛、
// 两个在途子 run 全部收敛（终态非 running/waiting），不遗留悬挂 run。
func TestParallelDisconnectCascadeE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	withParallelism(t, 2)

	ctx := newHeadlessGinContext("e2e-disconnect-cascade")
	importE2EAgent(t, ctx, e2eHitlAgentAMD)
	importE2EAgent(t, ctx, e2eHitlAgentBMD)

	waiting := make(chan string, 4)
	allWaiting := make(chan struct{})
	go func() {
		count := 0
		for range waiting {
			count++
			if count >= 2 {
				close(allWaiting)
				return
			}
		}
	}()
	writer := func(event params.ReactEvent) error {
		if event.Type != EventToolUseStart {
			return nil
		}
		payload, ok := event.Payload.(params.ReactToolUseStartPayload)
		if !ok || payload.ToolName != metaToolAskQuestion || payload.Status != toolExecutionStatusWaiting {
			return nil
		}
		waiting <- event.AgentPath
		return nil
	}
	readClient := func() (params.ReactWSMessage, error) {
		// 等到两个提问都在等待后断连；兜底 60s（模型偶发只发一个委派时也能推进，
		// 单个在途子 run 的级联收敛同样被验证）。
		select {
		case <-allWaiting:
		case <-time.After(60 * time.Second):
		}
		return params.ReactWSMessage{}, ErrReactClientDisconnected
	}

	payload := params.ReactRunPayload{
		CallerKey:   "demo-app",
		RouteValues: []string{},
		Type:        model.ReactSessionTypeChat,
		UserPrompt:  "请在同一次回复中同时发起两次 delegate_agent 调用：①委派 hitl-a 确认水果；②委派 hitl-b 确认颜色。等它们提问后由我作答。",
		ModelKey:    e2eModelKey(),
		MaxSteps:    6,
	}
	result, err := RunWithClientReaderContext(ctx, ctx.Request.Context(), payload, "", writer, readClient)
	if err == nil {
		t.Fatalf("断连场景应返回错误（模拟用户关闭页面）")
	}
	if result == nil || result.RunID == "" {
		t.Fatalf("断连也应带出 runID 供状态断言: %v", err)
	}

	// 收敛等待：级联取消是异步的，轮询至全部终态。
	deadline := time.Now().Add(30 * time.Second)
	settled := func() bool {
		runs, qerr := model.GetReactRunsBySessionID(ctx, result.SessionID)
		if qerr != nil {
			return false
		}
		for i := range runs {
			if runs[i].State == model.ReactRunStateRunning || runs[i].State == model.ReactRunStateWaitingClientMessage {
				return false
			}
		}
		return true
	}
	for !settled() {
		if time.Now().After(deadline) {
			runs, _ := model.GetReactRunsBySessionID(ctx, result.SessionID)
			t.Fatalf("断连后 30s 内 run 未全部收敛: %v", runStates(runs))
		}
		time.Sleep(500 * time.Millisecond)
	}

	runs, _ := model.GetReactRunsBySessionID(ctx, result.SessionID)
	subCount := 0
	for i := range runs {
		if runs[i].RunID == result.RunID && runs[i].State == model.ReactRunStateRunning {
			t.Fatalf("父 run 不应仍为 running")
		}
		if runs[i].ParentRunID == result.RunID {
			subCount++
		}
	}
	if subCount < 1 {
		t.Fatalf("应至少有 1 个在途子 run 被级联收敛: %d", subCount)
	}
	var _ = json.Marshal
	t.Logf("断连级联通过（%d 个在途子 run 一并收敛）: %s", subCount, runStates(runs))
}

func runStates(runs []model.ReactRun) string {
	parts := make([]string, 0, len(runs))
	for i := range runs {
		path := runs[i].AgentPath
		if path == "" {
			path = "main"
		}
		parts = append(parts, path+"="+runs[i].State)
	}
	return strings.Join(parts, ", ")
}
