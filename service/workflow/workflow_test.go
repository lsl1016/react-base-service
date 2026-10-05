package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// setReportPrompt 临时翻转报告纪律开关（测试间互不影响）。
func setReportPrompt(enabled bool) {
	conf.CustomConf.LLM.Workflow.Report.PromptEnabled = enabled
}

func TestEvaluateRiskLevel(t *testing.T) {
	patterns := "发现异常\nP0|P1\n失败率.*超过"
	cases := []struct {
		summary string
		want    string
	}{
		{"巡检完成，一切正常", model.WorkflowRiskLevelNone},
		{"检查发现异常队列堆积 3 条", model.WorkflowRiskLevelHigh},
		{"出现 P0 故障", model.WorkflowRiskLevelHigh},
		{"失败率超过阈值 5%", model.WorkflowRiskLevelHigh},
		{"", model.WorkflowRiskLevelNone},
	}
	for _, c := range cases {
		if got := evaluateRiskLevel(c.summary, patterns); got != c.want {
			t.Errorf("evaluateRiskLevel(%q) = %s, want %s", c.summary, got, c.want)
		}
	}
	// 非法正则行跳过、不 panic、不误判。
	if got := evaluateRiskLevel("任意文本", "([非法正则"); got != model.WorkflowRiskLevelNone {
		t.Errorf("非法正则应被跳过，got %s", got)
	}
}

func TestReplaceWorkflowPlaceholders(t *testing.T) {
	wf := &model.Workflow{Name: "每日巡检"}
	got := replaceWorkflowPlaceholders("今天是 {{date}} {{datetime}}，任务 {{workflow}}。", wf)
	if !strings.Contains(got, "任务 每日巡检") {
		t.Errorf("{{workflow}} 未替换: %s", got)
	}
	if strings.Contains(got, "{{date}}") || strings.Contains(got, "{{datetime}}") {
		t.Errorf("日期占位符未替换: %s", got)
	}
}

func TestRenderWorkflowPromptDiscipline(t *testing.T) {
	wf := &model.Workflow{Name: "巡检", Prompt: "检查 bd-cron"}
	got := renderWorkflowPrompt(wf)
	if !strings.Contains(got, "检查 bd-cron") {
		t.Errorf("任务提示词丢失: %s", got)
	}
	if !strings.Contains(got, "ask_question") {
		t.Errorf("缺无人值守纪律前缀: %s", got)
	}
}

func TestRenderWorkflowPromptReportDiscipline(t *testing.T) {
	wf := &model.Workflow{Name: "巡检", Prompt: "检查 bd-cron"}
	// 关闭时无报告要求；开启时要求 markdown 报告产物。
	if got := renderWorkflowPrompt(wf); strings.Contains(got, "markdown") {
		t.Errorf("报告纪律默认应关闭: %s", got)
	}
	setReportPrompt(true)
	defer setReportPrompt(false)
	got := renderWorkflowPrompt(wf)
	if !strings.Contains(got, "markdown") || !strings.Contains(got, "python_exec") {
		t.Errorf("报告纪律未注入: %s", got)
	}
	if !strings.Contains(got, "report-") {
		t.Errorf("报告文件名缺失: %s", got)
	}
}

func TestRenderJudgeFollowUpPrompt(t *testing.T) {
	got := renderJudgeFollowUpPrompt("补充慢 SQL 分析", 2)
	if !strings.Contains(got, "补充慢 SQL 分析") || !strings.Contains(got, `round="2"`) {
		t.Fatalf("追问 prompt 渲染错误: %s", got)
	}
	if !strings.Contains(got, "不要重头再来") {
		t.Fatalf("追问 prompt 缺续跑纪律: %s", got)
	}
}

func TestShouldTripFailureBreaker(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []string
		threshold int
		want      bool
	}{
		{"连续 3 次失败", []string{"failed", "failed", "failed"}, 3, true},
		{"失败+超时混合", []string{"timeout", "failed", "timeout"}, 3, true},
		{"中间有一次成功", []string{"failed", "completed", "failed"}, 3, false},
		{"样本不足", []string{"failed", "failed"}, 3, false},
		{"阈值关闭", []string{"failed", "failed", "failed"}, 0, false},
		{"最近一次成功清零", []string{"completed", "failed", "failed"}, 3, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldTripFailureBreaker(c.statuses, c.threshold); got != c.want {
				t.Fatalf("shouldTripFailureBreaker(%v, %d) = %v, want %v", c.statuses, c.threshold, got, c.want)
			}
		})
	}
}

