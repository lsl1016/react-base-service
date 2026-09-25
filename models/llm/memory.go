package model

import (
	"errors"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	// MemoryOwnerTypeCaller 记忆归属为 caller 维度：该调用方全部会话可见。
	MemoryOwnerTypeCaller = "caller"
	// MemoryOwnerTypeCallerUser 记忆归属为 caller+user 维度：仅该调用方下该用户的会话可见。
	MemoryOwnerTypeCallerUser = "caller_user"

	// MemoryLayerResident 常驻层：随 system 前缀全文注入。
	MemoryLayerResident = "resident"
	// MemoryLayerDetached 按需层：只注入目录索引，正文经 memory_read 按需读取。
	MemoryLayerDetached = "detached"

	MemoryStateActive  = "active"
	MemoryStateDeleted = "deleted"

	MemorySourceModel      = "model"
	MemorySourceReflection = "reflection"
	MemorySourceAdmin      = "admin"
	// MemorySourceExtractor V2 自动沉淀流水线（extractor/resolver）的写入来源。
	MemorySourceExtractor = "extractor"

	// V2 记忆类型（Phase 1 类型化）：preference 用户偏好 / fact 事实 / event 事件 / procedure 经验方法。
	MemoryTypePreference = "preference"
	MemoryTypeFact       = "fact"
	MemoryTypeEvent      = "event"
	MemoryTypeProcedure  = "procedure"

	// importance 取值域与 confidence 缺省值；与 DDL DEFAULT 成对维护。
	MemoryImportanceMin     = 1
	MemoryImportanceMax     = 5
	MemoryImportanceDefault = 3
	MemoryConfidenceDefault = 0.80
)

// memoryTypeSet 是合法记忆类型集合。
var memoryTypeSet = map[string]struct{}{
	MemoryTypePreference: {},
	MemoryTypeFact:       {},
	MemoryTypeEvent:      {},
	MemoryTypeProcedure:  {},
}

// IsValidMemoryType 判断记忆类型是否合法；管理面/入参校验用（严格拒绝）。
func IsValidMemoryType(memoryType string) bool {
	_, ok := memoryTypeSet[memoryType]
	return ok
}

// NormalizeMemoryType 归一化记忆类型：空/非法回退 fact（宽松路径：旧快照回滚、extractor 输出兜底）。
func NormalizeMemoryType(memoryType string) string {
	if _, ok := memoryTypeSet[memoryType]; ok {
		return memoryType
	}
	return MemoryTypeFact
}

// NormalizeMemoryConfidence 归一化可信度：<=0 或 >1 视为未提供/非法，回退默认值。
func NormalizeMemoryConfidence(confidence float64) float64 {
	if confidence <= 0 || confidence > 1 {
		return MemoryConfidenceDefault
	}
	return confidence
}

// NormalizeMemoryImportance 归一化重要程度：<=0 视为未提供，回退默认；越界 clamp 到 [1,5]。
func NormalizeMemoryImportance(importance int) int {
	if importance <= 0 {
		return MemoryImportanceDefault
	}
	if importance < MemoryImportanceMin {
		return MemoryImportanceMin
	}
	if importance > MemoryImportanceMax {
		return MemoryImportanceMax
	}
	return importance
}

// MemoryOwner 描述一个记忆归属维度；(OwnerType, OwnerKey) 唯一确定一个记忆空间。
type MemoryOwner struct {
	OwnerType string
	OwnerKey  string
}

// BuildCallerMemoryOwner 构造 caller 维度记忆归属。
func BuildCallerMemoryOwner(callerKey string) MemoryOwner {
	return MemoryOwner{OwnerType: MemoryOwnerTypeCaller, OwnerKey: callerKey}
}

// BuildCallerUserMemoryOwner 构造 caller+user 维度记忆归属。
func BuildCallerUserMemoryOwner(callerKey, userName string) MemoryOwner {
	return MemoryOwner{OwnerType: MemoryOwnerTypeCallerUser, OwnerKey: callerKey + "|" + userName}
}

