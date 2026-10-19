package react

import (
	"encoding/json"
	"strings"

	"react-base-service/components/params"
	commandService "react-base-service/service/command"
	reactService "react-base-service/service/react"

	"github.com/gin-gonic/gin"
)

// 斜杠命令 WS 拦截层（docs/plan/20261013_斜杠命令支持设计方案.md §4.1.2）：
// 位于 handleWSMessage 的 EventRun 分支、startWSRun 之前。选择 controller 层的原因：
// 该层已有 queue_send / plan 命令 / dispatchRunExecution 三个入口级分发先例；
// 不启 run 时可直接 write 推事件；命令改写 payload 后再 dispatch 复用全部 run 生命周期管理。

// applySlashCommand 在空闲会话（无活跃 run）执行命令解析与 payload 改写。
// 返回 true 表示消息已就地处理完毕（回执已写、不启动 run）；
// 返回 false 时 msg.Payload 可能已被命令改写（标记型置 CommandDirective、展开型替换
// UserPrompt 并把原文放 CommandDisplay），调用方继续 startWSRun。
// slash_command.enabled 未开或首 token 不是命令时零改写、零行为变化。
func applySlashCommand(ctx *gin.Context, write reactService.EventWriter, msg *params.ReactWSMessage) bool {
	if len(msg.Payload) == 0 {
		return false
	}
	var payload params.ReactRunPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		// 参数非法的报错口径留在 startWSRun，保持不变。
		return false
	}
	res := commandService.Resolve(ctx, payload.AppKey, payload.RouteValues, payload.UserPrompt)
	if res == nil {
		return false
	}
	if res.Definition == nil {
		emitCommandReceipt(write, msg, res.Name, params.ReactCommandReceiptRejected, params.ReactCommandRejectUnknown,
			"命令 /"+res.Name+" 当前不可用（未实现或所属能力开关未开启）")
		return true
	}
	switch res.Definition.Kind {
	case commandService.KindExpansion:
		if res.Definition.RequireArgs && strings.TrimSpace(res.Args) == "" {
			emitCommandReceipt(write, msg, res.Name, params.ReactCommandReceiptRejected, params.ReactCommandRejectUnknown,
				"命令 /"+res.Name+" 需要参数："+res.Definition.ArgumentHint)
			return true
		}
		// 自定义命令的 Expand 为 nil（模板在 CustomContent），必须先判 nil 再调用——
		// 直接调用 nil 函数值会 panic。
		var expanded string
		if res.Definition.Expand != nil {
			expanded = res.Definition.Expand(res.Args)
		} else {
			expanded = commandService.ExpandTemplate(res.Definition.Name, res.Definition.CustomContent, res.Args)
		}
		payload.CommandDisplay = strings.TrimSpace(payload.UserPrompt)
		payload.UserPrompt = expanded
	case commandService.KindFlaggedRun:
		if res.Definition.Name == "compact" {
			payload.CommandDirective = &params.ReactCommandDirective{
				ForceCompact:       true,
				CompactInstructions: strings.TrimSpace(res.Args),
			}
			// /compact 的 UserPrompt 保持原文（展示与压缩 run 的用户气泡一致），无需 CommandDisplay。
		}
	}
	if data, err := json.Marshal(payload); err == nil {
		msg.Payload = data
	}
	return false
}

// rejectSlashCommandWhileRunActive 在 run 活跃期（本连接或经 HandleWSSteer 准入前）
// 拒绝命令：命令一律不排队——排队晋升会在内部重建 payload，展开/标记语义会丢失。
// 返回 true 表示已写 command_receipt 回执，调用方不应继续 steering 流程。
func rejectSlashCommandWhileRunActive(ctx *gin.Context, write reactService.EventWriter, msg params.ReactWSMessage) bool {
	if len(msg.Payload) == 0 {
		return false
	}
	var payload params.ReactRunPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return false
	}
	res := commandService.Resolve(ctx, payload.AppKey, payload.RouteValues, payload.UserPrompt)
	if res == nil {
		return false
	}
	if res.Definition == nil {
		emitCommandReceipt(write, &msg, res.Name, params.ReactCommandReceiptRejected, params.ReactCommandRejectUnknown,
			"命令 /"+res.Name+" 当前不可用（未实现或所属能力开关未开启）")
		return true
	}
	emitCommandReceipt(write, &msg, res.Name, params.ReactCommandReceiptRejected, params.ReactCommandRejectRunActive,
		"当前对话正在执行，命令 /"+res.Name+" 请在本次 run 结束后重试")
	return true
}

func emitCommandReceipt(write reactService.EventWriter, msg *params.ReactWSMessage, name, result, reason, message string) {
	_ = write(params.ReactEvent{
		Type:      reactService.EventCommandReceipt,
		RunID:     msg.RunID,
		SessionID: msg.SessionID,
		Payload: params.ReactCommandReceiptPayload{
			Command: name,
			Result:  result,
			Reason:  reason,
			Message: message,
		},
	})
}
