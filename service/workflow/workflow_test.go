package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	model "react-base-service/models/llm"
)

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
