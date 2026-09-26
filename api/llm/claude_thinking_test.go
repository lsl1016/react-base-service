package llm

import (
	"context"
	"testing"

	"react-base-service/conf"
)

// claudeThinkingSettingsForModel 三态翻译表：
// off → nil；auto → maxTokens/4；custom 预算直传；effort 档位折算；
// 上下界钳位（>=1024 且 < maxTokens）；目录能力开关优先于旧启发式。
func TestClaudeThinkingSettingsForModel(t *testing.T) {
	supports := true
	notSupports := false
	conf.CustomConf.LLM.Models = map[string]conf.ModelCatalog{
		"glm": {
			DefaultVersion:   "glm-4.6",
			SupportsThinking: &supports,
		},
		"nonthinking": {
			DefaultVersion:   "no-think-1",
			SupportsThinking: &notSupports,
		},
	}

	cases := []struct {
		name      string
		opts      ReasoningOptions
		model     string
		maxTokens int
		wantType  string
		wantBudget int
	}{
		{
			name:  "off 不请求思考块",
			opts:  ReasoningOptions{Mode: ReasoningModeOff},
			model: "glm-4.6", maxTokens: 32768,
			wantType: "",
		},
		{
			name:  "auto 按窗口四分之一推导预算",
			opts:  ReasoningOptions{Mode: ReasoningModeAuto},
			model: "glm-4.6", maxTokens: 32768,
			wantType: "enabled", wantBudget: 8192,
		},
		{
			name:  "custom 预算直传",
			opts:  ReasoningOptions{Mode: ReasoningModeCustom, BudgetTokens: 20480},
			model: "glm-4.6", maxTokens: 32768,
			wantType: "enabled", wantBudget: 20480,
		},
		{
			name:  "custom effort 档位折算预算",
			opts:  ReasoningOptions{Mode: ReasoningModeCustom, Effort: "low"},
			model: "glm-4.6", maxTokens: 32768,
			wantType: "enabled", wantBudget: 4096,
		},
		{
			name:  "预算低于下限钳到 1024",
			opts:  ReasoningOptions{Mode: ReasoningModeCustom, BudgetTokens: 1},
			model: "glm-4.6", maxTokens: 32768,
			wantType: "enabled", wantBudget: 1024,
		},
		{
			name:  "预算超过 maxTokens 时向下收口且不越界",
			opts:  ReasoningOptions{Mode: ReasoningModeCustom, BudgetTokens: 40000},
			model: "glm-4.6", maxTokens: 32768,
			wantType: "enabled", wantBudget: 31744,
		},
		{
			name:  "maxTokens 过小直接关闭",
			opts:  ReasoningOptions{Mode: ReasoningModeAuto},
			model: "glm-4.6", maxTokens: 1024,
			wantType: "",
		},
		{
			name:  "目录显式声明不支持时关闭",
			opts:  ReasoningOptions{Mode: ReasoningModeAuto},
			model: "no-think-1", maxTokens: 32768,
			wantType: "",
		},
		{
			name:  "目录未命中回退旧启发式（claude+4 模型名）",
			opts:  ReasoningOptions{Mode: ReasoningModeAuto},
			model: "claude-sonnet-4-20250514", maxTokens: 8192,
			wantType: "enabled", wantBudget: 2048,
		},
		{
			name:  "目录未命中且非 claude 模型关闭",
			opts:  ReasoningOptions{Mode: ReasoningModeAuto},
			model: "unknown-model-9", maxTokens: 32768,
			wantType: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithReasoning(context.Background(), tc.opts)
			got := claudeThinkingSettingsForModel(ctx, tc.model, tc.maxTokens)
			if tc.wantType == "" {
				if got != nil {
					t.Fatalf("expected nil thinking, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected thinking settings, got nil")
			}
			if got.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", got.Type, tc.wantType)
			}
			if got.BudgetTokens != tc.wantBudget {
				t.Fatalf("budget = %d, want %d", got.BudgetTokens, tc.wantBudget)
			}
		})
	}
}

// 无目录配置时 legacy 启发式仍生效，且不再受历史 [1024,4096] 钳位限制。
func TestClaudeThinkingBudgetNotClampedToLegacyRange(t *testing.T) {
	conf.CustomConf.LLM.Models = map[string]conf.ModelCatalog{}
	opts := ReasoningOptions{Mode: ReasoningModeCustom, BudgetTokens: 16384}
	ctx := WithReasoning(context.Background(), opts)
	got := claudeThinkingSettingsForModel(ctx, "claude-sonnet-4-5", 32768)
	if got == nil || got.BudgetTokens != 16384 {
		t.Fatalf("budget = %+v, want 16384", got)
	}
}

// ReasoningEffortForGPT 三态翻译表：off → 空；auto → high；custom 档位直传；预算折算档位。
func TestReasoningEffortForGPT(t *testing.T) {
	cases := []struct {
		name string
		opts ReasoningOptions
		want string
	}{
		{"off 返回空", ReasoningOptions{Mode: ReasoningModeOff, Enabled: false}, ""},
		{"auto 保持历史默认 high", ReasoningOptions{Mode: ReasoningModeAuto, Enabled: true}, "high"},
		{"custom 档位直传", ReasoningOptions{Mode: ReasoningModeCustom, Enabled: true, Effort: "medium"}, "medium"},
		{"custom 仅预算按阈值折算", ReasoningOptions{Mode: ReasoningModeCustom, Enabled: true, BudgetTokens: 8000}, "medium"},
		{"custom 非法档位回退 auto", ReasoningOptions{Mode: ReasoningModeCustom, Enabled: true, Effort: "ultra"}, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReasoningEffortForGPT(tc.opts); got != tc.want {
				t.Fatalf("effort = %q, want %q", got, tc.want)
			}
		})
	}
}

// NormalizeReasoningOptions：空 Mode 补 auto；未知 Mode 收敛 auto；off 关闭 Enabled。
func TestNormalizeReasoningOptions(t *testing.T) {
	normalized := NormalizeReasoningOptions(ReasoningOptions{})
	if normalized.Mode != ReasoningModeAuto || !normalized.Enabled {
		t.Fatalf("default normalize broken: %+v", normalized)
	}
	normalized = NormalizeReasoningOptions(ReasoningOptions{Mode: "weird"})
	if normalized.Mode != ReasoningModeAuto {
		t.Fatalf("unknown mode should fall back auto: %+v", normalized)
	}
	normalized = NormalizeReasoningOptions(ReasoningOptions{Mode: ReasoningModeOff})
	if normalized.Enabled {
		t.Fatalf("off should disable: %+v", normalized)
	}
}
