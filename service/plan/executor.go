package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"react-base-service/components/params"
	model "react-base-service/models/llm"
	reactService "react-base-service/service/react"

	"github.com/gin-gonic/gin"
)

var errPlanPaused = errors.New("plan runtime paused")

// defaultStepTimeoutSeconds 是 step.TimeoutSeconds 未配置时的缺省执行超时（D4）。
const defaultStepTimeoutSeconds = 600

type executor struct {
	ctx        *gin.Context
	runCtx     context.Context
	prepared   *reactService.PreparedExternalRun
	execution  *model.PlanExecution
	emitter    *runtimeEmitter
	readClient reactService.ClientMessageReader
}

func (x *executor) run() error {
	for {
		if err := x.runCtx.Err(); err != nil {
			return context.Cause(x.runCtx)
		}
		step, err := nextPendingStep(x.ctx, x.execution)
		if err != nil {
			return err
		}
		if step == nil {
			return x.finalize()
		}
		claimed, err := claimStep(x.ctx, x.execution, step)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err := x.emitter.emitView(x.execution); err != nil {
			return err
		}

		switch step.StepType {
		case model.PlanStepTypeAgent:
			if err := x.runAgentStep(step); err != nil {
				return err
			}
		case model.PlanStepTypeUserInput, model.PlanStepTypeUserAction:
			return x.pauseForUser(step)
		default:
			return fmt.Errorf("unsupported plan step type: %s", step.StepType)
		}
	}
}

func (x *executor) runAgentStep(step *model.PlanStep) error {
	existing, err := listStepAttempts(x.ctx, step.StepID)
	if err != nil {
		return err
	}
	limit := step.MaxAttempts
	if limit <= 0 {
		limit = 1
	}
	// 手工 Retry 在自动重试耗尽后允许新增一个不可变 Attempt。
	if len(existing) >= limit {
		limit = len(existing) + 1
	}

	for attemptNo := len(existing) + 1; attemptNo <= limit; attemptNo++ {
		attempt, err := createAttempt(x.ctx, x.execution, step, attemptNo)
		if err != nil {
			return err
		}
		prompt, err := x.buildStepPrompt(step)
		if err != nil {
			return err
		}
		// D4：每个 Attempt 独立超时。DeadlineExceeded 不属于取消语义，走 Attempt 失败路径，
		// 未耗尽 maxAttempts 时自动换新 Attempt 重试；重试会重新计算超时。
		timeout := defaultStepTimeoutSeconds
		if step.TimeoutSeconds > 0 {
			timeout = step.TimeoutSeconds
		}
		stepCtx, cancelStep := context.WithTimeout(x.runCtx, time.Duration(timeout)*time.Second)
		result, runErr := reactService.RunScopedStep(
			x.ctx,
			stepCtx,
			x.prepared,
			reactService.ScopedStepSpec{
				StepKey: step.StepKey,
				UserPrompt: prompt,
				SystemPromptSuffix: "你正在执行 Plan Runtime 的一个隔离步骤。只完成当前步骤，不修改计划结构；需要的信息优先使用可用工具检索。不要把当前步骤扩展成新的全局计划。",
			},
			x.emitter.stepWriter(x.execution, step, attempt),
			x.readClient,
		)
		cancelStep()
		if result.RunID != "" {
			attempt.StepRunID = result.RunID
			_ = bindAttemptRun(x.ctx, attempt.StepAttemptID, result.RunID)
		}
		if runErr == nil {
			summary := compactText(result.FinalResponse, 1200)
			if _, err := completeAgentStepSuccess(
				x.ctx,
				x.execution,
				step,
				attempt,
				summary,
				map[string]interface{}{
					"finalResponse": result.FinalResponse,
					"stepRunId":     result.RunID,
				},
			); err != nil {
				return err
			}
			return x.emitter.emitView(x.execution)
		}

		if reactService.IsCancellation(runErr) || errors.Is(runErr, context.Canceled) {
			_ = finishAttempt(x.ctx, attempt.StepAttemptID, model.PlanAttemptStatusCancelled, "cancelled", runErr.Error())
			_ = finishStep(x.ctx, x.execution, step, model.PlanStepStatusCancelled, "步骤已取消", "")
			return runErr
		}

		errorSummary := compactText(runErr.Error(), 1200)
		terminal := attemptNo >= limit
		if err := completeAgentAttemptFailure(
			x.ctx,
			x.execution,
			step,
			attempt,
			errorSummary,
			terminal,
			map[string]interface{}{
				"error":     errorSummary,
				"stepRunId": result.RunID,
			},
		); err != nil {
			return err
		}
		if !terminal {
			continue
		}
		_ = x.emitter.emitView(x.execution)
		return fmt.Errorf("plan step %s failed: %w", step.StepKey, runErr)
	}
	return nil
}