func TestJudgeRiskToWorkflowRisk(t *testing.T) {
	cases := []struct {
		judgeRisk, keywordRisk, want string
	}{
		{model.WorkflowRiskLevelHigh, model.WorkflowRiskLevelNone, model.WorkflowRiskLevelHigh},
		{model.WorkflowRiskLevelLow, model.WorkflowRiskLevelNone, model.WorkflowRiskLevelLow},
		{model.WorkflowRiskLevelLow, model.WorkflowRiskLevelHigh, model.WorkflowRiskLevelHigh},
		{model.WorkflowRiskLevelNone, model.WorkflowRiskLevelHigh, model.WorkflowRiskLevelHigh},
		{model.WorkflowRiskLevelNone, model.WorkflowRiskLevelNone, model.WorkflowRiskLevelNone},
	}
	for _, c := range cases {
		if got := judgeRiskToWorkflowRisk(c.judgeRisk, c.keywordRisk); got != c.want {
			t.Errorf("judgeRiskToWorkflowRisk(%s, %s) = %s, want %s", c.judgeRisk, c.keywordRisk, got, c.want)
		}
	}
}

func TestIsMarkdownArtifact(t *testing.T) {
	if !isMarkdownArtifact(&model.ReactArtifact{MimeType: "text/markdown", FileName: "report.md"}) {
		t.Error("mime markdown 应命中")
	}
	if !isMarkdownArtifact(&model.ReactArtifact{MimeType: "", FileName: "Report.MD"}) {
		t.Error(".md 后缀应命中（大小写不敏感）")
	}
	if isMarkdownArtifact(&model.ReactArtifact{MimeType: "image/png", FileName: "shot.png"}) {
		t.Error("非 markdown 不应命中")
	}
}

func TestBuildWebhookPayload(t *testing.T) {
	feishu := buildWebhookPayload("https://open.feishu.cn/open-apis/bot/v2/hook/xxx", "内容")
	if feishu["msg_type"] != "text" {
		t.Errorf("飞书 payload 格式错误: %v", feishu)
	}
	wecom := buildWebhookPayload("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx", "内容")
	if wecom["msgtype"] != "markdown" {
		t.Errorf("企业微信 payload 格式错误: %v", wecom)
	}
	if got := buildWebhookPayload("://bad-url", "内容"); got != nil {
		t.Errorf("非法 URL 应返回 nil，got %v", got)
	}
}

func TestExtractAssistantText(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"modelMessage": map[string]any{"role": "assistant", "content": "巡检结论：正常"},
	})
	if got := extractAssistantText(string(content)); got != "巡检结论：正常" {
		t.Errorf("extractAssistantText = %q", got)
	}
	// 空/坏 JSON 安全返回空串。
	if got := extractAssistantText(""); got != "" {
		t.Errorf("空输入应返回空串，got %q", got)
	}
	if got := extractAssistantText("{bad json"); got != "" {
		t.Errorf("坏 JSON 应返回空串，got %q", got)
	}
}

func TestExtractUserInputText(t *testing.T) {
	content, _ := json.Marshal(map[string]any{"content": "巡检 bd-cron", "controlContext": json.RawMessage(`{}`)})
	if got := extractUserInputText(string(content)); got != "巡检 bd-cron" {
		t.Errorf("extractUserInputText = %q", got)
	}
}

func TestResolveTerminalStatusMapping(t *testing.T) {
	// 注意不依赖 react 包运行态，仅验证 error 分支映射（成功分支需查库，不在此覆盖）。
	status, _ := resolveTerminalStatus(nil, context.DeadlineExceeded, nil)
	if status != model.WorkflowRunStatusTimeout {
		t.Errorf("DeadlineExceeded 应映射 timeout，got %s", status)
	}
}

func TestParseRouteValues(t *testing.T) {
	if got := parseRouteValues(`["a","b"]`); len(got) != 2 || got[0] != "a" {
		t.Errorf("parseRouteValues = %v", got)
	}
	if got := parseRouteValues(""); got != nil {
		t.Errorf("空串应返回 nil，got %v", got)
	}
	if got := parseRouteValues("not json"); got != nil {
		t.Errorf("非法 JSON 应返回 nil，got %v", got)
	}
}

func TestSummarizeToolArtifacts(t *testing.T) {
	content := `{"toolUseId":"c1","content":"{\"exitCode\":0,\"stdout\":\"{}\",\"artifacts\":[{\"type\":\"text/markdown\",\"name\":\"e2e-report.md\",\"bytes\":31}]}"}`
	got := summarizeToolArtifacts(content)
	if !strings.Contains(got, "e2e-report.md") {
		t.Errorf("产物摘要应含文件名，got %q", got)
	}
	if got := summarizeToolArtifacts(`{"stdout":"ok"}`); got != "" {
		t.Errorf("无产物应返回空串，got %q", got)
	}
	if got := summarizeToolArtifacts(""); got != "" {
		t.Errorf("空输入应返回空串，got %q", got)
	}
}

func TestDecodeMessageParts(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"modelMessage": map[string]any{
			"role": "assistant",
			"parts": []map[string]any{
				{"type": "tool_use", "id": "call_1", "name": "python_exec"},
			},
		},
	})
	parts := decodeMessageParts(string(content))
	if len(parts) != 1 || parts[0].Name != "python_exec" || parts[0].ID != "call_1" {
		t.Fatalf("decodeMessageParts = %+v", parts)
	}
	if parts := decodeMessageParts("{bad"); parts != nil {
		t.Fatalf("坏 JSON 应返回 nil，got %+v", parts)
	}
}
