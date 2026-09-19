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
	minStepTimeoutSeconds           = 60
	maxStepTimeoutSeconds           = 3600
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
	// TimeoutSeconds 是 AGENT 步骤的单次 Attempt 执行上限；0 表示用运行时默认值。
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
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
		// 模型偶发漏填/写错 stepKey（如空串、含中文或大写）。计划整体因此判死代价过高，
		// 这里按 name / 序号确定性推导一个合法 key，保持 dependsOn 自动补链可用。
		step.StepKey = repairStepKey(step.StepKey, step.Name, index, seen)
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
			step.TimeoutSeconds = clampStepTimeoutSeconds(step.TimeoutSeconds)
		case model.PlanStepTypeUserInput, model.PlanStepTypeUserAction:
			step.MaxAttempts = 1
			step.TimeoutSeconds = 0
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

// repairStepKey 在 stepKey 不合法时按 name（其次按序号）推导合法 key，并保证相对 seen 唯一。
// 已合法的 key 原样返回；推导仅在本地生效，不回写模型输出语义。
func repairStepKey(raw, name string, index int, seen map[string]int) string {
	if planStepKeyPattern.MatchString(raw) {
		if _, exists := seen[raw]; !exists {
			return raw
		}
	}
	key := deriveStepKey(name)
	if key == "" {
		key = fmt.Sprintf("step_%d", index+1)
	}
	if _, exists := seen[key]; !exists {
		return key
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s_%d", key, suffix)
		if len(candidate) > 64 {
			candidate = fmt.Sprintf("step_%d_%d", index+1, suffix)
		}
		if _, exists := seen[candidate]; !exists {
			return candidate
		}
	}
}

// deriveStepKey 把 name 规范成 stepKey：小写、非 [a-z0-9_] 连缀成单下划线、去首尾下划线；
// 数字开头补 s 前缀（pattern 要求字母开头），超长截断到 64。
func deriveStepKey(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	lastUnderscore := false
	for _, char := range lowered {
		valid := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if valid {
			builder.WriteRune(char)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	key := strings.Trim(builder.String(), "_")
	if key == "" {
		return ""
	}
	if key[0] >= '0' && key[0] <= '9' {
		key = "s" + key
	}
	if len(key) > 64 {
		key = key[:64]
	}
	return key
}

// clampStepTimeoutSeconds 把显式配置的步骤超时压进 [min, max]；0 表示未配置（用默认值）。
func clampStepTimeoutSeconds(value int) int {
	if value == 0 {
		return 0
	}
	if value < minStepTimeoutSeconds {
		return minStepTimeoutSeconds
	}
	if value > maxStepTimeoutSeconds {
		return maxStepTimeoutSeconds
	}
	return value
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
