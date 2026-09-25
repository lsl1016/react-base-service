package react

// S3 队列管理 API（docs/plan/20260925_Session与Steering机制借鉴方案.md §2.3）：
// 会话队列（tblLlmReactPendingInput，kind=user_input、status=queued）的查询、编辑、
// 重排、删除（cancelled）与显式发送（WS queue_send 晋升开新 run）。
//
// 纪律（对齐 ZCode 教训）：
//   - 重排必须同步改写账本 seq——只改内存投影会造成"冷热事实分叉"；
//   - 全部写操作走条件更新（id + status=queued），与自动续跑晋升互斥（claim-once），
//     多端并发下编辑/删除/重排落空返回 claimed=false，不报错不覆盖；
//   - 归属校验复用 validateReactSessionContext 五元组口径。

import (
	"encoding/json"
	"fmt"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
	"gorm.io/gorm"
)

// EventQueueSend 是 WS 上行的显式发送消息类型（S3）：客户端指定一条排队输入，
// 服务端 claim-once 晋升并在当前连接开新 run（实时事件流与 S2 自动续跑一致）。
const EventQueueSend = "queue_send"

// validateQueueRequest 校验队列管理请求的会话归属（当前登录用户 + caller + 路由五元组），
// 返回校验通过的会话行。各 queue API 共用。
func validateQueueRequest(ctx *gin.Context, sessionID, callerKey string, routeValues []string) (*model.ReactSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, components.ErrorParamInvalid.Sprintf("sessionId 不能为空")
	}
	session, err := model.GetReactSessionBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, components.ErrorReactSessionNotFound.Sprintf(sessionID)
	}
	if routeValues == nil {
		routeValues = []string{}
	}
	routeValuesBytes, _ := json.Marshal(routeValues)
	if err := validateReactSessionContext(session, helpers.GetUserName(ctx), callerKey, string(routeValuesBytes), normalizeSessionType("")); err != nil {
		return nil, err
	}
	return session, nil
}

// ListSessionQueue 查询会话队列（FIFO）与当前 steering 配置回显。
func ListSessionQueue(ctx *gin.Context, req params.ReactQueueListReq) (params.ReactQueueListResp, error) {
	if _, err := validateQueueRequest(ctx, req.SessionID, req.CallerKey, req.RouteValues); err != nil {
		return params.ReactQueueListResp{}, err
	}
	rows, err := model.ListQueuedBySessionWithDB(ctx, helpers.MysqlClientLLM, strings.TrimSpace(req.SessionID))
	if err != nil {
		return params.ReactQueueListResp{}, err
	}
	items := make([]params.ReactQueueItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, params.ReactQueueItem{
			ID:             row.ID,
			PendingInputID: pendingInputID(row.ID),
			SessionID:      row.SessionID,
			Content:        row.Content,
			Seq:            row.Seq,
			Status:         row.Status,
			CreatedAt:      row.CreatedAt,
		})
	}
	cfg := conf.GetReactRuntimeConfig().Steering
	return params.ReactQueueListResp{
		Items:        items,
		QueueEnabled: cfg.QueueEnabled(),
		AutoDrain:    cfg.QueueAutoDrainEnabled(),
	}, nil
}

// UpdateQueuedItem 编辑一条排队输入的内容：事务内锁定读确认仍是 queued 后无条件改写
//（避免 MySQL"值未变化时 RowsAffected=0"误报）；输入已被晋升/作废时 claimed=false。
func UpdateQueuedItem(ctx *gin.Context, req params.ReactQueueUpdateReq) (params.ReactQueueMutateResp, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if _, err := validateQueueRequest(ctx, sessionID, req.CallerKey, req.RouteValues); err != nil {
		return params.ReactQueueMutateResp{}, err
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.ID <= 0 || req.Content == "" {
		return params.ReactQueueMutateResp{}, components.ParamInvalidf("id、content 不能为空")
	}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := model.ListQueuedBySessionForUpdateWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		for _, row := range current {
			if row.ID != req.ID {
				continue
			}
			return model.UpdateQueuedInputContentLockedWithDB(ctx, tx, sessionID, req.ID, req.Content)
		}
		return components.ParamInvalidf("排队输入 %d 不在队列中（可能已被发送或删除）", req.ID)
	})
	if err != nil {
		return params.ReactQueueMutateResp{}, err
	}
	zlog.Infof(ctx, "[React.Queue] 排队输入已编辑: sessionId=%s, id=%d", sessionID, req.ID)
	return s3MutateResp(ctx, sessionID, true)
}

