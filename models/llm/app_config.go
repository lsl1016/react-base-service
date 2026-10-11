package model

import (
	"errors"

	"react-base-service/components"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AppConfigSnapshot 是 App 配置复制所需的完整资源快照。
type AppConfigSnapshot struct {
	Skills           []Skill
	SystemPrompts    []SystemPrompt
	Tools            []Tool
	ToolUserPolicies []ToolUserPolicy
	ApiKeys          []ApiKey
}

// GetAppByKeyUnscoped 查询 App，并包含软删除记录。
func GetAppByKeyUnscoped(ctx *gin.Context, appKey string) (*App, error) {
	var app App
	err := GetLLMDB().WithContext(ctx).Unscoped().
		Where("app_key = ?", appKey).
		First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &app, nil
}

// GetAppByKeyWithDB 在指定数据库会话中查询未删除 App。
func GetAppByKeyWithDB(tx *gorm.DB, appKey string) (*App, error) {
	var app App
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("app_key = ?", appKey).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &app, nil
}

// GetAppByKeyUnscopedWithDB 在指定数据库会话中查询 App，并包含软删除记录。
func GetAppByKeyUnscopedWithDB(tx *gorm.DB, appKey string) (*App, error) {
	var app App
	err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("app_key = ?", appKey).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &app, nil
}

// LoadAppConfigSnapshotWithDB 加载 App 下所有未删除配置资源。
func LoadAppConfigSnapshotWithDB(tx *gorm.DB, appKey string) (*AppConfigSnapshot, error) {
	snapshot := &AppConfigSnapshot{}
	queries := []struct {
		dest  interface{}
		where string
		args  []interface{}
	}{
		{dest: &snapshot.Skills, where: "app_key = ?", args: []interface{}{appKey}},
		{dest: &snapshot.SystemPrompts, where: "app_key = ?", args: []interface{}{appKey}},
		{dest: &snapshot.Tools, where: "app_key = ?", args: []interface{}{appKey}},
		{dest: &snapshot.ApiKeys, where: "app_key = ?", args: []interface{}{appKey}},
	}
	for _, query := range queries {
		if err := tx.Where(query.where, query.args...).Find(query.dest).Error; err != nil {
			return nil, components.ErrorDbSelect.Wrap(err)
		}
	}

	toolIDs := make([]string, 0, len(snapshot.Tools))
	for i := range snapshot.Tools {
		toolIDs = append(toolIDs, snapshot.Tools[i].ToolID)
	}
	if len(toolIDs) > 0 {
		if err := tx.Where("tool_id IN ?", toolIDs).Find(&snapshot.ToolUserPolicies).Error; err != nil {
			return nil, components.ErrorDbSelect.Wrap(err)
		}
	}
	return snapshot, nil
}

// CreateAppConfigWithDB 在指定事务中写入目标 App 和全部配置资源。
func CreateAppConfigWithDB(tx *gorm.DB, app *App, snapshot *AppConfigSnapshot) error {
	if err := tx.Create(app).Error; err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	if len(snapshot.Skills) > 0 {
		if err := tx.Create(&snapshot.Skills).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
	}
	if len(snapshot.SystemPrompts) > 0 {
		if err := tx.Create(&snapshot.SystemPrompts).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
	}
	if len(snapshot.Tools) > 0 {
		if err := tx.Create(&snapshot.Tools).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
	}
	if len(snapshot.ToolUserPolicies) > 0 {
		if err := tx.Create(&snapshot.ToolUserPolicies).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
	}
	if len(snapshot.ApiKeys) > 0 {
		if err := tx.Create(&snapshot.ApiKeys).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
	}
	return nil
}

// SoftDeleteAppConfigsWithDB 软删除单个 App 及其聚合配置，并返回各资源受影响行数。
func SoftDeleteAppConfigsWithDB(tx *gorm.DB, appKey string) (map[string]int64, error) {
	stats := map[string]int64{}
	var toolIDs []string
	if err := tx.Model(&Tool{}).
		Where("app_key = ?", appKey).
		Pluck("tool_id", &toolIDs).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}

	deletes := []struct {
		key   string
		model interface{}
		where string
		args  []interface{}
	}{
		{key: "toolUserPolicies", model: &ToolUserPolicy{}, where: "tool_id IN ?", args: []interface{}{toolIDs}},
		{key: "skills", model: &Skill{}, where: "app_key = ?", args: []interface{}{appKey}},
		{key: "systemPrompts", model: &SystemPrompt{}, where: "app_key = ?", args: []interface{}{appKey}},
		{key: "tools", model: &Tool{}, where: "app_key = ?", args: []interface{}{appKey}},
		{key: "apiKeys", model: &ApiKey{}, where: "app_key = ?", args: []interface{}{appKey}},
		{key: "mcpServerApps", model: &McpServerApp{}, where: "app_key = ?", args: []interface{}{appKey}},
		{key: "apps", model: &App{}, where: "app_key = ?", args: []interface{}{appKey}},
	}
	for _, item := range deletes {
		if item.key == "toolUserPolicies" && len(toolIDs) == 0 {
			stats[item.key] = 0
			continue
		}
		result := tx.Where(item.where, item.args...).Delete(item.model)
		if result.Error != nil {
			return nil, components.ErrorDbUpdate.Wrap(result.Error)
		}
		stats[item.key] = result.RowsAffected
	}
	return stats, nil
}
