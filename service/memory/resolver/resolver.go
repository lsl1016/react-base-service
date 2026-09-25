// Package resolver 实现长期记忆 V2 的冲突消解后半程：为候选记忆召回相似存量条目，
// 单次 LLM 批量判定 ADD/UPDATE/SUPERSEDE/SKIP，并经统一写核心落库。
// 核心原则（借鉴 Graphiti）：失效不删除——被取代事实的内容进修订快照可回溯，本期不提供 DELETE。
package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	memory "react-base-service/service/memory"
	"react-base-service/service/memory/extractor"

	"github.com/gin-gonic/gin"
)

// 决策动作（LLM 输出的小写枚举）。
const (
	ActionAdd       = "add"
	ActionUpdate    = "update"
	ActionSupersede = "supersede"
	ActionSkip      = "skip"
)

// ApplyFunc 统一写核心的函数形态：默认 memory.ApplyMutation，单测可替换为 mock。
type ApplyFunc func(ctx *gin.Context, input memory.MutationInput) (map[string]interface{}, error)

// ResolveInput 是一次消解的运行参数。
type ResolveInput struct {
	// Owners 是候选召回的记忆空间集合（与触发 run 的可见作用域一致）。
	Owners []model.MemoryOwner
	// WriteOwner 是新增条目的写入目标（caller_user 优先语义由 scope 解析决定）。
	WriteOwner model.MemoryOwner
	// AllowedKeys 透传给写核心的作用域校验。
	AllowedKeys map[string]struct{}
	// Items 是作用域内全部 active 条目（已跨空间合并），消解与守卫的事实来源。
	Items []model.MemoryItem
	// SimilarTopK 是每个候选保留的相似存量条目上限。
	SimilarTopK int
	// MaxWrites 是本次沉淀允许落库的写操作上限。
	MaxWrites int
	// CreatedBy 是触发 run 的 ID（审计）。
	CreatedBy string
}

// Decision 是一条候选的消解结果（含落库执行情况）。
type Decision struct {
	Candidate     extractor.Candidate
	Action        string // add / update / supersede / skip
	TargetID      uint
	MergedContent string
	Reason        string
	Applied       bool
	Note          string // 跳过或失败原因
	// mentioned 表示 LLM 的输出中显式提到了该候选；未提及的候选走保守回退。
	mentioned bool
}

// Resolve 执行「候选召回 → 单次 LLM 批量消解 → 决策落库」。
// 无相似条目的候选直接 ADD（不进 LLM）；全部候选都无相似时跳过 LLM 调用。
func Resolve(ctx *gin.Context, llm extractor.LLMInvoker, apply ApplyFunc, input ResolveInput, candidates []extractor.Candidate) ([]Decision, error) {
	if apply == nil {
		apply = memory.ApplyMutation
	}
	itemByID := make(map[uint]model.MemoryItem, len(input.Items))
	for _, item := range input.Items {
		itemByID[item.ID] = item
	}

	recalls := make([][]model.MemoryItem, len(candidates))
	pending := make([]int, 0, len(candidates))
	for i := range candidates {
		recalls[i] = recallSimilar(ctx, input, candidates[i], itemByID)
		if len(recalls[i]) > 0 {
			pending = append(pending, i)
		}
	}

	decisions := make([]Decision, len(candidates))
	for i := range decisions {
		decisions[i] = Decision{Candidate: candidates[i], Action: ActionAdd}
	}
	if len(pending) > 0 {
		resolved, err := resolveWithLLM(ctx, llm, input, candidates, recalls, itemByID)
		if err != nil {
			return nil, err
		}
		for i, decision := range resolved {
			decisions[i] = decision
		}
	}

	writes := 0
	for i := range decisions {
		applyDecision(ctx, apply, &input, itemByID, &decisions[i], &writes)
	}
	return decisions, nil
}

// likeRecall 是 LIKE 兜底召回的函数变量：生产指向 model DAO，单测可打桩避免触库。
var likeRecall = model.FindActiveMemoryItemsLikeOwner

