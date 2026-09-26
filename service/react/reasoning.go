package react

import (
	"strings"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
)

// reasoningBudgetMax 是 run 级思考预算的上限（与参数 schema 的 reasoning.budgetTokens.max 一致）。
const reasoningBudgetMax = 131072

// parseReasoningOptions 把 payload 里的思考程度三态解析并校验为 llm.ReasoningOptions。
// nil 按 auto 兼容旧客户端（与历史硬编码 effort=high 行为等价）。
func parseReasoningOptions(opts *params.ReactReasoningOptions) (llm.ReasoningOptions, error) {
	if opts == nil {
		return llm.NormalizeReasoningOptions(llm.ReasoningOptions{Mode: llm.ReasoningModeAuto}), nil
	}
	mode := llm.ReasoningMode(strings.ToLower(strings.TrimSpace(opts.Mode)))
	switch mode {
	case llm.ReasoningModeOff, llm.ReasoningModeCustom:
	case "", llm.ReasoningModeAuto:
		mode = llm.ReasoningModeAuto
	default:
		return llm.ReasoningOptions{}, components.ErrorParamInvalid.Sprintf("reasoning.mode 仅支持 off/auto/custom: %s", opts.Mode)
	}

	effort := strings.ToLower(strings.TrimSpace(opts.Effort))
	if effort != "" && !llm.IsValidReasoningEffort(effort) {
		return llm.ReasoningOptions{}, components.ErrorParamInvalid.Sprintf("reasoning.effort 仅支持 minimal/low/medium/high: %s", opts.Effort)
	}
	if opts.BudgetTokens < 0 || opts.BudgetTokens > reasoningBudgetMax {
		return llm.ReasoningOptions{}, components.ErrorParamInvalid.Sprintf("reasoning.budgetTokens 取值范围 0-%d", reasoningBudgetMax)
	}
	return llm.NormalizeReasoningOptions(llm.ReasoningOptions{
		Mode:         mode,
		Effort:       effort,
		BudgetTokens: opts.BudgetTokens,
	}), nil
}
