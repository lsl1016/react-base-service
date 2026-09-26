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

// UserModel 用户自定义模型 / 平台默认模型注册表
type UserModel struct {
	ID           uint                  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ModelHash    string                `json:"modelHash" gorm:"column:model_hash;not null"`
	UserName     string                `json:"userName" gorm:"column:user_name;not null;default:''"`
	ModelName    string                `json:"modelName" gorm:"column:model_name;not null"`
	ModelKey     string                `json:"modelKey" gorm:"column:model_key;not null"`
	ModelVersion string                `json:"modelVersion" gorm:"column:model_version;not null"`
	ApiKey       string                `json:"-" gorm:"column:api_key;not null;default:''"`
	BizScenes    string                `json:"bizScenes" gorm:"column:biz_scenes;not null"`
	// ApiURL 是模型配置面板「厂商与密钥」填写的自定义接入面（base url）；空=走 api.yaml 全局端点。
	ApiURL string `json:"apiUrl" gorm:"column:api_url;not null;default:''"`
	// ContextTokens 是前端配置的上下文容量（token）；0=回退模型目录/全局压缩阈值。
	ContextTokens int `json:"contextTokens" gorm:"column:context_tokens;not null;default:0"`
	// MaxOutputTokens 是前端配置的单次最大输出 token；0=回退端点/目录/内置默认。
	MaxOutputTokens int `json:"maxOutputTokens" gorm:"column:max_output_tokens;not null;default:0"`
	// 能力开关（1=支持）：决定 thinking 参数是否随请求下发、前端是否透出对应选项。
	SupportThinking int `json:"supportThinking" gorm:"column:support_thinking;not null;default:0"`
	SupportTools    int `json:"supportTools" gorm:"column:support_tools;not null;default:0"`
	SupportVision   int `json:"supportVision" gorm:"column:support_vision;not null;default:0"`
	IsPlatformDefault int                 `json:"isPlatformDefault" gorm:"column:is_platform_default;not null;default:0"`
	CreatedAt         time.Time           `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt         time.Time           `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt         soft_delete.DeletedAt `json:"-" gorm:"column:deleted_at;not null;default:0"`
}

func (m *UserModel) TableName() string {
	return "tblLlmUserModel"
}

// decryptUserModelApiKey 原地解密 api_key（enc:v1: 前缀密文 → 明文；存量明文原样）。
func decryptUserModelApiKey(m *UserModel) {
	if m != nil {
		m.ApiKey = DecryptAPIKey(m.ApiKey)
	}
}

func CreateUserModel(ctx *gin.Context, m *UserModel) error {
	m.ApiKey = EncryptAPIKey(m.ApiKey)
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).Create(m).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetUserModelByID(ctx *gin.Context, id uint) (*UserModel, error) {
	var m UserModel
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("id = ?", id).First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	decryptUserModelApiKey(&m)
	return &m, nil
}

func GetUserModelByHash(ctx *gin.Context, modelHash string) (*UserModel, error) {
	var m UserModel
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("model_hash = ?", modelHash).First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	decryptUserModelApiKey(&m)
	return &m, nil
}

// ExistUserModelByUserAndName 检查同一用户下模型名称是否已存在
func ExistUserModelByUserAndName(ctx *gin.Context, userName, modelName string) (bool, error) {
	var count int64
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("user_name = ? AND model_name = ?", userName, modelName).
		Count(&count).Error
	if err != nil {
		return false, components.ErrorDbSelect.Wrap(err)
	}
	return count > 0, nil
}

func UpdateUserModelByID(ctx *gin.Context, id uint, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("id = ?", id).Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func SoftDeleteUserModelByID(ctx *gin.Context, id uint) error {
	tx := helpers.MysqlClientLLM.WithContext(ctx).Where("id = ?", id).Delete(&UserModel{})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// ListAllUserModels 返回全量（白名单用户使用）
func ListAllUserModels(ctx *gin.Context) ([]UserModel, error) {
	var list []UserModel
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Order("is_platform_default DESC, created_at DESC").
		Find(&list).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return list, nil
}

// ListUserModelsByViewer 普通用户：平台默认模型 + 自己创建的模型
func ListUserModelsByViewer(ctx *gin.Context, userName string) ([]UserModel, error) {
	var list []UserModel
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("is_platform_default = 1 OR user_name = ?", userName).
		Order("is_platform_default DESC, created_at DESC").
		Find(&list).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return list, nil
}

// ListAllUserModelsByBizScene 全量按业务场景过滤（JSON LIKE，白名单用）
func ListAllUserModelsByBizScene(ctx *gin.Context, bizScene string) ([]UserModel, error) {
	var list []UserModel
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("JSON_CONTAINS(biz_scenes, JSON_QUOTE(?))", bizScene).
		Order("is_platform_default DESC, created_at DESC").
		Find(&list).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return list, nil
}

// ListUserModelsByViewerAndBizScene 普通用户按业务场景过滤
func ListUserModelsByViewerAndBizScene(ctx *gin.Context, userName, bizScene string) ([]UserModel, error) {
	var list []UserModel
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("(is_platform_default = 1 OR user_name = ?) AND JSON_CONTAINS(biz_scenes, JSON_QUOTE(?))", userName, bizScene).
		Order("is_platform_default DESC, created_at DESC").
		Find(&list).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return list, nil
}
