package react

// 会话分叉（fork）：把一个历史会话在指定截断点之前的历史复制成一个全新的普通会话。
//
// 设计取舍（详见 docs/plan/20260925_Session与Steering机制借鉴方案.md §5 的 P3 立项）：
//   - 复制式 fork 而非同会话消息树：分叉出的会话就是一个普通 session，历史装配、事件回放、
//     compact 压缩等全部读路径零改动；原会话硬删也不影响分叉（独立行，无跨会话引用）；
//   - 截断点按 run 边界对齐（一期）：消息 Seq 是 run 内序号，run 是最小完整对话单元，
//     整轮复制/整轮丢弃可绕开 run 内截断的半成品消息、孤儿 tool_use、Plan 卡片等边界；
//   - 复制时全部业务 ID 重生成（session_id/run_id/message_id，均有唯一键），parent_run_id
//     与 compact 游标按旧→新映射重写；created_at 保留原值——timeline 主排序是 created_at，
//     保序即回放顺序不变。复制 run 行（而非只复制消息）还让 TodoState/已加载工具的
//     「最新外层 run 继承」机制（createReactRunContext）在分叉会话天然生效；
//   - 不复制：ToolResult（TTL 缓存，resultRef 全局可读，失效与自然过期同语义）、PendingInput
//     （排队账本属于原会话的将来，不属于历史）、Feedback/Artifact/AsyncTask/Plan 全家
//     （运行态数据不可跨会话复活，plan/* run 回放侧本就被过滤）；
//   - 活跃 run 一律拒绝（与 delete 同口径）：非终态 run 若被复制进新会话，会被
//     HasActiveReactRun 永久命中，分叉会话一条消息都发不出去。排队输入不参与判断——
//     晋升路径（steering/queue_send）与 fork 都先锁 session 行，天然串行化。

import (
	"encoding/json"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"react-base-service/golib/zlog"
)

// forkSessionTitleSuffix 是分叉会话默认标题后缀。
const forkSessionTitleSuffix = " (分叉)"

// customSessionTitleMaxLength 是调用方显式指定标题的截断上限（rune），列宽 VARCHAR(255)。
const customSessionTitleMaxLength = 64

// ForkReactSession 基于历史会话分叉出一个新会话（单事务复制）。
func ForkReactSession(ctx *gin.Context, req params.ReactSessionForkReq) (params.ReactSessionForkResp, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	throughMessageID := strings.TrimSpace(req.ThroughMessageID)
	if sessionID == "" {
		return params.ReactSessionForkResp{}, components.ErrorParamInvalid.Sprintf("sessionId 不能为空")
	}
	if throughMessageID == "" {
		return params.ReactSessionForkResp{}, components.ErrorParamInvalid.Sprintf("throughMessageId 不能为空")
	}

	var newSessionID string
	resp := params.ReactSessionForkResp{ForkedFrom: sessionID}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行锁 + 存在性（与 delete 同口径：锁住源会话，排除并发删除/并发启动 run）
		session, err := model.GetReactSessionBySessionIDForUpdate(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if session == nil {
			return components.ErrorReactSessionNotFound.Sprintf(sessionID)
		}
		// 归属校验（与 list/events/delete 同口径）
		if err := validateReactSessionContext(session, helpers.GetUserName(ctx), req.CallerKey, marshalRouteValuesForValidate(req.RouteValues), normalizeSessionType("")); err != nil {
			return err
		}
		// 活跃 run 一律拒绝：避免复制到正在写入的 run，也避免非终态 run 锁死新会话
		active, err := model.HasActiveReactRunWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if active {
			return components.ErrorParamInvalid.Sprintf("会话有正在运行的任务，请先停止后再分叉")
		}

		// 行锁下读全量 runs/messages，截断解析与复制计划是纯函数（可单测）
		runs, err := model.GetReactRunsBySessionIDWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		messages, err := model.GetReactMessagesBySessionIDWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		cut, err := resolveForkCut(runs, messages, throughMessageID)
		if err != nil {
			return err
		}
		plan, err := buildForkCopyPlan(session, req, runs, messages, cut)
		if err != nil {
			return err
		}

		if err := model.CreateReactSessionWithDB(ctx, tx, plan.session); err != nil {
			return err
		}
		if err := model.BatchCreateReactRunsWithDB(ctx, tx, plan.runs); err != nil {
			return err
		}
		if err := model.BatchCreateReactMessagesWithDB(ctx, tx, plan.messages); err != nil {
			return err
		}

		newSessionID = plan.session.SessionID
		resp.SessionID = newSessionID
		resp.CutRunID = cut.anchorRun.RunID
		resp.Inclusive = cut.inclusive
		resp.Runs = len(plan.runs)
		resp.Messages = len(plan.messages)
		return nil
	})
	if err != nil {
		return params.ReactSessionForkResp{}, err
	}
	metrics.SessionsCreatedTotal.Inc()
	zlog.Infof(ctx, "[React.Session] 会话已分叉: from=%s, to=%s, cutRun=%s, inclusive=%v, runs=%d, messages=%d",
		sessionID, newSessionID, resp.CutRunID, resp.Inclusive, resp.Runs, resp.Messages)
	return resp, nil
}