// recallSimilar 为单个候选召回相似存量条目：先取抽取器自报的 similarIds（以清单为准），
// 不足时用关键词 LIKE 兜底；两路结果按序去重，截断到 SimilarTopK。
func recallSimilar(ctx *gin.Context, input ResolveInput, candidate extractor.Candidate, itemByID map[uint]model.MemoryItem) []model.MemoryItem {
	topK := input.SimilarTopK
	if topK <= 0 {
		topK = 5
	}
	seen := make(map[uint]struct{}, topK)
	result := make([]model.MemoryItem, 0, topK)
	add := func(item model.MemoryItem) {
		if item.ID == 0 {
			return
		}
		if _, ok := seen[item.ID]; ok {
			return
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	for _, rawID := range candidate.SimilarIDs {
		if item, ok := itemByID[uint(rawID)]; ok {
			add(item)
		}
	}
	if len(result) < topK {
		keywords := recallKeywords(candidate)
		for _, owner := range input.Owners {
			items, err := likeRecall(ctx, owner, keywords, topK*2)
			if err != nil {
				zlog.Warnf(ctx, "[memory.resolver] LIKE 召回失败(忽略): owner=%s err=%v", owner.OwnerKey, err)
				continue
			}
			for _, item := range items {
				add(item)
				if len(result) >= topK {
					break
				}
			}
			if len(result) >= topK {
				break
			}
		}
	}
	if len(result) > topK {
		result = result[:topK]
	}
	return result
}

// recallKeywords 从候选的 title/description/content 提取 LIKE 关键词：
// 按空白与常见中英文标点切分，保留 ≥2 rune 的片段，超长截到 12 rune，总量封顶 12。
func recallKeywords(candidate extractor.Candidate) []string {
	text := candidate.Title + "\n" + candidate.Description + "\n" + candidate.Content
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '，', '。', '、', '；', '：', '！', '？', '（', '）', '「', '」',
			',', '.', ';', ':', '!', '?', '"', '\'', '(', ')', '/', '\\', '-', '_':
			return true
		}
		return false
	})
	keywords := make([]string, 0, 12)
	seen := make(map[string]struct{}, 12)
	for _, field := range fields {
		runes := []rune(strings.TrimSpace(field))
		if len(runes) < 2 {
			continue
		}
		if len(runes) > 12 {
			runes = runes[:12]
		}
		keyword := string(runes)
		if _, ok := seen[keyword]; ok {
			continue
		}
		seen[keyword] = struct{}{}
		keywords = append(keywords, keyword)
		if len(keywords) >= 12 {
			break
		}
	}
	return keywords
}

// rawDecision 是 LLM 返回的单条决策。
type rawDecision struct {
	Candidate     string `json:"candidate"`
	Decision      string `json:"decision"`
	Target        string `json:"target"`
	MergedContent string `json:"mergedContent"`
	Reason        string `json:"reason"`
}

