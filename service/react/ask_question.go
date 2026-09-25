package react

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

const (
	askQuestionMaxQuestions = 4
	askQuestionMinOptions   = 2
	askQuestionMaxOptions   = 4
)

type askQuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type askQuestionItem struct {
	ID            string              `json:"id"`
	Prompt        string              `json:"prompt"`
	AllowMultiple bool                `json:"allowMultiple"`
	Options       []askQuestionOption `json:"options"`
}

type askQuestionInput struct {
	Questions []askQuestionItem `json:"questions"`
}

// toolUseAnswerPayload 是通用上行消息 tool_use_answer 的信封：
// toolUseId 指向处于 waiting 状态的内置工具调用，content 的结构由该工具自行解释。
type toolUseAnswerPayload struct {
	ToolUseID string          `json:"toolUseId"`
	Content   json.RawMessage `json:"content"`
}

// askQuestionAnswerItem 是用户对单个问题的回答。
type askQuestionAnswerItem struct {
	QuestionID        string   `json:"questionId"`
	SelectedOptionIDs []string `json:"selectedOptionIds"`
	FreeText          string   `json:"freeText"`
}

// askQuestionAnswer 是 ask_question 对 tool_use_answer.content 的解释结构。
type askQuestionAnswer struct {
	Answers []askQuestionAnswerItem `json:"answers"`
	Skipped bool                    `json:"skipped"`
}

type askQuestionResolvedOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type askQuestionResultItem struct {
	QuestionID string                      `json:"questionId"`
	Prompt     string                      `json:"prompt"`
	Selected   []askQuestionResolvedOption `json:"selected"`
	FreeText   string                      `json:"freeText,omitempty"`
}

// askQuestionResult 是回填给模型的 tool result 结构；label 由后端按原始 questions 反查，前端上行不带 label。
type askQuestionResult struct {
	Answers []askQuestionResultItem `json:"answers"`
	Skipped bool                    `json:"skipped"`
	// Cancelled 表示等待作答期间 run 被取消或连接断开，问题未得到回答；与用户主动「跳过」区分。
	Cancelled bool   `json:"cancelled,omitempty"`
	Note      string `json:"note,omitempty"`
}

func askQuestionToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolAskQuestion,
		Description: "向用户提出结构化选择题，阻塞等待回答后再继续。仅当缺少无法从上下文推断、且会改变后续执行路径的关键信息时使用（如业务参数有歧义、多种方案需用户拍板）；不要用它确认「是否继续」，不要问自己能查到答案的问题。能攒的问题攒到一次调用里问完。推荐选项放在 options 第一位并在 label 末尾加「（推荐）」。UI 会自动提供自由输入项，不要自己添加「其他」选项。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次提问目的，名词短语，用于前端展示。"),
				"questions": map[string]interface{}{
					"type":        "array",
					"minItems":    1,
					"maxItems":    askQuestionMaxQuestions,
					"description": "要向用户展示的问题数组。",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":     stringSchema("问题唯一标识，回答按此 id 匹配，例如 time_range。"),
							"prompt": stringSchema("展示给用户的问题，具体明确，以问号结尾。"),
							"allowMultiple": map[string]interface{}{
								"type":        "boolean",
								"description": "是否允许多选，默认 false。",
							},
							"options": map[string]interface{}{
								"type":     "array",
								"minItems": askQuestionMinOptions,
								"maxItems": askQuestionMaxOptions,
								"items": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"id":          stringSchema("选项唯一标识，例如 last_30d。"),
										"label":       stringSchema("选项显示文本，简短。"),
										"description": stringSchema("选择该项的含义或影响，可选。"),
									},
									"required":             []string{"id", "label"},
									"additionalProperties": false,
								},
							},
						},
						"required":             []string{"id", "prompt", "options"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"description", "questions"},
			"additionalProperties": false,
		},
	}
}

