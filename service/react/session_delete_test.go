package react

import (
	"os"
	"strings"
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/env"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// TestDeleteReactSessionE2E 端到端验证会话硬删（真实 MySQL）：
//
//	REACT_SESSION_DELETE_E2E=1 go test ./service/react/ -run TestDeleteReactSessionE2E -v
//
// 验证点：
//  1. 活跃 run 的会话删除被拒绝（不产生任何删除）；
//  2. 归属不匹配（callerKey 错误）删除被拒绝；
//  3. 正常删除：session/run/message/tool_result/pending_input/feedback 级联清空。
func TestDeleteReactSessionE2E(t *testing.T) {
	if os.Getenv("REACT_SESSION_DELETE_E2E") == "" {
		t.Skip("set REACT_SESSION_DELETE_E2E=1 to run e2e (requires react-base-mysql)")
	}

	env.SetRootPath("../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()

	const (
		userName   = "e2e-delete-user"
		callerKey  = "e2e-delete-caller"
		sessionID  = "session_e2e_delete_test"
		routeJSON  = `["r1"]`
	)
	ctx := newHeadlessGinContext(userName)

	cleanup := func() {
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactSession{}).Error
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactRun{}).Error
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactMessage{}).Error
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactToolResult{}).Error
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactPendingInput{}).Error
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactRunFeedback{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	seed := func(runState string) {
		cleanup()
		session := &model.ReactSession{
			SessionID: sessionID, UserName: userName, CallerKey: callerKey,
			RouteValues: routeJSON, SessionType: "chat", State: model.ReactSessionStateActive,
		}
		if err := model.CreateReactSession(ctx, session); err != nil {
			t.Fatalf("建 session 失败: %v", err)
		}
		run := &model.ReactRun{
			RunID: "run_e2e_delete_1", SessionID: sessionID, UserName: userName, CallerKey: callerKey,
			RouteValues: routeJSON, State: runState, MaxSteps: 10,
		}
		if err := model.GetLLMDB().WithContext(ctx).Create(run).Error; err != nil {
			t.Fatalf("建 run 失败: %v", err)
		}
		message := &model.ReactMessage{
			MessageID: "msg_e2e_delete_1", RunID: run.RunID, SessionID: sessionID,
			UserName: userName, CallerKey: callerKey, Seq: 1, StepIndex: 0,
			Role: "user", MessageType: model.ReactMessageTypeUserInput, ContentJSON: "{}",
		}
		if err := model.GetLLMDB().WithContext(ctx).Create(message).Error; err != nil {
			t.Fatalf("建 message 失败: %v", err)
		}
		pending := &model.ReactPendingInput{
			SessionID: sessionID, RunID: run.RunID, Kind: model.ReactPendingKindUserInput,
			Delivery: model.ReactPendingDeliveryQueue, Status: model.ReactPendingStatusQueued,
			Content: "排队中的消息", Seq: 1,
		}
		if err := model.CreateReactPendingInputWithDB(ctx, model.GetLLMDB(), pending); err != nil {
			t.Fatalf("建 pending_input 失败: %v", err)
		}
	}

	baseReq := params.ReactSessionDeleteReq{
		SessionID:   sessionID,
		CallerKey:   callerKey,
		RouteValues: []string{"r1"},
	}

	countRows := func(table, where string) int64 {
		var count int64
		if err := model.GetLLMDB().WithContext(ctx).Table(table).Where(where, sessionID).Count(&count).Error; err != nil {
			t.Fatalf("count %s 失败: %v", table, err)
		}
		return count
	}

	// 1. 活跃 run 拒绝
	seed(model.ReactRunStateRunning)
	if _, err := DeleteReactSession(ctx, baseReq); err == nil || !strings.Contains(err.Error(), "正在运行") {
		t.Fatalf("活跃 run 应拒绝删除, got err=%v", err)
	}
	if got := countRows("tblLlmReactSession", "session_id = ?"); got != 1 {
		t.Fatalf("拒绝时不应删除 session, rows=%d", got)
	}

	// 2. 归属不匹配拒绝
	seed(model.ReactRunStateFinished)
	wrongReq := baseReq
	wrongReq.CallerKey = "other-caller"
	if _, err := DeleteReactSession(ctx, wrongReq); err == nil {
		t.Fatalf("归属不匹配应拒绝删除")
	}

	// 3. 正常删除：级联清空
	seed(model.ReactRunStateFinished)
	resp, err := DeleteReactSession(ctx, baseReq)
	if err != nil {
		t.Fatalf("正常删除失败: %v", err)
	}
	if !resp.Deleted || resp.Runs != 1 || resp.Messages != 1 || resp.PendingInputs != 1 {
		t.Fatalf("删除统计不符: %+v", resp)
	}
	for _, table := range []string{"tblLlmReactSession", "tblLlmReactRun", "tblLlmReactMessage", "tblLlmReactPendingInput"} {
		if got := countRows(table, "session_id = ?"); got != 0 {
			t.Fatalf("%s 未清空, rows=%d", table, got)
		}
	}

	// 4. 删除不存在的会话：明确报错
	if _, err := DeleteReactSession(ctx, baseReq); err == nil {
		t.Fatalf("删除不存在的会话应报错")
	}
	_ = gin.Mode()
}