// resolveWithLLM 单次批量消解：所有待判定候选 + 各自相似存量拼进同一 prompt（mem0 式编号映射），
// LLM 只输出编号与决策，时间/归属/限额等裁决全部由代码执行（Graphiti 经验：不信任 LLM 做确定性逻辑）。
func resolveWithLLM(ctx *gin.Context, llm extractor.LLMInvoker, input ResolveInput, candidates []extractor.Candidate, recalls [][]model.MemoryItem, itemByID map[uint]model.MemoryItem) ([]Decision, error) {
	// 相似存量并集（去重，保序）。
	existing := make([]model.MemoryItem, 0, len(candidates))
	existingSeen := make(map[uint]struct{}, len(candidates))
	for _, group := range recalls {
		for _, item := range group {
			if _, ok := existingSeen[item.ID]; ok {
				continue
			}
			existingSeen[item.ID] = struct{}{}
			existing = append(existing, item)
		}
	}

	text, err := llm.ChatOnce(context.Background(), ResolutionSystemPrompt, buildResolutionPrompt(candidates, existing))
	if err != nil {
		return nil, fmt.Errorf("消解调用失败: %w", err)
	}
	raw, err := extractor.ExtractJSON(text)
	if err != nil {
		return nil, fmt.Errorf("消解输出解析失败: %w", err)
	}
	var payload struct {
		Decisions []rawDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("消解 JSON 解析失败: %w", err)
	}

	// C 编号 → 候选下标；M 编号 → 真实 itemID（幻觉/越界 id 在落库守卫中拦截）。
	indexByCandidate := make(map[int]int, len(candidates))
	for i := range candidates {
		indexByCandidate[i+1] = i
	}
	decisions := make([]Decision, len(candidates))
	for i := range candidates {
		decisions[i] = Decision{Candidate: candidates[i], Action: ActionAdd}
	}
	for _, rd := range payload.Decisions {
		idx, ok := parseNumberedRef(rd.Candidate, "C")
		if !ok {
			continue
		}
		i, ok := indexByCandidate[idx]
		if !ok || i >= len(decisions) {
			continue
		}
		decision := &decisions[i]
		decision.mentioned = true
		decision.Reason = strings.TrimSpace(rd.Reason)
		action := strings.ToLower(strings.TrimSpace(rd.Decision))
		switch action {
		case ActionAdd, ActionUpdate, ActionSupersede, ActionSkip:
			decision.Action = action
		default:
			decision.Action = ActionSkip
			decision.Note = "未知决策: " + rd.Decision
			continue
		}
		if action == ActionUpdate || action == ActionSupersede || action == ActionSkip {
			if id, ok := parseNumberedRef(rd.Target, "M"); ok {
				decision.TargetID = uint(id)
			}
		}
		decision.MergedContent = strings.TrimSpace(rd.MergedContent)
		// LLM 未给出决策的候选：有相似项时保守 skip（宁可漏记不错记），无相似项保持 add。
	}
	for i, group := range recalls {
		// update/supersede 的目标必须真实存在，幻觉/越界 ID 按 SKIP 收敛。
		if decisions[i].Action == ActionUpdate || decisions[i].Action == ActionSupersede {
			if _, ok := itemByID[decisions[i].TargetID]; !ok {
				decisions[i].Action = ActionSkip
				decisions[i].Note = "目标条目不存在（幻觉 ID 或已删除）"
			}
			continue
		}
		// LLM 未提及的候选才做保守回退：有相似项时宁可漏记不错记（skip），无相似项保持 add。
		if !decisions[i].mentioned && len(group) == 0 {
			decisions[i].Action = ActionAdd
		} else if !decisions[i].mentioned {
			decisions[i].Action = ActionSkip
			decisions[i].Note = "LLM 未给出决策"
		}
	}
	return decisions, nil
}

