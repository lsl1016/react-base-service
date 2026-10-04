package model

import (
	"errors"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReactSessionTypeChat = "chat"
	// ReactSessionTypeReflection 是记忆自动整理（reflection）专用会话类型：
	// 工具集仅限 memory 三工具，历史列表默认不展示（显式传 type=reflection 可查）。
	ReactSessionTypeReflection = "reflection"
	// ReactSessionTypeScheduled 是定时触发工作流的无人值守会话类型：
	// 无前端连接，禁用 client 工具与 ask_question（等待人工输入即挂死）。
	ReactSessionTypeScheduled = "scheduled"

	ReactSessionStateActive   = "active"
	ReactSessionStateArchived = "archived"
	ReactSessionStateDeleted  = "deleted"
)

type ReactSession struct {
	ID          uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	SessionID   string    `json:"sessionId" gorm:"column:session_id;not null"`
	UserName    string    `json:"userName" gorm:"column:user_name;not null"`
	CallerKey   string    `json:"callerKey" gorm:"column:caller_key;not null"`
	RouteValues string    `json:"routeValues" gorm:"column:route_values;not null;default:''"`
	SessionType string    `json:"sessionType" gorm:"column:session_type;not null;default:'chat'"`
	Title       string    `json:"title" gorm:"column:title;not null;default:''"`
	LastRunID   string    `json:"lastRunId" gorm:"column:last_run_id;not null;default:''"`
	LastMessage string    `json:"lastMessage" gorm:"column:last_message;not null;default:''"`
	State       string    `json:"state" gorm:"column:state;not null;default:'active'"`
	CreatedAt   time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

func (s *ReactSession) TableName() string {
	return "tblLlmReactSession"
}

func CreateReactSession(ctx *gin.Context, session *ReactSession) error {
	return CreateReactSessionWithDB(ctx, helpers.MysqlClientLLM, session)
}

func CreateReactSessionWithDB(ctx *gin.Context, db *gorm.DB, session *ReactSession) error {
	if session.State == "" {
		session.State = ReactSessionStateActive
	}
	err := db.Model(&ReactSession{}).WithContext(ctx).Create(session).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetReactSessionBySessionID(ctx *gin.Context, sessionID string) (*ReactSession, error) {
	var session ReactSession
	err := helpers.MysqlClientLLM.Model(&ReactSession{}).WithContext(ctx).
		Where("session_id = ? AND state <> ?", sessionID, ReactSessionStateDeleted).
		First(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &session, nil
}

func GetReactSessionBySessionIDForUpdate(ctx *gin.Context, db *gorm.DB, sessionID string) (*ReactSession, error) {
	var session ReactSession
	err := db.Model(&ReactSession{}).WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("session_id = ? AND state <> ?", sessionID, ReactSessionStateDeleted).
		First(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &session, nil
}

func GetLatestReactSessionByCallerRouteUser(ctx *gin.Context, callerKey, routeValues, userName, sessionType string) (*ReactSession, error) {
	return getLatestReactSessionByCallerRouteUser(ctx, helpers.MysqlClientLLM, callerKey, routeValues, userName, sessionType, false)
}

func GetLatestReactSessionByCallerRouteUserForUpdate(ctx *gin.Context, db *gorm.DB, callerKey, routeValues, userName, sessionType string) (*ReactSession, error) {
	return getLatestReactSessionByCallerRouteUser(ctx, db, callerKey, routeValues, userName, sessionType, true)
}

func getLatestReactSessionByCallerRouteUser(ctx *gin.Context, db *gorm.DB, callerKey, routeValues, userName, sessionType string, forUpdate bool) (*ReactSession, error) {
	var session ReactSession
	query := db.Model(&ReactSession{}).WithContext(ctx).
		Where("caller_key = ? AND route_values = ? AND user_name = ? AND state = ?",
			callerKey, routeValues, userName, ReactSessionStateActive)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if strings.TrimSpace(sessionType) != "" {
		query = query.Where("session_type = ?", sessionType)
	}
	err := query.Order("updated_at DESC").First(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &session, nil
}

func UpdateReactSessionBySessionID(ctx *gin.Context, sessionID string, updates map[string]any) error {
	return UpdateReactSessionBySessionIDWithDB(ctx, helpers.MysqlClientLLM, sessionID, updates)
}

func UpdateReactSessionBySessionIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	tx := db.Model(&ReactSession{}).WithContext(ctx).
		Where("session_id = ?", sessionID).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListReactSessionsByCallerRouteUser(ctx *gin.Context, callerKey, routeValues, userName, sessionType, keyword string, page, pageSize int) ([]ReactSession, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	query := helpers.MysqlClientLLM.Model(&ReactSession{}).WithContext(ctx).
		Where("caller_key = ? AND route_values = ? AND user_name = ? AND state <> ?",
			callerKey, routeValues, userName, ReactSessionStateDeleted)
	if strings.TrimSpace(sessionType) != "" {
		query = query.Where("session_type = ?", sessionType)
	}
	if strings.TrimSpace(keyword) != "" {
		query = query.Where("title LIKE ?", "%"+strings.TrimSpace(keyword)+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, components.ErrorDbSelect.Wrap(err)
	}

	var sessions []ReactSession
	err := query.Order("updated_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&sessions).Error
	if err != nil {
		return nil, 0, components.ErrorDbSelect.Wrap(err)
	}
	return sessions, total, nil
}
