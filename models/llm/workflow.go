package model

import (
	"context"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 定时触发工作流（见 docs/定时触发工作流实现方案.md）：
// tblLlmWorkflow 存任务定义与调度计划（跑什么/何时跑/怎么通知），
// tblLlmWorkflowRun 存每次触发（cron 着火或手动 dispatch）的运行历史。
// 基座只存结论摘要与风险级别，领域明细留给业务层；完整对话在会话消息表。
// 入参统一 context.Context：调度器/runner 在后台 goroutine 运行（无 gin 上下文），
// 控制器传 *gin.Context 即可（实现了 context.Context）。

const (
	WorkflowTriggerTypeCron   = "cron"
	WorkflowTriggerTypeManual = "manual"

	// WorkflowRun 状态机（对齐 OpenHands 并按基座细化）：
	// pending → running → completed | failed | cancelled | timeout | skipped。
	// skipped = 并发闸门瞬时跳过（不算失败，防告警误报）；
	// timeout 从 failed 拆出（对巡检"超时未完成"是最需单独观察的失败模式）。
	WorkflowRunStatusPending   = "pending"
	WorkflowRunStatusRunning   = "running"
	WorkflowRunStatusCompleted = "completed"
	WorkflowRunStatusFailed    = "failed"
	WorkflowRunStatusCancelled = "cancelled"
	WorkflowRunStatusTimeout   = "timeout"
	WorkflowRunStatusSkipped   = "skipped"

	// 风险级别（一期规则判级：summary 命中 risk_patterns 正则即 high）。
	WorkflowRiskLevelNone = "none"
	WorkflowRiskLevelHigh = "high"
)

// workflowRunningStatuses 参与并发闸门计数的在途状态。
var workflowRunningStatuses = []string{WorkflowRunStatusPending, WorkflowRunStatusRunning}

// Workflow 定时触发工作流定义（定义与计划合一张表：trigger_type 判别 + cron_expr）。
type Workflow struct {
	ID           uint64 `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	WorkflowKey  string `json:"workflowKey" gorm:"column:workflow_key;not null"`
	Name         string `json:"name" gorm:"column:name;not null"`
	Description  string `json:"description" gorm:"column:description;type:text"`
	CallerKey    string `json:"callerKey" gorm:"column:caller_key;not null"`
	RouteValues  string `json:"routeValues" gorm:"column:route_values;not null;default:'[]'"`
	UserName     string `json:"userName" gorm:"column:user_name;not null;default:''"`
	Prompt       string `json:"prompt" gorm:"column:prompt;not null"`
	TriggerType  string `json:"triggerType" gorm:"column:trigger_type;not null;default:'cron'"`
	CronExpr     string `json:"cronExpr" gorm:"column:cron_expr;not null;default:''"`
	Timezone     string `json:"timezone" gorm:"column:timezone;not null;default:'Asia/Shanghai'"`
	ModelKey     string `json:"modelKey" gorm:"column:model_key;not null;default:''"`
	ModelVersion string `json:"modelVersion" gorm:"column:model_version;not null;default:''"`
	MaxSteps     int    `json:"maxSteps" gorm:"column:max_steps;not null;default:0"`
	TimeoutSec   int    `json:"timeoutSec" gorm:"column:timeout_sec;not null;default:0"`
	// NotifyWebhookURL 是完成通知地址（按域名识别企业微信/飞书通用 webhook 格式）。
	NotifyWebhookURL string `json:"notifyWebhookUrl" gorm:"column:notify_webhook_url;not null;default:''"`
	// RiskPatterns 是判级规则：每行一个正则，对 summary 命中即 risk=high。
	RiskPatterns string    `json:"riskPatterns" gorm:"column:risk_patterns;type:text"`
	Enabled      int       `json:"enabled" gorm:"column:enabled;not null;default:1"`
	// NextFireAt/LastFireAt 是列表页展示缓存快照（manager 注册/着火时回写），非调度依据。
	NextFireAt *time.Time `json:"nextFireAt,omitempty" gorm:"column:next_fire_at"`
	LastFireAt *time.Time `json:"lastFireAt,omitempty" gorm:"column:last_fire_at"`
	DeletedAt  int64      `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
	CreatedAt  time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt  time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}

func (w *Workflow) TableName() string {
	return "tblLlmWorkflow"
}

// CronSchedulable 判断该定义是否需要注册 cron entry。
func (w *Workflow) CronSchedulable() bool {
	return w.TriggerType == WorkflowTriggerTypeCron && w.Enabled == 1 && w.DeletedAt == 0
}

// WorkflowRun 一次触发的运行历史；planned_fire_at 唯一索引使「插入 run」本身即多实例防重锁。
type WorkflowRun struct {
	ID          uint64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	WorkflowID  uint64     `json:"workflowId" gorm:"column:workflow_id;not null"`
	TriggerType string     `json:"triggerType" gorm:"column:trigger_type;not null"`
	TriggerNote string     `json:"triggerNote" gorm:"column:trigger_note;not null;default:''"`
	// PlannedFireAt 是 cron 计划着火时刻（防重键）；手动触发为 NULL（唯一索引对 NULL 放行）。
	PlannedFireAt *time.Time `json:"plannedFireAt,omitempty" gorm:"column:planned_fire_at"`
	Status        string     `json:"status" gorm:"column:status;not null;default:'pending'"`
	SessionID     string     `json:"sessionId" gorm:"column:session_id;not null;default:''"`
	RunID         string     `json:"runId" gorm:"column:run_id;not null;default:''"`
	// Summary 是 run 最终 assistant 消息（结论快照，供列表页与通知渲染）；全文在会话消息表。
	Summary   string     `json:"summary" gorm:"column:summary;type:mediumtext"`
	RiskLevel string     `json:"riskLevel" gorm:"column:risk_level;not null;default:'none'"`
	ErrorMsg  string     `json:"errorMsg" gorm:"column:error_msg;type:text"`
	StartedAt  *time.Time `json:"startedAt,omitempty" gorm:"column:started_at"`
	FinishedAt *time.Time `json:"finishedAt,omitempty" gorm:"column:finished_at"`
	CreatedAt  time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt  time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}

func (r *WorkflowRun) TableName() string {
	return "tblLlmWorkflowRun"
}

// CreateWorkflow 新建工作流定义。
func CreateWorkflow(ctx context.Context, workflow *Workflow) error {
	if err := helpers.MysqlClientLLM.WithContext(ctx).Create(workflow).Error; err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// UpdateWorkflowByKey 按 workflow_key 局部更新未删除定义（columns 为非空字段集合）。
func UpdateWorkflowByKey(ctx context.Context, workflowKey string, columns map[string]any) error {
	if len(columns) == 0 {
		return nil
	}
	if err := helpers.MysqlClientLLM.WithContext(ctx).Model(&Workflow{}).
		Where("workflow_key = ? AND deleted_at = 0", workflowKey).
		Updates(columns).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}

// GetWorkflowByKey 按 workflow_key 取未删除定义；不存在返回 (nil, nil)。
func GetWorkflowByKey(ctx context.Context, workflowKey string) (*Workflow, error) {
	var row Workflow
	err := helpers.MysqlClientLLM.WithContext(ctx).
		Where("workflow_key = ? AND deleted_at = 0", workflowKey).
		First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &row, nil
}

// ListWorkflows 返回全部未删除定义（id 升序，管理面量级小不分页）。
func ListWorkflows(ctx context.Context) ([]Workflow, error) {
	var rows []Workflow
	if err := helpers.MysqlClientLLM.WithContext(ctx).
		Where("deleted_at = 0").Order("id ASC").Find(&rows).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

// LatestWorkflowRunStatuses 返回各 workflow 最近一次 run 的状态（定义列表页观测列）。
func LatestWorkflowRunStatuses(ctx context.Context) (map[uint64]string, error) {
	type idStatus struct {
		WorkflowID uint64 `gorm:"column:workflow_id"`
		Status     string `gorm:"column:status"`
	}
	var rows []idStatus
	if err := helpers.MysqlClientLLM.WithContext(ctx).Model(&WorkflowRun{}).
		Select("workflow_id, status").
		Where("id IN (?)", helpers.MysqlClientLLM.WithContext(ctx).Model(&WorkflowRun{}).
			Select("MAX(id)").Group("workflow_id")).
		Scan(&rows).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	statuses := make(map[uint64]string, len(rows))
	for _, row := range rows {
		statuses[row.WorkflowID] = row.Status
	}
	return statuses, nil
}

// SoftDeleteWorkflow 软删定义（置 deleted_at）并跳过其 pending run（OpenHands 同款，不物理删）。
func SoftDeleteWorkflow(ctx context.Context, workflowKey string) error {
	db := helpers.MysqlClientLLM.WithContext(ctx)
	if err := db.Model(&Workflow{}).
		Where("workflow_key = ? AND deleted_at = 0", workflowKey).
		Update("deleted_at", time.Now().UnixNano()).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	workflowIDs := helpers.MysqlClientLLM.WithContext(ctx).Model(&Workflow{}).
		Select("id").Where("workflow_key = ?", workflowKey)
	return db.Model(&WorkflowRun{}).
		Where("workflow_id IN (?)", workflowIDs).
		Where("status = ?", WorkflowRunStatusPending).
		Updates(map[string]any{"status": WorkflowRunStatusSkipped, "error_msg": "workflow 已删除"}).Error
}

// CreateWorkflowRun 插入运行历史；uk_workflow_fire(workflow_id, planned_fire_at) 冲突时
// 不插入并返回 created=false（多实例同刻着火防重路径；手动触发 planned_fire_at=NULL 天然放行）。
func CreateWorkflowRun(ctx context.Context, run *WorkflowRun) (bool, error) {
	result := helpers.MysqlClientLLM.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(run)
	if result.Error != nil {
		return false, components.ErrorDbInsert.Wrap(result.Error)
	}
	return result.RowsAffected > 0, nil
}

// UpdateWorkflowRunByID 局部更新运行历史。
func UpdateWorkflowRunByID(ctx context.Context, runID uint64, columns map[string]any) error {
	if len(columns) == 0 {
		return nil
	}
	if err := helpers.MysqlClientLLM.WithContext(ctx).Model(&WorkflowRun{}).
		Where("id = ?", runID).Updates(columns).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}

// CountRunningWorkflowRunsByCaller 统计某 caller 名下未删除 workflow 的在途 run 数（并发闸门）。
// excludeRunID 用于排除刚插入的本次 run 自身（0=不排除）。
func CountRunningWorkflowRunsByCaller(ctx context.Context, callerKey string, excludeRunID uint64) (int64, error) {
	query := helpers.MysqlClientLLM.WithContext(ctx).Model(&WorkflowRun{}).
		Joins("JOIN tblLlmWorkflow w ON w.id = tblLlmWorkflowRun.workflow_id").
		Where("w.caller_key = ? AND w.deleted_at = 0", callerKey).
		Where("tblLlmWorkflowRun.status IN ?", workflowRunningStatuses)
	if excludeRunID > 0 {
		query = query.Where("tblLlmWorkflowRun.id <> ?", excludeRunID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, components.ErrorDbSelect.Wrap(err)
	}
	return count, nil
}

// ListWorkflowRunsByWorkflowID 分页返回某 workflow 的运行历史（status 可空过滤，id 倒序）。
func ListWorkflowRunsByWorkflowID(ctx context.Context, workflowID uint64, status string, limit, offset int) ([]WorkflowRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := helpers.MysqlClientLLM.WithContext(ctx).Model(&WorkflowRun{}).
		Where("workflow_id = ?", workflowID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var rows []WorkflowRun
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return rows, nil
}

// CountWorkflowRunsByStatus 按状态聚合某 workflow 的 run 计数（历史页 status_counts）。
func CountWorkflowRunsByStatus(ctx context.Context, workflowID uint64) (map[string]int64, error) {
	type statusCount struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	var rows []statusCount
	if err := helpers.MysqlClientLLM.WithContext(ctx).Model(&WorkflowRun{}).
		Select("status, COUNT(*) AS count").
		Where("workflow_id = ?", workflowID).
		Group("status").Scan(&rows).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.Status] = row.Count
	}
	return counts, nil
}