// MemoryItem 长期记忆条目：一条原子事实，逻辑唯一键 (owner_type, owner_key, item_key)。
type MemoryItem struct {
	ID          uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	OwnerType   string    `json:"ownerType" gorm:"column:owner_type;not null"`
	OwnerKey    string    `json:"ownerKey" gorm:"column:owner_key;not null"`
	Layer       string    `json:"layer" gorm:"column:layer;not null;default:'detached'"`
	MemoryType  string    `json:"memoryType" gorm:"column:memory_type;not null;default:'fact'"`
	Confidence  float64   `json:"confidence" gorm:"column:confidence;not null;default:0.8"`
	Importance  int       `json:"importance" gorm:"column:importance;not null;default:3"`
	Title       string    `json:"title" gorm:"column:title;not null;default:''"`
	Content     string    `json:"content" gorm:"column:content;not null"`
	Description string    `json:"description" gorm:"column:description;not null;default:''"`
	Tags        string    `json:"tags" gorm:"column:tags;not null;default:''"`
	Source      string    `json:"source" gorm:"column:source;not null;default:'model'"`
	ItemKey     string    `json:"itemKey" gorm:"column:item_key;not null"`
	Version     int       `json:"version" gorm:"column:version;not null;default:1"`
	State       string    `json:"state" gorm:"column:state;not null;default:'active'"`
	LastReason  string    `json:"lastReason" gorm:"column:last_reason;not null;default:''"`
	CreatedBy   string    `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	CreatedAt   time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

func (m *MemoryItem) TableName() string {
	return "tblLlmMemoryItem"
}

// MemoryRevision 记忆修订流水：不可变，只插不改；回滚 = 用旧快照反向提交新修订。
type MemoryRevision struct {
	ID         uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ItemID     uint      `json:"itemId" gorm:"column:item_id;not null"`
	Action     string    `json:"action" gorm:"column:action;not null"`
	BeforeJSON string    `json:"beforeJSON" gorm:"column:before_json"`
	AfterJSON  string    `json:"afterJSON" gorm:"column:after_json"`
	Reason     string    `json:"reason" gorm:"column:reason;not null"`
	Source     string    `json:"source" gorm:"column:source;not null"`
	CreatedBy  string    `json:"createdBy" gorm:"column:created_by;not null"`
	CreatedAt  time.Time `json:"createdAt" gorm:"column:created_at"`
}

func (r *MemoryRevision) TableName() string {
	return "tblLlmMemoryRevision"
}

// buildMemoryOwnerScopeCondition 构造 owners 的 OR 查询条件；state 必须并入每个分支，
// GORM 的 Where(...).Or(...) 组合不会把先置条件下推进 Or 分支，漏并会把软删条目泄漏进结果。
func buildMemoryOwnerScopeCondition(owners []MemoryOwner) (string, []interface{}) {
	conditions := make([]string, 0, len(owners))
	args := make([]interface{}, 0, len(owners)*3)
	for _, owner := range owners {
		conditions = append(conditions, "(owner_type = ? AND owner_key = ? AND state = ?)")
		args = append(args, owner.OwnerType, owner.OwnerKey, MemoryStateActive)
	}
	return strings.Join(conditions, " OR "), args
}

// FindActiveMemoryItemsByOwners 查询一组记忆空间下的全部 active 条目，按更新时间倒序。
func FindActiveMemoryItemsByOwners(ctx *gin.Context, owners []MemoryOwner) ([]MemoryItem, error) {
	if len(owners) == 0 {
		return []MemoryItem{}, nil
	}
	condition, args := buildMemoryOwnerScopeCondition(owners)
	var items []MemoryItem
	err := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx).
		Where(condition, args...).
		Order("updated_at DESC").Find(&items).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return items, nil
}

// GetActiveMemoryItemByID 按 ID 查 active 条目；不存在返回 nil。
func GetActiveMemoryItemByID(ctx *gin.Context, id uint) (*MemoryItem, error) {
	var item MemoryItem
	err := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx).
		Where("id = ? AND state = ?", id, MemoryStateActive).First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

// FindMemoryItemByOwnerAndKeyWithDB 在指定事务内按逻辑唯一键查条目（含已软删，供幂等收敛/复活）。
func FindMemoryItemByOwnerAndKeyWithDB(ctx *gin.Context, tx *gorm.DB, owner MemoryOwner, itemKey string) (*MemoryItem, error) {
	var item MemoryItem
	err := tx.Model(&MemoryItem{}).WithContext(ctx).
		Where("owner_type = ? AND owner_key = ? AND item_key = ?", owner.OwnerType, owner.OwnerKey, itemKey).
		First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

// CountActiveMemoryItemsWithDB 统计指定记忆空间某层的 active 条目数，用于常驻/按需上限守门。
func CountActiveMemoryItemsWithDB(ctx *gin.Context, tx *gorm.DB, owner MemoryOwner, layer string) (int64, error) {
	var count int64
	query := tx.Model(&MemoryItem{}).WithContext(ctx).
		Where("owner_type = ? AND owner_key = ? AND state = ?", owner.OwnerType, owner.OwnerKey, MemoryStateActive)
	if layer != "" {
		query = query.Where("layer = ?", layer)
	}
	err := query.Count(&count).Error
	if err != nil {
		return 0, components.ErrorDbSelect.Wrap(err)
	}
	return count, nil
}

// CreateMemoryItemWithDB 在指定事务内创建记忆条目。
func CreateMemoryItemWithDB(ctx *gin.Context, tx *gorm.DB, item *MemoryItem) error {
	err := tx.Model(&MemoryItem{}).WithContext(ctx).Create(item).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// UpdateMemoryItemWithVersionWithDB 带乐观锁更新条目；expectedVersion>0 时必须与当前版本一致，否则返回 false。
// 更新恒定递增 version，由 updates 显式传入。
func UpdateMemoryItemWithVersionWithDB(ctx *gin.Context, tx *gorm.DB, id uint, expectedVersion int, updates map[string]interface{}) (bool, error) {
	query := tx.Model(&MemoryItem{}).WithContext(ctx).Where("id = ?", id)
	if expectedVersion > 0 {
		query = query.Where("version = ?", expectedVersion)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return false, components.ErrorDbUpdate.Wrap(result.Error)
	}
	return result.RowsAffected > 0, nil
}

// CreateMemoryRevisionWithDB 在指定事务内追加修订流水。
func CreateMemoryRevisionWithDB(ctx *gin.Context, tx *gorm.DB, revision *MemoryRevision) error {
	err := tx.Model(&MemoryRevision{}).WithContext(ctx).Create(revision).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// FindMemoryRevisionsByItemID 查询条目的修订历史（正序），供管理面与排障使用。
func FindMemoryRevisionsByItemID(ctx *gin.Context, itemID uint) ([]MemoryRevision, error) {
	var revisions []MemoryRevision
	err := helpers.MysqlClientLLM.Model(&MemoryRevision{}).WithContext(ctx).
		Where("item_id = ?", itemID).Order("id ASC").Find(&revisions).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return revisions, nil
}

// GetMemoryRevisionByID 按主键查修订；不存在返回 nil。
func GetMemoryRevisionByID(ctx *gin.Context, id uint) (*MemoryRevision, error) {
	var revision MemoryRevision
	err := helpers.MysqlClientLLM.Model(&MemoryRevision{}).WithContext(ctx).
		Where("id = ?", id).First(&revision).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &revision, nil
}

// GetMemoryItemByIDAnyState 按 ID 查条目（含软删），供回滚等管理操作使用；不存在返回 nil。
func GetMemoryItemByIDAnyState(ctx *gin.Context, id uint) (*MemoryItem, error) {
	var item MemoryItem
	err := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx).
		Where("id = ?", id).First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

// MemoryItemFilter 是管理面列表查询条件；零值字段不参与过滤。
type MemoryItemFilter struct {
	OwnerType  string
	OwnerKey   string
	Layer      string
	MemoryType string
	Tag        string
	Keyword    string
	// IncludeDeleted 为 true 时包含软删条目（管理面审计视图）。
	IncludeDeleted bool
	Limit          int
	Offset         int
}

// FindMemoryItemsByFilter 按条件分页查询记忆条目，返回明细与总数（不含分页）。
func FindMemoryItemsByFilter(ctx *gin.Context, filter MemoryItemFilter) ([]MemoryItem, int64, error) {
	query := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx)
	if filter.OwnerType != "" {
		query = query.Where("owner_type = ?", filter.OwnerType)
	}
	if filter.OwnerKey != "" {
		query = query.Where("owner_key = ?", filter.OwnerKey)
	}
	if filter.Layer != "" {
		query = query.Where("layer = ?", filter.Layer)
	}
	if filter.MemoryType != "" {
		query = query.Where("memory_type = ?", filter.MemoryType)
	}
	if filter.Tag != "" {
		query = query.Where("FIND_IN_SET(?, tags)", filter.Tag)
	}
	if filter.Keyword != "" {
		like := "%" + filter.Keyword + "%"
		query = query.Where("title LIKE ? OR description LIKE ? OR tags LIKE ? OR content LIKE ?", like, like, like, like)
	}
	if !filter.IncludeDeleted {
		query = query.Where("state = ?", MemoryStateActive)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, components.ErrorDbSelect.Wrap(err)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var items []MemoryItem
	err := query.Order("updated_at DESC").Limit(limit).Offset(filter.Offset).Find(&items).Error
	if err != nil {
		return nil, 0, components.ErrorDbSelect.Wrap(err)
	}
	return items, total, nil
}

// MemoryOwnerLayerCount 是指标聚合行：active 条目数按 owner_type × layer。
type MemoryOwnerLayerCount struct {
	OwnerType string
	Layer     string
	Count     int64
}

// CountActiveMemoryItemsGrouped 按 owner_type × layer 聚合 active 条目数（指标观测）。
func CountActiveMemoryItemsGrouped(ctx *gin.Context) ([]MemoryOwnerLayerCount, error) {
	var rows []MemoryOwnerLayerCount
	err := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx).
		Select("owner_type, layer, COUNT(*) AS count").
		Where("state = ?", MemoryStateActive).
		Group("owner_type, layer").
		Scan(&rows).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

// MemoryResidentChars 是指标聚合行：常驻层字符量按 owner_type。
type MemoryResidentChars struct {
	OwnerType string
	Chars     int64
}

// SumResidentCharsGroupedByOwnerType 聚合常驻层正文字符量（CHAR_LENGTH 按 rune 计，指标观测）。
func SumResidentCharsGroupedByOwnerType(ctx *gin.Context) ([]MemoryResidentChars, error) {
	var rows []MemoryResidentChars
	err := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx).
		Select("owner_type, COALESCE(SUM(CHAR_LENGTH(content)), 0) AS chars").
		Where("state = ? AND layer = ?", MemoryStateActive, MemoryLayerResident).
		Group("owner_type").
		Scan(&rows).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

// MemoryTypeCount 是指标聚合行：active 条目数按 owner_type × memory_type。
type MemoryTypeCount struct {
	OwnerType  string
	MemoryType string
	Count      int64
}

// CountActiveMemoryItemsGroupedByType 按 owner_type × memory_type 聚合 active 条目数（指标观测）。
func CountActiveMemoryItemsGroupedByType(ctx *gin.Context) ([]MemoryTypeCount, error) {
	var rows []MemoryTypeCount
	err := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx).
		Select("owner_type, memory_type, COUNT(*) AS count").
		Where("state = ?", MemoryStateActive).
		Group("owner_type, memory_type").
		Scan(&rows).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

// FindActiveMemoryItemsLikeOwner 在单个记忆空间内按关键词多列 LIKE 查询 active 条目（V2 Resolver 候选召回）。
// keywords 任一命中 title/description/content/tags 即返回；命中集合的去重与打分由调用方完成。
// 返回按更新时间倒序，limit<=0 时取 20。
func FindActiveMemoryItemsLikeOwner(ctx *gin.Context, owner MemoryOwner, keywords []string, limit int) ([]MemoryItem, error) {
	condition, args, ok := buildMemoryKeywordLikeCondition(owner, keywords)
	if !ok {
		return []MemoryItem{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	var items []MemoryItem
	err := helpers.MysqlClientLLM.Model(&MemoryItem{}).WithContext(ctx).
		Where(condition, args...).
		Order("updated_at DESC").Limit(limit).Find(&items).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return items, nil
}

// buildMemoryKeywordLikeCondition 构造单 owner 的关键词 LIKE 条件：每个关键词命中
// title/description/content/tags 任一列即算命中；≥2 rune 的关键词才参与（防单字符全表扫）。
// 无有效关键词时 ok=false，调用方应直接返回空集。
func buildMemoryKeywordLikeCondition(owner MemoryOwner, keywords []string) (string, []interface{}, bool) {
	trimmed := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if keyword = strings.TrimSpace(keyword); len([]rune(keyword)) >= 2 {
			trimmed = append(trimmed, keyword)
		}
	}
	if len(trimmed) == 0 {
		return "", nil, false
	}
	conditions := make([]string, 0, len(trimmed))
	args := make([]interface{}, 0, len(trimmed)*4)
	for _, keyword := range trimmed {
		like := "%" + keyword + "%"
		conditions = append(conditions, "(title LIKE ? OR description LIKE ? OR content LIKE ? OR tags LIKE ?)")
		args = append(args, like, like, like, like)
	}
	condition := "owner_type = ? AND owner_key = ? AND state = ? AND (" + strings.Join(conditions, " OR ") + ")"
	args = append([]interface{}{owner.OwnerType, owner.OwnerKey, MemoryStateActive}, args...)
	return condition, args, true
}
