package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	memory "react-base-service/service/memory"
)

// Candidate 是一次抽取得到的候选记忆（未经写核心校验的原始形态）。
type Candidate struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Confidence  float64  `json:"confidence"`
	Importance  int      `json:"importance"`
	SimilarIDs  []uint64 `json:"similarIds"`
	Reason      string   `json:"reason"`
}

// ExtractInput 是抽取所需的全部素材；Transcript/Manifest 由触发层预先渲染与截断。
type ExtractInput struct {
	Summary       string // compact 摘要
	Transcript    string // 被压缩原文转录（已按预算截断）
	Manifest      string // 现有记忆清单（含 id/type/confidence/title/description/正文预览）
	CurrentDate   string // YYYY-MM-DD，相对时间绝对化的观察基准
	MaxCandidates int    // 候选条数上限
}

// Extract 执行一次 LLM 抽取：组装 prompt → 非工具调用 → JSON 鲁棒解析 → 字段归一化。
// 返回空切片表示模型判断没有值得沉淀的内容（非错误）；LLM 故障返回 error。
func Extract(ctx context.Context, llm LLMInvoker, input ExtractInput) ([]Candidate, error) {
	if input.MaxCandidates <= 0 {
		input.MaxCandidates = 8
	}
	text, err := llm.ChatOnce(context.Background(), ExtractionSystemPrompt, buildExtractionPrompt(input))
	if err != nil {
		return nil, fmt.Errorf("抽取调用失败: %w", err)
	}
	raw, err := ExtractJSON(text)
	if err != nil {
		return nil, fmt.Errorf("抽取输出解析失败: %w", err)
	}
	var payload struct {
		Candidates []Candidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("抽取 JSON 解析失败: %w", err)
	}
	return normalizeCandidates(payload.Candidates), nil
}

// normalizeCandidates 归一化候选字段：类型白名单、字段裁剪到写核心长度约束、丢弃无正文的条目。
// Confidence 保留原值（预过滤阈值与落库 clamp 在下游处理），避免把 LLM 未输出误抬到默认值。
func normalizeCandidates(candidates []Candidate) []Candidate {
	result := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Type = model.NormalizeMemoryType(strings.TrimSpace(candidate.Type))
		candidate.Title = TruncateRunesStrict(strings.TrimSpace(candidate.Title), memory.MaxTitleRunes)
		candidate.Content = TruncateRunesStrict(strings.TrimSpace(candidate.Content), memory.MaxContentRunes)
		candidate.Description = TruncateRunesStrict(strings.TrimSpace(candidate.Description), memory.MaxDescriptionRunes)
		candidate.Reason = strings.TrimSpace(candidate.Reason)
		if candidate.Content == "" {
			zlog.Warnf(nil, "[memory.extractor] 丢弃无正文候选: title=%q", candidate.Title)
			continue
		}
		if candidate.Title == "" {
			candidate.Title = TruncateRunesStrict(candidate.Content, memory.MaxTitleRunes)
		}
		if candidate.Description == "" {
			candidate.Description = candidate.Content
		}
		if candidate.Importance <= 0 {
			candidate.Importance = model.MemoryImportanceDefault
		}
		result = append(result, candidate)
	}
	return result
}

// PrefilterCandidates 确定性预过滤（不过 LLM）：内容指纹去重（命中存量或批内重复即丢弃）、
// 置信度阈值、按 confidence 降序的条数上限。
func PrefilterCandidates(ctx context.Context, candidates []Candidate, existingKeys map[string]struct{}, minConfidence float64, maxCount int) []Candidate {
	result := make([]Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.Confidence < minConfidence {
			zlog.Infof(ctx, "[memory.extractor] 候选置信度过低丢弃: conf=%.2f title=%q", candidate.Confidence, candidate.Title)
			continue
		}
		key := memory.ItemKey(candidate.Content)
		if _, ok := existingKeys[key]; ok {
			zlog.Infof(ctx, "[memory.extractor] 候选与存量重复丢弃: title=%q", candidate.Title)
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	if maxCount > 0 && len(result) > maxCount {
		slices.SortStableFunc(result, func(a, b Candidate) int {
			return int((b.Confidence - a.Confidence) * 100)
		})
		result = result[:maxCount]
	}
	return result
}

// 清单渲染上限：清单是给 LLM 做去重与 similar_ids 自报的，必须严格控制预算。
const (
	manifestMaxItems            = 80
	manifestMaxChars            = 4000
	manifestContentPreviewRunes = 80
)

// BuildManifest 渲染现有记忆清单：`- #id [type|conf] title — description :: 正文预览`。
func BuildManifest(items []model.MemoryItem) string {
	if len(items) == 0 {
		return "（当前没有长期记忆）"
	}
	var sb strings.Builder
	used := 0
	overflow := 0
	for i, item := range items {
		if i >= manifestMaxItems {
			overflow = len(items) - i
			break
		}
		preview := TruncateRunes(strings.TrimSpace(item.Content), manifestContentPreviewRunes)
		line := fmt.Sprintf("- #%d [%s|%.2f] %s — %s :: %s\n",
			item.ID, item.MemoryType, item.Confidence, item.Title, item.Description, preview)
		used += len([]rune(line))
		if used > manifestMaxChars {
			overflow = len(items) - i
			break
		}
		sb.WriteString(line)
	}
	if overflow > 0 {
		sb.WriteString(fmt.Sprintf("（另有 %d 条未列出）\n", overflow))
	}
	return sb.String()
}
