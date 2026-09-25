package react

// Microcompact（微压缩，终止边界治理 Phase 3，对照 ZCode compact/microcompact.ts）：
//
// 全量压缩之前的第一道减压阀。token 压力达到全量压缩阈值的 90% 时，把较旧的工具结果
// 消息内容替换为占位文案（完整内容仍在 tblLlmReactToolResult，模型可用 read_tool_result
// 按 resultRef 续读），保留最近 N 组工具结果不动，推迟/避免代价更高的全量压缩。
//
// 与全量压缩的差异：替换只发生在 run 内存（state.messages），不落库、不改回放——
// DB 消息始终保留完整结果；跨 run 历史按 DB 原文重建（全量压缩的 compact_summary
// 才负责跨 run 的历史收敛）。

import (
	"encoding/json"
	"strings"

	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// microcompactMinTokenSavings 是本次清理节省 token 低于该值时放弃（不值得改变请求尾部语义）。
const microcompactMinTokenSavings = 256

// microcompactPlaceholderPrefix 是已清理占位文案前缀，用于幂等跳过。
const microcompactPlaceholderPrefix = "[旧工具结果已清理"

// maybeMicrocompactContext 在每轮模型调用前按 token 压力执行微压缩。
func (s *reactEngineState) maybeMicrocompactContext() error {
	cfg := conf.GetReactRuntimeConfig().ContextCompact
	// microcompact_enabled 未配置默认关闭（灰度开关），custom.yaml 显式开启。
	if cfg.MicrocompactEnabled == nil || !*cfg.MicrocompactEnabled {
		return nil
	}
	if cfg.Enabled != nil && !*cfg.Enabled {
		return nil
	}
	threshold := reactCompactThreshold(s.currentModel.ModelKey)
	if threshold <= 0 {
		threshold = cfg.TokenTrigger
	}
	if threshold <= 0 {
		return nil
	}
	// 主锚点是上一轮真实 usage；冷启动（无真实值）不触发（等第一轮 usage 锚定后再说）。
	if s.lastInputTokens <= 0 || s.lastInputTokens <= threshold*9/10 {
		return nil
	}
	return s.microcompactNow()
}

// microcompactNow 执行一次微压缩：替换较旧工具结果为占位，保留最近 keepRecent 组。
func (s *reactEngineState) microcompactNow() error {
	cfg := conf.GetReactRuntimeConfig().ContextCompact
	keepRecent := cfg.MicrocompactKeepRecent
	if keepRecent <= 0 {
		keepRecent = 5
	}

	// 先找到工具结果消息（含 tool_result part 的 user 消息）的全部下标。
	toolResultIndexes := make([]int, 0, 8)
	for i, msg := range s.messages {
		if msg.Role != model.ReactMessageRoleUser {
			continue
		}
		if hasPartOfType(msg, "tool_result") {
			toolResultIndexes = append(toolResultIndexes, i)
		}
	}
	if len(toolResultIndexes) <= keepRecent {
		return nil
	}

	beforeTokens := estimateMessagesTokens(s.messages)
	cleared := 0
	// 从旧到新清理，保留最近 keepRecent 组不动。
	for _, index := range toolResultIndexes[:len(toolResultIndexes)-keepRecent] {
		if s.clearToolResultMessageAt(index) {
			cleared++
		}
	}
	saved := beforeTokens - estimateMessagesTokens(s.messages)
	if cleared == 0 || saved < microcompactMinTokenSavings {
		return nil
	}
	// 微压缩只写日志不发 compact 事件：compact_start/end 是全量压缩的前端语义。
	s.logInfof("[react.microcompact] 微压缩完成: runId=%s, clearedMessages=%d, savedTokens≈%d", s.runID, cleared, saved)
	return nil
}

// clearToolResultMessageAt 就地替换一条工具结果消息中的 tool_result part 内容为占位。
// 错误结果与已清理过的消息跳过。返回是否发生了替换。
func (s *reactEngineState) clearToolResultMessageAt(index int) bool {
	message := &s.messages[index]
	replaced := false
	for pi := range message.Parts {
		part := &message.Parts[pi]
		if part.Type != "tool_result" || part.IsError || strings.TrimSpace(part.Content) == "" {
			continue
		}
		if strings.HasPrefix(part.Content, microcompactPlaceholderPrefix) {
			continue
		}
		part.Content = microcompactPlaceholder(part.Content)
		replaced = true
	}
	return replaced
}

// microcompactPlaceholder 生成占位文案；能从结果信封中解析出 resultRef 时一并带回，
// 模型可据此用 read_tool_result 读取完整内容。
func microcompactPlaceholder(content string) string {
	var envelope struct {
		ResultRef string `json:"resultRef"`
		Truncated bool   `json:"truncated"`
	}
	ref := ""
	if err := json.Unmarshal([]byte(content), &envelope); err == nil {
		ref = strings.TrimSpace(envelope.ResultRef)
	}
	if ref != "" {
		return microcompactPlaceholderPrefix + "以释放上下文空间；完整内容可通过 read_tool_result 按 resultRef=" + ref + " 分页读取]"
	}
	return microcompactPlaceholderPrefix + "以释放上下文空间]"
}