// forkCut 是按 run 边界对齐后的截断点：anchorRun 是截断点所在的外层 run，
// inclusive=true 表示 anchorRun 整轮保留（带着这轮回答继续），false 表示
// anchorRun 起全部丢弃（回到这轮提问之前重新问）。
type forkCut struct {
	anchorRun *model.ReactRun
	inclusive bool
}

// resolveForkCut 把前端传入的消息级分叉点对齐到外层 run 边界：
//   - 分叉点可能落在 delegate 子 run 的消息上（回放时间线包含子 run 消息），上溯到外层 run；
//   - 命中外层 run 的起始用户输入（seq=1 且 react_user_input；guide 消息同为
//     react_user_input 但 seq>1，不构成 run 起点）→ exclusive；
//   - 命中其余消息（assistant/tool_result/compact/guide/notice…）→ inclusive。
func resolveForkCut(runs []model.ReactRun, messages []model.ReactMessage, throughMessageID string) (forkCut, error) {
	var picked *model.ReactMessage
	for i := range messages {
		if messages[i].MessageID == throughMessageID {
			picked = &messages[i]
			break
		}
	}
	if picked == nil {
		return forkCut{}, components.ErrorParamInvalid.Sprintf("分叉点消息不存在: %s", throughMessageID)
	}

	runByID := make(map[string]*model.ReactRun, len(runs))
	for i := range runs {
		runByID[runs[i].RunID] = &runs[i]
	}
	run, ok := runByID[picked.RunID]
	if !ok {
		return forkCut{}, components.ErrorParamInvalid.Sprintf("分叉点消息所属 run 不存在: %s", picked.RunID)
	}
	// 上溯到外层 run（父 run 一定先于子 run 创建，链路必然存在；缺失按数据异常拒绝）
	outer := run
	for outer.ParentRunID != "" {
		parent, parentOK := runByID[outer.ParentRunID]
		if !parentOK {
			return forkCut{}, components.ErrorParamInvalid.Sprintf("分叉点所在 run 的父链不完整: %s", outer.RunID)
		}
		outer = parent
	}
	if strings.HasPrefix(strings.TrimSpace(outer.AgentPath), "plan/") {
		return forkCut{}, components.ErrorParamInvalid.Sprintf("分叉点不能落在 Plan 执行消息上")
	}

	// exclusive 仅当命中的正是 anchor run 的起始用户输入
	for i := range messages {
		m := &messages[i]
		if m.RunID == outer.RunID && m.Seq == 1 && m.MessageType == model.ReactMessageTypeUserInput {
			return forkCut{anchorRun: outer, inclusive: m.MessageID != picked.MessageID}, nil
		}
	}
	return forkCut{anchorRun: outer, inclusive: true}, nil
}

// forkCopyPlan 是一次分叉复制的纯内存计算结果（不落库，便于单测）。
type forkCopyPlan struct {
	session  *model.ReactSession
	runs     []model.ReactRun
	messages []model.ReactMessage
	// runIDMap/messageIDMap 保留旧→新映射，用于 compact 游标重写与测试断言。
	runIDMap     map[string]string
	messageIDMap map[string]string
	lastRunID    string // 新会话最后一个外层 run 的新 ID（写 session.last_run_id）
}

