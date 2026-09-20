package model

import (
	"context"
	"errors"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	ReactRunStateRunning              = "running"
	ReactRunStateWaitingClientMessage = "waiting_client_message"
	ReactRunStateWaitingPlan          = "waiting_plan"
	ReactRunStateCancelling           = "cancelling"
	ReactRunStateFinished             = "finished"
	ReactRunStateError                = "error"
	ReactRunStateCancelled            = "cancelled"
	ReactRunStateExpired              = "expired"
)

type ReactRun struct {
	ID                      uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	RunID                   string `json:"runId" gorm:"column:run_id;not null"`
	SessionID               string `json:"sessionId" gorm:"column:session_id;not null"`
	UserName                string `json:"userName" gorm:"column:user_name;not null"`
	CallerKey               string `json:"callerKey" gorm:"column:caller_key;not null"`
	RouteValues             string `json:"routeValues" gorm:"column:route_values;not null;default:''"`
	State                   string `json:"state" gorm:"column:state;not null"`
	StepIndex               int    `json:"stepIndex" gorm:"column:step_index;not null;default:0"`
	MaxSteps                int    `json:"maxSteps" gorm:"column:max_steps;not null"`
	ModelKey                string `json:"modelKey" gorm:"column:model_key"`
	ModelVersion            string `json:"modelVersion" gorm:"column:model_version"`
	ApiKey                  string `json:"apiKey" gorm:"column:api_key"`
	ControlContextJSON      string `json:"controlContextJson" gorm:"column:control_context_json;type:mediumtext"`
	LLMContextJSON          string `json:"llmContextJson" gorm:"column:llm_context_json;type:mediumtext"`
	ToolIndexSnapshotJSON   string `json:"toolIndexSnapshotJson" gorm:"column:tool_index_snapshot_json;type:mediumtext"`
	ActiveToolIDs           string `json:"activeToolIds" gorm:"column:active_tool_ids;type:text"`
	ActiveToolDefsJSON      string `json:"activeToolDefsJson" gorm:"column:active_tool_defs_json;type:mediumtext"`
	SkillsIndexSnapshotJSON string `json:"skillsIndexSnapshotJson" gorm:"column:skills_index_snapshot_json;type:mediumtext"`
	LoadedSkillIDs          string `json:"loadedSkillIds" gorm:"column:loaded_skill_ids;type:text"`
	PendingToolUseIDs       string `json:"pendingToolUseIds" gorm:"column:pending_tool_use_ids;type:text"`
	TodoStateJSON           string `json:"todoStateJson" gorm:"column:todo_state_json;type:text"`
	TotalInputTokens        int    `json:"totalInputTokens" gorm:"column:total_input_tokens;not null;default:0"`
	TotalOutputTokens       int    `json:"totalOutputTokens" gorm:"column:total_output_tokens;not null;default:0"`
	// DelegatedInput/OutputTokens 是本 run 委派子 Agent 的 token 消耗（递归口径），
	// 由子 run 终态时原子累加（OH delegate:{id} 计量口径的等价物）。
	DelegatedInputTokens  int    `json:"delegatedInputTokens" gorm:"column:delegated_input_tokens;not null;default:0"`
	DelegatedOutputTokens int    `json:"delegatedOutputTokens" gorm:"column:delegated_output_tokens;not null;default:0"`
	LastInputTokens       int    `json:"lastInputTokens" gorm:"column:last_input_tokens;not null;default:0"`
	LastOutputTokens      int    `json:"lastOutputTokens" gorm:"column:last_output_tokens;not null;default:0"`
	CacheReadTokens       int    `json:"cacheReadTokens" gorm:"column:cache_read_tokens;not null;default:0"`
	CacheCreateTokens     int    `json:"cacheCreateTokens" gorm:"column:cache_create_tokens;not null;default:0"`
	// ContextBreakdownJSON 是最近一轮模型调用前上下文的分类 token 估算
	// （systemPrompt/messages/mcpTools/systemTools/skills/others），供上下文容量
	// 看板按构成展示；估算口径见 service/react/context_breakdown.go。
	ContextBreakdownJSON string `json:"contextBreakdownJson" gorm:"column:context_breakdown_json;type:text"`
	ErrorMessage          string `json:"errorMessage" gorm:"column:error_message;type:text"`
	// ParentRunID 非空表示这是 delegate_agent 委派出的子 run，指向父 run；外层 run 为空。
	ParentRunID string `json:"parentRunId" gorm:"column:parent_run_id;default:null"`
	// AgentPath 是多 Agent 事件归属路径（如 main/ops-agent）；外层 run 为空（事件侧缺省 main）。
	AgentPath string    `json:"agentPath" gorm:"column:agent_path;default:null"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

func (r *ReactRun) TableName() string {
	return "tblLlmReactRun"
}

func CreateReactRun(ctx *gin.Context, run *ReactRun) error {
	return CreateReactRunWithDB(ctx, helpers.MysqlClientLLM, run)
}

func CreateReactRunWithDB(ctx *gin.Context, db *gorm.DB, run *ReactRun) error {
	if run.State == "" {
		run.State = ReactRunStateRunning
	}
	err := db.Model(&ReactRun{}).WithContext(ctx).Create(run).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetReactRunByRunID(ctx *gin.Context, runID string) (*ReactRun, error) {
	var run ReactRun
	err := helpers.MysqlClientLLM.Model(&ReactRun{}).WithContext(ctx).
		Where("run_id = ?", runID).First(&run).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &run, nil
}

func UpdateReactRunByRunID(ctx *gin.Context, runID string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	tx := helpers.MysqlClientLLM.Model(&ReactRun{}).WithContext(ctx).
		Where("run_id = ?", runID).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// AccumulateReactRunDelegatedTokens 把子 run 的 token 消耗原子累加进父 run 的 delegated 列：
// SQL 自增表达式天然并发安全（并行委派的多个子 run 同时累加同一父行）。
func AccumulateReactRunDelegatedTokens(ctx *gin.Context, parentRunID string, inputTokens, outputTokens int) error {
	if inputTokens == 0 && outputTokens == 0 {
		return nil
	}
	tx := helpers.MysqlClientLLM.Model(&ReactRun{}).WithContext(ctx).
		Where("run_id = ?", parentRunID).
		Updates(map[string]any{
			"delegated_input_tokens":  gorm.Expr("delegated_input_tokens + ?", inputTokens),
			"delegated_output_tokens": gorm.Expr("delegated_output_tokens + ?", outputTokens),
		})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ExpireReactRunsByRunIDs(ctx context.Context, runIDs []string) error {
	if len(runIDs) == 0 {
		return nil
	}
	tx := helpers.MysqlClientLLM.Model(&ReactRun{}).WithContext(ctx).
		Where("run_id IN ? AND state IN ?", runIDs, []string{
			ReactRunStateRunning,
			ReactRunStateWaitingClientMessage,
			ReactRunStateWaitingPlan,
			ReactRunStateCancelling,
		}).Updates(map[string]any{
		"state": ReactRunStateExpired,
	})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// staleActiveReactRunCondition 是陈旧活跃 run 清理的过滤条件，独立成函数供 DryRun 单测断言。
// waiting_plan 不参与 stale 过期：Plan 等待可以无限长，等待态的权威状态在 tblLlmPlanExecution，
// 由 plan_resume/plan_cancel 管理生命周期；误过期会让会话解锁但计划仍悬挂（D2）。
func staleActiveReactRunCondition(cutoff time.Time) (string, []any) {
	return "state IN ? AND updated_at < ?", []any{
		[]string{
			ReactRunStateRunning,
			ReactRunStateWaitingClientMessage,
			ReactRunStateCancelling,
		},
		cutoff,
	}
}

// ExpireStaleActiveReactRuns 把超过 olderThan 未更新的活跃 run（running / waiting_client_message /
// cancelling）批量置为 expired，返回受影响行数。waiting_plan 有意不在清理范围（见上方条件说明）。
//
// 服务重启后取消注册表与等待中的 goroutine 已随进程丢失，DB 中残留的活跃 run 会让
// HasActiveReactRun 永远命中，该会话的新消息被「run is active」静默拒绝；启动时调用一次兜底。
// updated_at 下限保护滚动重启场景：重启后仍在推进（updated_at 新鲜）的 run 不会被误伤。
func ExpireStaleActiveReactRuns(ctx context.Context, olderThan time.Duration) (int64, error) {
	return ExpireStaleActiveReactRunsWithDB(ctx, helpers.MysqlClientLLM, olderThan)
}

func ExpireStaleActiveReactRunsWithDB(ctx context.Context, db *gorm.DB, olderThan time.Duration) (int64, error) {
	condition, args := staleActiveReactRunCondition(time.Now().Add(-olderThan))
	tx := db.Model(&ReactRun{}).WithContext(ctx).
		Where(condition, args...).
		Updates(map[string]any{
			"state":         ReactRunStateExpired,
			"error_message": "expired by stale active-run cleanup (service restart)",
		})
	if tx.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected, nil
}

func HasActiveReactRun(ctx *gin.Context, sessionID string) (bool, error) {
	return HasActiveReactRunWithDB(ctx, helpers.MysqlClientLLM, sessionID)
}

func HasActiveReactRunWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) (bool, error) {
	var count int64
	err := db.Model(&ReactRun{}).WithContext(ctx).
		Where("session_id = ? AND state IN ?", sessionID, []string{
			ReactRunStateRunning,
			ReactRunStateWaitingClientMessage,
			ReactRunStateWaitingPlan,
			ReactRunStateCancelling,
		}).Count(&count).Error
	if err != nil {
		return false, components.ErrorDbSelect.Wrap(err)
	}
	return count > 0, nil
}

func GetLatestReactRunBySessionIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) (*ReactRun, error) {
	var run ReactRun
	err := db.Model(&ReactRun{}).WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at DESC, id DESC").First(&run).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &run, nil
}

// outerRunCondition 是外层 run（非 delegate 子 run）的过滤条件：
// 子 run 不参与「会话并发检查 / 上一 run 状态继承 / 外层历史装配」。
const outerRunCondition = "(parent_run_id IS NULL OR parent_run_id = '')"

// GetLatestOuterReactRunBySessionIDWithDB 查询会话最近一个外层 run（排除委派子 run），
// 供外层 run 启动时继承 todo / 已加载工具状态。
func GetLatestOuterReactRunBySessionIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) (*ReactRun, error) {
	var run ReactRun
	err := db.Model(&ReactRun{}).WithContext(ctx).
		Where("session_id = ? AND "+outerRunCondition, sessionID).
		Order("created_at DESC, id DESC").First(&run).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &run, nil
}

func GetReactRunsBySessionID(ctx *gin.Context, sessionID string) ([]ReactRun, error) {
	return GetReactRunsBySessionIDWithDB(ctx, helpers.MysqlClientLLM, sessionID)
}

func GetReactRunsBySessionIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) ([]ReactRun, error) {
	var runs []ReactRun
	err := db.Model(&ReactRun{}).WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at ASC, id ASC").Find(&runs).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return runs, nil
}
