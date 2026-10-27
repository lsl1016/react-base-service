package gateway

import (
	"os"
	"strings"
	"testing"
	"time"

	"react-base-service/conf"
	"react-base-service/golib/env"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	react "react-base-service/service/react"
)

// TestCrossChannelDeliveryE2E 端到端验证 M2 跨端投递（真实 MySQL，模型不涉及——投递不依赖 run 执行）：
//
//	GATEWAY_E2E=1 go test ./service/gateway/ -run TestCrossChannelDeliveryE2E -v -timeout 120s
//
// 前置：react-base-mysql 容器（3317）已执行 init.sql 的 tblLlmUserIdentity 建表；
// conf/mount/custom.yaml 的 llm.gateway.cross_delivery=true。
// 验证点：身份绑定解析（(kind, platform_user_id) → userName）、投递目标解析
//（按 userName 反查其他渠道聊天、排除发起渠道）、投递推送文案（会话引用）、
// 投递绑定落库（投递端回复该消息即续聊原会话）。
func TestCrossChannelDeliveryE2E(t *testing.T) {
	if os.Getenv("GATEWAY_E2E") == "" {
		t.Skip("set GATEWAY_E2E=1 to run e2e (requires react-base-mysql)")
	}

	// 测试进程工作目录是包目录，配置根指回仓库根。
	env.SetRootPath("../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()
	if !conf.CustomConf.LLM.Gateway.CrossDelivery {
		t.Fatal("llm.gateway.cross_delivery 必须为 true（conf/mount/custom.yaml）")
	}

	const (
		e2eUser          = "e2e-gw-user"
		originChannelID  = "gwch_e2e_origin"
		targetChannelID  = "gwch_e2e_target"
		originChatID     = "e2e_chat_origin"
		targetChatID     = "e2e_chat_target"
		platformUserID   = "e2e-777001"
		e2eSessionID     = "session_e2edelivery0000000000000001"
	)
	e2eGinCtx := react.NewHeadlessGinContext("e2e-gateway")
	cleanupE2ERows(t, originChannelID, targetChannelID, platformUserID, e2eSessionID)
	t.Cleanup(func() { cleanupE2ERows(t, originChannelID, targetChannelID, platformUserID, e2eSessionID) })

	// 1) 身份绑定：平台用户 → 基座 userName（入站解析 + 投递寻址共用）。
	if err := model.CreateUserIdentity(e2eGinCtx, &model.UserIdentity{
		IdentityID:     "idnt_e2e_delivery",
		Kind:           model.ChannelKindTelegram,
		PlatformUserID: platformUserID,
		UserName:       e2eUser,
	}); err != nil {
		t.Fatalf("身份绑定创建失败: %v", err)
	}
	identity, err := model.GetUserIdentity(e2eGinCtx, model.ChannelKindTelegram, platformUserID)
	if err != nil || identity == nil || identity.UserName != e2eUser {
		t.Fatalf("身份绑定解析不符: %v %+v", err, identity)
	}

	// 2) 渠道会话映射：同一 userName 在两个渠道各有一个聊天（发起/接收端）。
	now := time.Now()
	for _, row := range []*model.ChannelSession{
		{ChannelID: originChannelID, ChatID: originChatID, SessionID: "session_e2e_origin", UserName: e2eUser, LastActiveAt: now},
		{ChannelID: targetChannelID, ChatID: targetChatID, SessionID: "session_e2e_target", UserName: e2eUser, LastActiveAt: now},
	} {
		if err := model.BindChannelSession(e2eGinCtx, row); err != nil {
			t.Fatalf("会话映射创建失败: %v", err)
		}
	}

	// 3) 投递目标解析：按 userName 反查、排除发起渠道。
	targets, err := model.ListChannelSessionsByUserName(e2eGinCtx, e2eUser, originChannelID, 10)
	if err != nil {
		t.Fatalf("投递目标解析失败: %v", err)
	}
	if len(targets) != 1 || targets[0].ChannelID != targetChannelID || targets[0].ChatID != targetChatID {
		t.Fatalf("投递目标应只剩接收渠道: %+v", targets)
	}

	// 4) 跨端投递执行（真实绑定落库；推送走 fake 渠道——投递逻辑与平台传输解耦）。
	origin := &fakeChannelWithID{fakeChannel: fakeChannel{}, id: originChannelID}
	target := &fakeChannelWithID{fakeChannel: fakeChannel{}, id: targetChannelID}
	gw := &service{adapters: map[string]*imAdapter{}, resolveDeliveryTargets: defaultResolveDeliveryTargets}
	gw.manager = newManager(nil)
	gw.manager.channels[originChannelID] = origin
	gw.manager.channels[targetChannelID] = target

	a := newIMAdapter(gw, originChannelID, originChatID, "m1", e2eSessionID, e2eUser, func() {})
	a.setReplayInfo("demo-app", `[]`)
	gw.crossDeliver(a, "e2e 跨端投递结果正文")

	if target.textCount() != 1 || origin.textCount() != 0 {
		t.Fatalf("投递扇出不符: target=%d origin=%d", target.textCount(), origin.textCount())
	}
	if got := target.lastText(); !strings.Contains(got, "e2e 跨端投递结果正文") || !strings.Contains(got, e2eSessionID[:16]) {
		t.Fatalf("投递文案应含结果与会话引用: %q", got)
	}

	// 5) 投递绑定落库：接收端回复该消息即续聊原会话（跨端续聊入口）。
	binding, err := model.GetChannelMessageByMessageID(e2eGinCtx, targetChannelID, target.lastID)
	if err != nil {
		t.Fatalf("投递绑定查询失败: %v", err)
	}
	if binding == nil || binding.SessionID != e2eSessionID {
		t.Fatalf("投递绑定应指向原会话: %+v", binding)
	}
}

// cleanupE2ERows 清理 e2e 造的数据（会话映射/消息绑定/身份绑定）。
func cleanupE2ERows(t *testing.T, originChannelID, targetChannelID, platformUserID, sessionID string) {
	t.Helper()
	db := helpers.MysqlClientLLM
	if db == nil {
		return
	}
	_ = db.Where("channel_id IN ?", []string{originChannelID, targetChannelID}).Delete(&model.ChannelSession{}).Error
	_ = db.Where("channel_id IN ?", []string{originChannelID, targetChannelID}).Delete(&model.ChannelMessage{}).Error
	_ = db.Where("platform_user_id = ?", platformUserID).Delete(&model.UserIdentity{}).Error
	_ = db.Where("session_id = ?", sessionID).Delete(&model.ChannelMessage{}).Error
}
