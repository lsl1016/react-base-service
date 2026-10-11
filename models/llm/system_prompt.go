package model

import (
	"errors"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

type SystemPrompt struct {
	ID          uint                  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	AppKey   string                `json:"appKey" gorm:"column:app_key;not null"`
	RouteValues string                `json:"routeValues" gorm:"column:route_values"`
	Name        string                `json:"name" gorm:"column:name;not null"`
	Content     string                `json:"content" gorm:"column:content;not null"`
	Status      int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy   string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy   string                `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt   time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt   soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (s *SystemPrompt) TableName() string {
	return "tblLlmSystemPrompt"
}

func CreateSystemPrompt(ctx *gin.Context, sp *SystemPrompt) error {
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).Create(sp).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetSystemPromptByID(ctx *gin.Context, id uint) (*SystemPrompt, error) {
	var sp SystemPrompt
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Where("id = ?", id).First(&sp).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &sp, nil
}

// ExistSystemPromptByAppAndRoute 检查 app+route 组合下是否已有有效记录
func ExistSystemPromptByAppAndRoute(ctx *gin.Context, appKey, routeValues string) (bool, error) {
	var count int64
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Where("app_key = ? AND route_values = ?", appKey, routeValues).
		Count(&count).Error
	if err != nil {
		return false, components.ErrorDbSelect.Wrap(err)
	}
	return count > 0, nil
}

func UpdateSystemPromptByID(ctx *gin.Context, id uint, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Where("id = ?", id).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func SoftDeleteSystemPromptByID(ctx *gin.Context, id uint) error {
	tx := helpers.MysqlClientLLM.WithContext(ctx).Where("id = ?", id).Delete(&SystemPrompt{})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListSystemPromptsByApp(ctx *gin.Context, appKey string) ([]SystemPrompt, error) {
	var prompts []SystemPrompt
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Where("app_key = ?", appKey).
		Order("created_at DESC").
		Find(&prompts).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return prompts, nil
}

// ListSystemPromptsByAppAndRoutes 按 appKey + 路由前缀匹配查询系统提示词列表（不过滤 status，管理后台用）
func ListSystemPromptsByAppAndRoutes(ctx *gin.Context, appKey string, routePrefixes []string) ([]SystemPrompt, error) {
	var prompts []SystemPrompt
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Where("app_key = ? AND route_values IN ?", appKey, routePrefixes).
		Order("created_at DESC").
		Find(&prompts).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return prompts, nil
}

// ListSystemPromptsByAppAndExactRoute 按 appKey + routeValues 精确匹配查询系统提示词列表
func ListSystemPromptsByAppAndExactRoute(ctx *gin.Context, appKey string, routeValues string) ([]SystemPrompt, error) {
	var prompts []SystemPrompt
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Where("app_key = ? AND route_values = ?", appKey, routeValues).
		Order("created_at DESC").
		Find(&prompts).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return prompts, nil
}

// FindSystemPromptsByAppAndRoutes 按 appKey + 路由前缀匹配查询系统提示词；
// 同时并入「默认作用域」（app_key=default）下命中的提示词，对全部 app 生效。
func FindSystemPromptsByAppAndRoutes(ctx *gin.Context, appKey string, routePrefixes []string) ([]SystemPrompt, error) {
	var prompts []SystemPrompt
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Where("app_key IN ? AND status = 1 AND route_values IN ?", AppScopeKeys(appKey), routePrefixes).
		Find(&prompts).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return prompts, nil
}

// ListAllSystemPrompts 列出全部 app 的系统提示词（管理控制台「全部」视图用）。
func ListAllSystemPrompts(ctx *gin.Context) ([]SystemPrompt, error) {
	var prompts []SystemPrompt
	err := helpers.MysqlClientLLM.Model(&SystemPrompt{}).WithContext(ctx).
		Order("app_key ASC, created_at DESC").
		Find(&prompts).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return prompts, nil
}