func (x *executor) pauseForUser(step *model.PlanStep) error {
	draft := decodeDraftStep(step.InputJSON)
	waitType := model.PlanWaitTypeUserInput
	if step.StepType == model.PlanStepTypeUserAction {
		waitType = model.PlanWaitTypeUserAction
	}
	data := map[string]interface{}{
		"stepName": step.StepName,
		"instruction": step.Instruction,
		"expectedOutput": step.ExpectedOutput,
	}
	_, err := createWait(x.ctx, x.execution, step, nil, waitType, draft.Question, draft.ResponseSchema, data)
	if err != nil {
		return err
	}
	if err := reactService.MarkExternalRunState(x.ctx, x.prepared, model.ReactRunStateWaitingPlan, ""); err != nil {
		return err
	}
	if err := x.emitter.emitView(x.execution); err != nil {
		return err
	}
	return errPlanPaused
}

func (x *executor) buildStepPrompt(step *model.PlanStep) (string, error) {
	steps, err := listSteps(x.ctx, x.execution.PlanExecutionID, x.execution.CurrentVersionID)
	if err != nil {
		return "", err
	}
	var completed []string
	for _, item := range steps {
		if item.StepOrder >= step.StepOrder {
			break
		}
		if item.Status != model.PlanStepStatusSucceeded && item.Status != model.PlanStepStatusSkipped {
			continue
		}
		line := fmt.Sprintf("Step %d %s [%s]", item.StepOrder, item.StepName, item.Status)
		if strings.TrimSpace(item.ResultSummary) != "" {
			line += "\n结果摘要: " + item.ResultSummary
		}
		if strings.TrimSpace(item.ResultRef) != "" {
			line += "\n结果引用: " + item.ResultRef + "（仅在摘要不足以完成当前步骤时使用 read_tool_result 按需读取）"
		}
		completed = append(completed, line)
	}
	var criteria []string
	_ = json.Unmarshal([]byte(step.SuccessCriteria), &criteria)
	return strings.Join([]string{
		"<plan_execution>",
		"用户目标:",
		x.prepared.UserPrompt,
		"",
		fmt.Sprintf("当前步骤: Step %d/%d %s", step.StepOrder, len(steps), step.StepName),
		"当前步骤指令:",
		step.Instruction,
		"预期输出:",
		step.ExpectedOutput,
		"成功标准:",
		strings.Join(criteria, "；"),
		"",
		"已完成步骤:",
		strings.Join(completed, "\n\n"),
		"</plan_execution>",
		"",
		"只执行当前步骤。最终回答应清晰描述本步骤实际完成内容、关键事实/修改以及供后续步骤使用的结论。",
	}, "\n"), nil
}

func (x *executor) finalize() error {
	steps, err := listSteps(x.ctx, x.execution.PlanExecutionID, x.execution.CurrentVersionID)
	if err != nil {
		return err
	}
	summaries := make([]string, 0, len(steps))
	for _, step := range steps {
		summaries = append(summaries, fmt.Sprintf("Step %d %s [%s]\n%s", step.StepOrder, step.StepName, step.Status, step.ResultSummary))
	}
	result, err := reactService.CompleteText(
		x.ctx,
		x.runCtx,
		x.prepared,
		"你是 Plan Runtime Finalizer。只基于已执行步骤的结果生成最终用户答复，不调用工具，不虚构未完成事项。明确说明完成内容、关键发现/修改、验证结果以及仍未完成或被跳过的事项。",
		"用户原始目标:\n"+x.prepared.UserPrompt+"\n\nPlan执行结果:\n"+strings.Join(summaries, "\n\n"),
	)
	if err != nil {
		return err
	}
	if err := reactService.AddExternalRunUsage(x.ctx, x.prepared, result.InputTokens, result.OutputTokens, result.CacheReadTokens, result.CacheCreateTokens); err != nil {
		return err
	}
	if err := setExecutionResult(x.ctx, x.execution, map[string]interface{}{"summary": result.Content}); err != nil {
		return err
	}
	if err := reactService.PersistExternalFinalAnswer(x.ctx, x.prepared, result.Content, result.ModelKey, result.ModelVersion); err != nil {
		return err
	}
	if err := x.emitter.emitView(x.execution); err != nil {
		return err
	}
	_ = x.emitter.emit(reactService.EventContentStart, params.ReactContentStartPayload{})
	_ = x.emitter.emit(reactService.EventContentDelta, params.ReactContentDeltaPayload{ContentDelta: result.Content})
	_ = x.emitter.emit(reactService.EventContentEnd, params.ReactContentEndPayload{
		Content: result.Content,
		InputTokens: result.InputTokens,
		OutputTokens: result.OutputTokens,
		CacheReadTokens: result.CacheReadTokens,
		CacheCreateTokens: result.CacheCreateTokens,
	})
	return x.emitter.emit(reactService.EventDone, params.ReactDonePayload{
		InputTokens: result.InputTokens,
		OutputTokens: result.OutputTokens,
		CacheReadTokens: result.CacheReadTokens,
		CacheCreateTokens: result.CacheCreateTokens,
		TerminationReason: "plan_completed",
	})
}

func compactText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
