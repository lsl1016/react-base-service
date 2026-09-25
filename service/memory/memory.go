// Package memory 提供长期记忆的统一写核心：引擎工具（source=model）与管理面接口（source=admin）
// 共享同一套校验、幂等收敛、乐观锁、修订流水与敏感信息拦截，保证审计链完整、无旁门写入。
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	// 条目字段长度约束（按 rune 计），与 DDL 注释保持一致。
	MaxTitleRunes       = 32
	MaxContentRunes     = 500
	MaxDescriptionRunes = 512
	MaxReasonRunes      = 512
)

// MutationInput 描述一次记忆写操作；来源（引擎/管理面/reflection/extractor）只影响 Source 与 CreatedBy。
type MutationInput struct {
	Action string // create / update / delete
	ItemID uint   // update/delete 必填
	// Version 是乐观锁版本号：>0 时必须与条目当前版本一致，否则报冲突；0 表示直接覆盖。
	Version     int
	Layer       string // resident / detached；create 空默认 detached，update 空保持原层级
	Title       string
	Content     string
	Description string // 检索提示
	Tags        string
	// V2 类型化字段（Phase 1）：create 空值落默认（fact/0.80/3）；update 空值保持原字段不变。
	MemoryType string
	Confidence float64
	Importance int
	Reason     string // 必填，审计根
	// Owner 是 create 的目标记忆空间。
	Owner model.MemoryOwner
	// Source 是写入来源：model / admin / reflection / extractor。
	Source string
	// CreatedBy 是触发方标识：runID 或管理面操作人。
	CreatedBy string
	// AllowedOwnerKeys 是可操作的记忆空间集合（OwnerScopeKey）；nil 表示不限制（管理面）。
	AllowedOwnerKeys map[string]struct{}
	// RespectLocked 为 true 时，tags 含 locked 的既有条目视为只读（update/delete/收敛合并均拒绝）。
	// reflection 整理开启此约束，管理面不开启（保留最终干预权）。
	RespectLocked bool
}

// OwnerScopeKey 生成记忆空间的集合键，供作用域校验复用（引擎与写核心必须同构）。
func OwnerScopeKey(ownerType, ownerKey string) string {
	return ownerType + "|" + ownerKey
}

// ItemKey 计算记忆正文的语义指纹：规范化（去首尾空白、折叠连续空白、ASCII 小写）后取 SHA-256 前 16 位。
// 同一事实的细微排版差异收敛为同一 ItemKey，实现 create 幂等。
func ItemKey(content string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(content), " "))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:16]
}

// NormalizeTags 归一化标签串：按逗号切分、去空、去重、保持原顺序。
func NormalizeTags(tags string) string {
	parts := strings.Split(tags, ",")
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return strings.Join(result, ",")
}

// ValidatePayload 校验条目内容字段（create/update 共用）。
func ValidatePayload(title, content, description string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("title 必填")
	}
	if len([]rune(title)) > MaxTitleRunes {
		return fmt.Errorf("title 超长（≤%d字）", MaxTitleRunes)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("content 必填")
	}
	if len([]rune(content)) > MaxContentRunes {
		return fmt.Errorf("content 超长（≤%d字）", MaxContentRunes)
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("description 必填")
	}
	if len([]rune(description)) > MaxDescriptionRunes {
		return fmt.Errorf("description 超长（≤%d字）", MaxDescriptionRunes)
	}
	return nil
}

// memorySensitivePattern 内置敏感信息指纹：命中任一即拒绝入库。
type memorySensitivePattern struct {
	Name    string
	Pattern *regexp.Regexp
}

