package react

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	llm "react-base-service/api/llm"
	"react-base-service/conf"

	"github.com/gin-gonic/gin"
)

// workflow 工具（create_workflow / dispatch_workflow）接线说明：
//
// 依赖方向是 workflow → react（workflow runner 复用 ReAct 引擎），因此 react 不能反向
// import service/workflow。这里以启动时注入的执行器 hook 解耦：react 定义签名与注册入口，
// service/workflow 提供实现，router.Tasks 启动时接线（与 workflow.Start 同处）。
//
// 工具可见性 = llm.workflow.enabled × 执行器已接线；归属隔离 = 执行时以当前 run 的
// appKey/userName/routeValues 作为 workflow 归属落库（创建的资源天然归属调用方 app）。

// WorkflowToolExecutor 是 workflow 工具执行器的签名（service/workflow 提供）。
// 返回 (content, isError, err)，与 executeInternalToolContent 的三元返回同构。
type WorkflowToolExecutor func(ctx *gin.Context, toolName, appKey, userName string, routeValues []string, input json.RawMessage) (string, bool, error)

var workflowToolExecutorHook atomic.Value // WorkflowToolExecutor

// RegisterWorkflowToolExecutor 注册 workflow 工具执行器（启动时调用一次）。
// 未注册时 create_workflow/dispatch_workflow 不注册进工具清单，模型不可见。
func RegisterWorkflowToolExecutor(fn WorkflowToolExecutor) {
	workflowToolExecutorHook.Store(fn)
}

func registeredWorkflowToolExecutor() WorkflowToolExecutor {
	fn, _ := workflowToolExecutorHook.Load().(WorkflowToolExecutor)
	return fn
}

// workflowToolsAvailable 判定 workflow 工具是否注册（工具清单装配用）。
func workflowToolsAvailable() bool {
	return conf.CustomConf.LLM.Workflow.Enabled && registeredWorkflowToolExecutor() != nil
}

func (s *reactEngineState) executeWorkflowTool(call llm.ToolCall) (string, bool, error) {
	fn := registeredWorkflowToolExecutor()
	if fn == nil {
		return "", true, fmt.Errorf("workflow 工具未接入（llm.workflow.enabled 或调度器未启动）")
	}
	return fn(s.ctx, call.Name, s.req.payload.AppKey, s.req.userName, s.req.payload.RouteValues, call.Input)
}

func createWorkflowToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: metaToolCreateWorkflow,
		Description: "创建一个无人值守 workflow（定时或手动触发的完整自动执行任务，独立于当前会话运行）。" +
			"创建前必须先用 ask_question 与用户确认关键参数：触发方式（cron/manual）、cron 表达式与时区、超时与步数上限、是否需要通知 webhook。" +
			"prompt 是无人值守执行的完整任务提示词，必须自包含（该 run 无人交互，不要引用当前对话上下文中未写进 prompt 的内容）。" +
			"workflow_key 全局唯一，建议「业务域-任务-序号」风格（如 report-daily-error-1）。创建成功返回 workflow_key；用户要求立即执行时再调用 dispatch_workflow。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次工具调用目的，用于前端展示。"),
				"workflow_key": stringSchema("全局唯一 key：字母/数字/中划线/下划线，1-64 位，字母或数字开头。"),
				"name":        stringSchema("workflow 展示名。"),
				"task_description": stringSchema("workflow 用途描述（管理面板展示）。"),
				"prompt":      stringSchema("无人值守执行的完整任务提示词（自包含，run 期间无人工交互）。"),
				"trigger_type": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"cron", "manual"},
					"description": "触发方式：cron=定时（需 cron_expr），manual=手动触发。",
				},
				"cron_expr":         stringSchema("cron 表达式（trigger_type=cron 时必填），如「0 8 * * *」表示每天 08:00。"),
				"timezone":          stringSchema("时区（cron 时使用），默认 Asia/Shanghai。"),
				"timeout_sec":       numberSchema("单次 run 超时秒数，默认跟随全局配置。"),
				"max_steps":         numberSchema("单次 run 最大推理轮数，默认跟随全局配置。"),
				"notify_webhook_url": stringSchema("run 结束通知 webhook（企业微信/飞书），可选。"),
			},
			"required":             []string{"description", "workflow_key", "name", "prompt"},
			"additionalProperties": false,
		},
	}
}

func dispatchWorkflowToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolDispatchWorkflow,
		Description: "手动触发一次已创建 workflow 的执行（新建 workflow 的验证入口；可重复触发）。workflow 必须处于启用状态。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description":  stringSchema("本次工具调用目的，用于前端展示。"),
				"workflow_key": stringSchema("create_workflow 返回的 workflow_key。"),
				"note":         stringSchema("触发备注（记录进运行历史），可选。"),
			},
			"required":             []string{"description", "workflow_key"},
			"additionalProperties": false,
		},
	}
}

// appendWorkflowToolDefinitions 在工具清单尾部按门控追加 workflow 工具。
func appendWorkflowToolDefinitions(definitions []llm.ToolDefinition) []llm.ToolDefinition {
	if !workflowToolsAvailable() {
		return definitions
	}
	return append(definitions, createWorkflowToolDefinition(), dispatchWorkflowToolDefinition())
}
