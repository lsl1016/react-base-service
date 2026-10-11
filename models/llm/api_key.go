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

type ApiKey struct {
	ID          uint                  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	AppKey   string                `json:"appKey" gorm:"column:app_key;not null"`
	RouteValues string                `json:"routeValues" gorm:"column:route_values"`
	Name        string                `json:"name" gorm:"column:name;not null"`
	ApiKeyValue string                `json:"-" gorm:"column:api_key;not null"`
	Status      int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy   string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy   string                `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt   time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt   soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (a *ApiKey) TableName() string {
	return "tblLlmApiKey"
}

func CreateApiKey(ctx *gin.Context, ak *ApiKey) error {
	err := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).Create(ak).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetApiKeyByID(ctx *gin.Context, id uint) (*ApiKey, error) {
	var ak ApiKey
	err := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).
		Where("id = ?", id).First(&ak).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &ak, nil
}

// ExistApiKeyByAppAndRoute 检查 app+route 组合下是否已有有效记录
func ExistApiKeyByAppAndRoute(ctx *gin.Context, appKey, routeValues string) (bool, error) {
	var count int64
	err := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).
		Where("app_key = ? AND route_values = ?", appKey, routeValues).
		Count(&count).Error
	if err != nil {
		return false, components.ErrorDbSelect.Wrap(err)
	}
	return count > 0, nil
}

func UpdateApiKeyByID(ctx *gin.Context, id uint, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).
		Where("id = ?", id).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func SoftDeleteApiKeyByID(ctx *gin.Context, id uint) error {
	tx := helpers.MysqlClientLLM.WithContext(ctx).Where("id = ?", id).Delete(&ApiKey{})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListApiKeysByApp(ctx *gin.Context, appKey string) ([]ApiKey, error) {
	var keys []ApiKey
	err := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).
		Where("app_key = ?", appKey).
		Order("created_at DESC").
		Find(&keys).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return keys, nil
}

// ListApiKeysByAppAndRoutes 按 appKey + 路由前缀匹配查询 API Key 列表（不过滤 status，管理后台用）
func ListApiKeysByAppAndRoutes(ctx *gin.Context, appKey string, routePrefixes []string) ([]ApiKey, error) {
	var keys []ApiKey
	err := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).
		Where("app_key = ? AND route_values IN ?", appKey, routePrefixes).
		Order("created_at DESC").
		Find(&keys).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return keys, nil
}

// ListApiKeysByAppAndExactRoute 按 appKey + routeValues 精确匹配查询 API Key 列表
func ListApiKeysByAppAndExactRoute(ctx *gin.Context, appKey string, routeValues string) ([]ApiKey, error) {
	var keys []ApiKey
	err := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).
		Where("app_key = ? AND route_values = ?", appKey, routeValues).
		Order("created_at DESC").
		Find(&keys).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return keys, nil
}

// FindApiKeysByAppAndRoutes 按 appKey + 路由前缀匹配查询 API Key
func FindApiKeysByAppAndRoutes(ctx *gin.Context, appKey string, routePrefixes []string) ([]ApiKey, error) {
	var keys []ApiKey
	err := helpers.MysqlClientLLM.Model(&ApiKey{}).WithContext(ctx).
		Where("app_key = ? AND status = 1 AND route_values IN ?", appKey, routePrefixes).
		Find(&keys).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return keys, nil
}