// 内置拦截规则：常见凭证/密钥形态与中国身份证/手机号。
// 拦截目标是"整段形态匹配"，不做语义判断；误伤由调用方改写表述后重试。
var memorySensitivePatterns = []memorySensitivePattern{
	{Name: "OpenAI 风格 API Key", Pattern: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	{Name: "AWS AccessKey", Pattern: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{Name: "GitHub Token", Pattern: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`)},
	{Name: "JWT", Pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`)},
	{Name: "私钥文件", Pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{Name: "身份证号", Pattern: regexp.MustCompile(`\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:[0-2]\d|3[01])\d{3}[\dXx]\b`)},
	{Name: "手机号", Pattern: regexp.MustCompile(`\b1[3-9]\d{9}\b`)},
}

// ScanSensitiveContent 扫描写入口径下的文本字段，命中敏感形态即拒绝。
func ScanSensitiveContent(fields ...string) error {
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		for _, rule := range memorySensitivePatterns {
			if rule.Pattern.MatchString(field) {
				return fmt.Errorf("内容包含疑似敏感信息（%s），禁止写入长期记忆；如确需记录请脱敏后再写入", rule.Name)
			}
		}
	}
	return nil
}

// ApplyMutation 是记忆唯一写入口：归一化 → 校验 → 敏感拦截 → 事务写入（含修订流水）→ 打点。
// 引擎工具与管理面都必须经此函数落库。
func ApplyMutation(ctx *gin.Context, input MutationInput) (map[string]interface{}, error) {
	result, err := applyMutation(ctx, input)
	status := "success"
	if err != nil {
		status = "error"
	}
	action := strings.TrimSpace(input.Action)
	if action == "" {
		action = "unknown"
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "unknown"
	}
	metrics.MemoryWritesTotal.WithLabelValues(action, source, status).Inc()
	if err == nil {
		RefreshMetrics(ctx)
	}
	return result, err
}

func applyMutation(ctx *gin.Context, input MutationInput) (map[string]interface{}, error) {
	input.Action = strings.TrimSpace(input.Action)
	input.Reason = strings.TrimSpace(input.Reason)
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	input.Description = strings.TrimSpace(input.Description)
	input.Tags = NormalizeTags(input.Tags)
	input.Layer = strings.TrimSpace(input.Layer)

	if input.Source == "" {
		input.Source = model.MemorySourceModel
	}
	if input.Reason == "" {
		return nil, fmt.Errorf("reason 必填：说明为什么本次写入/修改/删除")
	}
	if len([]rune(input.Reason)) > MaxReasonRunes {
		return nil, fmt.Errorf("reason 超长（≤%d字）", MaxReasonRunes)
	}
	if input.Layer != "" && input.Layer != model.MemoryLayerResident && input.Layer != model.MemoryLayerDetached {
		return nil, fmt.Errorf("layer 仅支持 resident/detached")
	}
	if input.MemoryType != "" && !model.IsValidMemoryType(input.MemoryType) {
		return nil, fmt.Errorf("memory_type 仅支持 preference/fact/event/procedure")
	}
	if input.Confidence < 0 || input.Confidence > 1 {
		return nil, fmt.Errorf("confidence 取值范围 0-1")
	}
	if input.Importance != 0 && (input.Importance < model.MemoryImportanceMin || input.Importance > model.MemoryImportanceMax) {
		return nil, fmt.Errorf("importance 取值范围 %d-%d", model.MemoryImportanceMin, model.MemoryImportanceMax)
	}
	if err := ScanSensitiveContent(input.Title, input.Content, input.Description, input.Reason); err != nil {
		return nil, err
	}

	cfg := conf.GetReactRuntimeConfig().Memory
	var result map[string]interface{}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var txErr error
		switch input.Action {
		case "create":
			result, txErr = mutateCreate(ctx, tx, &input, cfg)
		case "update":
			result, txErr = mutateUpdate(ctx, tx, &input, cfg)
		case "delete":
			result, txErr = mutateDelete(ctx, tx, &input)
		default:
			txErr = fmt.Errorf("action 仅支持 create/update/delete")
		}
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// mutateCreate 新增记忆；同 ItemKey 的存量条目（含软删）自动收敛为更新/复活，保证幂等。
func mutateCreate(ctx *gin.Context, tx *gorm.DB, op *MutationInput, cfg conf.ReactMemoryConfig) (map[string]interface{}, error) {
	if err := ValidatePayload(op.Title, op.Content, op.Description); err != nil {
		return nil, err
	}
	if op.AllowedOwnerKeys != nil {
		if _, ok := op.AllowedOwnerKeys[OwnerScopeKey(op.Owner.OwnerType, op.Owner.OwnerKey)]; !ok {
			return nil, fmt.Errorf("目标记忆空间不在当前作用域内，无权写入")
		}
	}
	layer := op.Layer
	if layer == "" {
		layer = model.MemoryLayerDetached
	}

	itemKey := ItemKey(op.Content)
	existing, err := model.FindMemoryItemByOwnerAndKeyWithDB(ctx, tx, op.Owner, itemKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if op.RespectLocked && ItemHasLockedTag(existing.Tags) {
			return nil, fmt.Errorf("记忆 #%d 带 locked 标签（只读），本次操作被拒绝", existing.ID)
		}
		// 幂等收敛：同一事实重复写入转为更新（软删条目同时复活），reason 标注收敛语义。
		convOp := &MutationInput{
			ItemID:        existing.ID,
			Layer:         firstNonEmpty(op.Layer, existing.Layer),
			Title:         op.Title,
			Content:       op.Content,
			Description:   op.Description,
			Tags:          op.Tags,
			MemoryType:    op.MemoryType,
			Confidence:    op.Confidence,
			Importance:    op.Importance,
			Reason:        "create 命中同指纹条目，收敛为更新；" + op.Reason,
			Source:        op.Source,
			CreatedBy:     op.CreatedBy,
			RespectLocked: op.RespectLocked,
		}
		result, updateErr := applyUpdate(ctx, tx, existing, convOp, cfg, true)
		if updateErr != nil {
			return nil, updateErr
		}
		result["converged"] = true
		return result, nil
	}

	owner := op.Owner
	if err := checkLayerCap(ctx, tx, owner, layer, cfg, 1); err != nil {
		return nil, err
	}
	item := &model.MemoryItem{
		OwnerType:   owner.OwnerType,
		OwnerKey:    owner.OwnerKey,
		Layer:       layer,
		MemoryType:  model.NormalizeMemoryType(op.MemoryType),
		Confidence:  model.NormalizeMemoryConfidence(op.Confidence),
		Importance:  model.NormalizeMemoryImportance(op.Importance),
		Title:       op.Title,
		Content:     op.Content,
		Description: op.Description,
		Tags:        op.Tags,
		Source:      op.Source,
		ItemKey:     itemKey,
		Version:     1,
		State:       model.MemoryStateActive,
		LastReason:  op.Reason,
		CreatedBy:   op.CreatedBy,
	}
	if err := model.CreateMemoryItemWithDB(ctx, tx, item); err != nil {
		return nil, err
	}
	if err := createRevision(ctx, tx, item.ID, "create", nil, item, op.Reason, op.Source, op.CreatedBy); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"action":  "create",
		"itemId":  item.ID,
		"layer":   item.Layer,
		"version": item.Version,
		"reason":  op.Reason,
	}, nil
}

// mutateUpdate 按 itemId 更新记忆；容量守门按条目自身所属空间计算。
func mutateUpdate(ctx *gin.Context, tx *gorm.DB, op *MutationInput, cfg conf.ReactMemoryConfig) (map[string]interface{}, error) {
	if op.ItemID == 0 {
		return nil, fmt.Errorf("update 需要 itemId")
	}
	item, err := loadItemForMutation(ctx, tx, op, op.ItemID)
	if err != nil {
		return nil, err
	}
	if err := ValidatePayload(op.Title, op.Content, op.Description); err != nil {
		return nil, err
	}
	return applyUpdate(ctx, tx, item, op, cfg, false)
}

// applyUpdate 执行条目更新（含 create 收敛复用）：乐观锁 + 层级守门 + 修订流水。
func applyUpdate(ctx *gin.Context, tx *gorm.DB, item *model.MemoryItem, op *MutationInput, cfg conf.ReactMemoryConfig, ignoreVersion bool) (map[string]interface{}, error) {
	targetLayer := item.Layer
	if op.Layer != "" {
		targetLayer = op.Layer
	}
	tags := item.Tags
	if op.Tags != "" {
		tags = op.Tags
	}
	itemOwner := model.MemoryOwner{OwnerType: item.OwnerType, OwnerKey: item.OwnerKey}
	if targetLayer == model.MemoryLayerResident && item.Layer != model.MemoryLayerResident {
		// detached → resident 需要为常驻层腾出容量（本条不计入存量）。
		if err := checkLayerCap(ctx, tx, itemOwner, targetLayer, cfg, 1); err != nil {
			return nil, err
		}
	}

	before := *item
	updates := map[string]interface{}{
		"layer":       targetLayer,
		"title":       op.Title,
		"content":     op.Content,
		"description": op.Description,
		"tags":        tags,
		"state":       model.MemoryStateActive,
		"last_reason": op.Reason,
		"version":     item.Version + 1,
	}
	// 类型化字段：update 只在显式提供时覆盖，否则保持原值（零值=未指定的语义）。
	if op.MemoryType != "" {
		updates["memory_type"] = model.NormalizeMemoryType(op.MemoryType)
	}
	if op.Confidence > 0 {
		updates["confidence"] = op.Confidence
	}
	if op.Importance > 0 {
		updates["importance"] = model.NormalizeMemoryImportance(op.Importance)
	}
	expectedVersion := 0
	if !ignoreVersion {
		expectedVersion = op.Version
	}
	updated, err := model.UpdateMemoryItemWithVersionWithDB(ctx, tx, item.ID, expectedVersion, updates)
	if err != nil {
		return nil, err
	}
	if !updated {
		if op.Version > 0 {
			return nil, fmt.Errorf("记忆 #%d 版本冲突（当前版本已变化），请重读后重试", item.ID)
		}
		return nil, fmt.Errorf("记忆 #%d 更新失败：条目不存在或状态异常", item.ID)
	}
	after := before
	after.Layer = targetLayer
	after.Title = op.Title
	after.Content = op.Content
	after.Description = op.Description
	after.Tags = tags
	after.State = model.MemoryStateActive
	after.LastReason = op.Reason
	after.Version = before.Version + 1
	if op.MemoryType != "" {
		after.MemoryType = model.NormalizeMemoryType(op.MemoryType)
	}
	if op.Confidence > 0 {
		after.Confidence = op.Confidence
	}
	if op.Importance > 0 {
		after.Importance = model.NormalizeMemoryImportance(op.Importance)
	}
	if err := createRevision(ctx, tx, item.ID, "update", &before, &after, op.Reason, op.Source, op.CreatedBy); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"action":  "update",
		"itemId":  item.ID,
		"layer":   targetLayer,
		"version": after.Version,
		"reason":  op.Reason,
	}, nil
}

