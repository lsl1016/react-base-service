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

type App struct {
	ID          uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	AppKey      string `json:"appKey" gorm:"column:app_key;not null"`
	Name        string `json:"name" gorm:"column:name;not null"`
	Description string `json:"description" gorm:"column:description"`
	Platform    string `json:"platform" gorm:"column:platform;not null;default:''"`
	// AllowPlan 是 create_plan/Plan 模式的 app 级开关（三态）：
	// nil=跟随全局 custom.yaml llm.react.allow_plan；0=app 级强制关；1=app 级强制开。
	AllowPlan *int `json:"allowPlan" gorm:"column:allow_plan"`
	// DefaultModelHash 是 app 默认模型：指向一条用户模型（tblLlmUserModel.model_hash），
	// 无 ModelHash 的 run 与 workflow 凭证解析以它为第一优先——厂商（连接）与模型
	// 一次选定。空 = 未配置（走连接解析 → 旧 app 级 API Key 兜底）；
	// 引用的模型/连接失效时运行时同样回退，不阻断 run。
	DefaultModelHash string `json:"defaultModelHash" gorm:"column:default_model_hash;not null;default:''"`
	// BindDefaultCapabilities 控制是否继承 default 伪 app 的默认能力池（工具/Skill）：
	// 1=继承（注册新 app 时的默认值，也是存量行为）；0=只解析自身名下的工具与 Skill。
	// 系统提示词/子 Agent/模型凭证不受此开关影响，始终按 app+default 合并解析。
	// 注意 gorm 标签不能带 default:1——0 是有意义的值（不绑定），带 default 时
	// GORM 会把零值当"未设置"跳过该列，导致 false 落库变 1（DB 列默认值仍为 1）。
	BindDefaultCapabilities int                   `json:"bindDefaultCapabilities" gorm:"column:bind_default_capabilities;not null"`
	Status                  int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy               string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	CreatedAt               time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt               time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt               soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (c *App) TableName() string {
	return "tblLlmApp"
}

// AllowPlanOverride 把 allow_plan 列的三态（NULL/0/1）规整为布尔覆盖指针：
// nil=未覆盖（跟随全局配置）；false=app 级强制关；true=app 级强制开。
func (c *App) AllowPlanOverride() *bool {
	if c.AllowPlan == nil {
		return nil
	}
	v := *c.AllowPlan != 0
	return &v
}

// DefaultAppKey 是「默认作用域」伪 app：工具/系统提示词/skill 挂在该 app 下时，
// 对全部 app 生效（解析时与具体 app 合并查询）。该 appKey 为保留字，不允许注册为真实 app。
const DefaultAppKey = "default"

// AppScopeKeys 返回资源解析用的 app 范围：具体 app + 默认作用域。
func AppScopeKeys(appKey string) []string {
	return []string{appKey, DefaultAppKey}
}

// AppCapabilityScopeKeys 返回工具/Skill 能力解析作用域：app 关闭默认能力绑定时
// 只解析自身名下资源，不再合并 default 伪 app 的公共能力池。
func AppCapabilityScopeKeys(app *App) []string {
	if app == nil {
		return AppScopeKeys("")
	}
	if app.BindDefaultCapabilities == 0 {
		return []string{app.AppKey}
	}
	return AppScopeKeys(app.AppKey)
}

// IsReservedAppKey 判断 appKey 是否为保留字（默认作用域伪 app）。
func IsReservedAppKey(appKey string) bool {
	return appKey == DefaultAppKey
}

func CreateApp(ctx *gin.Context, app *App) error {
	err := helpers.MysqlClientLLM.Model(&App{}).WithContext(ctx).Create(app).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// GetAppByKey 按 appKey 查询（不过滤 status，用于管理后台查重/更新等场景）
func GetAppByKey(ctx *gin.Context, appKey string) (*App, error) {
	var app App
	err := helpers.MysqlClientLLM.Model(&App{}).WithContext(ctx).
		Where("app_key = ?", appKey).First(&app).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &app, nil
}

// GetActiveAppByKey 按 appKey 查询已启用的 app（用于 pipeline 运行时校验）
func GetActiveAppByKey(ctx *gin.Context, appKey string) (*App, error) {
	var app App
	err := helpers.MysqlClientLLM.Model(&App{}).WithContext(ctx).
		Where("app_key = ? AND status = 1", appKey).First(&app).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &app, nil
}

func UpdateAppByKey(ctx *gin.Context, appKey string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&App{}).WithContext(ctx).
		Where("app_key = ?", appKey).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListApps(ctx *gin.Context) ([]App, error) {
	var apps []App
	err := helpers.MysqlClientLLM.Model(&App{}).WithContext(ctx).
		Order("created_at DESC").
		Find(&apps).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return apps, nil
}
