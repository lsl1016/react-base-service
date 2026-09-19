package model

import "testing"

func TestPlanRuntimeTableNames(t *testing.T) {
	cases := map[string]string{
		(&PlanExecution{}).TableName():   "tblLlmPlanExecution",
		(&PlanVersion{}).TableName():     "tblLlmPlanVersion",
		(&PlanStep{}).TableName():        "tblLlmPlanStep",
		(&PlanStepAttempt{}).TableName(): "tblLlmPlanStepAttempt",
		(&PlanStepResult{}).TableName():  "tblLlmPlanStepResult",
		(&PlanWait{}).TableName():        "tblLlmPlanWait",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("unexpected plan table name: got=%s want=%s", got, want)
		}
	}
}

func TestPlanRuntimeStatusConstants(t *testing.T) {
	if PlanExecutionStatusRunning != "RUNNING" || PlanStepStatusSkipped != "SKIPPED" || PlanWaitStatusPending != "PENDING" {
		t.Fatalf("plan runtime status constants changed unexpectedly")
	}
}
