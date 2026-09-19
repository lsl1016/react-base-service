package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	reactService "react-base-service/service/react"

	"github.com/gin-gonic/gin"
)

const plannerSystemPrompt = `你是 Agent Plan Runtime 的 Planner。
你的唯一职责是把用户目标拆成一组可执行、可审计、按顺序推进的步骤，不执行任何业务操作。
必须调用 submit_plan 提交计划，不能直接回答用户业务问题。

规则：
1. V1 是线性执行器：步骤按数组顺序执行；dependsOn 只能引用前面的 stepKey。
2. 需要模型调用工具、检索、分析、修改、验证的步骤使用 AGENT。
3. 只有确实需要用户补充新的业务数据时才使用 USER_INPUT。
4. 需要用户明确确认/批准后才能继续的步骤使用 USER_ACTION；有副作用的修改前优先规划 USER_ACTION。
5. 不要把“思考一下”“总结一下”拆成无价值步骤；每一步必须有明确产出。
6. stepKey 使用小写字母、数字、下划线，且以字母开头。
7. AGENT 步骤 maxAttempts 建议 1-3；等待用户步骤固定为 1。
8. 不规划并行步骤、SUB_PLAN、EXTERNAL_TASK；这些不属于 V1。
9. 计划必须足够完整，让每个 Step 在只看到 Plan 上下文、当前 Step 和前序结果摘要时也能执行。`

func planSchema() reactService.StructuredToolSpec {
	stringArray := map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}}
	return reactService.StructuredToolSpec{
		Name:        "submit_plan",
		Description: "提交结构化执行计划。只生成计划，不执行任何业务工具。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title":    map[string]interface{}{"type": "string", "minLength": 1},
				"overview": map[string]interface{}{"type": "string"},
				"steps": map[string]interface{}{
					"type":     "array",
					"minItems": 1,
					"maxItems": maxPlanSteps,
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"stepKey":         map[string]interface{}{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
							"name":            map[string]interface{}{"type": "string", "minLength": 1},
							"instruction":     map[string]interface{}{"type": "string", "minLength": 1},
							"expectedOutput":  map[string]interface{}{"type": "string"},
							"stepType":        map[string]interface{}{"type": "string", "enum": []string{"AGENT", "USER_INPUT", "USER_ACTION"}},
							"dependsOn":       stringArray,
							"maxAttempts":     map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 3},
							"required":        map[string]interface{}{"type": "boolean"},
							"successCriteria": stringArray,
							"question":        map[string]interface{}{"type": "string"},
							"responseSchema":  map[string]interface{}{"type": "object"},
						},
						"required":             []string{"stepKey", "name", "instruction", "expectedOutput", "stepType", "dependsOn", "maxAttempts", "successCriteria"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"title", "overview", "steps"},
			"additionalProperties": false,
		},
	}
}

func buildPlannerUserPrompt(userPrompt string) string {
	return "用户原始目标：\n" + strings.TrimSpace(userPrompt) +
		"\n\n请生成执行计划并调用 submit_plan。Plan 模式已由用户主动选择，计划生成后会自动从 Step 1 开始执行，不需要额外添加“确认开始计划”的步骤。"
}

// generateDraft 使用 schema-constrained 虚拟 Tool 获取 PlanDraft；Planner 不进入 ReAct，也不会执行 Tool。
func generateDraft(ctx *gin.Context, runCtx context.Context, prepared *reactService.PreparedExternalRun, userPrompt string) (PlanDraft, reactService.StructuredCompletionResult, error) {
	result, err := reactService.CompleteStructured(ctx, runCtx, prepared, plannerSystemPrompt, buildPlannerUserPrompt(userPrompt), planSchema())
	if err != nil {
		return PlanDraft{}, result, err
	}
	var draft PlanDraft
	if err := json.Unmarshal(result.Arguments, &draft); err != nil {
		return PlanDraft{}, result, fmt.Errorf("decode plan draft: %w", err)
	}
	if err := validateDraft(&draft); err != nil {
		return PlanDraft{}, result, err
	}
	return draft, result, nil
}
