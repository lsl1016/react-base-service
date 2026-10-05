package react

import (
	"os"
	"strings"
	"testing"
	"time"

	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/env"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// forkFixture 构造一个五 run 会话的标准夹具（时间顺序）：
//
//	run_src_1（外层，finished，含 1 条 compact 摘要覆盖 seq1-2）
//	run_src_2（外层，finished）
//	run_src_sub2（run_src_2 的 delegate 子 run，cancelled）
//	run_src_plan（plan/x 步骤 run，外层形态，finished）
//	run_src_3（外层，finished）
func forkFixture() (*model.ReactSession, []model.ReactRun, []model.ReactMessage) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	source := &model.ReactSession{
		SessionID: "session_src", UserName: "u1", CallerKey: "c1",
		RouteValues: `["r1"]`, SessionType: "chat", Title: "连接池排查", State: model.ReactSessionStateActive,
	}
	mkRun := func(id string, offset int, state, parent, agentPath string) model.ReactRun {
		return model.ReactRun{
			RunID: id, SessionID: source.SessionID, UserName: "u1", CallerKey: "c1",
			RouteValues: source.RouteValues, State: state, MaxSteps: 10,
			ParentRunID: parent, AgentPath: agentPath, CreatedAt: base.Add(time.Duration(offset) * time.Minute),
		}
	}
	runs := []model.ReactRun{
		mkRun("run_src_1", 0, model.ReactRunStateFinished, "", ""),
		mkRun("run_src_2", 10, model.ReactRunStateFinished, "", ""),
		mkRun("run_src_sub2", 11, model.ReactRunStateCancelled, "run_src_2", "main/ops-agent"),
		mkRun("run_src_plan", 12, model.ReactRunStateFinished, "", "plan/p-1"),
		mkRun("run_src_3", 20, model.ReactRunStateFinished, "", ""),
	}
	mkMsg := func(id, runID string, offset int, seq int, role, msgType, content string) model.ReactMessage {
		return model.ReactMessage{
			MessageID: id, RunID: runID, SessionID: source.SessionID, UserName: "u1", CallerKey: "c1",
			Seq: seq, StepIndex: 0, Role: role, MessageType: msgType, ContentJSON: content,
			CreatedAt: base.Add(time.Duration(offset)*time.Minute + time.Duration(seq)*time.Second),
		}
	}
	messages := []model.ReactMessage{
		mkMsg("msg_1_1", "run_src_1", 0, 1, "user", model.ReactMessageTypeUserInput, `{"content":"第一个问题","modelMessage":{}}`),
		mkMsg("msg_1_2", "run_src_1", 0, 2, "assistant", model.ReactMessageTypeAssistant, `{"modelMessage":{}}`),
		mkMsg("msg_1_3", "run_src_1", 1, 3, "user", model.ReactMessageTypeCompactSummary, `{"summary":"早期摘要","coveredThrough":[{"runId":"run_src_1","messageId":"msg_1_1","seq":2}]}`),
		mkMsg("msg_1_4", "run_src_1", 1, 4, "assistant", model.ReactMessageTypeAssistant, `{"modelMessage":{}}`),
		mkMsg("msg_2_1", "run_src_2", 10, 1, "user", model.ReactMessageTypeUserInput, `{"content":"第二个问题","modelMessage":{}}`),
		mkMsg("msg_2_2", "run_src_2", 10, 2, "assistant", model.ReactMessageTypeAssistant, `{"modelMessage":{}}`),
		mkMsg("msg_2_3", "run_src_2", 10, 3, "user", model.ReactMessageTypeUserInput, `{"content":"引导补充","modelMessage":{}}`),
		mkMsg("msg_sub_1", "run_src_sub2", 11, 1, "assistant", model.ReactMessageTypeAssistant, `{"modelMessage":{}}`),
		mkMsg("msg_plan_1", "run_src_plan", 12, 1, "assistant", model.ReactMessageTypeAssistant, `{"modelMessage":{}}`),
		mkMsg("msg_3_1", "run_src_3", 20, 1, "user", model.ReactMessageTypeUserInput, `{"content":"第三个问题","modelMessage":{}}`),
	}
	return source, runs, messages
}

