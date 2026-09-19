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

// McpApp 是 MCP 服务端网关的应用凭证（tblLlmMcpApp）：
// 外部 MCP 客户端（Claude/Cursor 等）以 Bearer <app_key>:<app_secret> 接入，
// 应用绑定一个 caller 作用域——该 caller 名下（含 default 通用作用域）启用的
// http 类型工具（tblLlmTool）即该应用在 tools/list 里可见的工具集合。
type McpApp struct {
	ID        uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	AppID     string `json:"appId" gorm:"column:app_id;not null"`
	AppName   string `json:"appName" gorm:"column:app_name;not null"`
	AppKey    string `json:"appKey" gorm:"column:app_key;not null"`
	AppSecret string `json:"appSecret" gorm:"column:app_secret;not null"`
	// CallerKey 是应用可见的 caller 作用域（default=全部 caller 的通用工具）。
	CallerKey string `json:"callerKey" gorm:"column:caller_key;not null"`
	Status    int    `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy string `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy string `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (a *McpApp) TableName() string {
	return "tblLlmMcpApp"
}

func CreateMcpApp(ctx *gin.Context, app *McpApp) error {
	err := helpers.MysqlClientLLM.Model(&McpApp{}).WithContext(ctx).Create(app).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// GetMcpAppByAppID 按 appId 查询（含停用，不含已删除）。
func GetMcpAppByAppID(ctx *gin.Context, appID string) (*McpApp, error) {
	var app McpApp
	err := helpers.MysqlClientLLM.Model(&McpApp{}).WithContext(ctx).
		Where("app_id = ?", appID).First(&app).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &app, nil
}

// GetMcpAppByAppKey 按 app_key 查询启用中的应用（网关鉴权用；app_key 全局唯一）。
func GetMcpAppByAppKey(ctx *gin.Context, appKey string) (*McpApp, error) {
	var app McpApp
	err := helpers.MysqlClientLLM.Model(&McpApp{}).WithContext(ctx).
		Where("app_key = ? AND status = 1", appKey).First(&app).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &app, nil
}

func ListMcpApps(ctx *gin.Context) ([]McpApp, error) {
	var apps []McpApp
	err := helpers.MysqlClientLLM.Model(&McpApp{}).WithContext(ctx).
		Order("created_at DESC").Find(&apps).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return apps, nil
}

func UpdateMcpAppByAppID(ctx *gin.Context, appID string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&McpApp{}).WithContext(ctx).
		Where("app_id = ?", appID).Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func SoftDeleteMcpAppByAppID(ctx *gin.Context, appID string) error {
	tx := helpers.MysqlClientLLM.WithContext(ctx).Where("app_id = ?", appID).Delete(&McpApp{})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// McpCallLog 是 MCP 网关调用审计（tblLlmMcpCallLog）：异步批量落库，只插不改。
type McpCallLog struct {
	ID            uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	RequestID     string    `json:"requestId" gorm:"column:request_id;not null;default:''"`
	AppKey        string    `json:"appKey" gorm:"column:app_key;not null;default:''"`
	UserName      string    `json:"userName" gorm:"column:user_name;not null;default:''"`
	McpMethod     string    `json:"mcpMethod" gorm:"column:mcp_method;not null;default:''"`
	ToolName      string    `json:"toolName" gorm:"column:tool_name;not null;default:''"`
	Arguments     string    `json:"arguments" gorm:"column:arguments"`
	ResponseText  string    `json:"responseText" gorm:"column:response_text"`
	ResultCode    int       `json:"resultCode" gorm:"column:result_code;not null;default:0"`
	ErrorMsg      string    `json:"errorMsg" gorm:"column:error_msg;not null;default:''"`
	CostMs        int       `json:"costMs" gorm:"column:cost_ms;not null;default:0"`
	ClientIP      string    `json:"clientIP" gorm:"column:client_ip;not null;default:''"`
	ClientInfo    string    `json:"clientInfo" gorm:"column:client_info;not null;default:''"`
	CreatedAt     time.Time `json:"createdAt" gorm:"column:created_at"`
}

func (l *McpCallLog) TableName() string {
	return "tblLlmMcpCallLog"
}

// BatchInsertMcpCallLogs 批量写入审计记录（异步落库协程调用，无请求上下文）。
func BatchInsertMcpCallLogs(ctx *gin.Context, logs []*McpCallLog) error {
	if len(logs) == 0 {
		return nil
	}
	err := helpers.MysqlClientLLM.Model(&McpCallLog{}).WithContext(ctx).Create(&logs).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// McpCallLogFilter 审计查询条件（均可选）。
type McpCallLogFilter struct {
	AppKey   string
	ToolName string
}

// ListMcpCallLogsPage 分页查询审计记录（按 id 倒序），返回记录与总数。
func ListMcpCallLogsPage(ctx *gin.Context, filter McpCallLogFilter, offset, limit int) ([]McpCallLog, int64, error) {
	query := helpers.MysqlClientLLM.Model(&McpCallLog{}).WithContext(ctx)
	if filter.AppKey != "" {
		query = query.Where("app_key = ?", filter.AppKey)
	}
	if filter.ToolName != "" {
		query = query.Where("tool_name = ?", filter.ToolName)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, components.ErrorDbSelect.Wrap(err)
	}
	var logs []McpCallLog
	err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&logs).Error
	if err != nil {
		return nil, 0, components.ErrorDbSelect.Wrap(err)
	}
	return logs, total, nil
}
