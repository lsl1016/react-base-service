package model

import (
	"time"

	"react-base-service/components"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReactPendingInput 是会话级「排队/引导输入」持久账本（Steering，S1/S2），
// 对齐 ZCode session_input 设计：准入即落账本（崩溃不丢"输入存在过"这一事实），
// 引导消费 = 同一事务内"账本置 guided + 用户消息落库"；重启后残留 admitted 一律作废（不复活队列）。
const (
	ReactPendingKindUserInput    = "user_input"
	ReactPendingKindNotification = "notification"

	ReactPendingDeliveryGuide = "guide"
	ReactPendingDeliveryQueue = "queue"

	// admitted：已准入、待消费（guide 等引擎在模型步边界消费；queue 等 run 结束后晋升）。
	// guided：已被引擎消费为真实用户消息。queued：排队待晋升。
	// cancelled：用户主动取消（S3 队列管理）。discarded：系统结算（终态，settle_reason 记原因）。
	ReactPendingStatusAdmitted  = "admitted"
	ReactPendingStatusGuided    = "guided"
	ReactPendingStatusQueued    = "queued"
	ReactPendingStatusCancelled = "cancelled"
	ReactPendingStatusDiscarded = "discarded"

	// settle_reason（discarded 时必填，对齐 ZCode discardPendingInput 的 reason 词汇）：
	// turn_cancelled=用户取消/断连；turn_failed=run 出错/超时/过期；
	// session_resumed=进程重启后残留作废（重启不复活队列）；run_finished=run 正常结束仍未消费。
	ReactPendingSettleTurnCancelled  = "turn_cancelled"
	ReactPendingSettleTurnFailed     = "turn_failed"
	ReactPendingSettleSessionResumed = "session_resumed"
	ReactPendingSettleRunFinished    = "run_finished"
)

type ReactPendingInput struct {
	ID        uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	SessionID string    `json:"sessionId" gorm:"column:session_id;not null"`
	RunID     string    `json:"runId" gorm:"column:run_id;not null"`
	Kind      string    `json:"kind" gorm:"column:kind;not null;default:'user_input'"`
	Delivery  string    `json:"delivery" gorm:"column:delivery;not null;default:'guide'"`
	Status    string    `json:"status" gorm:"column:status;not null;default:'admitted'"`
	Content   string    `json:"content" gorm:"column:content;type:mediumtext"`
	// Seq 是会话内准入序号（单调递增），决定 guide/queue 的 FIFO 消费顺序。
	Seq int `json:"seq" gorm:"column:seq;not null;default:0"`
	// PayloadJSON 是准入时原始 run 请求快照（ReactRunPayload 序列化）：
	// queue 晋升/自动续跑时据此重建 run 请求（模型选择等按新 run 正常重新解析）。
	PayloadJSON string    `json:"payloadJson,omitempty" gorm:"column:payload_json;type:mediumtext"`
	SettleReason string    `json:"settleReason,omitempty" gorm:"column:settle_reason;not null;default:''"`
	CreatedAt   time.Time `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

func (p *ReactPendingInput) TableName() string {
	return "tblLlmReactPendingInput"
}

func CreateReactPendingInputWithDB(ctx *gin.Context, db *gorm.DB, pending *ReactPendingInput) error {
	if pending.Status == "" {
		pending.Status = ReactPendingStatusAdmitted
	}
	if pending.Kind == "" {
		pending.Kind = ReactPendingKindUserInput
	}
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).Create(pending).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// GetNextAdmittedGuideForUpdateWithDB 按会话内准入序号取最早一条待消费 guide 并加行锁，
// 供引擎在模型步边界的消费事务内 claim-once（未命中返回 nil）。
// 用 Limit(1).Find 而非 First：guide 边界检查高频发生，避免 gorm 的 record not found 噪音日志。
func GetNextAdmittedGuideForUpdateWithDB(ctx *gin.Context, db *gorm.DB, runID string) (*ReactPendingInput, error) {
	var pendings []ReactPendingInput
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("run_id = ? AND kind = ? AND delivery = ? AND status = ?", runID, ReactPendingKindUserInput, ReactPendingDeliveryGuide, ReactPendingStatusAdmitted).
		Order("seq ASC, id ASC").Limit(1).Find(&pendings).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	if len(pendings) == 0 {
		return nil, nil
	}
	return &pendings[0], nil
}

// MarkPendingInputGuidedWithDB 将账本行置 guided（条件更新实现 claim-once）；
// 返回是否命中。与用户消息落库在同一事务内执行，保证 promote 原子性。
func MarkPendingInputGuidedWithDB(ctx *gin.Context, db *gorm.DB, id uint) (bool, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("id = ? AND status = ?", id, ReactPendingStatusAdmitted).
		Updates(map[string]any{"status": ReactPendingStatusGuided})
	if tx.Error != nil {
		return false, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected > 0, nil
}

// SettlePendingInputsByRunIDWithDB 将 run 内未消费的 admitted 用户输入（kind=user_input）
// 批量结算为 discarded，返回受影响行数。run 取消/出错/超时/正常结束时调用
// （WHERE status='admitted' 保证与消费事务互斥）。
// 后台通知（kind=notification）由 SettleNotificationsByRunIDWithDB 单独结算：
// 通知绝不降级排队，run 终态后命令箱已死、没有消费方。
func SettlePendingInputsByRunIDWithDB(ctx *gin.Context, db *gorm.DB, runID, settleReason string) (int64, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("run_id = ? AND kind = ? AND status = ?", runID, ReactPendingKindUserInput, ReactPendingStatusAdmitted).
		Updates(map[string]any{"status": ReactPendingStatusDiscarded, "settle_reason": settleReason})
	if tx.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected, nil
}

// SettleNotificationsByRunIDWithDB 将 run 内未消费的 admitted 后台通知结算为 discarded
// （run 终态后命令箱随 run 死亡，通知没有消费方；结果留在子 run 记录可查），返回受影响行数。
func SettleNotificationsByRunIDWithDB(ctx *gin.Context, db *gorm.DB, runID, settleReason string) (int64, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("run_id = ? AND kind = ? AND status = ?", runID, ReactPendingKindNotification, ReactPendingStatusAdmitted).
		Updates(map[string]any{"status": ReactPendingStatusDiscarded, "settle_reason": settleReason})
	if tx.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected, nil
}

// SettlePendingInputsBySessionWithDB 将会话内全部残留 admitted 输入结算为 discarded(session_resumed)。
// 在 createReactRunContext 事务内调用：能走到"创建新 run"，说明该会话已无活跃 run，
// 任何 admitted 残留都来自已死进程/竞态窗口，按重启语义作废（重启不复活队列）。
func SettlePendingInputsBySessionWithDB(ctx *gin.Context, db *gorm.DB, sessionID, settleReason string) (int64, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND status = ?", sessionID, ReactPendingStatusAdmitted).
		Updates(map[string]any{"status": ReactPendingStatusDiscarded, "settle_reason": settleReason})
	if tx.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected, nil
}

func CountReactPendingInputsBySessionStatusWithDB(ctx *gin.Context, db *gorm.DB, sessionID, status string) (int64, error) {
	var count int64
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND status = ?", sessionID, status).Count(&count).Error
	if err != nil {
		return 0, components.ErrorDbSelect.Wrap(err)
	}
	return count, nil
}

// GetReactPendingInputMaxSeqBySessionIDWithDB 返回会话内已用到的最大准入序号（空会话返回 0）。
// 调用方必须已持有 session 行锁（ admission 与 createReactRunContext 均在锁内执行），保证 seq 分配串行。
func GetReactPendingInputMaxSeqBySessionIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) (int, error) {
	var maxSeq *int
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ?", sessionID).
		Select("MAX(seq)").Scan(&maxSeq).Error
	if err != nil {
		return 0, components.ErrorDbSelect.Wrap(err)
	}
	if maxSeq == nil {
		return 0, nil
	}
	return *maxSeq, nil
}

// GetNextQueuedBySessionWithDB 按会话准入序号返回最早一条 queued 输入（run 结束自动续跑的队首）；
// 未命中返回 nil。晋升采用条件更新（MarkPendingInputPromotedWithDB）claim-once，这里无需行锁。
func GetNextQueuedBySessionWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) (*ReactPendingInput, error) {
	var pendings []ReactPendingInput
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND kind = ? AND status = ?", sessionID, ReactPendingKindUserInput, ReactPendingStatusQueued).
		Order("seq ASC, id ASC").Limit(1).Find(&pendings).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	if len(pendings) == 0 {
		return nil, nil
	}
	return &pendings[0], nil
}

// ListQueuedBySessionWithDB 返回会话内全部 queued 用户输入（S3 队列管理查询），
// 按准入序号 FIFO 排序。
func ListQueuedBySessionWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) ([]ReactPendingInput, error) {
	var pendings []ReactPendingInput
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND kind = ? AND status = ?", sessionID, ReactPendingKindUserInput, ReactPendingStatusQueued).
		Order("seq ASC, id ASC").Find(&pendings).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return pendings, nil
}

// GetQueuedByIDWithDB 按会话与 ID 读取一条 queued 用户输入（显式发送前加载 payload 快照）；
// 未命中或非 queued 返回 nil。
func GetQueuedByIDWithDB(ctx *gin.Context, db *gorm.DB, sessionID string, id uint) (*ReactPendingInput, error) {
	var pendings []ReactPendingInput
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND id = ? AND kind = ? AND status = ?", sessionID, id, ReactPendingKindUserInput, ReactPendingStatusQueued).
		Limit(1).Find(&pendings).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	if len(pendings) == 0 {
		return nil, nil
	}
	return &pendings[0], nil
}

// GetReactPendingInputByIDWithDB 按主键读取账本行（A2 发送后复核投递结果）；
// 未命中返回 nil。
func GetReactPendingInputByIDWithDB(ctx *gin.Context, db *gorm.DB, id uint) (*ReactPendingInput, error) {
	var pendings []ReactPendingInput
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("id = ?", id).Limit(1).Find(&pendings).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	if len(pendings) == 0 {
		return nil, nil
	}
	return &pendings[0], nil
}

// ListQueuedBySessionForUpdateWithDB 是 ListQueuedBySessionWithDB 的行锁版本：
// 重排事务内先锁定当前 queued 集合并重新校验，再无条件改写 seq（避免
// MySQL"值未变化时 RowsAffected=0"与 claim 判定混淆）。
func ListQueuedBySessionForUpdateWithDB(ctx *gin.Context, db *gorm.DB, sessionID string) ([]ReactPendingInput, error) {
	var pendings []ReactPendingInput
	err := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("session_id = ? AND kind = ? AND status = ?", sessionID, ReactPendingKindUserInput, ReactPendingStatusQueued).
		Order("seq ASC, id ASC").Find(&pendings).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return pendings, nil
}

// UpdateQueuedInputSeqLockedWithDB 无条件改写一条 queued 输入的 seq。
// 调用方必须在同一事务内先用 ListQueuedBySessionForUpdateWithDB 锁定并校验该行。
func UpdateQueuedInputSeqLockedWithDB(ctx *gin.Context, db *gorm.DB, sessionID string, id uint, seq int) error {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND id = ?", sessionID, id).
		Updates(map[string]any{"seq": seq})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// UpdateQueuedInputContentLockedWithDB 无条件改写一条 queued 输入的内容。
// 调用方必须在同一事务内先用 ListQueuedBySessionForUpdateWithDB 锁定并确认该行存在。
func UpdateQueuedInputContentLockedWithDB(ctx *gin.Context, db *gorm.DB, sessionID string, id uint, content string) error {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND id = ?", sessionID, id).
		Updates(map[string]any{"content": content})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// CancelQueuedInputWithDB 将一条 queued 输入置 cancelled（S3 用户主动删除）：
// 条件更新 claim-safe，与晋升互斥。cancelled 是用户语义的终态，不写 settle_reason。
func CancelQueuedInputWithDB(ctx *gin.Context, db *gorm.DB, sessionID string, id uint) (bool, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND id = ? AND kind = ? AND status = ?", sessionID, id, ReactPendingKindUserInput, ReactPendingStatusQueued).
		Updates(map[string]any{"status": ReactPendingStatusCancelled})
	if tx.Error != nil {
		return false, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected > 0, nil
}

// MarkPendingInputPromotedWithDB 将 queued 行晋升为某次新 run 的用户输入（S2 自动续跑/显式发送）：
// 条件更新实现 claim-once，并归属到新 run。必须与该 run 的创建、用户消息落库在同一事务内执行，
// 保证"账本置 guided + 用户消息落库"的 promote 原子性。
func MarkPendingInputPromotedWithDB(ctx *gin.Context, db *gorm.DB, id uint, runID string) (bool, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("id = ? AND status = ?", id, ReactPendingStatusQueued).
		Updates(map[string]any{"status": ReactPendingStatusGuided, "run_id": runID})
	if tx.Error != nil {
		return false, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected > 0, nil
}

// FallbackPendingInputsToQueueWithDB 将 run 内未消费的 admitted guide 降级为 queue
// （对齐 ZCode fallbackPendingGuidesToQueue：turn 打断/结束时不丢输入，delivery 改写为 queue），
// 返回受影响行数。queue 排队关闭时调用方应走 discard 结算而不是本函数。
// 仅限 kind=user_input：后台通知绝不能变成排队用户输入被自动续跑。
func FallbackPendingInputsToQueueWithDB(ctx *gin.Context, db *gorm.DB, runID string) (int64, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("run_id = ? AND kind = ? AND status = ?", runID, ReactPendingKindUserInput, ReactPendingStatusAdmitted).
		Updates(map[string]any{"status": ReactPendingStatusQueued, "delivery": ReactPendingDeliveryQueue})
	if tx.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected, nil
}

// SettleStaleQueuedBySessionWithDB 将进程启动前遗留的 queued 行结算为 discarded(session_resumed)：
// 重启不复活队列——队列的存在性持久、内容不跨进程复活（对齐 ZCode resume.ts 结算语义）。
// 本进程内新产生的 queued 行不受影响（它们正等待当前 run 结束后的自动续跑）。
func SettleStaleQueuedBySessionWithDB(ctx *gin.Context, db *gorm.DB, sessionID string, processStart time.Time) (int64, error) {
	tx := db.Model(&ReactPendingInput{}).WithContext(ctx).
		Where("session_id = ? AND status = ? AND created_at < ?", sessionID, ReactPendingStatusQueued, processStart).
		Updates(map[string]any{"status": ReactPendingStatusDiscarded, "settle_reason": ReactPendingSettleSessionResumed})
	if tx.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return tx.RowsAffected, nil
}