func TestResolveForkCut(t *testing.T) {
	_, runs, messages := forkFixture()

	cases := []struct {
		name        string
		messageID   string
		wantAnchor  string
		wantInclude bool
		wantErr     string
	}{
		{name: "命中 run 起始用户输入 → exclusive", messageID: "msg_2_1", wantAnchor: "run_src_2", wantInclude: false},
		{name: "命中 assistant 回复 → inclusive", messageID: "msg_2_2", wantAnchor: "run_src_2", wantInclude: true},
		{name: "命中 guide 用户消息(seq>1) → inclusive", messageID: "msg_2_3", wantAnchor: "run_src_2", wantInclude: true},
		{name: "命中 compact 摘要 → inclusive", messageID: "msg_1_3", wantAnchor: "run_src_1", wantInclude: true},
		{name: "命中 delegate 子 run 消息 → 上溯外层 inclusive", messageID: "msg_sub_1", wantAnchor: "run_src_2", wantInclude: true},
		{name: "命中 plan run 消息 → 拒绝", messageID: "msg_plan_1", wantErr: "Plan"},
		{name: "消息不存在 → 拒绝", messageID: "msg_other_session", wantErr: "不存在"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cut, err := resolveForkCut(runs, messages, tc.messageID)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("期望错误含 %q, got err=%v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveForkCut 失败: %v", err)
			}
			if cut.anchorRun.RunID != tc.wantAnchor || cut.inclusive != tc.wantInclude {
				t.Fatalf("anchor=%s inclusive=%v, want anchor=%s inclusive=%v",
					cut.anchorRun.RunID, cut.inclusive, tc.wantAnchor, tc.wantInclude)
			}
		})
	}
}

func TestBuildForkCopyPlanInclusive(t *testing.T) {
	source, runs, messages := forkFixture()
	cut, err := resolveForkCut(runs, messages, "msg_2_2")
	if err != nil {
		t.Fatalf("resolveForkCut: %v", err)
	}
	plan, err := buildForkCopyPlan(source, params.ReactSessionForkReq{}, runs, messages, cut)
	if err != nil {
		t.Fatalf("buildForkCopyPlan: %v", err)
	}

	// 复制范围：run1 + run2 + sub2；plan run 与 run3 剔除
	wantIncluded := map[string]bool{"run_src_1": true, "run_src_2": true, "run_src_sub2": true}
	if len(plan.runIDMap) != len(wantIncluded) {
		t.Fatalf("runIDMap=%v, want %v", plan.runIDMap, wantIncluded)
	}
	for oldID := range wantIncluded {
		if plan.runIDMap[oldID] == "" || plan.runIDMap[oldID] == oldID {
			t.Fatalf("run %s 未重生成新 ID: %v", oldID, plan.runIDMap[oldID])
		}
	}
	// 消息复制范围：run1(4) + run2(3) + sub2(1) = 8
	if len(plan.messages) != 8 || len(plan.runs) != 3 {
		t.Fatalf("复制规模 runs=%d messages=%d, want 3/8", len(plan.runs), len(plan.messages))
	}
	newRunIDs := make(map[string]bool, len(plan.runIDMap))
	for _, newID := range plan.runIDMap {
		newRunIDs[newID] = true
	}
	for _, copied := range plan.messages {
		if copied.SessionID != plan.session.SessionID {
			t.Fatalf("消息未指向新会话: %+v", copied)
		}
		if !newRunIDs[copied.RunID] {
			t.Fatalf("消息 run 未重映射到新 ID: %+v", copied)
		}
		if copied.MessageID == "" || plan.messageIDMap[copied.MessageID] != "" {
			t.Fatalf("消息 ID 未重生成: %+v", copied)
		}
	}

	// 子 run 父子关系重写 + created_at 保序 + 快照列保留
	for _, copied := range plan.runs {
		if copied.SessionID != plan.session.SessionID {
			t.Fatalf("run 未指向新会话")
		}
		switch copied.RunID {
		case plan.runIDMap["run_src_sub2"]:
			if copied.ParentRunID != plan.runIDMap["run_src_2"] {
				t.Fatalf("子 run 父 ID 未重写: %s", copied.ParentRunID)
			}
			if copied.State != model.ReactRunStateCancelled {
				t.Fatalf("终态被误改: %s", copied.State)
			}
		case plan.runIDMap["run_src_1"]:
			if !copied.CreatedAt.Equal(runs[0].CreatedAt) {
				t.Fatalf("created_at 未保序: %v", copied.CreatedAt)
			}
		}
	}
	// last_run_id = 最后一个外层 run（run2）的新 ID
	if plan.lastRunID != plan.runIDMap["run_src_2"] {
		t.Fatalf("lastRunID=%s, want run2 新 ID", plan.lastRunID)
	}
	// 会话行：标题后缀 + 预览取复制范围内最后一条用户输入
	if !strings.HasSuffix(plan.session.Title, forkSessionTitleSuffix) || !strings.HasPrefix(plan.session.Title, "连接池排查") {
		t.Fatalf("标题=%q", plan.session.Title)
	}
	if plan.session.LastMessage != "第二个问题" {
		t.Fatalf("LastMessage=%q, want 第二个问题", plan.session.LastMessage)
	}
	if plan.session.State != model.ReactSessionStateActive {
		t.Fatalf("新会话状态=%s", plan.session.State)
	}

	// compact 游标重写：runId/messageId 换新、seq 不变；重写后过滤语义成立
	var compact *model.ReactMessage
	for i := range plan.messages {
		if plan.messages[i].MessageType == model.ReactMessageTypeCompactSummary {
			compact = &plan.messages[i]
		}
	}
	if compact == nil {
		t.Fatalf("compact 摘要未被复制")
	}
	refs := parseCompactCoveredThrough(compact.ContentJSON)
	if len(refs) != 1 || refs[0].RunID != plan.runIDMap["run_src_1"] ||
		refs[0].MessageID != plan.messageIDMap["msg_1_1"] || refs[0].Seq != 2 {
		t.Fatalf("coveredThrough 未正确重写: %+v", refs)
	}
	covered := 0
	for _, copied := range plan.messages {
		if copied.RunID == plan.runIDMap["run_src_1"] && copied.Seq <= 2 {
			covered++
		}
	}
	if covered != 2 {
		t.Fatalf("被游标覆盖的消息数=%d, want 2", covered)
	}
}

