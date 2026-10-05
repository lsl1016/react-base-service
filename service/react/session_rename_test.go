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

// TestRenameReactSessionE2E 端到端验证会话重命名（真实 MySQL）：
//
//	REACT_SESSION_RENAME_E2E=1 go test ./service/react/ -run TestRenameReactSessionE2E -v
//
// 验证点：
//  1. 归属不匹配 / 会话不存在 / 空白标题拒绝；
//  2. 有活跃 run 的会话仍可重命名（元数据操作不做级联，与 delete/fork 不同）；
//  3. 正常重命名：标题落库且归一化（去空白 + rune 截断）；幂等重命名不报错。
func TestRenameReactSessionE2E(t *testing.T) {
	if os.Getenv("REACT_SESSION_RENAME_E2E") == "" {
		t.Skip("set REACT_SESSION_RENAME_E2E=1 to run e2e (requires react-base-mysql)")
	}

	env.SetRootPath("../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()

	const (
		userName  = "e2e-rename-user"
		callerKey = "e2e-rename-caller"
		sessionID = "session_e2e_rename_test"
	)
	ctx := newHeadlessGinContext(userName)

	seed := func(withActiveRun bool) {
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactRun{}).Error
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactSession{}).Error
		session := &model.ReactSession{
			SessionID: sessionID, UserName: userName, CallerKey: callerKey,
			RouteValues: `["r1"]`, SessionType: "chat", Title: "旧标题",
			State: model.ReactSessionStateActive,
		}
		if err := model.CreateReactSession(ctx, session); err != nil {
			t.Fatalf("建 session 失败: %v", err)
		}
		if withActiveRun {
			run := &model.ReactRun{
				RunID: "run_e2e_rename_1", SessionID: sessionID, UserName: userName, CallerKey: callerKey,
				RouteValues: `["r1"]`, State: model.ReactRunStateRunning, MaxSteps: 10,
			}
			if err := model.GetLLMDB().WithContext(ctx).Create(run).Error; err != nil {
				t.Fatalf("建活跃 run 失败: %v", err)
			}
		}
	}
	cleanup := func() {
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactRun{}).Error
		_ = model.GetLLMDB().WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.ReactSession{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	baseReq := params.ReactSessionRenameReq{
		SessionID:   sessionID,
		Title:       "新标题",
		CallerKey:   callerKey,
		RouteValues: []string{"r1"},
	}

	// 1. 归属不匹配 / 会话不存在 / 空白标题拒绝
	seed(false)
	wrongReq := baseReq
	wrongReq.CallerKey = "other-caller"
	if _, err := RenameReactSession(ctx, wrongReq); err == nil {
		t.Fatalf("归属不匹配应拒绝重命名")
	}
	missingReq := baseReq
	missingReq.SessionID = "session_e2e_rename_missing"
	if _, err := RenameReactSession(ctx, missingReq); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("会话不存在应明确报错, got err=%v", err)
	}
	blankReq := baseReq
	blankReq.Title = "   "
	if _, err := RenameReactSession(ctx, blankReq); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("空白标题应拒绝, got err=%v", err)
	}

	// 2. 活跃 run 不拦截重命名
	seed(true)
	resp, err := RenameReactSession(ctx, baseReq)
	if err != nil {
		t.Fatalf("活跃 run 不应拦截重命名: %v", err)
	}
	if !resp.Renamed || resp.Title != "新标题" {
		t.Fatalf("重命名响应不符: %+v", resp)
	}

	// 3. 正常重命名：标题落库且归一化（去空白 + rune 截断）
	longReq := baseReq
	longReq.Title = "  " + strings.Repeat("标", customSessionTitleMaxLength+10) + "  "
	if _, err := RenameReactSession(ctx, longReq); err != nil {
		t.Fatalf("超长标题重命名失败: %v", err)
	}
	renamed, err := model.GetReactSessionBySessionID(ctx, sessionID)
	if err != nil || renamed == nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	want := strings.Repeat("标", customSessionTitleMaxLength) + "..."
	if renamed.Title != want {
		t.Fatalf("标题归一化不符: %q, want %q", renamed.Title, want)
	}

	// 4. 幂等：同标题重复重命名成功
	if resp, err := RenameReactSession(ctx, longReq); err != nil || !resp.Renamed || resp.Title != want {
		t.Fatalf("幂等重命名失败: resp=%+v err=%v", resp, err)
	}
	_ = gin.Mode()
}