// ReorderSessionQueue 按请求顺序重排全部排队输入：
// 事务内锁定读当前 queued 集合并重新校验（IDList 必须恰为排列），随后无条件改写账本 seq
//（从当前最小 seq 起连续分配），防冷热事实分叉；并发晋升/删除时锁定读可见最新状态并整体拒绝。
func ReorderSessionQueue(ctx *gin.Context, req params.ReactQueueReorderReq) (params.ReactQueueMutateResp, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if _, err := validateQueueRequest(ctx, sessionID, req.CallerKey, req.RouteValues); err != nil {
		return params.ReactQueueMutateResp{}, err
	}
	if len(req.IDList) == 0 {
		return params.ReactQueueMutateResp{}, components.ParamInvalidf("idList 不能为空")
	}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := model.ListQueuedBySessionForUpdateWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if len(current) == 0 {
			return components.ParamInvalidf("队列为空，无需重排")
		}
		assign, planErr := planQueueReorder(current, req.IDList)
		if planErr != nil {
			return components.ParamInvalidf("重排请求非法: %v", planErr)
		}
		for id, seq := range assign {
			if err := model.UpdateQueuedInputSeqLockedWithDB(ctx, tx, sessionID, id, seq); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return params.ReactQueueMutateResp{}, err
	}
	zlog.Infof(ctx, "[React.Queue] 队列已重排: sessionId=%s, count=%d", sessionID, len(req.IDList))
	return s3MutateResp(ctx, sessionID, true)
}

// DeleteQueuedItem 删除（取消）一条排队输入；输入已被晋升/作废时 claimed=false。
func DeleteQueuedItem(ctx *gin.Context, req params.ReactQueueDeleteReq) (params.ReactQueueMutateResp, error) {
	if _, err := validateQueueRequest(ctx, req.SessionID, req.CallerKey, req.RouteValues); err != nil {
		return params.ReactQueueMutateResp{}, err
	}
	if req.ID <= 0 {
		return params.ReactQueueMutateResp{}, components.ErrorParamInvalid.Sprintf("id 不能为空")
	}
	claimed, err := model.CancelQueuedInputWithDB(ctx, helpers.MysqlClientLLM, strings.TrimSpace(req.SessionID), req.ID)
	if err != nil || !claimed {
		return params.ReactQueueMutateResp{}, err
	}
	zlog.Infof(ctx, "[React.Queue] 排队输入已删除: sessionId=%s, id=%d", req.SessionID, req.ID)
	return s3MutateResp(ctx, req.SessionID, true)
}

// s3MutateResp 组装变更类响应（含变更后的队列长度）。
func s3MutateResp(ctx *gin.Context, sessionID string, claimed bool) (params.ReactQueueMutateResp, error) {
	length, err := model.CountReactPendingInputsBySessionStatusWithDB(ctx, helpers.MysqlClientLLM, strings.TrimSpace(sessionID), model.ReactPendingStatusQueued)
	if err != nil {
		return params.ReactQueueMutateResp{}, err
	}
	return params.ReactQueueMutateResp{Claimed: claimed, QueueLength: int(length)}, nil
}