// buildForkCopyPlan 计算分叉复制计划：确定复制范围（外层 run 至截断点 + 其 delegate
// 子 run，剔除 plan/* run）、生成新 ID、重写父子关系与 compact 游标、保序复制 created_at。
func buildForkCopyPlan(source *model.ReactSession, req params.ReactSessionForkReq, runs []model.ReactRun, messages []model.ReactMessage, cut forkCut) (*forkCopyPlan, error) {
	newSessionID := generateSessionID()
	now := time.Now()

	// 1. 外层 run 复制范围：created_at 顺序推进到 anchor（plan run 跳过、不断开推进）
	included := make(map[string]bool, len(runs))
	sawAnchor := false
	for i := range runs {
		run := &runs[i]
		if run.ParentRunID != "" || isPlanReactRun(run) {
			continue
		}
		if run.RunID == cut.anchorRun.RunID {
			if cut.inclusive {
				included[run.RunID] = true
			}
			sawAnchor = true
			break
		}
		included[run.RunID] = true
	}
	if !sawAnchor {
		return nil, components.ErrorParamInvalid.Sprintf("截断点 run 不在会话 run 列表中: %s", cut.anchorRun.RunID)
	}
	if len(included) == 0 {
		return nil, components.ErrorParamInvalid.Sprintf("分叉点之前没有任何历史消息，无需分叉")
	}
	// 2. delegate 子 run 随父复制：runs 按 created_at 升序且子晚于父创建，单趟闭包即可
	for i := range runs {
		run := &runs[i]
		if run.ParentRunID == "" || isPlanReactRun(run) {
			continue
		}
		if included[run.ParentRunID] {
			included[run.RunID] = true
		}
	}

	plan := &forkCopyPlan{
		runIDMap:     make(map[string]string, len(included)),
		messageIDMap: make(map[string]string, len(messages)),
	}
	lastOuterSourceRunID := ""

	// 3. 复制 run 行：ID 重生成、parent 重写、非终态收敛（双保险）、created_at 保序
	for i := range runs {
		run := &runs[i]
		if !included[run.RunID] {
			continue
		}
		plan.runIDMap[run.RunID] = generateRunID()
	}
	for i := range runs {
		run := &runs[i]
		if !included[run.RunID] {
			continue
		}
		copied := *run
		copied.ID = 0
		copied.RunID = plan.runIDMap[run.RunID]
		copied.SessionID = newSessionID
		if copied.ParentRunID != "" {
			copied.ParentRunID = plan.runIDMap[run.ParentRunID]
		}
		copied.State = coerceForkedRunState(copied.State)
		copied.CreatedAt = run.CreatedAt
		copied.UpdatedAt = now
		if copied.ParentRunID == "" {
			plan.lastRunID = copied.RunID
			lastOuterSourceRunID = run.RunID
		}
		plan.runs = append(plan.runs, copied)
	}

	// 4. 复制 message 行：message_id 重生成（唯一键）、run 重映射、created_at 保序；
	//    compact 摘要的 coveredThrough 游标按旧→新映射重写（filterCoveredReactMessages
	//    按 RunID+Seq 匹配，游标失配会让已摘要覆盖的旧消息重新进入上下文）
	for i := range messages {
		message := &messages[i]
		if !included[message.RunID] {
			continue
		}
		plan.messageIDMap[message.MessageID] = generateMessageID()
	}
	for i := range messages {
		message := &messages[i]
		if !included[message.RunID] {
			continue
		}
		copied := *message
		copied.ID = 0
		copied.MessageID = plan.messageIDMap[message.MessageID]
		copied.RunID = plan.runIDMap[message.RunID]
		copied.SessionID = newSessionID
		copied.CreatedAt = message.CreatedAt
		copied.UpdatedAt = now
		if copied.MessageType == model.ReactMessageTypeCompactSummary {
			copied.ContentJSON = rewriteForkCompactContent(message.ContentJSON, plan.runIDMap, plan.messageIDMap)
		}
		plan.messages = append(plan.messages, copied)
	}

	// 5. 新会话行：五元组照抄源会话（归属已在事务内校验），标题默认「原题 (分叉)」
	plan.session = &model.ReactSession{
		SessionID:   newSessionID,
		UserName:    source.UserName,
		CallerKey:   source.CallerKey,
		RouteValues: source.RouteValues,
		SessionType: source.SessionType,
		Title:       forkSessionTitle(source.Title, req.Title),
		LastRunID:   plan.lastRunID,
		LastMessage: forkLastMessage(messages, lastOuterSourceRunID, source.LastMessage),
		State:       model.ReactSessionStateActive,
	}
	return plan, nil
}