// mutateDelete 软删记忆。
func mutateDelete(ctx *gin.Context, tx *gorm.DB, op *MutationInput) (map[string]interface{}, error) {
	if op.ItemID == 0 {
		return nil, fmt.Errorf("delete 需要 itemId")
	}
	item, err := loadItemForMutation(ctx, tx, op, op.ItemID)
	if err != nil {
		return nil, err
	}
	before := *item
	updated, err := model.UpdateMemoryItemWithVersionWithDB(ctx, tx, item.ID, op.Version, map[string]interface{}{
		"state":       model.MemoryStateDeleted,
		"last_reason": op.Reason,
		"version":     item.Version + 1,
	})
	if err != nil {
		return nil, err
	}
	if !updated {
		if op.Version > 0 {
			return nil, fmt.Errorf("记忆 #%d 版本冲突（当前版本已变化），请重读后重试", item.ID)
		}
		return nil, fmt.Errorf("记忆 #%d 删除失败：条目不存在或状态异常", item.ID)
	}
	if err := createRevision(ctx, tx, item.ID, "delete", &before, nil, op.Reason, op.Source, op.CreatedBy); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"action":  "delete",
		"itemId":  item.ID,
		"version": before.Version + 1,
		"reason":  op.Reason,
	}, nil
}

// loadItemForMutation 在事务内按 ID 取 active 条目并做作用域校验（update/delete 共用）。
func loadItemForMutation(ctx *gin.Context, tx *gorm.DB, op *MutationInput, itemID uint) (*model.MemoryItem, error) {
	var item model.MemoryItem
	err := tx.Model(&model.MemoryItem{}).WithContext(ctx).
		Where("id = ? AND state = ?", itemID, model.MemoryStateActive).First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("记忆 #%d 不存在或已删除", itemID)
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	if op.AllowedOwnerKeys != nil {
		if _, ok := op.AllowedOwnerKeys[OwnerScopeKey(item.OwnerType, item.OwnerKey)]; !ok {
			return nil, fmt.Errorf("记忆 #%d 不在当前作用域内，无权操作", itemID)
		}
	}
	if op.RespectLocked && ItemHasLockedTag(item.Tags) {
		return nil, fmt.Errorf("记忆 #%d 带 locked 标签（只读），本次操作被拒绝", itemID)
	}
	return &item, nil
}