func TestBuildForkCopyPlanExclusive(t *testing.T) {
	source, runs, messages := forkFixture()
	cut, err := resolveForkCut(runs, messages, "msg_2_1")
	if err != nil {
		t.Fatalf("resolveForkCut: %v", err)
	}
	plan, err := buildForkCopyPlan(source, params.ReactSessionForkReq{}, runs, messages, cut)
	if err != nil {
		t.Fatalf("buildForkCopyPlan: %v", err)
	}
	// exclusive：只保留 run1（4 条消息），sub2 随 run2 一起丢弃
	if len(plan.runs) != 1 || len(plan.messages) != 4 || plan.runIDMap["run_src_2"] != "" {
		t.Fatalf("exclusive 复制范围错误: runs=%d messages=%d", len(plan.runs), len(plan.messages))
	}
	if plan.lastRunID != plan.runIDMap["run_src_1"] || plan.session.LastMessage != "第一个问题" {
		t.Fatalf("lastRun/LastMessage=%+v/%q", plan.lastRunID, plan.session.LastMessage)
	}

	// 截断点在首个 run 之前 → 无历史可复制，拒绝
	cutFirst, err := resolveForkCut(runs, messages, "msg_1_1")
	if err != nil {
		t.Fatalf("resolveForkCut: %v", err)
	}
	if _, err := buildForkCopyPlan(source, params.ReactSessionForkReq{}, runs, messages, cutFirst); err == nil ||
		!strings.Contains(err.Error(), "无需分叉") {
		t.Fatalf("首个 run 前 fork 应拒绝, got err=%v", err)
	}
}