// isPlanReactRun 判定 Plan Executor 的步骤 run（agentPath=plan/*）：
// 主会话回放不展示，Plan 执行状态也不可跨会话复制，分叉时整体剔除。
func isPlanReactRun(run *model.ReactRun) bool {
	return strings.HasPrefix(strings.TrimSpace(run.AgentPath), "plan/")
}

// coerceForkedRunState 把非终态 run 状态收敛为 expired：事务入口已拒绝活跃 run，
// 这里是防御未来新增状态/异常数据的双保险，保证分叉会话不会被并发检查锁死。
func coerceForkedRunState(state string) string {
	switch state {
	case model.ReactRunStateRunning, model.ReactRunStateWaitingClientMessage,
		model.ReactRunStateWaitingPlan, model.ReactRunStateCancelling:
		return model.ReactRunStateExpired
	}
	return state
}

// rewriteForkCompactContent 重写 compact 摘要 coveredThrough 游标里的旧 run/message ID；
// 解析失败或无命中时原样返回（游标失配只会导致被覆盖旧消息重新进入上下文，功能无损）。
func rewriteForkCompactContent(contentJSON string, runIDMap, messageIDMap map[string]string) string {
	var content compactSummaryContent
	if err := json.Unmarshal([]byte(contentJSON), &content); err != nil {
		return contentJSON
	}
	changed := false
	for i := range content.CoveredThrough {
		if newRunID, ok := runIDMap[content.CoveredThrough[i].RunID]; ok {
			content.CoveredThrough[i].RunID = newRunID
			changed = true
		}
		if newMessageID, ok := messageIDMap[content.CoveredThrough[i].MessageID]; ok {
			content.CoveredThrough[i].MessageID = newMessageID
			changed = true
		}
	}
	if !changed {
		return contentJSON
	}
	rewritten, err := json.Marshal(content)
	if err != nil {
		return contentJSON
	}
	return string(rewritten)
}

// forkSessionTitle 计算分叉会话标题：显式传入优先，否则「源标题 (分叉)」，
// 截断口径与 buildSessionTitle 的 20 rune 保持一致。
func forkSessionTitle(sourceTitle, custom string) string {
	if v := strings.TrimSpace(custom); v != "" {
		return truncateSessionTitle(v, customSessionTitleMaxLength)
	}
	base := strings.TrimSpace(sourceTitle)
	if base == "" {
		base = buildSessionTitle("")
	}
	return truncateSessionTitle(base, maxReactSessionTitleLength) + forkSessionTitleSuffix
}

// truncateSessionTitle 按 rune 数截断标题（超出部分以 ... 结尾），保证不超列宽。
func truncateSessionTitle(title string, maxRunes int) string {
	runes := []rune(title)
	if len(runes) <= maxRunes {
		return title
	}
	return string(runes[:maxRunes]) + "..."
}

// forkLastMessage 提取最后一个被复制外层 run 的起始用户输入作为列表页预览——
// 与源会话 last_message 口径一致（run 启动时的 payload.UserPrompt，不含 run 中途
// 注入的 guide 消息），缺失时回退源会话值。
func forkLastMessage(messages []model.ReactMessage, lastOuterRunID, fallback string) string {
	if lastOuterRunID == "" {
		return fallback
	}
	for i := range messages {
		message := &messages[i]
		if message.RunID != lastOuterRunID || message.Seq != 1 ||
			message.MessageType != model.ReactMessageTypeUserInput {
			continue
		}
		var content struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(message.ContentJSON), &content); err != nil || strings.TrimSpace(content.Content) == "" {
			return fallback
		}
		return trimRunLastMessage(content.Content)
	}
	return fallback
}