// parseAskQuestionInput 解析并校验模型入参；非法输入以 isError 工具结果回给模型重试，不打扰用户。
func parseAskQuestionInput(raw json.RawMessage) (askQuestionInput, error) {
	var input askQuestionInput
	if len(raw) == 0 || !json.Valid(raw) {
		return input, fmt.Errorf("ask_question input must be a valid JSON object")
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return input, fmt.Errorf("ask_question input invalid: %v", err)
	}
	if len(input.Questions) == 0 {
		return input, fmt.Errorf("ask_question.questions must not be empty")
	}
	if len(input.Questions) > askQuestionMaxQuestions {
		return input, fmt.Errorf("ask_question.questions must not exceed %d items", askQuestionMaxQuestions)
	}
	questionIDs := make(map[string]bool, len(input.Questions))
	for i := range input.Questions {
		question := &input.Questions[i]
		question.ID = strings.TrimSpace(question.ID)
		question.Prompt = strings.TrimSpace(question.Prompt)
		if question.ID == "" {
			return input, fmt.Errorf("ask_question.questions[%d].id is required", i)
		}
		if questionIDs[question.ID] {
			return input, fmt.Errorf("duplicate question id: %s", question.ID)
		}
		questionIDs[question.ID] = true
		if question.Prompt == "" {
			return input, fmt.Errorf("ask_question.questions[%d].prompt is required", i)
		}
		if len(question.Options) < askQuestionMinOptions || len(question.Options) > askQuestionMaxOptions {
			return input, fmt.Errorf("question %s must have %d-%d options", question.ID, askQuestionMinOptions, askQuestionMaxOptions)
		}
		optionIDs := make(map[string]bool, len(question.Options))
		for j := range question.Options {
			option := &question.Options[j]
			option.ID = strings.TrimSpace(option.ID)
			option.Label = strings.TrimSpace(option.Label)
			if option.ID == "" {
				return input, fmt.Errorf("question %s options[%d].id is required", question.ID, j)
			}
			if optionIDs[option.ID] {
				return input, fmt.Errorf("question %s has duplicate option id: %s", question.ID, option.ID)
			}
			optionIDs[option.ID] = true
			if option.Label == "" {
				return input, fmt.Errorf("question %s options[%d].label is required", question.ID, j)
			}
		}
	}
	return input, nil
}

// executeAskQuestion 执行 ask_question 内置工具：下发 tool_use_start(waiting) 后阻塞等待前端上行 tool_use_answer。
func (s *reactEngineState) executeAskQuestion(call llm.ToolCall, step int) (llm.ToolResultContent, error) {
	description := extractToolDescription(call.Input)
	toolInput := stripToolDescriptionInput(call.Input)

	input, parseErr := parseAskQuestionInput(toolInput)
	if parseErr != nil {
		return llm.ToolResultContent{ToolUseID: call.ID, Content: parseErr.Error(), IsError: true}, nil
	}

	interactionEmitter := s.clientToolEmitter()
	_ = interactionEmitter.EmitStep(step, EventToolUseStart, params.ReactToolUseStartPayload{ToolUseID: call.ID, ToolName: metaToolAskQuestion, ToolInput: toolInput, Description: description, ExecutedBy: executedByInternal, Status: toolExecutionStatusWaiting})
	start := time.Now()

	pendingJSON, _ := json.Marshal([]string{call.ID})
	if err := model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"state": model.ReactRunStateWaitingClientMessage, "pending_tool_use_ids": string(pendingJSON)}); err != nil {
		return llm.ToolResultContent{}, err
	}

	answer, err := s.waitAskQuestionAnswer(call.ID)
	if !IsReactRunCancelled(err) {
		_ = model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"state": model.ReactRunStateRunning, "pending_tool_use_ids": "[]"})
	}
	if err != nil {
		// 交互等待超时：以错误工具结果回灌模型继续循环（模型可换路径或稍后再问），不终止 run。
		if IsErrInteractionTimeout(err) {
			content := renderAskQuestionTimeoutResult()
			normalized := normalizeToolResult(call.ID, content, true, executedByInternal)
			_ = interactionEmitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: normalized.Content, IsError: true, ExecutedBy: executedByInternal, Status: normalized.Status, DurationMs: time.Since(start).Milliseconds()})
			return llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: true}, nil
		}
		// 取消/断线时补一条「未作答」tool_result：历史可见、悬空 tool_use 有配对、实时卡片可收敛。
		if IsReactRunCancelled(err) || IsReactClientDisconnected(err) {
			s.persistAskQuestionCancelledResult(call, input, step, start, err)
		}
		return llm.ToolResultContent{}, err
	}

	content := renderAskQuestionResult(input, answer)
	normalized := normalizeToolResult(call.ID, content, false, executedByInternal)
	if normalized.ResultRef != "" {
		if err := storeResultRef(s.ctx, s.sessionID, s.runID, call.ID, normalized.ResultRef, content); err != nil {
			return llm.ToolResultContent{}, err
		}
	}
	_ = interactionEmitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: normalized.Content, ResultRef: normalized.ResultRef, Truncated: normalized.Truncated, OmittedChars: normalized.OmittedChars, IsError: normalized.IsError, ExecutedBy: executedByInternal, Status: normalized.Status, DurationMs: time.Since(start).Milliseconds()})
	return llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: normalized.IsError}, nil
}