// parseNumberedRef 解析 "C1"/"M12" 形态的编号引用。
func parseNumberedRef(ref, prefix string) (int, bool) {
	id, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(ref)), strings.ToUpper(prefix))))
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// applyDecision 执行单条决策的落库：全部经统一写核心（Source=extractor、RespectLocked、作用域校验），
// 写限/locked/乐观锁冲突在此裁决。
func applyDecision(ctx *gin.Context, apply ApplyFunc, input *ResolveInput, itemByID map[uint]model.MemoryItem, decision *Decision, writes *int) {
	if decision.Action == ActionSkip {
		return
	}
	if *writes >= input.MaxWrites {
		decision.Action = ActionSkip
		decision.Note = "写入限额已用尽"
		return
	}

	switch decision.Action {
	case ActionAdd:
		created, err := apply(ctx, memory.MutationInput{
			Action:           "create",
			Layer:            model.MemoryLayerDetached,
			Title:            decision.Candidate.Title,
			Content:          decision.Candidate.Content,
			Description:      decision.Candidate.Description,
			MemoryType:       model.NormalizeMemoryType(decision.Candidate.Type),
			Confidence:       clampConfidence(decision.Candidate.Confidence),
			Importance:       model.NormalizeMemoryImportance(decision.Candidate.Importance),
			Tags:             joinTags(decision.Candidate.Tags),
			Reason:           decisionReason(decision, "自动沉淀新记忆"),
			Owner:            input.WriteOwner,
			Source:           model.MemorySourceExtractor,
			CreatedBy:        input.CreatedBy,
			AllowedOwnerKeys: input.AllowedKeys,
			RespectLocked:    true,
		})
		if err != nil {
			decision.Action = ActionSkip
			decision.Note = "写入失败: " + err.Error()
			return
		}
		*writes++
		decision.Applied = true
		decision.Note = createdItemNote(created)

	case ActionUpdate, ActionSupersede:
		target, ok := itemByID[decision.TargetID]
		if !ok {
			decision.Action = ActionSkip
			decision.Note = "目标条目不存在"
			return
		}
		if memory.ItemHasLockedTag(target.Tags) {
			decision.Action = ActionSkip
			decision.Note = fmt.Sprintf("记忆 #%d 带 locked 标签（只读）", target.ID)
			return
		}
		content := decision.MergedContent
		if decision.Action == ActionSupersede || content == "" {
			content = decision.Candidate.Content
		}
		// LLM 输出的合并内容不受写核心长度约束信任，落库前严格裁剪。
		content = extractor.TruncateRunesStrict(content, memory.MaxContentRunes)
		description := decision.Candidate.Description
		if description == "" {
			description = target.Description
		}
		description = extractor.TruncateRunesStrict(description, memory.MaxDescriptionRunes)
		tags := target.Tags
		if joined := joinTags(decision.Candidate.Tags); joined != "" {
			tags = joined
		}
		actionPrefix := "补充/修订既有记忆"
		if decision.Action == ActionSupersede {
			actionPrefix = "取代旧事实（旧内容见修订快照）"
		}
		mutation := memory.MutationInput{
			Action:           "update",
			ItemID:           target.ID,
			Version:          target.Version,
			Title:            target.Title,
			Content:          content,
			Description:      description,
			MemoryType:       model.NormalizeMemoryType(decision.Candidate.Type),
			Confidence:       maxConfidence(target.Confidence, clampConfidence(decision.Candidate.Confidence)),
			Importance:       model.NormalizeMemoryImportance(maxInt(target.Importance, decision.Candidate.Importance)),
			Tags:             tags,
			Reason:           decisionReason(decision, actionPrefix),
			Source:           model.MemorySourceExtractor,
			CreatedBy:        input.CreatedBy,
			AllowedOwnerKeys: input.AllowedKeys,
			RespectLocked:    true,
		}
		if _, err := apply(ctx, mutation); err != nil {
			// 乐观锁冲突：清单里的版本可能已过期，重读后重试一次；再失败按 SKIP 收敛。
			fresh, fetchErr := model.GetActiveMemoryItemByID(ctx, target.ID)
			if fetchErr != nil || fresh == nil || !strings.Contains(err.Error(), "版本冲突") {
				decision.Action = ActionSkip
				decision.Note = "更新失败: " + err.Error()
				return
			}
			mutation.Version = fresh.Version
			if _, err := apply(ctx, mutation); err != nil {
				decision.Action = ActionSkip
				decision.Note = "更新重试失败: " + err.Error()
				return
			}
		}
		*writes++
		decision.Applied = true
	}
}

func decisionReason(decision *Decision, prefix string) string {
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		// 无 LLM 决策的 ADD（无相似条目直录）回退用候选自报理由。
		reason = strings.TrimSpace(decision.Candidate.Reason)
	}
	if reason == "" {
		reason = "无"
	}
	return prefix + "：" + reason
}

func createdItemNote(result map[string]interface{}) string {
	if result == nil {
		return ""
	}
	if id, ok := result["itemId"].(uint); ok {
		return fmt.Sprintf("落库为记忆 #%d", id)
	}
	return ""
}

func joinTags(tags []string) string {
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			cleaned = append(cleaned, tag)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return strings.Join(cleaned, ",")
}

func clampConfidence(confidence float64) float64 {
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func maxConfidence(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func maxInt(a, b int) int {
	if b > a {
		return b
	}
	return a
}
