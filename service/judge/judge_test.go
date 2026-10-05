package judge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubInvoker struct {
	raw string
	err error
}

func (s stubInvoker) ChatOnce(_ context.Context, _, _ string) (string, error) {
	return s.raw, s.err
}

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		achieved   bool
		risk       string
		followUp   string
		wantErr    bool
	}{
		{
			name:     "纯 JSON",
			raw:      `{"achieved":true,"risk_level":"none","reason":"完成","follow_up":""}`,
			achieved: true, risk: RiskNone,
		},
		{
			name:     "json 围栏",
			raw:      "```json\n{\"achieved\":false,\"risk_level\":\"high\",\"reason\":\"缺 DBA 分析\",\"follow_up\":\"补充慢 SQL 分析\"}\n```",
			achieved: false, risk: RiskHigh, followUp: "补充慢 SQL 分析",
		},
		{
			name:     "前后杂文",
			raw:      "判定如下：\n{\"achieved\":false,\"risk_level\":\"low\",\"reason\":\"部分完成\",\"follow_up\":\"继续\"}\n以上。",
			achieved: false, risk: RiskLow, followUp: "继续",
		},
		{
			name:    "无 JSON",
			raw:     "我判定完成了",
			wantErr: true,
		},
		{
			name:    "坏 JSON",
			raw:     `{"achieved":,}`,
			wantErr: true,
		},
		{
			name:     "未知风险级别归 none",
			raw:      `{"achieved":true,"risk_level":"extreme","reason":"r"}`,
			achieved: true, risk: RiskNone,
		},
		{
			name: "JSON 含花括号字符串",
			raw:  `{"achieved":false,"risk_level":"high","reason":"发现 {异常} 情况","follow_up":"处理 {}"}`,
			risk: RiskHigh, followUp: "处理 {}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict, err := ParseVerdict(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，got %+v", verdict)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if verdict.Achieved != c.achieved || verdict.RiskLevel != c.risk || verdict.FollowUp != c.followUp {
				t.Fatalf("verdict = %+v, want achieved=%v risk=%s followUp=%q", verdict, c.achieved, c.risk, c.followUp)
			}
		})
	}
}

func TestLLMJudgeReview(t *testing.T) {
	judge := NewLLMJudge(stubInvoker{raw: `{"achieved":false,"risk_level":"low","reason":"目标只完成一半","follow_up":"继续后半部分"}`}, 0)
	if judge == nil {
		t.Fatal("invoker 非空时不应返回 nil")
	}
	verdict, err := judge.Review(context.Background(), Input{
		Goal: "巡检 bd-cron",
		Transcript: []Message{
			{Role: RoleUser, Content: "巡检 bd-cron"},
			{Role: RoleAssistant, Content: "只查了错误日志"},
		},
	})
	if err != nil {
		t.Fatalf("Review 报错: %v", err)
	}
	if verdict.Achieved || verdict.FollowUp != "继续后半部分" {
		t.Fatalf("verdict = %+v", verdict)
	}
	if !strings.Contains(verdict.Reason, "一半") {
		t.Fatalf("reason = %q", verdict.Reason)
	}
}

func TestLLMJudgeInvokerError(t *testing.T) {
	judge := NewLLMJudge(stubInvoker{err: errors.New("llm down")}, 0)
	if _, err := judge.Review(context.Background(), Input{Goal: "g"}); err == nil {
		t.Fatal("invoker 报错应向上传播（调用方 fail-open）")
	}
}

func TestNewLLMJudgeNilInvoker(t *testing.T) {
	if NewLLMJudge(nil, 0) != nil {
		t.Fatal("nil invoker 应返回 nil（表示未启用）")
	}
}

func TestRenderTranscript(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: "任务"},
		{Role: RoleAssistant, Content: strings.Repeat("长", 3000)},
		{Role: RoleUser, Content: "最近一条"},
	}
	got := RenderTranscript(messages, 4000)
	// 最近消息必须保留，更早的超长消息被单条截断。
	if !strings.Contains(got, "最近一条") {
		t.Fatalf("最近消息丢失: %s", got)
	}
	if !strings.Contains(got, "…(截断)") {
		t.Fatalf("超长消息未截断: %s", got)
	}
	if RenderTranscript(nil, 0) != "(空)" {
		t.Fatal("空转录应渲染 (空)")
	}
	// 总长预算不足时丢弃更早内容并标注。
	small := RenderTranscript(messages, 50)
	if !strings.Contains(small, "最近一条") || !strings.Contains(small, "已省略") {
		t.Fatalf("小预算应保最近弃最早: %s", small)
	}
}
