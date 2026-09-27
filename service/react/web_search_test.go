package react

import (
	"strings"
	"testing"

	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// TestValidateContinueRun：continue_run_id 的可续跑性校验（存在性/归属/agent 一致/终态）。
func TestValidateContinueRun(t *testing.T) {
	const parentID = "parent-1"

	// 查询失败
	if reason := validateContinueRun(nil, "r1", parentID, "echo-agent", true); reason == "" {
		t.Fatalf("查询失败应拒绝续跑")
	}
	// run 不存在
	if reason := validateContinueRun(nil, "r1", parentID, "echo-agent", false); reason == "" || !strings.Contains(reason, "不存在") {
		t.Fatalf("run 不存在应拒绝: %q", reason)
	}
	// 不是本 run 委派的子 run（防跨 run 窥探）
	other := &model.ReactRun{ParentRunID: "other-parent", AgentPath: "main/echo-agent", State: model.ReactRunStateFinished}
	if reason := validateContinueRun(other, "r1", parentID, "echo-agent", false); reason == "" || !strings.Contains(reason, "不允许跨 run") {
		t.Fatalf("跨 run 应拒绝: %q", reason)
	}
	// agent_key 不一致（run 属于本 run，但 agentPath 是别的 agent）
	mismatch := &model.ReactRun{ParentRunID: parentID, AgentPath: "main/echo-agent", State: model.ReactRunStateFinished}
	if reason := validateContinueRun(mismatch, "r1", parentID, "dba-agent", false); reason == "" || !strings.Contains(reason, "同一个子 Agent") {
		t.Fatalf("agent 不一致应拒绝: %q", reason)
	}
	// 仍在运行
	running := &model.ReactRun{ParentRunID: parentID, AgentPath: "main/echo-agent", State: model.ReactRunStateRunning}
	if reason := validateContinueRun(running, "r1", parentID, "echo-agent", false); reason == "" || !strings.Contains(reason, "wait_agent") {
		t.Fatalf("运行中应引导 wait_agent: %q", reason)
	}
	// HITL 等待
	waiting := &model.ReactRun{ParentRunID: parentID, AgentPath: "main/echo-agent", State: model.ReactRunStateWaitingClientMessage}
	if reason := validateContinueRun(waiting, "r1", parentID, "echo-agent", false); reason == "" || !strings.Contains(reason, "send_message") {
		t.Fatalf("HITL 等待应引导 send_message: %q", reason)
	}
	// 已终态 → 通过
	finished := &model.ReactRun{ParentRunID: parentID, AgentPath: "main/echo-agent", State: model.ReactRunStateFinished}
	if reason := validateContinueRun(finished, "r1", parentID, "echo-agent", false); reason != "" {
		t.Fatalf("已终态应允许续跑: %q", reason)
	}
	if reason := validateContinueRun(&model.ReactRun{ParentRunID: parentID, AgentPath: "main/echo-agent", State: model.ReactRunStateTimeout}, "r1", parentID, "echo-agent", false); reason != "" {
		t.Fatalf("timeout 终态应允许续跑: %q", reason)
	}
}

// TestParseSearxngResponse：SearXNG JSON 解析（空 title/url 丢弃、maxResults 截断、snippet 截断）。
func TestParseSearxngResponse(t *testing.T) {
	body := []byte(`{"results":[
		{"title":"结果一","url":"https://a.example/1","content":"第一段摘要"},
		{"title":"","url":"https://a.example/2","content":"缺标题应丢弃"},
		{"title":"结果三","url":"","content":"缺链接应丢弃"},
		{"title":"结果四","url":"https://a.example/4","content":"很长很长很长"}
	]}`)
	results, err := parseSearxngResponse(body, 10)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("应保留 2 条有效结果: %+v", results)
	}
	if results[0].Title != "结果一" || results[0].URL != "https://a.example/1" || results[0].Snippet != "第一段摘要" {
		t.Fatalf("首条结果不符: %+v", results[0])
	}

	// maxResults 截断
	results, err = parseSearxngResponse(body, 1)
	if err != nil || len(results) != 1 {
		t.Fatalf("maxResults 截断不符: n=%d, err=%v", len(results), err)
	}
	// 非法 JSON
	if _, err := parseSearxngResponse([]byte(`{bad`), 5); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
}

// TestRenderWebSearchResults：结果清单渲染与截断。
func TestRenderWebSearchResults(t *testing.T) {
	rendered := renderWebSearchResults("golang 测试", []webSearchResult{
		{Title: "Go 测试指南", URL: "https://go.dev/testing", Snippet: "如何写测试"},
	})
	for _, want := range []string{"golang 测试", "Go 测试指南", "https://go.dev/testing", "如何写测试", "web_fetch"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("渲染结果缺少 %q: %q", want, rendered)
		}
	}
	// 超长结果集截断到上限
	long := make([]webSearchResult, 0, 30)
	for i := 0; i < 30; i++ {
		long = append(long, webSearchResult{Title: strings.Repeat("标", 300), URL: "https://x.example"})
	}
	if got := renderWebSearchResults("q", long); len([]rune(got)) > webSearchMaxOutputRunes+50 {
		t.Fatalf("渲染结果应被截断: runes=%d", len([]rune(got)))
	}
}

// TestWebSearchProfileGate：conf 门控（开关 + kind/base_url 完整性）。
func TestWebSearchProfileGate(t *testing.T) {
	original := conf.CustomConf.LLM.React.WebSearch
	defer func() { conf.CustomConf.LLM.React.WebSearch = original }()

	enabled := true
	// 开关开但配置不完整 → 不可执行
	conf.CustomConf.LLM.React.WebSearch = conf.ReactWebSearchConfig{Enabled: &enabled}
	if webSearchProfileEnabled() {
		t.Fatalf("kind/base_url 缺失时不应放行")
	}
	// 完整配置 → 放行
	conf.CustomConf.LLM.React.WebSearch = conf.ReactWebSearchConfig{Enabled: &enabled, Kind: "searxng", BaseURL: "http://searxng:8080"}
	if !webSearchProfileEnabled() {
		t.Fatalf("完整 searxng 配置应放行")
	}
	// 开关关 → 不可执行
	disabled := false
	conf.CustomConf.LLM.React.WebSearch = conf.ReactWebSearchConfig{Enabled: &disabled, Kind: "searxng", BaseURL: "http://searxng:8080"}
	if webSearchProfileEnabled() {
		t.Fatalf("开关关闭时不应放行")
	}
}
