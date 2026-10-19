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

// 渠道类型词汇（M1：飞书；M2：Telegram；钉钉/QQ 见方案 §5.5）。
const (
	ChannelKindFeishu   = "feishu"
	ChannelKindTelegram = "telegram"
)

// ChannelConfig 是多渠道网关的渠道配置：一个渠道 = 一个 IM 机器人应用（如一个飞书自建应用）。
//
// 设计要点（见 docs/多渠道接入网关与跨端续聊实现方案.md §5.3）：
//   - 凭证（AppID/AppSecret）与默认路由快照（AppKey/RouteValues/UserName）在此表；
//   - DefaultUserName 是 M1 的单用户身份映射：该渠道全部消息归属此 userName；
//     M2 引入 tblLlmUserIdentity 身份绑定表后按平台用户维度解析；
//   - Enabled 默认 0：新建渠道不自动上线，运维显式开启后才拉起连接。
type ChannelConfig struct {
	ID        uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ChannelID string `json:"channelId" gorm:"column:channel_id;not null"`
	Kind      string `json:"kind" gorm:"column:kind;not null"`
	Name      string `json:"name" gorm:"column:name;not null;default:''"`
	// AppID/AppSecret 是 IM 开放平台的应用凭证；管理面回显时 AppSecret 打码。
	AppID     string `json:"appID" gorm:"column:app_id;not null;default:''"`
	AppSecret string `json:"appSecret" gorm:"column:app_secret;not null;default:''"`
	// Enabled 1=启用（拉起长连接收发），0=停用。
	Enabled int `json:"enabled" gorm:"column:enabled;not null;default:0"`
	// DefaultAppKey/DefaultRouteValues/DefaultUserName 是该渠道新建会话时的五元组来源快照；
	// 一旦会话建立即粘滞在 ChannelSession 行上，后续改动配置不影响存量会话。
	DefaultAppKey      string `json:"defaultAppKey" gorm:"column:default_app_key;not null;default:''"`
	DefaultRouteValues string `json:"defaultRouteValues" gorm:"column:default_route_values;not null;default:'[]'"`
	DefaultUserName    string `json:"defaultUserName" gorm:"column:default_user_name;not null;default:''"`
	// AllowedUsers 是接入白名单（JSON 字符串数组）：Telegram 存数字 user/chat ID 或
	// @username，飞书存 open_id/chat_id；空 = 不限制（bot 公开可搜索，建议配置）。
	AllowedUsers string `json:"allowedUsers" gorm:"column:allowed_users;not null;default:''"`
	CreatedBy          string `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy          string `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt          time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt          time.Time `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt          soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (c *ChannelConfig) TableName() string {
	return "tblLlmChannelConfig"
}

func CreateChannelConfig(ctx *gin.Context, cfg *ChannelConfig) error {
	if err := helpers.MysqlClientLLM.Model(&ChannelConfig{}).WithContext(ctx).Create(cfg).Error; err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetChannelConfigByChannelID(ctx *gin.Context, channelID string) (*ChannelConfig, error) {
	var cfg ChannelConfig
	err := helpers.MysqlClientLLM.Model(&ChannelConfig{}).WithContext(ctx).
		Where("channel_id = ?", channelID).First(&cfg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &cfg, nil
}

func ListChannelConfigs(ctx *gin.Context, onlyEnabled bool) ([]ChannelConfig, error) {
	query := helpers.MysqlClientLLM.Model(&ChannelConfig{}).WithContext(ctx)
	if onlyEnabled {
		query = query.Where("enabled = ?", 1)
	}
	var rows []ChannelConfig
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

func UpdateChannelConfigByChannelID(ctx *gin.Context, channelID string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&ChannelConfig{}).WithContext(ctx).
		Where("channel_id = ?", channelID).Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func DeleteChannelConfigByChannelID(ctx *gin.Context, channelID string) error {
	tx := helpers.MysqlClientLLM.Model(&ChannelConfig{}).WithContext(ctx).
		Where("channel_id = ?", channelID).Delete(&ChannelConfig{})
	if tx.Error != nil {
		return components.ErrorDbDelete.Wrap(tx.Error)
	}
	return nil
}

// ChannelSession 是平台会话映射：一个 IM 聊天（飞书 chat）绑定一个 ReAct 会话。
//
// 五元组快照（UserName/AppKey/RouteValues/SessionType）在首次绑定时写入并粘滞——
// 续聊路由按快照构造 payload，保证 validateReactSessionContext 校验恒通过，
// 渠道配置后续变更不影响存量会话（方案 D3）。
type ChannelSession struct {
	ID        uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ChannelID string `json:"channelId" gorm:"column:channel_id;not null"`
	ChatID    string `json:"chatId" gorm:"column:chat_id;not null"`
	SessionID string `json:"sessionId" gorm:"column:session_id;not null"`
	// 五元组快照（粘滞）。
	UserName     string `json:"userName" gorm:"column:user_name;not null;default:''"`
	AppKey       string `json:"appKey" gorm:"column:app_key;not null;default:''"`
	RouteValues  string `json:"routeValues" gorm:"column:route_values;not null;default:'[]'"`
	SessionType  string `json:"sessionType" gorm:"column:session_type;not null;default:'chat'"`
	LastMessage  string `json:"lastMessage" gorm:"column:last_message;not null;default:''"`
	LastActiveAt time.Time `json:"lastActiveAt" gorm:"column:last_active_at"`
	CreatedAt    time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt    time.Time `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt    soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (s *ChannelSession) TableName() string {
	return "tblLlmChannelSession"
}

// GetChannelSessionByChat 按渠道 + 平台会话定位映射行；不存在返回 nil。
func GetChannelSessionByChat(ctx *gin.Context, channelID, chatID string) (*ChannelSession, error) {
	var row ChannelSession
	err := helpers.MysqlClientLLM.Model(&ChannelSession{}).WithContext(ctx).
		Where("channel_id = ? AND chat_id = ?", channelID, chatID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &row, nil
}

// BindChannelSession 首次绑定：写入映射与五元组快照。已存在（含软删占键）时由调用方先查再决定。
func BindChannelSession(ctx *gin.Context, row *ChannelSession) error {
	if err := helpers.MysqlClientLLM.Model(&ChannelSession{}).WithContext(ctx).Create(row).Error; err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// RebindChannelSession 改绑：/new 强制新会话时把既有映射行更新到新会话
// （快照字段一并按当前渠道配置刷新，保留行 ID 与创建时间）。
func RebindChannelSession(ctx *gin.Context, row *ChannelSession) error {
	tx := helpers.MysqlClientLLM.Model(&ChannelSession{}).WithContext(ctx).
		Where("channel_id = ? AND chat_id = ?", row.ChannelID, row.ChatID).
		Updates(map[string]interface{}{
			"session_id":     row.SessionID,
			"user_name":      row.UserName,
			"app_key":        row.AppKey,
			"route_values":   row.RouteValues,
			"session_type":   row.SessionType,
			"last_message":   row.LastMessage,
			"last_active_at": row.LastActiveAt,
		})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// ListChannelSessionsByChat 列出该聊天的会话映射（最近活跃优先；/sessions 命令用）。
func ListChannelSessionsByChat(ctx *gin.Context, channelID, chatID string, limit int) ([]ChannelSession, error) {
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	var rows []ChannelSession
	err := helpers.MysqlClientLLM.Model(&ChannelSession{}).WithContext(ctx).
		Where("channel_id = ? AND chat_id = ?", channelID, chatID).
		Order("last_active_at DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

func TouchChannelSessionActive(ctx *gin.Context, id uint, lastMessage string) error {
	updates := map[string]interface{}{
		"last_active_at": time.Now(),
	}
	if lastMessage != "" {
		updates["last_message"] = lastMessage
	}
	tx := helpers.MysqlClientLLM.Model(&ChannelSession{}).WithContext(ctx).
		Where("id = ?", id).Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListChannelSessions(ctx *gin.Context, channelID string, limit int) ([]ChannelSession, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := helpers.MysqlClientLLM.Model(&ChannelSession{}).WithContext(ctx)
	if channelID != "" {
		query = query.Where("channel_id = ?", channelID)
	}
	var rows []ChannelSession
	if err := query.Order("last_active_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

// ChannelMessage 是消息绑定（续聊的钥匙）：网关投递到 IM 的每条结果消息记录
// 平台 messageID ↔ ReAct sessionID 的对应关系；用户在 IM 里回复该消息时，
// 入站路由按 parent messageID 命中绑定，把回复续进原会话（方案 §5.1 续聊路由分支 1）。
type ChannelMessage struct {
	ID        uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ChannelID string `json:"channelId" gorm:"column:channel_id;not null"`
	ChatID    string `json:"chatId" gorm:"column:chat_id;not null"`
	MessageID string `json:"messageId" gorm:"column:message_id;not null"`
	SessionID string `json:"sessionId" gorm:"column:session_id;not null"`
	RunID     string `json:"runId" gorm:"column:run_id;not null;default:''"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`
	DeletedAt soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (m *ChannelMessage) TableName() string {
	return "tblLlmChannelMessage"
}

// RecordChannelMessage 写入投递绑定；同 messageID 重复投递按幂等忽略（INSERT IGNORE 语义）。
func RecordChannelMessage(ctx *gin.Context, row *ChannelMessage) error {
	tx := helpers.MysqlClientLLM.Model(&ChannelMessage{}).WithContext(ctx).
		Where("channel_id = ? AND message_id = ?", row.ChannelID, row.MessageID).
		FirstOrCreate(row)
	if tx.Error != nil {
		return components.ErrorDbInsert.Wrap(tx.Error)
	}
	return nil
}

func GetChannelMessageByMessageID(ctx *gin.Context, channelID, messageID string) (*ChannelMessage, error) {
	var row ChannelMessage
	err := helpers.MysqlClientLLM.Model(&ChannelMessage{}).WithContext(ctx).
		Where("channel_id = ? AND message_id = ?", channelID, messageID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &row, nil
}

// DeleteChannelMessagesBefore 清理过期绑定（M3 TTL 策略的落点；会话删除级联同此入口）。
func DeleteChannelMessagesBefore(ctx *gin.Context, before time.Time) (int64, error) {
	tx := helpers.MysqlClientLLM.Model(&ChannelMessage{}).WithContext(ctx).
		Where("created_at < ?", before).Delete(&ChannelMessage{})
	if tx.Error != nil {
		return 0, components.ErrorDbDelete.Wrap(tx.Error)
	}
	return tx.RowsAffected, nil
}