func TestForkHelpers(t *testing.T) {
	// 非终态收敛（双保险）：active 集合 → expired，终态原样
	for _, state := range []string{model.ReactRunStateRunning, model.ReactRunStateWaitingClientMessage,
		model.ReactRunStateWaitingPlan, model.ReactRunStateCancelling} {
		if got := coerceForkedRunState(state); got != model.ReactRunStateExpired {
			t.Fatalf("state=%s → %s, want expired", state, got)
		}
	}
	if got := coerceForkedRunState(model.ReactRunStateFinished); got != model.ReactRunStateFinished {
		t.Fatalf("终态被误改: %s", got)
	}

	// 标题：显式传入优先；源标题为空回退「新对话」；超长截断
	if got := forkSessionTitle("原标题", "自定义"); got != "自定义" {
		t.Fatalf("自定义标题=%q", got)
	}
	if got := forkSessionTitle("", ""); got != "新对话"+forkSessionTitleSuffix {
		t.Fatalf("空标题回退=%q", got)
	}
	if got := forkSessionTitle(strings.Repeat("长", 30), ""); !strings.HasPrefix(got, strings.Repeat("长", maxReactSessionTitleLength)) ||
		!strings.Contains(got, "...") || !strings.HasSuffix(got, forkSessionTitleSuffix) {
		t.Fatalf("超长标题未截断: %q", got)
	}

	// compact 重写：解析失败原样返回；未命中映射的 ref 不动
	broken := `{not-json`
	if got := rewriteForkCompactContent(broken, map[string]string{}, map[string]string{}); got != broken {
		t.Fatalf("解析失败应原样返回: %q", got)
	}
	if got := rewriteForkCompactContent(`{"summary":"s","coveredThrough":[{"runId":"run_x","seq":3}]}`,
		map[string]string{"run_y": "run_new"}, map[string]string{}); !strings.Contains(got, `"runId":"run_x"`) {
		t.Fatalf("未命中映射不应改动: %q", got)
	}
}