// waitAskQuestionAnswer 在 run 进入 waiting_client_message 后经 clientHub 等待前端上行 tool_use_answer，
// 按 toolUseId 认领消息后把通用信封中的 content 解析为 ask_question 的回答结构；
// 并行委派的其它等待者（父/兄弟子 run）互不干扰，cancel 广播语义保持一致。
func (s *reactEngineState) waitAskQuestionAnswer(toolUseID string) (askQuestionAnswer, error) {
	msg, err := s.waitClientMessage(func(m params.ReactWSMessage) bool {
		return toolUseAnswerID(m) == toolUseID
	})
	if err != nil {
		// 交互超时不是断连：run 状态已由调用方恢复 running，不置 expired。
		if !IsReactClientDisconnected(err) && !IsErrInteractionTimeout(err) {
			_ = model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"state": model.ReactRunStateExpired})
		}
		return askQuestionAnswer{}, err
	}
	switch msg.Type {
	case EventCancel:
		return askQuestionAnswer{}, ErrReactRunCancelled
	case EventToolUseAnswer:
		var envelope toolUseAnswerPayload
		if err := json.Unmarshal(msg.Payload, &envelope); err != nil {
			return askQuestionAnswer{}, err
		}
		if envelope.ToolUseID != toolUseID {
			return askQuestionAnswer{}, fmt.Errorf("tool_use_answer missing: %s", toolUseID)
		}
		var answer askQuestionAnswer
		if len(envelope.Content) > 0 {
			if err := json.Unmarshal(envelope.Content, &answer); err != nil {
				return askQuestionAnswer{}, fmt.Errorf("tool_use_answer content invalid: %v", err)
			}
		}
		return answer, nil
	default:
		return askQuestionAnswer{}, fmt.Errorf("unexpected message while waiting tool_use_answer: %s", msg.Type)
	}
}

// renderAskQuestionResult 按问题原始顺序拼装回填给模型的结果，label 以后端持有的 questions 为权威来源反查。
func renderAskQuestionResult(input askQuestionInput, answer askQuestionAnswer) string {
	if answer.Skipped {
		// 跳过时仍按原始问题列表逐题输出（selected 为空），保证历史回放和模型都能看到当时问了什么。
		data, _ := json.Marshal(askQuestionResult{
			Answers: unansweredAskQuestionItems(input),
			Skipped: true,
			Note:    "用户跳过了提问。请基于合理默认值继续执行，并在最终答复中说明所做假设。",
		})
		return string(data)
	}

	answerByQuestionID := make(map[string]askQuestionAnswerItem, len(answer.Answers))
	for _, item := range answer.Answers {
		answerByQuestionID[strings.TrimSpace(item.QuestionID)] = item
	}

	result := askQuestionResult{Answers: make([]askQuestionResultItem, 0, len(input.Questions))}
	unanswered := make([]string, 0)
	for _, question := range input.Questions {
		item := askQuestionResultItem{QuestionID: question.ID, Prompt: question.Prompt, Selected: []askQuestionResolvedOption{}}
		if userAnswer, ok := answerByQuestionID[question.ID]; ok {
			item.Selected = resolveAskQuestionOptions(question, userAnswer.SelectedOptionIDs)
			item.FreeText = strings.TrimSpace(userAnswer.FreeText)
		}
		if len(item.Selected) == 0 && item.FreeText == "" {
			unanswered = append(unanswered, question.ID)
		}
		result.Answers = append(result.Answers, item)
	}
	if len(unanswered) > 0 {
		result.Note = fmt.Sprintf("以下问题用户未作答：%s。请基于合理默认值继续执行，并在最终答复中说明所做假设。", strings.Join(unanswered, "、"))
	}
	data, _ := json.Marshal(result)
	return string(data)
}

