package model

import (
	"errors"
	"time"

	"react-base-service/components"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RuntimeSetting 是运行时设置表：把 custom.yaml 中需要运营期在线调整的策略参数
// （当前为 subagent 委派策略）落到 DB 单行 JSON，管理面板写入后由 service/setting
// 刷新进 conf 内存覆盖快照，yaml 保持兜底基线（未覆盖字段仍取 yaml/默认值）。
// setting_key 预留扩展位：当前仅 subagent，未来其它运行时策略各占一行。
type RuntimeSetting struct {
	ID         uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	SettingKey string    `json:"settingKey" gorm:"column:setting_key;not null"`
	ValueJSON  string    `json:"valueJson" gorm:"column:value_json;type:mediumtext;not null"`
	UpdatedBy  string    `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt  time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt  time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

func (r *RuntimeSetting) TableName() string {
	return "tblLlmRuntimeSetting"
}

// GetRuntimeSettingByKey 按设置键取行；不存在返回 nil,nil（= 无覆盖，回落 yaml）。
func GetRuntimeSettingByKey(ctx *gin.Context, settingKey string) (*RuntimeSetting, error) {
	var setting RuntimeSetting
	tx := GetLLMDB().WithContext(ctx).
		Where("setting_key = ?", settingKey).
		Limit(1).
		Find(&setting)
	if tx.Error != nil {
		return nil, components.ErrorDbSelect.Wrap(tx.Error)
	}
	if tx.RowsAffected == 0 {
		return nil, nil
	}
	return &setting, nil
}

// UpsertRuntimeSetting 按 setting_key 覆盖写入。
// 设置行语义是「整行覆盖」而非字段合并：service 层以「读现状 → 合并请求字段 → 整行写回」
// 保证并发更新不丢字段；此函数不做任何业务校验。
func UpsertRuntimeSetting(ctx *gin.Context, setting *RuntimeSetting) error {
	return GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing RuntimeSetting
		err := tx.Where("setting_key = ?", setting.SettingKey).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(setting).Error; err != nil {
				return components.ErrorDbInsert.Wrap(err)
			}
			return nil
		}
		if err != nil {
			return components.ErrorDbSelect.Wrap(err)
		}
		updates := map[string]interface{}{
			"value_json": setting.ValueJSON,
			"updated_by": setting.UpdatedBy,
		}
		if err := tx.Model(&RuntimeSetting{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return components.ErrorDbUpdate.Wrap(err)
		}
		return nil
	})
}