// TestForkReactSessionE2E 端到端验证会话分叉（真实 MySQL）：
//
//	REACT_SESSION_FORK_E2E=1 go test ./service/react/ -run TestForkReactSessionE2E -v
//
// 验证点：
//  1. 活跃 run 拒绝；归属不匹配拒绝；跨会话分叉点拒绝；首个 run 前 fork 拒绝；
//  2. 正常分叉（inclusive）：新会话 run/message 规模、外层历史装配、compact 游标重写后过滤语义成立；
//  3. 独立性：删除源会话后分叉会话数据完好。
func TestForkReactSessionE2E(t *testing.T) {
	if os.Getenv("REACT_SESSION_FORK_E2E") == "" {
		t.Skip("set REACT_SESSION_FORK_E2E=1 to run e2e (requires react-base-mysql)")
	}

	env.SetRootPath("../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()

	ctx := newHeadlessGinContext("u1")
	seedAndCleanup := func(t *testing.T) {
		tables := []interface{}{&model.ReactSession{}, &model.ReactRun{}, &model.ReactMessage{}}
		for _, dest := range tables {
			if err := model.GetLLMDB().WithContext(ctx).Where("session_id = ?", "session_e2e_fork_src").Delete(dest).Error; err != nil {
				t.Fatalf("清理失败: %v", err)
			}
		}
		source, runs, messages := forkFixture()
		source.SessionID = "session_e2e_fork_src"
		for i := range runs {
			runs[i].SessionID = source.SessionID
		}
		for i := range messages {
			messages[i].SessionID = source.SessionID
		}
		if err := model.CreateReactSession(ctx, source); err != nil {
			t.Fatalf("建 session 失败: %v", err)
		}
		if err := model.BatchCreateReactRunsWithDB(ctx, model.GetLLMDB(), runs); err != nil {
			t.Fatalf("建 runs 失败: %v", err)
		}
		if err := model.BatchCreateReactMessagesWithDB(ctx, model.GetLLMDB(), messages); err != nil {
			t.Fatalf("建 messages 失败: %v", err)
		}
	}
	cleanupForked := func(t *testing.T) {
		for _, dest := range []interface{}{&model.ReactSession{}, &model.ReactRun{}, &model.ReactMessage{}} {
			if err := model.GetLLMDB().WithContext(ctx).
				Where("session_id LIKE ?", "session_e2e_fork_new%").Delete(dest).Error; err != nil {
				t.Fatalf("清理分叉会话失败: %v", err)
			}
		}
	}
	cleanupAll := func(t *testing.T) { cleanupForked(t); seedAndCleanup(t) }
	cleanupAll(t)
	t.Cleanup(func() { cleanupAll(t) })

	baseReq := params.ReactSessionForkReq{
		SessionID:        "session_e2e_fork_src",
		ThroughMessageID: "msg_2_2",
		CallerKey:        "c1",
		RouteValues:      []string{"r1"},
	}

	// 1. 活跃 run 拒绝
	if err := model.UpdateReactRunByRunID(ctx, "run_src_3", map[string]any{"state": model.ReactRunStateRunning}); err != nil {
		t.Fatalf("置活跃失败: %v", err)
	}
	if _, err := ForkReactSession(ctx, baseReq); err == nil || !strings.Contains(err.Error(), "正在运行") {
		t.Fatalf("活跃 run 应拒绝分叉, got err=%v", err)
	}
	if err := model.UpdateReactRunByRunID(ctx, "run_src_3", map[string]any{"state": model.ReactRunStateFinished}); err != nil {
		t.Fatalf("恢复终态失败: %v", err)
	}

	// 2. 归属不匹配拒绝 / 跨会话分叉点拒绝 / 首个 run 前 fork 拒绝
	wrongReq := baseReq
	wrongReq.CallerKey = "other-caller"
	if _, err := ForkReactSession(ctx, wrongReq); err == nil {
		t.Fatalf("归属不匹配应拒绝")
	}
	foreignReq := baseReq
	foreignReq.ThroughMessageID = "msg_elsewhere"
	if _, err := ForkReactSession(ctx, foreignReq); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("跨会话分叉点应拒绝, got err=%v", err)
	}
	firstReq := baseReq
	firstReq.ThroughMessageID = "msg_1_1"
	if _, err := ForkReactSession(ctx, firstReq); err == nil || !strings.Contains(err.Error(), "无需分叉") {
		t.Fatalf("首个 run 前 fork 应拒绝, got err=%v", err)
	}

	// 3. 正常分叉（inclusive：run1+run2+sub2）
	resp, err := ForkReactSession(ctx, baseReq)
	if err != nil {
		t.Fatalf("分叉失败: %v", err)
	}
	if resp.SessionID == "" || resp.SessionID == baseReq.SessionID || resp.Runs != 3 || resp.Messages != 8 ||
		resp.CutRunID != "run_src_2" || !resp.Inclusive {
		t.Fatalf("分叉响应不符: %+v", resp)
	}
	forkedRuns, err := model.GetReactRunsBySessionID(ctx, resp.SessionID)
	if err != nil || len(forkedRuns) != 3 {
		t.Fatalf("分叉会话 runs=%d err=%v, want 3", len(forkedRuns), err)
	}
	for _, run := range forkedRuns {
		if run.SessionID != resp.SessionID || run.State == model.ReactRunStateRunning {
			t.Fatalf("分叉 run 异常: %+v", run)
		}
	}
	outerMessages, err := model.GetOuterReactMessagesBySessionIDWithDB(ctx, model.GetLLMDB(), resp.SessionID)
	if err != nil || len(outerMessages) != 7 {
		t.Fatalf("外层历史装配=%d err=%v, want 7（run1 4 条 + run2 3 条）", len(outerMessages), err)
	}
	// compact 游标重写后过滤语义成立：外层 7 条中被覆盖 2 条（run1 seq1-2）
	if filtered := filterCoveredReactMessages(outerMessages); len(filtered) != 5 {
		t.Fatalf("compact 过滤后=%d, want 5", len(filtered))
	}
	forked, err := model.GetReactSessionBySessionID(ctx, resp.SessionID)
	if err != nil || forked == nil {
		t.Fatalf("分叉会话不存在: %v", err)
	}
	if forked.Title == "" || forked.LastMessage != "第二个问题" {
		t.Fatalf("分叉会话标题/预览异常: %+v", forked)
	}

	// 4. 独立性：删除源会话后分叉会话完好
	if _, err := DeleteReactSession(ctx, params.ReactSessionDeleteReq{
		SessionID: baseReq.SessionID, CallerKey: "c1", RouteValues: []string{"r1"},
	}); err != nil {
		t.Fatalf("删除源会话失败: %v", err)
	}
	if again, err := model.GetOuterReactMessagesBySessionIDWithDB(ctx, model.GetLLMDB(), resp.SessionID); err != nil || len(again) != 7 {
		t.Fatalf("删除源会话后分叉历史受损: %d err=%v", len(again), err)
	}
	_ = gin.Mode()
}
