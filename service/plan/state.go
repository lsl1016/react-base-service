package plan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	model "react-base-service/models/llm"

	"github.com/google/uuid"
)

const (
	maxPlanSteps                    = 20
	defaultAgentStepMaxAttempts     = 2
	maxAgentStepAttempts            = 3
)

var planStepKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type PlanDraft struct {
	Title    string          `json:"title"`
	Overview string          `json:"overview"`
	Steps    []PlanDraftStep `json:"steps"`
}

type PlanDraftStep struct {
	StepKey         string                 `json:"stepKey"`
	Name            string                 `json:"name"`
	Instruction     string                 `json:"instruction"`
	ExpectedOutput  string                 `json:"expectedOutput"`
	StepType        string                 `json:"stepType"`
	DependsOn       []string               `json:"dependsOn"`
	MaxAttempts     int                    `json:"maxAttempts"`
	Required        *bool                  `json:"required,omitempty"`
	SuccessCriteria []string               `json:"successCriteria"`
	Question        string                 `json:"question,omitempty"`
	ResponseSchema  map[string]interface{} `json:"responseSchema,omitempty"`
}

func newID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.New().String(), "-", "")
}

func validateDraft(draft *PlanDraft) error {
	if draft == nil {
		return fmt.Errorf("plan draft is nil")
	}
	draft.Title = strings.TrimSpace(draft.Title)
	draft.Overview = strings.TrimSpace(draft.Overview)
	if draft.Title == "" {
		return fmt.Errorf("plan title is required")
	}
	if len(draft.Steps) == 0 || len(draft.Steps) > maxPlanSteps {
		return fmt.Errorf("plan steps must be between 1 and %d", maxPlanSteps)
	}
	seen := make(map[string]int, len(draft.Steps))
	for index := range draft.Steps {
		step := &draft.Steps[index]
		step.StepKey = strings.TrimSpace(step.StepKey)
		step.Name = strings.TrimSpace(step.Name)
		step.Instruction = strings.TrimSpace(step.Instruction)
		step.ExpectedOutput = strings.TrimSpace(step.ExpectedOutput)
		step.StepType = strings.ToUpper(strings.TrimSpace(step.StepType))
		step.Question = strings.TrimSpace(step.Question)
		if !planStepKeyPattern.MatchString(step.StepKey) {
			return fmt.Errorf("invalid stepKey %q", step.StepKey)
		}
		if _, exists := seen[step.StepKey]; exists {
			return fmt.Errorf("duplicate stepKey %q", step.StepKey)
		}
		if step.Name == "" || step.Instruction == "" {
			return fmt.Errorf("step %s requires name and instruction", step.StepKey)
		}
		switch step.StepType {
		case model.PlanStepTypeAgent:
			if step.MaxAttempts <= 0 {
				step.MaxAttempts = defaultAgentStepMaxAttempts
			}
			if step.MaxAttempts > maxAgentStepAttempts {
				step.MaxAttempts = maxAgentStepAttempts
			}
		case model.PlanStepTypeUserInput, model.PlanStepTypeUserAction:
			step.MaxAttempts = 1
			if step.Question == "" {
				step.Question = step.Instruction
			}
			if step.StepType == model.PlanStepTypeUserInput && len(step.ResponseSchema) == 0 {
				step.ResponseSchema = map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"answer": map[string]interface{}{"type": "string", "title": "补充信息"},
					},
					"required": []string{"answer"},
				}
			}
		default:
			return fmt.Errorf("step %s has unsupported stepType %q", step.StepKey, step.StepType)
		}
		for _, dependency := range step.DependsOn {
			dependency = strings.TrimSpace(dependency)
			position, exists := seen[dependency]
			if !exists || position >= index {
				return fmt.Errorf("step %s dependency %q must reference an earlier step", step.StepKey, dependency)
			}
		}
		if index > 0 && len(step.DependsOn) == 0 {
			step.DependsOn = []string{draft.Steps[index-1].StepKey}
		}
		seen[step.StepKey] = index
	}
	return nil
}

func stepRequired(step PlanDraftStep) bool {
	return step.Required == nil || *step.Required
}

func encodeDraftStep(step PlanDraftStep) string {
	data, _ := json.Marshal(step)
	return string(data)
}

func decodeDraftStep(raw string) PlanDraftStep {
	var step PlanDraftStep
	_ = json.Unmarshal([]byte(raw), &step)
	return step
}
