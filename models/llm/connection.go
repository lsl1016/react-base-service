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

// Connection LLM 连接注册表：协议 + base url + api key 的自包含接入单元。
// 设计（见 docs/模型配置优化方案.md §3.2/§3.4）：
//   - 「连接」是模型配置的主体，模型（tblLlmUserModel.connection_id）与无 ModelHash
//     运行路径（caller+route 前缀匹配）都引用连接取凭证与端点；
//   - 它同时是旧 tblLlmApiKey（caller 级 key）的升维形态——多出 protocol/base_url
//     两个字段，route_values 语义与旧表一致（JSON 数组，[]=通用）。
type Connection struct {
	ID          uint                  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	CallerKey   string                `json:"callerKey" gorm:"column:caller_key;not null"`
	RouteValues string                `json:"routeValues" gorm:"column:route_values"`
	Name        string                `json:"name" gorm:"column:name;not null"`
	Protocol    string                `json:"protocol" gorm:"column:protocol;not null;default:'openai'"`
	BaseURL     string                `json:"baseUrl" gorm:"column:base_url;not null;default:''"`
	ApiKeyValue string                `json:"-" gorm:"column:api_key;not null;default:''"`
	Status      int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy   string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy   string                `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt   time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt   soft_delete.DeletedAt `json:"-" gorm:"column:deleted_at;not null;default:0"`
}

func (c *Connection) TableName() string {
	return "tblLlmConnection"
}

// decryptConnectionApiKey 原地解密 api_key（enc:v1: 前缀密文 → 明文；存量明文原样）。
func decryptConnectionApiKey(c *Connection) {
	if c != nil {
		c.ApiKeyValue = DecryptAPIKey(c.ApiKeyValue)
	}
}

func CreateConnection(ctx *gin.Context, c *Connection) error {
	c.ApiKeyValue = EncryptAPIKey(c.ApiKeyValue)
	err := helpers.MysqlClientLLM.Model(&Connection{}).WithContext(ctx).Create(c).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetConnectionByID(ctx *gin.Context, id uint) (*Connection, error) {
	var c Connection
	err := helpers.MysqlClientLLM.Model(&Connection{}).WithContext(ctx).
		Where("id = ?", id).First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	decryptConnectionApiKey(&c)
	return &c, nil
}

// ExistConnectionByCallerAndRoute 检查 caller+route 组合下是否已有连接（防重复注册）
func ExistConnectionByCallerAndRoute(ctx *gin.Context, callerKey, routeValues string) (bool, error) {
	var count int64
	err := helpers.MysqlClientLLM.Model(&Connection{}).WithContext(ctx).
		Where("caller_key = ? AND route_values = ?", callerKey, routeValues).
		Count(&count).Error
	if err != nil {
		return false, components.ErrorDbSelect.Wrap(err)
	}
	return count > 0, nil
}

func UpdateConnectionByID(ctx *gin.Context, id uint, updates map[string]interface{}) error {
	// api_key 走密文落库：调用方放入 updates 前自行 EncryptAPIKey。
	tx := helpers.MysqlClientLLM.Model(&Connection{}).WithContext(ctx).
		Where("id = ?", id).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func SoftDeleteConnectionByID(ctx *gin.Context, id uint) error {
	tx := helpers.MysqlClientLLM.WithContext(ctx).Where("id = ?", id).Delete(&Connection{})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// ListConnections 全量连接列表（管理/配置面板用，含禁用；key 不在此解密）
func ListConnections(ctx *gin.Context) ([]Connection, error) {
	var conns []Connection
	err := helpers.MysqlClientLLM.Model(&Connection{}).WithContext(ctx).
		Order("created_at DESC").
		Find(&conns).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return conns, nil
}

// FindConnectionsByCallerAndRoutes 查询启用中的连接：caller 精确命中 + 空 caller（全局）
// 两类候选都返回，按 route_values 前缀匹配；调用方先取 caller 精确命中、再取全局兜底
//（各取 route_values 最长者，与旧 tblLlmApiKey 解析口径一致）。
func FindConnectionsByCallerAndRoutes(ctx *gin.Context, callerKey string, routePrefixes []string) ([]Connection, error) {
	var conns []Connection
	err := helpers.MysqlClientLLM.Model(&Connection{}).WithContext(ctx).
		Where("caller_key IN ? AND status = 1 AND route_values IN ?", []string{callerKey, ""}, routePrefixes).
		Find(&conns).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	for i := range conns {
		decryptConnectionApiKey(&conns[i])
	}
	return conns, nil
}

// CountUserModelsByConnectionID 统计引用该连接的用户模型数（删除连接前校验用）
func CountUserModelsByConnectionID(ctx *gin.Context, connectionID uint) (int64, error) {
	var count int64
	err := helpers.MysqlClientLLM.Model(&UserModel{}).WithContext(ctx).
		Where("connection_id = ?", connectionID).
		Count(&count).Error
	if err != nil {
		return 0, components.ErrorDbSelect.Wrap(err)
	}
	return count, nil
}
