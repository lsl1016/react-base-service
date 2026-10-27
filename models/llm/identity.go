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

// UserIdentity 是多渠道网关的身份绑定（M2 跨端投递的寻址地基）：
// 平台用户（Telegram 数字 ID / 飞书 open_id）→ 基座 userName。
//
// 语义（见 docs/多渠道接入网关与跨端续聊实现方案.md §5.3）：
//   - 入站解析：按 (kind, platform_user_id) 命中则覆盖渠道默认 default_user_name
//     （未绑定回落 M1 单用户语义）；解析结果粘滞进 ChannelSession 快照，绑定后续
//     变更不影响存量会话；
//   - 跨端投递：按 user_name 反查该逻辑用户绑定的各渠道聊天（ChannelSession 行），
//     run 终态结果推给发起渠道之外的端——「在这端发起、另一端收到」。
type UserIdentity struct {
	ID         uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	IdentityID string `json:"identityId" gorm:"column:identity_id;not null"`
	// Kind 是平台类型（feishu/telegram），与 ChannelConfig.Kind 同词汇。
	Kind string `json:"kind" gorm:"column:kind;not null"`
	// PlatformUserID 是平台用户标识：Telegram 数字 user ID、飞书 open_id（ou_ 前缀）。
	// 注意不用 @username（可变）作主键，username 仅可作为附加候选（M2 未用）。
	PlatformUserID string `json:"platformUserId" gorm:"column:platform_user_id;not null"`
	// UserName 是映射到的基座 userName（会话归属与投递寻址键）。
	UserName    string `json:"userName" gorm:"column:user_name;not null"`
	DisplayName string `json:"displayName" gorm:"column:display_name;not null;default:''"`
	CreatedBy   string `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy   string `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt   time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt   soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (i *UserIdentity) TableName() string {
	return "tblLlmUserIdentity"
}

func CreateUserIdentity(ctx *gin.Context, row *UserIdentity) error {
	if err := helpers.MysqlClientLLM.Model(&UserIdentity{}).WithContext(ctx).Create(row).Error; err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// GetUserIdentity 按平台键定位绑定行；不存在返回 nil。
func GetUserIdentity(ctx *gin.Context, kind, platformUserID string) (*UserIdentity, error) {
	if kind == "" || platformUserID == "" {
		return nil, nil
	}
	var row UserIdentity
	err := helpers.MysqlClientLLM.Model(&UserIdentity{}).WithContext(ctx).
		Where("kind = ? AND platform_user_id = ?", kind, platformUserID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &row, nil
}

func GetUserIdentityByIdentityID(ctx *gin.Context, identityID string) (*UserIdentity, error) {
	var row UserIdentity
	err := helpers.MysqlClientLLM.Model(&UserIdentity{}).WithContext(ctx).
		Where("identity_id = ?", identityID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &row, nil
}

// ListUserIdentities 全量绑定列表（面板用；可按平台/基座用户名过滤）。
func ListUserIdentities(ctx *gin.Context, kind, userName string) ([]UserIdentity, error) {
	query := helpers.MysqlClientLLM.Model(&UserIdentity{}).WithContext(ctx)
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	if userName != "" {
		query = query.Where("user_name = ?", userName)
	}
	var rows []UserIdentity
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

func UpdateUserIdentityByIdentityID(ctx *gin.Context, identityID string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&UserIdentity{}).WithContext(ctx).
		Where("identity_id = ?", identityID).Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func DeleteUserIdentityByIdentityID(ctx *gin.Context, identityID string) error {
	tx := helpers.MysqlClientLLM.Model(&UserIdentity{}).WithContext(ctx).
		Where("identity_id = ?", identityID).Delete(&UserIdentity{})
	if tx.Error != nil {
		return components.ErrorDbDelete.Wrap(tx.Error)
	}
	return nil
}
