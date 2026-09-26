package react

import (
	"testing"

	llm "react-base-service/api/llm"
	"react-base-service/components/params"
)

// parseReasoningOptions：nil 兼容旧客户端按 auto；三态与档位/预算校验。
func TestParseReasoningOptions(t *testing.T) {
	if got, err := parseReasoningOptions(nil); err != nil || got.Mode != llm.ReasoningModeAuto || !got.Enabled {
		t.Fatalf("nil should default to auto: %+v, err=%v", got, err)
	}

	cases := []struct {
		name      string
		payload   *params.ReactReasoningOptions
		wantMode  llm.ReasoningMode
		wantErr   bool
	}{
		{"off 关闭", &params.ReactReasoningOptions{Mode: "off"}, llm.ReasoningModeOff, false},
		{"auto 自适应", &params.ReactReasoningOptions{Mode: "auto"}, llm.ReasoningModeAuto, false},
		{"custom 预算", &params.ReactReasoningOptions{Mode: "custom", BudgetTokens: 16384}, llm.ReasoningModeCustom, false},
		{"custom 档位", &params.ReactReasoningOptions{Mode: "custom", Effort: "medium"}, llm.ReasoningModeCustom, false},
		{"空 mode 按 auto 兼容", &params.ReactReasoningOptions{}, llm.ReasoningModeAuto, false},
		{"非法 mode", &params.ReactReasoningOptions{Mode: "turbo"}, "", true},
		{"非法 effort", &params.ReactReasoningOptions{Mode: "custom", Effort: "ultra"}, "", true},
		{"预算越上界", &params.ReactReasoningOptions{Mode: "custom", BudgetTokens: reasoningBudgetMax + 1}, "", true},
		{"预算为负", &params.ReactReasoningOptions{Mode: "custom", BudgetTokens: -1}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReasoningOptions(tc.payload)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", got.Mode, tc.wantMode)
			}
		})
	}
}

// off 必须表现为 Enabled=false，保证协议侧不下发思考参数。
func TestParseReasoningOptionsOffDisables(t *testing.T) {
	got, err := parseReasoningOptions(&params.ReactReasoningOptions{Mode: "off"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Enabled {
		t.Fatalf("off should disable reasoning: %+v", got)
	}
}
