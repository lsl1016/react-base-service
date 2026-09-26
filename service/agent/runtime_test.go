package agent

import (
	"encoding/json"
	"strings"
	"testing"

	model "react-base-service/models/llm"
)

func TestRuntimePolicyFromAgent(t *testing.T) {
	policy := DefaultRuntime().Policy(model.Agent{
		ToolsJSON:       `[" query_log ","tool_schema"]`,
		SkillsJSON:      `["db-diagnosis"]`,
		PermissionMode:  "",
		MaxSteps:        6,
		MaxTokensPerRun: 2400,
	})
	if len(policy.ToolRefs) != 2 || policy.ToolRefs[0] != "query_log" {
		t.Fatalf("unexpected tool refs: %v", policy.ToolRefs)
	}
	if len(policy.SkillRefs) != 1 || policy.SkillRefs[0] != "db-diagnosis" {
		t.Fatalf("unexpected skill refs: %v", policy.SkillRefs)
	}
	if policy.PermissionMode != model.AgentPermissionModeInherit {
		t.Fatalf("empty permission mode should normalize to inherit: %q", policy.PermissionMode)
	}
	if policy.EffectiveMaxSteps(8) != 6 || policy.MaxTokensPerRun != 2400 {
		t.Fatalf("unexpected budget policy: %+v", policy)
	}
}

func TestRuntimePolicyEffectiveMaxStepsFallback(t *testing.T) {
	if got := (RuntimePolicy{}).EffectiveMaxSteps(8); got != 8 {
		t.Fatalf("expected default max steps, got %d", got)
	}
}

func TestRuntimePolicyFiltersToolSnapshot(t *testing.T) {
	snapshot := `[{"toolId":"tool_a","name":"query_log","description":"log"},{"toolId":"tool_b","name":"query_schema","description":"schema"}]`
	if got := (RuntimePolicy{}).FilterToolIndexSnapshot(snapshot, nil); got != snapshot {
		t.Fatalf("empty tool refs should inherit snapshot: %s", got)
	}
	got := (RuntimePolicy{ToolRefs: []string{"tool_b"}}).FilterToolIndexSnapshot(snapshot, nil)
	var items []map[string]any
	if err := json.Unmarshal([]byte(got), &items); err != nil {
		t.Fatalf("invalid filtered json: %v", err)
	}
	if len(items) != 1 || items[0]["name"] != "query_schema" {
		t.Fatalf("unexpected filtered tools: %s", got)
	}
}

func TestRuntimePolicyFiltersToolSnapshotReadOnlyToken(t *testing.T) {
	snapshot := `[{"toolId":"tool_a","name":"query_log","description":"log"},{"toolId":"tool_b","name":"update_row","description":"write"}]`
	tools := []model.Tool{
		{ToolID: "tool_a", Name: "query_log", Config: `{"readOnly":true}`},
		{ToolID: "tool_b", Name: "update_row", Config: `{}`},
	}
	// @none：显式不继承任何业务工具
	if got := (RuntimePolicy{ToolRefs: []string{AgentToolRefNone}}).FilterToolIndexSnapshot(snapshot, tools); got != "[]" {
		t.Fatalf("@none should return empty index: %s", got)
	}
	// @readonly：只保留声明 readOnly 的工具
	got := (RuntimePolicy{ToolRefs: []string{AgentToolRefReadOnly}}).FilterToolIndexSnapshot(snapshot, tools)
	var items []map[string]any
	if err := json.Unmarshal([]byte(got), &items); err != nil {
		t.Fatalf("invalid filtered json: %v", err)
	}
	if len(items) != 1 || items[0]["name"] != "query_log" {
		t.Fatalf("@readonly should keep only readOnly tools: %s", got)
	}
}

func TestRuntimePolicyFiltersSkillSnapshot(t *testing.T) {
	snapshot := `[{"skillId":"skill_1","name":"db-diagnosis"},{"skillId":"skill_2","name":"log-analysis"}]`
	if got := (RuntimePolicy{}).FilterSkillIndexSnapshot(snapshot); got != "[]" {
		t.Fatalf("empty skill refs should isolate skills: %s", got)
	}
	got := (RuntimePolicy{SkillRefs: []string{"log-analysis"}}).FilterSkillIndexSnapshot(snapshot)
	if !strings.Contains(got, "log-analysis") || strings.Contains(got, "db-diagnosis") {
		t.Fatalf("unexpected filtered skills: %s", got)
	}
}
