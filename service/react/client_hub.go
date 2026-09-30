package react

import (
	"encoding/json"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	hub "react-base-service/service/react/internal/hub"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// client_hub/command_box 的引擎侧入口：等待交互消息、步边界吸收通知、通知消息落库。
// 枢纽与命令箱本体在 internal/hub。

// waitClientMessage 是交互等待函数的统一入口：经 hub 注册匹配规则并阻塞等待。
// 配置 react.loop.interaction_timeout_sec > 0 时启用交互等待超时：超时返回
// ErrInteractionTimeout，由各交互工具以错误工具结果回灌模型继续循环（不终止 run）。
func (s *reactEngineState) waitClientMessage(match func(params.ReactWSMessage) bool) (params.ReactWSMessage, error) {
	if seconds := conf.GetReactRuntimeConfig().Loop.InteractionTimeoutSec; seconds > 0 {
		return s.clientHub.WaitTimeout(match, time.Duration(seconds)*time.Second)
	}
	return s.clientHub.Wait(match)
}

// drainRuntimeNotifications 在模型步边界吸收全部待投递通知：
// 同一事务内"逐条条件更新 claim-once（admitted→guided）+ 合并通知落库为一条
// react_notice 类型 user 消息"，成功后追加进当前轮上下文——不新开 turn。
// 返回是否发生了注入；事务失败时通知放回箱首（账本行未动），下一边界重试。
func (s *reactEngineState) drainRuntimeNotifications(step int) (bool, error) {
	if s.commands == nil {
		return false, nil
	}
	notices := s.commands.DrainNotifications()
	if len(notices) == 0 {
		return false, nil
	}
	claimed := make([]runtimeNotification, 0, len(notices))
	var noticeRef reactMessageRef
	var content string
	err := model.GetLLMDB().WithContext(s.ctx).Transaction(func(tx *gorm.DB) error {
		claimed = claimed[:0]
		for _, n := range notices {
			ok, err := model.MarkPendingInputGuidedWithDB(s.ctx, tx, n.PendingInputID)
			if err != nil {
				return err
			}
			if ok {
				claimed = append(claimed, n)
			}
		}
		if len(claimed) == 0 {
			return nil
		}
		content = hub.MergeTaskNotificationContent(claimed)
		_, ref, err := persistNoticeMessageTx(s.ctx, tx, s.req, s.runID, s.sessionID, content, step)
		if err != nil {
			return err
		}
		noticeRef = ref
		return nil
	})
	if err != nil {
		s.commands.PushFrontNotifications(notices)
		s.logWarnf("[React.Notify] 通知消费失败(放回箱首,下一边界重试): runId=%s, count=%d, err=%v", s.runID, len(notices), err)
		return false, err
	}
	if len(claimed) == 0 {
		return false, nil
	}
	// 追加上下文放在事务提交之后：与落库消息顺序不变量一致，失败重试不会重复注入。
	s.messages = append(s.messages, llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: content})
	s.messageRefs = append(s.messageRefs, []reactMessageRef{noticeRef})
	// 吸收即重置重复调用检测器（对齐 ZCode 轮内吸收器语义）：通知带来新信息，
	// 上一轮的重复调用 streak 不应跨通知累计。
	s.anomalyStreak = 0
	s.anomalyLastSignature = ""
	s.logInfof("[React.Notify] 后台完成通知已注入下一模型轮: runId=%s, step=%d, count=%d, messageId=%s", s.runID, step, len(claimed), noticeRef.MessageID)
	if s.emitter != nil {
		_ = s.emitter.EmitStep(step, EventNoticeDrained, params.ReactNoticeDrainedPayload{
			MessageID: noticeRef.MessageID,
			Count:     len(claimed),
		})
	}
	return true, nil
}

// persistNoticeMessageTx 把合并通知作为 model-only 的 user 消息落库（react_notice 类型）。
// 存储结构与 guide/user_input 一致（modelMessage 信封），历史回放与上下文重建走同一解析路径；
// 前端按 react_notice 渲染系统卡片而非用户气泡。
func persistNoticeMessageTx(ctx *gin.Context, tx *gorm.DB, req *runtimeRequest, runID, sessionID, content string, step int) (llm.ChatMessage, reactMessageRef, error) {
	message := llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: content}
	contentJSON, err := json.Marshal(map[string]any{
		"content":      content,
		"modelMessage": message,
	})
	if err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	seq, err := model.GetReactMessageMaxSeqByRunIDWithDB(ctx, tx, runID)
	if err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	ref := reactMessageRef{RunID: runID, MessageID: generateMessageID(), Seq: seq + 1}
	row := &model.ReactMessage{
		MessageID:   ref.MessageID,
		RunID:       runID,
		SessionID:   sessionID,
		Seq:         ref.Seq,
		StepIndex:   step,
		Role:        model.ReactMessageRoleUser,
		MessageType: model.ReactMessageTypeNotice,
		ContentJSON: string(contentJSON),
	}
	if req != nil {
		row.UserName = req.userName
		row.CallerKey = req.payload.CallerKey
	}
	if err := model.CreateReactMessageWithDB(ctx, tx, row); err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	return message, ref, nil
}
