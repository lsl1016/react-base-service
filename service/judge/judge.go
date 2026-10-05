// Package judge 是通用的 run 质量裁判（裁判外环，二期）：审阅一次 run 的 transcript 与
// 任务目标，语义判定「目标是否达成 / 异常等级」，未达成时给出追问建议。
//
// 独立性约束：本包不依赖 react 引擎与模型层——输入输出是纯结构（transcript + 目标 →
// 达成判定 + 追问建议），LLM 调用经 Invoker 接口注入。当前只接 workflow runner（按方案
// docs/定时触发工作流实现方案.md §5.3 二期）；后续接入 react 引擎 run 终态钩子时，
// 引擎侧只需把会话消息转成 Message 列表并实现 Invoker，无需改动本包。
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 消息角色（转录渲染用；与 LLM 消息角色同义，调用方自行映射自有会话消息）。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// 风险级别（语义判级输出；none=无风险，low=提示级，high=需告警）。
const (
	RiskNone = "none"
	RiskLow  = "low"
	RiskHigh = "high"
)

// Message 是审阅输入的对话消息（通用结构，不绑定任何存储模型）。
type Message struct {
	Role    string // user | assistant
	Content string
}

// Input 是一次审阅的通用输入：任务目标 + 本次 run 的 transcript。
type Input struct {
	// Goal 是任务目标（触发时的提示词原文，含占位符替换后的实际内容）。
	Goal string
	// Transcript 是本次 run 的对话转录（建议只含 user/assistant 文本，工具结果由调用方摘要）。
	Transcript []Message
}

// Verdict 是一次审阅的通用输出。
type Verdict struct {
	// Achieved 表示任务目标是否达成（跑完 ≠ 干好；由语义判定）。
	Achieved bool
	// RiskLevel 是语义风险级别：none | low | high。
	RiskLevel string
	// Reason 是判定理由（供日志、run 历史与通知展示）。
	Reason string
	// FollowUp 是未达成时的追问建议（达成时为空；调用方注入原会话驱动续跑）。
	FollowUp string
}

// Invoker 是裁判对 LLM 的最小依赖：以 system+user 发起一次非工具纯文本调用。
// 注入式设计使 judge 包与具体 LLM 客户端解耦（单测可用桩实现）。
type Invoker interface {
	ChatOnce(ctx context.Context, system, user string) (string, error)
}

// Judge 是通用裁判接口；后续接引擎钩子时依赖此接口而非具体实现。
type Judge interface {
	Review(ctx context.Context, in Input) (*Verdict, error)
}

// LLMJudge 是基于一次轻量 LLM 调用的 Judge 实现（OpenHands goal loop 对照）。
type LLMJudge struct {
	invoker Invoker
	// MaxTranscriptChars 限制转录总字符数（防超长 transcript 吃爆判定调用预算）。
	MaxTranscriptChars int
}

// NewLLMJudge 构造 LLM 裁判；invoker 为空返回 nil（调用方按 nil 判定未启用）。
func NewLLMJudge(invoker Invoker, maxTranscriptChars int) *LLMJudge {
	if invoker == nil {
		return nil
	}
	if maxTranscriptChars <= 0 {
		maxTranscriptChars = DefaultTranscriptChars
	}
	return &LLMJudge{invoker: invoker, MaxTranscriptChars: maxTranscriptChars}
}

// DefaultTranscriptChars 是转录总字符数默认预算。
const DefaultTranscriptChars = 24000

// ChatTimeout 是单次判定调用的默认超时。
const ChatTimeout = 120 * time.Second

// Review 执行一次审阅：渲染裁判提示词 → 一次 LLM 调用 → 解析 JSON 结论。
func (j *LLMJudge) Review(ctx context.Context, in Input) (*Verdict, error) {
	user := renderJudgeUserPrompt(in, j.MaxTranscriptChars)
	callCtx, cancel := context.WithTimeout(ctx, ChatTimeout)
	defer cancel()
	raw, err := j.invoker.ChatOnce(callCtx, judgeSystemPrompt, user)
	if err != nil {
		return nil, fmt.Errorf("裁判 LLM 调用失败: %w", err)
	}
	return ParseVerdict(raw)
}

