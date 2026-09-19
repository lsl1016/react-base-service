package plan

import (
	"encoding/json"
	"strings"
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

func TestValidateDraftRepairsInvalidStepKeys(t *testing.T) {
	draft := PlanDraft{
		Title: "stepKey 修复",
		Steps: []PlanDraftStep{
			{
				StepKey: "", // 模型漏填：从 name 推导
				Name: "Collect 任务数据",
				Instruction: "第一步",
				StepType: model.PlanStepTypeAgent,
			},
			{
				StepKey: "步骤二", // 含中文：从 name 推导，且与第一步推导结果相同（触发去重后缀）
				Name: "Collect 任务数据",
				Instruction: "第二步",
				StepType: model.PlanStepTypeAgent,
			},
			{
				StepKey: "3rd_step", // 数字开头非法
				Name: "第三步",
				Instruction: "第三步",
				StepType: model.PlanStepTypeAgent,
			},
		},
	}
	if err := validateDraft(&draft); err != nil {
		t.Fatalf("validateDraft() should repair invalid stepKeys instead of failing: %v", err)
	}
	for index, step := range draft.Steps {
		if !planStepKeyPattern.MatchString(step.StepKey) {
			t.Fatalf("step %d key %q should match pattern after repair", index, step.StepKey)
		}
	}
	if draft.Steps[0].StepKey == draft.Steps[1].StepKey {
		t.Fatalf("repaired keys should be unique: %q", draft.Steps[0].StepKey)
	}
	// 线性依赖自动补链引用的是修复后的 key
	if got := draft.Steps[1].DependsOn; len(got) != 1 || got[0] != draft.Steps[0].StepKey {
		t.Fatalf("auto dependency should reference repaired key: %#v", got)
	}
}

func TestValidateDraftRepairsDuplicateStepKey(t *testing.T) {
	draft := PlanDraft{
		Title: "重复 key",
		Steps: []PlanDraftStep{
			{StepKey: "run_job", Name: "一", Instruction: "一", StepType: model.PlanStepTypeAgent},
			{StepKey: "run_job", Name: "二", Instruction: "二", StepType: model.PlanStepTypeAgent},
		},
	}
	if err := validateDraft(&draft); err != nil {
		t.Fatalf("duplicate stepKey should be deduped instead of failing: %v", err)
	}
	if draft.Steps[0].StepKey == draft.Steps[1].StepKey {
		t.Fatalf("duplicate keys should be deduped: %q", draft.Steps[0].StepKey)
	}
}

func TestValidateDraftClampsTimeoutSeconds(t *testing.T) {
	draft := PlanDraft{
		Title: "超时钳制",
		Steps: []PlanDraftStep{
			{StepKey: "small", Name: "a", Instruction: "a", StepType: model.PlanStepTypeAgent, TimeoutSeconds: 5},
			{StepKey: "large", Name: "b", Instruction: "b", StepType: model.PlanStepTypeAgent, TimeoutSeconds: 99999},
			{StepKey: "unset", Name: "c", Instruction: "c", StepType: model.PlanStepTypeAgent},
			{StepKey: "ask", Name: "d", Instruction: "d", StepType: model.PlanStepTypeUserInput, TimeoutSeconds: 120},
		},
	}
	if err := validateDraft(&draft); err != nil {
		t.Fatalf("validateDraft() error = %v", err)
	}
	if got := draft.Steps[0].TimeoutSeconds; got != minStepTimeoutSeconds {
		t.Fatalf("timeout below minimum should clamp up: got=%d want=%d", got, minStepTimeoutSeconds)
	}
	if got := draft.Steps[1].TimeoutSeconds; got != maxStepTimeoutSeconds {
		t.Fatalf("timeout above maximum should clamp down: got=%d want=%d", got, maxStepTimeoutSeconds)
	}
	if got := draft.Steps[2].TimeoutSeconds; got != 0 {
		t.Fatalf("unset timeout should stay 0 (runtime default): got=%d", got)
	}
	if got := draft.Steps[3].TimeoutSeconds; got != 0 {
		t.Fatalf("USER_INPUT step should not carry timeout: got=%d", got)
	}
}

func TestPlanSchemaExposesTimeoutSeconds(t *testing.T) {
	spec := planSchema()
	raw, err := json.Marshal(spec.Parameters)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	if !strings.Contains(string(raw), "timeoutSeconds") {
		t.Fatal("planner schema should expose timeoutSeconds so heavy steps can extend the 600s default")
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