// renderAskQuestionCancelledResult 生成「等待作答被中断」的结果内容，保留原始问题列表。
//
// 区分两种中断：
//   - 连接断开：用户只是没来得及答，并非跳过。不标 Skipped，并指示模型在续跑里重新提问，
//     避免误当作「用户跳过」而直接用默认值继续。
//   - 用户主动取消本次运行：按跳过语义处理。
func renderAskQuestionCancelledResult(input askQuestionInput, waitErr error) string {
	if IsReactClientDisconnected(waitErr) {
		data, _ := json.Marshal(askQuestionResult{
			Answers:   unansweredAskQuestionItems(input),
			Cancelled: true,
			Note:      "连接断开，用户尚未作答（并非跳过）。请重新用 ask_question 提出上述问题让用户作答，不要使用默认值继续。",
		})
		return string(data)
	}
	data, _ := json.Marshal(askQuestionResult{
		Answers:   unansweredAskQuestionItems(input),
		Skipped:   true,
		Cancelled: true,
		Note:      "用户取消了本次运行，问题未作答。",
	})
	return string(data)
}

// renderAskQuestionTimeoutResult 生成「等待作答超时」的错误结果内容。
// 与取消不同：run 继续运行，模型可基于已有信息推进、稍后重新提问或直接向用户说明。
func renderAskQuestionTimeoutResult() string {
	seconds := conf.GetReactRuntimeConfig().Loop.InteractionTimeoutSec
	data, _ := json.Marshal(map[string]any{
		"error": fmt.Sprintf("用户未在限时内（%d 秒）作答，本次提问未获回答。请基于已有信息继续推进，或稍后用 ask_question 重新提问，不要臆造用户答案。", seconds),
	})
	return string(data)
}

// persistAskQuestionCancelledResult 在等待作答被取消或断线时补一条 tool_result 落库：
// 一是让历史消息能看出「问题未作答」，二是避免下一轮 LLM 上下文出现悬空 tool_use。
// 取消时连接仍在，额外补发 tool_use_end 让实时 UI 的提问卡片收敛到终态；落库失败不阻断原有取消流程。
func (s *reactEngineState) persistAskQuestionCancelledResult(call llm.ToolCall, input askQuestionInput, step int, start time.Time, waitErr error) {
	status, _, interrupted := classifyToolInterruption(s.runCtx, waitErr)
	if !interrupted {
		return
	}
	content := renderAskQuestionCancelledResult(input, waitErr)
	normalized := normalizeToolResult(call.ID, content, true, executedByInternal)
	normalized.Status = status
	if _, err := persistToolResultMessage(s.ctx, s.req, s.runID, s.sessionID, []llm.ToolResultContent{{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: normalized.IsError}}, step); err != nil {
		return
	}
	if status == toolExecutionStatusCancelled {
		_ = s.clientToolEmitter().EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: normalized.Content, IsError: normalized.IsError, ExecutedBy: executedByInternal, Status: normalized.Status, DurationMs: time.Since(start).Milliseconds()})
	}
}

// unansweredAskQuestionItems 按原始问题列表生成全部未作答的结果项，用于跳过和取消场景。
func unansweredAskQuestionItems(input askQuestionInput) []askQuestionResultItem {
	items := make([]askQuestionResultItem, 0, len(input.Questions))
	for _, question := range input.Questions {
		items = append(items, askQuestionResultItem{QuestionID: question.ID, Prompt: question.Prompt, Selected: []askQuestionResolvedOption{}})
	}
	return items
}

// resolveAskQuestionOptions 按选项 id 反查 label；未知 id 保留原值并以 id 兜底 label，避免丢失用户意图。
func resolveAskQuestionOptions(question askQuestionItem, selectedIDs []string) []askQuestionResolvedOption {
	labelByID := make(map[string]string, len(question.Options))
	for _, option := range question.Options {
		labelByID[option.ID] = option.Label
	}
	selected := make([]askQuestionResolvedOption, 0, len(selectedIDs))
	seen := make(map[string]bool, len(selectedIDs))
	for _, rawID := range selectedIDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		label, ok := labelByID[id]
		if !ok {
			label = id
		}
		selected = append(selected, askQuestionResolvedOption{ID: id, Label: label})
	}
	return selected
}
