package skill

import (
	model "react-base-service/models/llm"
	"testing"
	"time"

	"gorm.io/plugin/soft_delete"
)

func TestResolveCreateSkillIsDefault(t *testing.T) {
	tests := []struct {
		name    string
		input   *int
		want    int
		wantErr bool
	}{
		{
			name:  "nil defaults to zero",
			input: nil,
			want:  0,
		},
		{
			name:  "explicit zero",
			input: intPtr(0),
			want:  0,
		},
		{
			name:  "explicit one",
			input: intPtr(1),
			want:  1,
		},
		{
			name:    "invalid value",
			input:   intPtr(2),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCreateSkillIsDefault(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolveSkillStatus(t *testing.T) {
	tests := []struct {
		name    string
		input   *int
		want    int
		wantErr bool
	}{
		{
			name:    "nil status",
			input:   nil,
			wantErr: true,
		},
		{
			name:  "disabled",
			input: intPtr(0),
			want:  0,
		},
		{
			name:  "enabled",
			input: intPtr(1),
			want:  1,
		},
		{
			name:    "invalid value",
			input:   intPtr(2),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveSkillStatus(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestToSkillResp(t *testing.T) {
	createdAt := time.Date(2026, 3, 20, 10, 0, 0, 0, time.Local)
	updatedAt := time.Date(2026, 3, 21, 11, 30, 0, 0, time.Local)
	deletedAt := time.Date(2026, 3, 22, 12, 45, 0, 0, time.Local)

	resp := ToSkillResp(&model.Skill{
		SkillID:            "skill_123",
		Name:               "分析技能",
		Description:        "desc",
		TriggerCondition:   "trigger",
		ForbiddenCondition: "forbidden",
		ExecutionSteps:     "step1",
		BusinessContext:    "biz",
		PromptSupplement:   "prompt",
		AppKey:          "app",
		RouteValues:        `["space_a","report_b"]`,
		IsDefault:          0,
		Status:             1,
		CreatedBy:          "alice",
		UpdatedBy:          "bob",
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
		DeletedAt:          soft_delete.DeletedAt(deletedAt.Unix()),
	})

	if resp.CreatedBy != "alice" {
		t.Fatalf("got createdBy %q, want %q", resp.CreatedBy, "alice")
	}
	if resp.UpdatedBy != "bob" {
		t.Fatalf("got updatedBy %q, want %q", resp.UpdatedBy, "bob")
	}
	if resp.CreatedAt != "2026-03-20 10:00:00" {
		t.Fatalf("got createdAt %q", resp.CreatedAt)
	}
	if resp.UpdatedAt != "2026-03-21 11:30:00" {
		t.Fatalf("got updatedAt %q", resp.UpdatedAt)
	}
	if resp.DeletedAt != "2026-03-22 12:45:00" {
		t.Fatalf("got deletedAt %q", resp.DeletedAt)
	}
	if len(resp.RouteValues) != 2 || resp.RouteValues[0] != "space_a" || resp.RouteValues[1] != "report_b" {
		t.Fatalf("unexpected routeValues: %#v", resp.RouteValues)
	}

	resp = ToSkillResp(&model.Skill{})
	if resp.CreatedAt != "" || resp.UpdatedAt != "" || resp.DeletedAt != "" {
		t.Fatalf("expected empty timestamps, got createdAt=%q updatedAt=%q deletedAt=%q",
			resp.CreatedAt, resp.UpdatedAt, resp.DeletedAt)
	}
}

func intPtr(v int) *int {
	return &v
}
