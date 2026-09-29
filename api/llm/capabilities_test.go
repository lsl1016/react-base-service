package llm

import (
	"context"
	"testing"
)

func TestRejectVisionFilesForCaps(t *testing.T) {
	imageFile := FilePayload{FileName: "chart.png", MediaType: "image/png"}
	textFile := FilePayload{FileName: "data.csv", MediaType: "text/csv"}

	t.Run("未声明能力时不拒绝", func(t *testing.T) {
		if err := rejectVisionFilesForCaps(context.Background(), []FilePayload{imageFile}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("声明支持视觉时不拒绝", func(t *testing.T) {
		ctx := WithModelCapabilities(context.Background(), ModelCapabilities{Known: true, SupportVision: true})
		if err := rejectVisionFilesForCaps(ctx, []FilePayload{imageFile}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("声明不支持视觉时拒绝图片放行文本", func(t *testing.T) {
		ctx := WithModelCapabilities(context.Background(), ModelCapabilities{Known: true, SupportVision: false})
		if err := rejectVisionFilesForCaps(ctx, []FilePayload{textFile}); err != nil {
			t.Fatalf("text file should pass, got: %v", err)
		}
		if err := rejectVisionFilesForCaps(ctx, []FilePayload{imageFile}); err == nil {
			t.Fatal("image file should be rejected when vision disabled")
		}
	})
}

func TestLookupModelCapabilities(t *testing.T) {
	cases := []struct {
		name    string
		modelID string
		known   bool
		tools   bool
		thinking bool
		vision  bool
	}{
		{"exact 命中 glm-5.3", "glm-5.3", true, true, true, false},
		{"exact 命中大小写归一", "GLM-5.3", true, true, true, false},
		{"前缀命中未知 glm 版本", "glm-6.9-preview", true, true, true, false},
		{"视觉变体前缀优先于家族前缀", "glm-4.5v-plus", true, true, true, true},
		{"claude 前缀三能力全开", "claude-sonnet-4.6", true, true, true, true},
		{"gpt 前缀默认无思考", "gpt-4o-mini", true, true, false, true},
		{"deepseek 前缀", "deepseek-r2", true, true, true, false},
		{"未命中返回未知", "llama-3-somewhere", false, false, false, false},
		{"空串未知", "  ", false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := LookupModelCapabilities(tc.modelID)
			if caps.Known != tc.known {
				t.Fatalf("Known = %v, want %v", caps.Known, tc.known)
			}
			if caps.Known {
				if caps.SupportTools != tc.tools || caps.SupportThinking != tc.thinking || caps.SupportVision != tc.vision {
					t.Fatalf("caps = %+v, want tools=%v thinking=%v vision=%v", caps, tc.tools, tc.thinking, tc.vision)
				}
			}
		})
	}
}