// planQueueReorder 校验重排请求并生成 seq 分配（纯函数）：
// requestedIDs 必须恰好是当前 queued 集合的排列；新 seq 从当前最小 seq 起按请求顺序连续分配
//（保持与更晚准入输入的相对先后，只压缩队列内部的序号）。
func planQueueReorder(queued []model.ReactPendingInput, requestedIDs []uint) (map[uint]int, error) {
	if len(requestedIDs) != len(queued) {
		return nil, fmt.Errorf("idList 数量(%d)与队列长度(%d)不一致", len(requestedIDs), len(queued))
	}
	seen := make(map[uint]bool, len(requestedIDs))
	for _, id := range requestedIDs {
		if id <= 0 {
			return nil, fmt.Errorf("非法 id: %d", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("重复 id: %d", id)
		}
		seen[id] = true
	}
	minSeq := 0
	for i, row := range queued {
		if !seen[row.ID] {
			return nil, fmt.Errorf("id %d 不在当前队列中", row.ID)
		}
		if i == 0 || row.Seq < minSeq {
			minSeq = row.Seq
		}
	}
	assign := make(map[uint]int, len(requestedIDs))
	for i, id := range requestedIDs {
		assign[id] = minSeq + i
	}
	return assign, nil
}

// PrepareQueuedRunPayload 为显式发送（WS queue_send）加载排队输入并构造新 run 的 payload：
// 准入时保存的 payload_json 快照重建请求（模型选择等由 prepareRuntimeRequest 重新解析），
// UserPrompt 取队列内容，携带 PromotePendingInputID（晋升 claim 在 createReactRunContext
// 事务内完成，与 run 创建、用户消息落库原子）。
func PrepareQueuedRunPayload(ctx *gin.Context, sessionID, pendingInputId string) (*params.ReactRunPayload, string, error) {
	id, err := parsePendingInputID(pendingInputId)
	if err != nil {
		return nil, "", err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, "", components.ErrorParamInvalid.Sprintf("sessionId 不能为空")
	}
	session, err := model.GetReactSessionBySessionID(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	if session == nil {
		return nil, "", components.ErrorReactSessionNotFound.Sprintf(sessionID)
	}
	row, err := model.GetQueuedByIDWithDB(ctx, helpers.MysqlClientLLM, sessionID, id)
	if err != nil {
		return nil, "", err
	}
	if row == nil {
		return nil, "", components.ErrorParamInvalid.Sprintf("排队输入不存在或已不在队列中: %s", pendingInputId)
	}
	var payload params.ReactRunPayload
	if strings.TrimSpace(row.PayloadJSON) != "" {
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			zlog.Warnf(ctx, "[React.Queue] 排队输入 payload 快照解析失败(按空请求发送): id=%d, err=%v", row.ID, err)
			payload = params.ReactRunPayload{}
		}
	}
	if err := validateReactSessionContext(session, helpers.GetUserName(ctx), payload.CallerKey, marshalRouteValuesForValidate(payload.RouteValues), normalizeSessionType(payload.Type)); err != nil {
		return nil, "", err
	}
	payload.UserPrompt = row.Content
	payload.PromotePendingInputID = row.ID
	return &payload, pendingInputID(row.ID), nil
}

// parsePendingInputID 还原账本对外 ID（"pend_N" → N）。
func parsePendingInputID(pendingInputID string) (uint, error) {
	var id uint
	if _, err := fmt.Sscanf(strings.TrimSpace(pendingInputID), "pend_%d", &id); err != nil || id <= 0 {
		return 0, components.ErrorParamInvalid.Sprintf("pendingInputId 非法: %s", pendingInputID)
	}
	return id, nil
}

// marshalRouteValuesForValidate 是队列发送路径的 routeValues 归一化（nil → []，JSON 序列化口径与准入一致）。
func marshalRouteValuesForValidate(routeValues []string) string {
	if routeValues == nil {
		routeValues = []string{}
	}
	encoded, _ := json.Marshal(routeValues)
	return string(encoded)
}
