package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"react-base-service/components"
	"react-base-service/conf"
	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// ExecuteReactWorkflowTool 实现 react 引擎的 create_workflow / dispatch_workflow 内置工具
//（经 react.RegisterWorkflowToolExecutor 在启动时接线，见 service/react/workflow_tools.go）。
//
// 归属隔离：以当前 run 的 appKey/userName/routeValues 作为 workflow 归属落库/校验——
// 创建的资源天然归属调用方 app；dispatch 校验 workflow 属于当前 app。
// 底层直接调 CreateWorkflowDef / DispatchWorkflowDef（含校验、manager 同步、goroutine 跟踪），不绕 HTTP。
//
// 工具名与 service/react/internal/core/metatool.go 的 MetaToolCreateWorkflow/MetaToolDispatchWorkflow
// 保持一致（字符串字面量，避免 workflow → react 之外再引反向依赖）。
func ExecuteReactWorkflowTool(ctx *gin.Context, toolName, appKey, userName string, routeValues []string, input json.RawMessage) (string, bool, error) {
	if !conf.CustomConf.LLM.Workflow.Enabled {
		return "", true, fmt.Errorf("workflow 能力未开启（llm.workflow.enabled=false）")
	}
	switch toolName {
	case "create_workflow":
		return executeCreateWorkflowTool(ctx, appKey, userName, routeValues, input)
	case "dispatch_workflow":
		return executeDispatchWorkflowTool(ctx, appKey, input)
	default:
		return "", true, fmt.Errorf("unknown workflow tool: %s", toolName)
	}
}

type createWorkflowToolInput struct {
	WorkflowKey     string `json:"workflow_key"`
	Name            string `json:"name"`
	TaskDescription string `json:"task_description"`
	Prompt          string `json:"prompt"`
	TriggerType     string `json:"trigger_type"`
	CronExpr        string `json:"cron_expr"`
	Timezone        string `json:"timezone"`
	TimeoutSec      int    `json:"timeout_sec"`
	MaxSteps        int    `json:"max_steps"`
	NotifyWebhookURL string `json:"notify_webhook_url"`
}

func executeCreateWorkflowTool(ctx *gin.Context, appKey, userName string, routeValues []string, input json.RawMessage) (string, bool, error) {
	var req createWorkflowToolInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", true, components.ParamInvalidf("入参不合法: %v", err)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", true, components.ParamInvalidf("prompt 不能为空（无人值守 run 的完整任务提示词）")
	}
	triggerType := strings.TrimSpace(req.TriggerType)
	if triggerType == "" {
		triggerType = model.WorkflowTriggerTypeManual
	}
	def, err := CreateWorkflowDef(ctx, strings.TrimSpace(req.WorkflowKey), DefinitionInput{
		Name:             strings.TrimSpace(req.Name),
		Description:      strings.TrimSpace(req.TaskDescription),
		AppKey:           appKey,
		RouteValues:      routeValues,
		UserName:         userName,
		Prompt:           req.Prompt,
		TriggerType:      triggerType,
		CronExpr:         strings.TrimSpace(req.CronExpr),
		Timezone:         strings.TrimSpace(req.Timezone),
		MaxSteps:         req.MaxSteps,
		TimeoutSec:       req.TimeoutSec,
		NotifyWebhookURL: strings.TrimSpace(req.NotifyWebhookURL),
	})
	if err != nil {
		return "", true, err
	}
	zlog.Infof(ctx, "[workflow.tool] create_workflow: key=%s app=%s user=%s trigger=%s",
		def.WorkflowKey, appKey, userName, def.TriggerType)
	result, _ := json.Marshal(map[string]any{
		"workflow_key":  def.WorkflowKey,
		"name":          def.Name,
		"trigger_type":  def.TriggerType,
		"cron_expr":     def.CronExpr,
		"enabled":       def.Enabled == 1,
		"next_step":     "已创建。用户要求立即执行时调用 dispatch_workflow；否则告知用户已创建完成。",
	})
	return string(result), false, nil
}

type dispatchWorkflowToolInput struct {
	WorkflowKey string `json:"workflow_key"`
	Note        string `json:"note"`
}

func executeDispatchWorkflowTool(ctx *gin.Context, appKey string, input json.RawMessage) (string, bool, error) {
	var req dispatchWorkflowToolInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", true, components.ParamInvalidf("入参不合法: %v", err)
	}
	workflowKey := strings.TrimSpace(req.WorkflowKey)
	// app 归属校验：workflow 是 app 级资源，只允许归属 app 的会话触发。
	def, err := model.GetWorkflowByKey(ctx, workflowKey)
	if err != nil {
		return "", true, err
	}
	if def == nil {
		return "", true, components.ParamInvalidf("workflow 不存在: %s", workflowKey)
	}
	if def.AppKey != appKey {
		return "", true, components.ParamInvalidf("workflow %s 不属于当前 app，无权触发", workflowKey)
	}
	run, err := DispatchWorkflowDef(ctx, workflowKey, req.Note)
	if err != nil {
		return "", true, err
	}
	zlog.Infof(ctx, "[workflow.tool] dispatch_workflow: key=%s app=%s runID=%d", workflowKey, appKey, run.ID)
	result, _ := json.Marshal(map[string]any{
		"workflow_key": workflowKey,
		"run_id":       run.ID,
		"status":       run.Status,
		"next_step":    "已触发一次执行（后台异步运行）。结果与风险等级可经管理面板或通知 webhook 查看。",
	})
	return string(result), false, nil
}