// ItemHasLockedTag 判断 tags 是否含 locked（用户钉死的条目，reflection 只读）。
func ItemHasLockedTag(tags string) bool {
	for _, part := range strings.Split(tags, ",") {
		if strings.TrimSpace(part) == memoryLockedTag {
			return true
		}
	}
	return false
}

// memoryLockedTag 是只读保护标签名。
const memoryLockedTag = "locked"

// RollbackRevision 把条目回滚到指定修订的 before 快照：以 rollback 动作反向提交一条新修订，
// 修订流水只增不改；回滚 delete 修订可复活条目，create 修订没有前置状态、拒绝回滚。
func RollbackRevision(ctx *gin.Context, revisionID uint, reason, operator string) (map[string]interface{}, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("reason 必填：说明为什么回滚")
	}
	reason = "回滚至修订 #" + fmt.Sprintf("%d", revisionID) + "：" + strings.TrimSpace(reason)

	result, err := rollbackRevision(ctx, revisionID, reason, operator)
	status := "success"
	if err != nil {
		status = "error"
	}
	metrics.MemoryWritesTotal.WithLabelValues("rollback", model.MemorySourceAdmin, status).Inc()
	if err == nil {
		RefreshMetrics(ctx)
	}
	return result, err
}

func rollbackRevision(ctx *gin.Context, revisionID uint, reason, operator string) (map[string]interface{}, error) {
	revision, err := model.GetMemoryRevisionByID(ctx, revisionID)
	if err != nil {
		return nil, err
	}
	if revision == nil {
		return nil, fmt.Errorf("修订 #%d 不存在", revisionID)
	}
	if strings.TrimSpace(revision.BeforeJSON) == "" {
		return nil, fmt.Errorf("修订 #%d 是 create（没有前置状态），不能回滚；删除请使用 delete", revisionID)
	}
	var target model.MemoryItem
	if err := json.Unmarshal([]byte(revision.BeforeJSON), &target); err != nil {
		return nil, fmt.Errorf("修订 #%d 前置快照解析失败: %v", revisionID, err)
	}
	// V2 兼容：Phase 1 之前的旧快照没有类型化字段，反序列化后为零值，按默认口径补齐。
	target.MemoryType = model.NormalizeMemoryType(target.MemoryType)
	target.Confidence = model.NormalizeMemoryConfidence(target.Confidence)
	target.Importance = model.NormalizeMemoryImportance(target.Importance)

	currentVersion := 0
	err = model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.MemoryItem
		err := tx.Model(&model.MemoryItem{}).WithContext(ctx).
			Where("id = ?", revision.ItemID).First(&item).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("记忆 #%d 已不存在，无法回滚", revision.ItemID)
			}
			return components.ErrorDbSelect.Wrap(err)
		}
		currentVersion = item.Version
		before := item
		updated, err := model.UpdateMemoryItemWithVersionWithDB(ctx, tx, item.ID, 0, map[string]interface{}{
			"layer":       target.Layer,
			"memory_type": target.MemoryType,
			"confidence":  target.Confidence,
			"importance":  target.Importance,
			"title":       target.Title,
			"content":     target.Content,
			"description": target.Description,
			"tags":        target.Tags,
			"state":       target.State,
			"last_reason": reason,
			"version":     item.Version + 1,
		})
		if err != nil {
			return err
		}
		if !updated {
			return fmt.Errorf("记忆 #%d 回滚失败：条目不存在或状态异常", item.ID)
		}
		after := target
		after.ID = item.ID
		after.Version = item.Version + 1
		after.LastReason = reason
		return createRevision(ctx, tx, item.ID, "rollback", &before, &after, reason, model.MemorySourceAdmin, operator)
	})
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"action":     "rollback",
		"itemId":     revision.ItemID,
		"revisionId": revisionID,
		"restored": map[string]interface{}{
			"layer":      target.Layer,
			"memoryType": target.MemoryType,
			"title":      target.Title,
			"content":    target.Content,
			"state":      target.State,
		},
		"version": currentVersion + 1,
		"reason":  reason,
	}, nil
}

