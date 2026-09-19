package plan

import (
	"testing"

	model "react-base-service/models/llm"
)

func boolPtr(value bool) *bool { return &value }

func TestValidateDraftNormalizesLinearPlan(t *testing.T) {
	draft := PlanDraft{
		Title: "检查并修复任务",
		Steps: []PlanDraftStep{
			{
				StepKey: "load_task",
				Name: "获取任务",
				Instruction: "读取任务基本信息",
				StepType: model.PlanStepTypeAgent,
				MaxAttempts: 9,
			},
			{
				StepKey: "approve_fix",
				Name: "确认修复",
				Instruction: "等待用户确认修复方案",
				StepType: model.PlanStepTypeUserAction,
				Required: boolPtr(true),
			},
			{
				StepKey: "apply_fix",
				Name: "执行修复",
				Instruction: "应用已经确认的修复",
				StepType: model.PlanStepTypeAgent,
			},
		},
	}
	if err := validateDraft(&draft); err != nil {
		t.Fatalf("validateDraft() error = %v", err)
	}
	if got := draft.Steps[0].MaxAttempts; got != maxAgentStepAttempts {
		t.Fatalf("agent maxAttempts should be capped: got=%d want=%d", got, maxAgentStepAttempts)
	}
	if got := draft.Steps[1].MaxAttempts; got != 1 {
		t.Fatalf("user action maxAttempts should be 1: got=%d", got)
	}
	if len(draft.Steps[2].DependsOn) != 1 || draft.Steps[2].DependsOn[0] != "approve_fix" {
		t.Fatalf("missing linear dependency normalization: %#v", draft.Steps[2].DependsOn)
	}
}

func TestValidateDraftAddsUserInputSchema(t *testing.T) {
	draft := PlanDraft{
		Title: "补充环境",
		Steps: []PlanDraftStep{{
			StepKey: "ask_env",
			Name: "询问环境",
			Instruction: "请提供目标环境",
			StepType: model.PlanStepTypeUserInput,
		}},
	}
	if err := validateDraft(&draft); err != nil {
		t.Fatalf("validateDraft() error = %v", err)
	}
	step := draft.Steps[0]
	if step.Question != step.Instruction {
		t.Fatalf("question should default to instruction: got=%q", step.Question)
	}
	required, ok := step.ResponseSchema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "answer" {
		t.Fatalf("unexpected default response schema: %#v", step.ResponseSchema)
	}
}

func TestValidateDraftRejectsForwardDependency(t *testing.T) {
	draft := PlanDraft{
		Title: "非法计划",
		Steps: []PlanDraftStep{
			{
				StepKey: "first",
				Name: "第一步",
				Instruction: "第一步",
				StepType: model.PlanStepTypeAgent,
				DependsOn: []string{"later"},
			},
			{
				StepKey: "later",
				Name: "第二步",
				Instruction: "第二步",
				StepType: model.PlanStepTypeAgent,
			},
		},
	}
	if err := validateDraft(&draft); err == nil {
		t.Fatal("validateDraft() should reject forward dependency")
	}
}

func TestValidateDraftRejectsUnsupportedStepType(t *testing.T) {
	draft := PlanDraft{
		Title: "非法类型",
		Steps: []PlanDraftStep{{
			StepKey: "external",
			Name: "外部等待",
			Instruction: "等待外部任务",
			StepType: "EXTERNAL_TASK",
		}},
	}
	if err := validateDraft(&draft); err == nil {
		t.Fatal("validateDraft() should reject unsupported V1 step type")
	}
}


func TestValidateWaitResponseRequiresExplicitUserActionDecision(t *testing.T) {
	wait := &model.PlanWait{WaitType: model.PlanWaitTypeUserAction}
	if err := validateWaitResponse(wait, map[string]interface{}{}); err == nil {
		t.Fatal("USER_ACTION without approved should be rejected")
	}
	if err := validateWaitResponse(wait, map[string]interface{}{"approved": "true"}); err == nil {
		t.Fatal("USER_ACTION approved must be boolean")
	}
	if err := validateWaitResponse(wait, map[string]interface{}{"approved": true}); err != nil {
		t.Fatalf("USER_ACTION approved=true should pass: %v", err)
	}
	if err := validateWaitResponse(wait, map[string]interface{}{"approved": false}); err != nil {
		t.Fatalf("USER_ACTION approved=false should pass: %v", err)
	}
}