// judgeSystemPrompt 是裁判角色提示词：只判定不执行，输出严格 JSON。
const judgeSystemPrompt = `你是无人值守任务的质量裁判。给你一次 Agent 运行的任务目标与对话转录，请判定：
1. achieved：任务目标是否实质达成（跑完流程不等于达成；结论与目标对不上、关键项缺失、明显未完成都算未达成）
2. risk_level：结果风险级别——none=无风险；low=提示级瑕疵（不影响主结论）；high=存在需要人工告警的风险或异常
3. reason：一两句判定理由（中文）
4. follow_up：仅当未达成时给出一句话追问建议（指明缺什么、接下来做什么）；达成时为空字符串

只输出一个 JSON 对象，不要输出任何其他文字：
{"achieved": true/false, "risk_level": "none|low|high", "reason": "...", "follow_up": ""}`

// renderJudgeUserPrompt 渲染审阅输入：任务目标 + 转录（从最近往前、总长截断保新）。
func renderJudgeUserPrompt(in Input, maxChars int) string {
	var sb strings.Builder
	sb.WriteString("## 任务目标\n")
	if goal := strings.TrimSpace(in.Goal); goal != "" {
		sb.WriteString(goal)
	} else {
		sb.WriteString("(未提供)")
	}
	sb.WriteString("\n\n## 运行转录（user=任务/追问，assistant=模型输出）\n")
	sb.WriteString(RenderTranscript(in.Transcript, maxChars))
	return sb.String()
}

// RenderTranscript 把消息列表渲染为裁判可读的转录文本：
// 从最近往前累计、单条截断、总长超限时丢弃更早内容并标注省略条数。
func RenderTranscript(messages []Message, maxChars int) string {
	if len(messages) == 0 {
		return "(空)"
	}
	if maxChars <= 0 {
		maxChars = DefaultTranscriptChars
	}
	const perMessageLimit = 2000
	lines := make([]string, 0, len(messages))
	used := 0
	truncated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		if runes := []rune(content); len(runes) > perMessageLimit {
			content = string(runes[:perMessageLimit]) + "…(截断)"
		}
		role := messages[i].Role
		if role != RoleAssistant && role != RoleUser {
			role = RoleUser
		}
		line := fmt.Sprintf("[%s] %s", role, content)
		if used+len([]rune(line)) > maxChars && len(lines) > 0 {
			truncated = i + 1
			break
		}
		lines = append([]string{line}, lines...)
		used += len([]rune(line))
	}
	prefix := ""
	if truncated > 0 {
		prefix = fmt.Sprintf("(更早的 %d 条消息已省略)\n", truncated)
	}
	return prefix + strings.Join(lines, "\n")
}

// ParseVerdict 解析裁判 LLM 输出为 Verdict：容忍 ```json 围栏与前后杂文；
// 解析失败返回错误（调用方 fail-open 降级为规则判级，不让裁判故障阻断收尾）。
func ParseVerdict(raw string) (*Verdict, error) {
	payload := extractJSONPayload(raw)
	if payload == "" {
		return nil, fmt.Errorf("裁判输出不含 JSON")
	}
	var parsed struct {
		Achieved  bool   `json:"achieved"`
		RiskLevel string `json:"risk_level"`
		Reason    string `json:"reason"`
		FollowUp  string `json:"follow_up"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil, fmt.Errorf("裁判 JSON 解析失败: %w", err)
	}
	return &Verdict{
		Achieved:  parsed.Achieved,
		RiskLevel: NormalizeRiskLevel(parsed.RiskLevel),
		Reason:    strings.TrimSpace(parsed.Reason),
		FollowUp:  strings.TrimSpace(parsed.FollowUp),
	}, nil
}

// extractJSONPayload 从模型输出中提取首个完整 JSON 对象（剥围栏/前后解释文字）。
func extractJSONPayload(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := range text[start:] {
		ch := text[start+i]
		if escaped {
			escaped = false
			continue
		}
		switch ch {
		case '\\':
			if inString {
				escaped = true
			}
		case '"':
			if !escaped {
				inString = !inString
			}
		case '{':
			if !inString {
				depth++
			}
		case '}':
			if !inString {
				depth--
				if depth == 0 {
					return text[start : start+i+1]
				}
			}
		}
	}
	return ""
}

// NormalizeRiskLevel 把裁判输出的风险级别收敛到受支持集合。
func NormalizeRiskLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case RiskLow:
		return RiskLow
	case RiskHigh:
		return RiskHigh
	default:
		return RiskNone
	}
}