// checkLayerCap 常驻/按需层上限守门：extra 是本次操作将要新增的条目数（纯新增传 1），
// 结果总数超过上限才拒绝（上限值本身可达成）。
func checkLayerCap(ctx *gin.Context, tx *gorm.DB, owner model.MemoryOwner, layer string, cfg conf.ReactMemoryConfig, extra int64) error {
	if layer == model.MemoryLayerResident {
		count, err := model.CountActiveMemoryItemsWithDB(ctx, tx, owner, model.MemoryLayerResident)
		if err != nil {
			return err
		}
		if count+extra > int64(cfg.ResidentMaxItems) {
			return fmt.Errorf("常驻层已满（上限 %d 条）：请先将一条常驻记忆改为 detached（update layer=detached）再写入", cfg.ResidentMaxItems)
		}
		return nil
	}
	count, err := model.CountActiveMemoryItemsWithDB(ctx, tx, owner, "")
	if err != nil {
		return err
	}
	if count+extra > int64(cfg.DetachedMaxItems) {
		return fmt.Errorf("该记忆空间条目数已达软上限（%d 条）：请先整理，删除或合并过时记忆后再写入", cfg.DetachedMaxItems)
	}
	return nil
}

// createRevision 落一条不可变修订流水。
func createRevision(ctx *gin.Context, tx *gorm.DB, itemID uint, action string, before, after *model.MemoryItem, reason, source, createdBy string) error {
	revision := &model.MemoryRevision{
		ItemID:    itemID,
		Action:    action,
		Reason:    reason,
		Source:    source,
		CreatedBy: createdBy,
	}
	var err error
	if before != nil {
		if revision.BeforeJSON, err = snapshotJSON(before); err != nil {
			return err
		}
	}
	if after != nil {
		if revision.AfterJSON, err = snapshotJSON(after); err != nil {
			return err
		}
	}
	return model.CreateMemoryRevisionWithDB(ctx, tx, revision)
}

