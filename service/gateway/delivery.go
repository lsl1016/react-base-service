package gateway

import (
	"context"
	"net/url"
	"strings"
	"time"

	"react-base-service/conf"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"

	"react-base-service/golib/zlog"
)

// 跨端投递（M2，见 docs/多渠道接入网关与跨端续聊实现方案.md §5.1 delivery.go）：
// run 终态在发起渠道推送之外，把结果投给同一 userName 绑定的其他渠道聊天
//（「在 Telegram 发起、飞书收到结果」）。投递消息同样写入消息绑定表——
// 在投递端回复该消息即续聊原会话，这是跨端续聊的主要入口。
//
// 决策对齐方案 D2/D5：发起端负责对话过程（HITL 只回发起 chat）；投递端只收终态，
// 不扇出过程事件；投递挂在 imAdapter.pushTerminal 之后，不进 react 核心。
// 寻址键 = ChannelSession.user_name（入站经身份绑定表解析；未绑定渠道为 M1 默认映射）。

// defaultResolveDeliveryTargets 默认投递目标解析：查库取该 userName 绑定的其他渠道聊天。
func defaultResolveDeliveryTargets(ctx *gin.Context, userName, excludeChannelID string) ([]model.ChannelSession, error) {
	return model.ListChannelSessionsByUserName(ctx, userName, excludeChannelID, 10)
}

// recordCrossDeliveryBinding 投递消息绑定写入（默认落库；测试注入替身）。
var recordCrossDeliveryBinding = func(row *model.ChannelMessage) error {
	return model.RecordChannelMessage(newGatewayGinContext(), row)
}

// crossDeliver 跨端投递：结果 + 会话引用（回放链接）推给其他渠道聊天并写绑定。
// 由 pushTerminal 在 goroutine 中调用（不阻塞 run 事件流）；单目标失败只记日志继续。
func (s *service) crossDeliver(origin *imAdapter, text string) {
	if origin == nil || strings.TrimSpace(text) == "" {
		return
	}
	if !conf.CustomConf.LLM.Gateway.CrossDelivery {
		return
	}
	ginCtx := newGatewayGinContext()
	targets, err := s.resolveDeliveryTargets(ginCtx, origin.userName, origin.channelID)
	if err != nil {
		zlog.Errorf(ginCtx, "[gateway] 跨端投递目标解析失败: user=%s origin=%s err=%v", origin.userName, origin.channelID, err)
		return
	}
	if len(targets) == 0 {
		return
	}
	originKind := ""
	if ch := s.manager.Get(origin.channelID); ch != nil {
		originKind = ch.Kind()
	}
	message := buildDeliveryMessage(originKind, text, origin.sessionID, origin.appKey, origin.routeValues)
	for _, target := range targets {
		if target.ChannelID == origin.channelID {
			continue
		}
		ch := s.manager.Get(target.ChannelID)
		if ch == nil {
			// 渠道未启用/未运行：跳过（配置仍在，下次渠道拉起后可投）。
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		messageID, err := s.pushBounded(ctx, PushAddr{ChannelID: target.ChannelID, ChatID: target.ChatID}, message)
		cancel()
		if err != nil || messageID == "" {
			zlog.Errorf(ginCtx, "[gateway] 跨端投递失败: user=%s target=%s/%s err=%v", origin.userName, target.ChannelID, target.ChatID, err)
			continue
		}
		if err := recordCrossDeliveryBinding(&model.ChannelMessage{
			ChannelID: target.ChannelID,
			ChatID:    target.ChatID,
			MessageID: messageID,
			SessionID: origin.sessionID,
			RunID:     origin.runID,
		}); err != nil {
			zlog.Errorf(ginCtx, "[gateway] 跨端投递绑定写入失败: target=%s message=%s err=%v", target.ChannelID, messageID, err)
		}
		zlog.Infof(ginCtx, "[gateway] 跨端投递完成: session=%s origin=%s target=%s/%s message=%s",
			origin.sessionID, origin.channelID, target.ChannelID, target.ChatID, messageID)
	}
}

// channelKindLabels 投递文案里的渠道展示名（未知 kind 原样展示）。
var channelKindLabels = map[string]string{
	model.ChannelKindFeishu:   "飞书",
	model.ChannelKindTelegram: "Telegram",
}

// buildDeliveryMessage 组装跨端投递文案：结果 + 会话引用 + 回放链接 + 回复续聊指引。
// replay_base_url 未配置时不附链接（仅会话短 ID）；链接指向既有回放页 replay.html
//（URL 参数 sessionId/appKey/routeValues 原样对齐 replay.js 解析）。
func buildDeliveryMessage(originKind, text, sessionID, appKey, routeValues string) string {
	label := originKind
	if v, ok := channelKindLabels[originKind]; ok {
		label = v
	}
	var sb strings.Builder
	sb.WriteString("📡 跨端投递（来自 " + label + " 的任务结果）\n\n")
	sb.WriteString(strings.TrimSpace(text))
	sb.WriteString("\n\n—\n会话：" + shortSessionID(sessionID))
	if base := strings.TrimSpace(conf.CustomConf.LLM.Gateway.ReplayBaseURL); base != "" && strings.TrimSpace(appKey) != "" {
		link := strings.TrimRight(base, "?") +
			"?sessionId=" + url.QueryEscape(sessionID) +
			"&appKey=" + url.QueryEscape(appKey) +
			"&routeValues=" + url.QueryEscape(normalizeRouteValuesJSON(routeValues))
		sb.WriteString("\n回放：" + link)
	}
	sb.WriteString("\n💬 直接回复本消息即可在此端续聊该会话")
	return sb.String()
}
