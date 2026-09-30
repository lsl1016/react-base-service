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
	"react-base-service/service/mcpclient"

	"github.com/gin-gonic/gin"
)

// 复杂委派 e2e：接入真实 MCP 网关（mcp-server 容器）注册业务工具，验证
// 子 Agent 白名单装配、子 run 内多步 MCP 工具链、并行委派、嵌套 HITL 冒泡、二级委派深度。
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/internal/e2e/ -run TestDelegateComplex -v -timeout 900s
//
// 前置：react-base-mysql（3327）+ mcp-server 网关（18080，demo-app 凭证）+ 模型 API 可用。

const (
	e2eMCPGatewayName = "mcpgw"
	e2eMCPGatewayURL  = "http://127.0.0.1:18080/api/mcp"
	e2eMCPGatewayAuth = "Bearer demo-app-key:demo-app-secret-please-rotate"
)

const (
	e2eGeoAgentMD = `---
agent_key: geo-agent
name: 天气查询代理
description: |
  适用问题：查询某城市的当前天气（需要先地理编码拿经纬度再查天气）
  不适用：汇率、假日等与天气无关的查询
max_steps: 6
tools:
  - mcpgw_geocode_city
  - mcpgw_weather_forecast
---
你是天气查询代理。收到任务后按顺序执行：
1. 用 get_tool 加载工具 mcpgw_geocode_city，再通过 execute_tool 执行它（城市名建议传英文，如 Shanghai）拿到经纬度；
2. 用 get_tool 加载工具 mcpgw_weather_forecast，再通过 execute_tool 用上一步经纬度查询当前天气；
3. 用一句中文汇报「<城市>当前气温 X°C（WMO 天气代码 Y）」。
不要调用上述两个以外的工具，不要向用户提问。`

	e2eFinanceAgentMD = `---
agent_key: finance-agent
name: 汇率查询代理
description: |
  适用问题：货币汇率查询与简单换算
  不适用：天气、假日等与汇率无关的查询
max_steps: 3
tools:
  - mcpgw_exchange_rate
---
你是汇率查询代理。收到任务后：
1. 用 get_tool 加载工具 mcpgw_exchange_rate，再通过 execute_tool 查询指定货币对的汇率；
2. 用一句中文汇报「1 <基准货币> = <汇率> <目标货币>」。
不要调用其它工具，不要向用户提问。`

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

// setupComplexE2E 幂等初始化配置/MySQL，并把 mcp-server 网关注册进基座工具注册表。
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
	if mcpclient.GetServer(e2eMCPGatewayName) == nil {
		_, err := mcpclient.EnsureServer(mcpclient.ServerConfig{
			Name:      e2eMCPGatewayName,
			Kind:      "http",
			Endpoint:  e2eMCPGatewayURL,
			TimeoutMs: 30000,
			Headers:   map[string]string{"Authorization": e2eMCPGatewayAuth},
			// mcp-server 部署在本机容器（环回地址），依赖 allow_private_endpoint 放行。
			AllowPrivate: true,
		})
		if err != nil {
			t.Fatalf("拉起 MCP 网关客户端失败: %v", err)
		}
		t.Cleanup(mcpclient.Shutdown)
	}
	synced, err := mcpclient.SyncServerRegistry("demo-app", e2eMCPGatewayName)
	if err != nil {
		t.Fatalf("同步 MCP 工具注册表失败: %v", err)
	}
	if synced == 0 {
		t.Fatal("MCP 网关未同步到任何工具")
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

// TestDelegateComplexMCPToolChain 验证：子 Agent 按白名单装配工具索引，并在隔离子 run 内
// 完成「地理编码 → 天气查询」两步真实 MCP 工具链（get_tool → execute_tool 全流程）。
func TestDelegateComplexMCPToolChain(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	ctx := newHeadlessGinContext("e2e-complex")
	importE2EAgent(t, ctx, e2eGeoAgentMD)

	result, _ := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「查询上海（Shanghai）的当前天气」委派给子代理 geo-agent（agent_key: geo-agent），拿到结论后向用户转述气温。若子代理执行失败，直接汇报失败原因，不要重复委派。",
		6, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 {
		t.Fatalf("应有且仅有 1 个子 run: %d", len(subRuns))
	}
	subRun := subRuns[0]
	if subRun.AgentPath != "main/geo-agent" || subRun.State != model.ReactRunStateFinished {
		t.Fatalf("子 run 状态异常: agentPath=%s state=%s", subRun.AgentPath, subRun.State)
	}

	// 白名单端到端：子 run 工具索引快照只含两个白名单工具，不含网关其它工具。
	var snapshot []react.ReactToolIndexItem
	if err := json.Unmarshal([]byte(subRun.ToolIndexSnapshotJSON), &snapshot); err != nil {
		t.Fatalf("子 run 工具索引快照解析失败: %v", err)
	}
	names := make(map[string]bool, len(snapshot))
	for _, item := range snapshot {
		names[item.Name] = true
	}
	if !names["mcpgw_geocode_city"] || !names["mcpgw_weather_forecast"] {
		t.Fatalf("白名单工具缺失: %v", names)
	}
	if names["mcpgw_exchange_rate"] || len(snapshot) != 2 {
		t.Fatalf("白名单外工具泄漏进子 run 索引: %v", names)
	}

	// 子 run 内真实工具链：至少两轮工具结果（geocode + weather），最终回复含气温。
	messages, err := model.GetReactMessagesByRunID(ctx, subRun.RunID)
	if err != nil {
		t.Fatalf("查询子 run 消息失败: %v", err)
	}
	toolResultCount := 0
	for _, message := range messages {
		if message.MessageType == model.ReactMessageTypeToolResult {
			toolResultCount++
		}
	}
	if toolResultCount < 2 {
		t.Fatalf("子 run 应有至少 2 轮工具结果（地理编码+天气），实际 %d", toolResultCount)
	}
	subFinal := runFinalContent(t, ctx, subRun.RunID)
	if !strings.Contains(subFinal, "°") && !strings.Contains(subFinal, "气温") && !strings.Contains(subFinal, "温度") {
		t.Fatalf("子 run 最终回复应包含气温: %s", subFinal)
	}
	t.Logf("geo-agent 回复: %s", subFinal)
}

// TestDelegateComplexParallel 验证：同轮多个 delegate_agent 调用（max_parallel>1）分别派给
// 不同子 Agent，各自完成真实 MCP 工具查询并回填。
func TestDelegateComplexParallel(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)

	originalSubAgent := conf.CustomConf.LLM.React.SubAgent
	conf.CustomConf.LLM.React.SubAgent.MaxParallel = 3
	t.Cleanup(func() { conf.CustomConf.LLM.React.SubAgent = originalSubAgent })

	ctx := newHeadlessGinContext("e2e-parallel")
	importE2EAgent(t, ctx, e2eFinanceAgentMD)
	importE2EAgent(t, ctx, e2eGeoAgentMD)

	result, _ := runE2EConversation(t, ctx,
		"请把两个子任务分别委派：①调用 delegate_agent 委派给 finance-agent（agent_key: finance-agent）查询 1 USD 兑换 CNY 的最新汇率；②调用 delegate_agent 委派给 geo-agent（agent_key: geo-agent）查询上海（Shanghai）的当前天气。两件事互不依赖，请在同一次回复中同时发起两次委派，全部完成后分别转述两个结论。若某个子代理失败，直接汇报其失败原因，不要重复委派。",
		8, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 2 {
		t.Fatalf("应有 2 个子 run（finance/geo），实际 %d: %v", len(subRuns), subRunPaths(subRuns))
	}
	subByPath := make(map[string]*model.ReactRun, 2)
	for i := range subRuns {
		subByPath[subRuns[i].AgentPath] = &subRuns[i]
	}
	financeRun, hasFinance := subByPath["main/finance-agent"]
	geoRun, hasGeo := subByPath["main/geo-agent"]
	if !hasFinance || !hasGeo {
		t.Fatalf("子 run agentPath 不符: %v", subRunPaths(subRuns))
	}
	for name, run := range map[string]*model.ReactRun{"finance": financeRun, "geo": geoRun} {
		if run.State != model.ReactRunStateFinished {
			t.Fatalf("%s 子 run 应为 finished: %s", name, run.State)
		}
	}
	financeFinal := runFinalContent(t, ctx, financeRun.RunID)
	if !strings.Contains(financeFinal, "CNY") && !strings.Contains(financeFinal, "汇率") {
		t.Fatalf("finance-agent 回复应包含汇率结论: %s", financeFinal)
	}
	geoFinal := runFinalContent(t, ctx, geoRun.RunID)
	if !strings.Contains(geoFinal, "°") && !strings.Contains(geoFinal, "气温") && !strings.Contains(geoFinal, "温度") {
		t.Fatalf("geo-agent 回复应包含气温: %s", geoFinal)
	}
	// 时间区间重叠即视为发生过并行（秒级精度，弱断言：仅记录）。
	t.Logf("并行委派完成: finance[%s ~ %s] 回复=%s; geo[%s ~ %s] 回复=%s",
		financeRun.CreatedAt.Format("15:04:05"), financeRun.UpdatedAt.Format("15:04:05"), financeFinal,
		geoRun.CreatedAt.Format("15:04:05"), geoRun.UpdatedAt.Format("15:04:05"), geoFinal)
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