func snapshotJSON(item *model.MemoryItem) (string, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// RefreshMetrics 重算记忆水位指标（条目数按 owner_type×layer 与 owner_type×memory_type、常驻字符按 owner_type）；
// best-effort：失败仅记日志，不影响写路径。
func RefreshMetrics(ctx *gin.Context) {
	counts, err := model.CountActiveMemoryItemsGrouped(ctx)
	if err != nil {
		zlog.Warnf(ctx, "[memory] 刷新记忆条目指标失败: %v", err)
		return
	}
	metrics.MemoryItems.Reset()
	for _, row := range counts {
		metrics.MemoryItems.WithLabelValues(row.OwnerType, row.Layer).Set(float64(row.Count))
	}
	typeCounts, err := model.CountActiveMemoryItemsGroupedByType(ctx)
	if err != nil {
		zlog.Warnf(ctx, "[memory] 刷新记忆类型指标失败: %v", err)
		return
	}
	metrics.MemoryItemsByType.Reset()
	for _, row := range typeCounts {
		metrics.MemoryItemsByType.WithLabelValues(row.OwnerType, row.MemoryType).Set(float64(row.Count))
	}
	chars, err := model.SumResidentCharsGroupedByOwnerType(ctx)
	if err != nil {
		zlog.Warnf(ctx, "[memory] 刷新常驻字符指标失败: %v", err)
		return
	}
	metrics.MemoryResidentChars.Reset()
	for _, row := range chars {
		metrics.MemoryResidentChars.WithLabelValues(row.OwnerType).Set(float64(row.Chars))
	}
}
