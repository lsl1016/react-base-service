package model

import (
	"errors"
	"sort"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReactMessageRoleSystem    = "system"
	ReactMessageRoleUser      = "user"
	ReactMessageRoleAssistant = "assistant"

	ReactMessageTypeUserInput        = "react_user_input"
	ReactMessageTypeAssistant        = "react_assistant"
	ReactMessageTypeAssistantPartial = "react_assistant_partial"
	ReactMessageTypeToolResult       = "react_tool_result"
	ReactMessageTypeCompactSummary   = "react_compact_summary"
	ReactMessageTypeRuntimeContext   = "react_runtime_context"
)

type ReactMessage struct {
	ID           uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	MessageID    string    `json:"messageId" gorm:"column:message_id;not null"`
	RunID        string    `json:"runId" gorm:"column:run_id;not null"`
	SessionID    string    `json:"sessionId" gorm:"column:session_id;not null"`
	UserName     string    `json:"userName" gorm:"column:user_name;not null"`
	CallerKey    string    `json:"callerKey" gorm:"column:caller_key;not null"`
	Seq          int       `json:"seq" gorm:"column:seq;not null"`
	StepIndex    int       `json:"stepIndex" gorm:"column:step_index;not null;default:0"`
	Role         string    `json:"role" gorm:"column:role;not null"`
	MessageType  string    `json:"messageType" gorm:"column:message_type;not null"`
	ModelKey     string    `json:"modelKey,omitempty" gorm:"column:model_key;not null;default:''"`
	ModelVersion string    `json:"modelVersion,omitempty" gorm:"column:model_version;not null;default:''"`
	ContentJSON  string    `json:"contentJson" gorm:"column:content_json;type:mediumtext"`
	CreatedAt    time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt    time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

func (m *ReactMessage) TableName() string {
	return "tblLlmReactMessage"
}

func CreateReactMessage(ctx *gin.Context, message *ReactMessage) error {
	return CreateReactMessageWithDB(ctx, helpers.MysqlClientLLM, message)
}

func CreateReactMessageWithDB(ctx *gin.Context, db *gorm.DB, message *ReactMessage) error {
	err := db.Model(&ReactMessage{}).WithContext(ctx).Create(message).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func CreateReactMessageOnceWithDB(ctx *gin.Context, db *gorm.DB, message *ReactMessage) (bool, error) {
	result := db.Model(&ReactMessage{}).WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(message)
	if result.Error != nil {
		return false, components.ErrorDbInsert.Wrap(result.Error)
	}
	return result.RowsAffected > 0, nil
}

func BatchCreateReactMessages(ctx *gin.Context, messages []ReactMessage) error {
	if len(messages) == 0 {
		return nil
	}
	err := helpers.MysqlClientLLM.WithContext(ctx).Create(&messages).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetReactMessagesByRunID(ctx *gin.Context, runID string) ([]ReactMessage, error) {
	var messages []ReactMessage
	err := helpers.MysqlClientLLM.Model(&ReactMessage{}).WithContext(ctx).
		Where("run_id = ?", runID).
		Order("seq ASC").Find(&messages).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return messages, nil
}

func GetReactMessagesBySessionID(ctx *gin.Context, sessionID string) ([]ReactMessage, error) {
	return GetReactMessagesBySessionIDWithDB(ctx, helpers.MysqlClientLLM, sessionID)
}

func GetReactMessagesBySessionIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) ([]ReactMessage, error) {
	var messages []ReactMessage
	err := db.Model(&ReactMessage{}).WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at ASC, seq ASC").Find(&messages).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return messages, nil
}

// GetOuterReactMessagesBySessionIDWithDB 返回会话内「外层 run」的消息（排除 delegate_agent 子 run），
// 供外层 run 启动时装配 LLM 历史——子 run 的消息只属于子 run 自己的上下文，绝不进入父历史。
func GetOuterReactMessagesBySessionIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) ([]ReactMessage, error) {
	var messages []ReactMessage
	err := db.Model(&ReactMessage{}).WithContext(ctx).
		Joins("JOIN `tblLlmReactRun` `run` ON `run`.`run_id` = `tblLlmReactMessage`.`run_id`").
		Where("`tblLlmReactMessage`.`session_id` = ? AND (`run`.`parent_run_id` IS NULL OR `run`.`parent_run_id` = '')", sessionID).
		Order("`tblLlmReactMessage`.`created_at` ASC, `tblLlmReactMessage`.`seq` ASC").
		Find(&messages).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return messages, nil
}

func GetReactMessagesBySessionIDTimeline(ctx *gin.Context, sessionID string) ([]ReactMessage, error) {
	return GetReactMessagesBySessionIDTimelineWithDB(ctx, helpers.MysqlClientLLM, sessionID)
}

func GetReactMessagesBySessionIDTimelineWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) ([]ReactMessage, error) {
	runs, err := GetReactRunsBySessionIDWithDB(ctx, db, sessionID)
	if err != nil {
		return nil, err
	}
	// 主会话回放保留普通外层与 delegate 子 run；Plan Step Run(agentPath=plan/*)
	// 只在 PlanRuntimeCard 的 Attempt 详情里按需回放，不能混入主 Agent Lane。
	visibleRuns := make([]ReactRun, 0, len(runs))
	visibleRunIDs := make(map[string]bool, len(runs))
	for _, run := range runs {
		if strings.HasPrefix(strings.TrimSpace(run.AgentPath), "plan/") {
			continue
		}
		visibleRunIDs[run.RunID] = true
		visibleRuns = append(visibleRuns, run)
	}
	messages, err := GetReactMessagesBySessionIDWithDB(ctx, db, sessionID)
	if err != nil {
		return nil, err
	}
	filteredMessages := make([]ReactMessage, 0, len(messages))
	for _, message := range messages {
		if visibleRunIDs[message.RunID] {
			filteredMessages = append(filteredMessages, message)
		}
	}
	messages = filteredMessages
	runs = visibleRuns

	runOrder := make(map[string]int, len(runs))
	for i, run := range runs {
		runOrder[run.RunID] = i
	}

	// created_at 为主序保证子 run 消息按真实发生位置插入父 run 消息之间（子 run 创建晚于父、
	// 结束早于父恢复）；runOrder/seq/id 仅作同秒内的稳定次序。
	sort.SliceStable(messages, func(i, j int) bool {
		left := messages[i]
		right := messages[j]

		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		leftOrder, leftOK := runOrder[left.RunID]
		rightOrder, rightOK := runOrder[right.RunID]
		switch {
		case leftOK && rightOK && leftOrder != rightOrder:
			return leftOrder < rightOrder
		case leftOK != rightOK:
			return leftOK
		case left.Seq != right.Seq:
			return left.Seq < right.Seq
		default:
			return left.ID < right.ID
		}
	})

	return messages, nil
}

func GetReactMessageByMessageID(ctx *gin.Context, messageID string) (*ReactMessage, error) {
	var message ReactMessage
	err := helpers.MysqlClientLLM.Model(&ReactMessage{}).WithContext(ctx).
		Where("message_id = ?", messageID).First(&message).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &message, nil
}

func GetReactMessageMaxSeqByRunID(ctx *gin.Context, runID string) (int, error) {
	var maxSeq *int
	err := helpers.MysqlClientLLM.Model(&ReactMessage{}).WithContext(ctx).
		Where("run_id = ?", runID).
		Select("MAX(seq)").Scan(&maxSeq).Error
	if err != nil {
		return 0, components.ErrorDbSelect.Wrap(err)
	}
	if maxSeq == nil {
		return 0, nil
	}
	return *maxSeq, nil
}
