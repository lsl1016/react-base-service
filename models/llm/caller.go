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

type Caller struct {
	ID          uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	CallerKey   string `json:"callerKey" gorm:"column:caller_key;not null"`
	Name        string `json:"name" gorm:"column:name;not null"`
	Description string `json:"description" gorm:"column:description"`
	Platform    string `json:"platform" gorm:"column:platform;not null;default:''"`
	// AllowPlan 是 create_plan/Plan 模式的 caller 级开关（三态）：
	// nil=跟随全局 custom.yaml llm.react.allow_plan；0=caller 级强制关；1=caller 级强制开。
	AllowPlan *int                  `json:"allowPlan" gorm:"column:allow_plan"`
	Status    int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	CreatedAt time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (c *Caller) TableName() string {
	return "tblLlmCaller"
}

// AllowPlanOverride 把 allow_plan 列的三态（NULL/0/1）规整为布尔覆盖指针：
// nil=未覆盖（跟随全局配置）；false=caller 级强制关；true=caller 级强制开。
func (c *Caller) AllowPlanOverride() *bool {
	if c.AllowPlan == nil {
		return nil
	}
	v := *c.AllowPlan != 0
	return &v
}

// DefaultCallerKey 是「默认作用域」伪 caller：工具/系统提示词/skill 挂在该 caller 下时，
// 对全部 caller 生效（解析时与具体 caller 合并查询）。该 callerKey 为保留字，不允许注册为真实 caller。
const DefaultCallerKey = "default"

// CallerScopeKeys 返回资源解析用的 caller 范围：具体 caller + 默认作用域。
func CallerScopeKeys(callerKey string) []string {
	return []string{callerKey, DefaultCallerKey}
}

// IsReservedCallerKey 判断 callerKey 是否为保留字（默认作用域伪 caller）。
func IsReservedCallerKey(callerKey string) bool {
	return callerKey == DefaultCallerKey
}

func CreateCaller(ctx *gin.Context, caller *Caller) error {
	err := helpers.MysqlClientLLM.Model(&Caller{}).WithContext(ctx).Create(caller).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// GetCallerByKey 按 callerKey 查询（不过滤 status，用于管理后台查重/更新等场景）
func GetCallerByKey(ctx *gin.Context, callerKey string) (*Caller, error) {
	var caller Caller
	err := helpers.MysqlClientLLM.Model(&Caller{}).WithContext(ctx).
		Where("caller_key = ?", callerKey).First(&caller).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &caller, nil
}

// GetActiveCallerByKey 按 callerKey 查询已启用的 caller（用于 pipeline 运行时校验）
func GetActiveCallerByKey(ctx *gin.Context, callerKey string) (*Caller, error) {
	var caller Caller
	err := helpers.MysqlClientLLM.Model(&Caller{}).WithContext(ctx).
		Where("caller_key = ? AND status = 1", callerKey).First(&caller).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &caller, nil
}

func UpdateCallerByKey(ctx *gin.Context, callerKey string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&Caller{}).WithContext(ctx).
		Where("caller_key = ?", callerKey).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListCallers(ctx *gin.Context) ([]Caller, error) {
	var callers []Caller
	err := helpers.MysqlClientLLM.Model(&Caller{}).WithContext(ctx).
		Order("created_at DESC").
		Find(&callers).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return callers, nil
}
