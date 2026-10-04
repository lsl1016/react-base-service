// Package workflow 定时触发工作流管理接口（方案 §5.4）：
// 定义 CRUD、启停、手动触发、运行历史查询。
// 所有端点在 llm.workflow.enabled=false 时直接拒绝（调度器未启动、表未建时避免裸 SQL 报错）。
package workflow

import (
	"strconv"

	"react-base-service/components"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	workflowService "react-base-service/service/workflow"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

type workflowCreateRequest struct {
	WorkflowKey      string   `json:"workflowKey"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	CallerKey        string   `json:"callerKey"`
	RouteValues      []string `json:"routeValues"`
	UserName         string   `json:"userName"`
	Prompt           string   `json:"prompt"`
	TriggerType      string   `json:"triggerType"`
	CronExpr         string   `json:"cronExpr"`
	Timezone         string   `json:"timezone"`
	ModelKey         string   `json:"modelKey"`
	ModelVersion     string   `json:"modelVersion"`
	MaxSteps         int      `json:"maxSteps"`
	TimeoutSec       int      `json:"timeoutSec"`
	NotifyWebhookURL string   `json:"notifyWebhookUrl"`
	RiskPatterns     string   `json:"riskPatterns"`
	// Enabled 创建即启停；缺省启用。
	Enabled *bool `json:"enabled"`
}

type workflowUpdateRequest struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	CallerKey        string   `json:"callerKey"`
	RouteValues      []string `json:"routeValues"`
	UserName         string   `json:"userName"`
	Prompt           string   `json:"prompt"`
	TriggerType      string   `json:"triggerType"`
	CronExpr         string   `json:"cronExpr"`
	Timezone         string   `json:"timezone"`
	ModelKey         string   `json:"modelKey"`
	ModelVersion     string   `json:"modelVersion"`
	MaxSteps         int      `json:"maxSteps"`
	TimeoutSec       int      `json:"timeoutSec"`
	NotifyWebhookURL string   `json:"notifyWebhookUrl"`
	RiskPatterns     string   `json:"riskPatterns"`
	// Enabled 显式启停（nil=不变）；启停经 manager 同步/摘除调度 entry。
	Enabled *bool `json:"enabled"`
}

type workflowDispatchRequest struct {
	Note string `json:"note"`
}

type workflowRunsRequest struct {
	Status string `json:"status"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// requireWorkflowEnabled 是管理面统一前置：未启用时直接参数错误返回。
func requireWorkflowEnabled(ctx *gin.Context) bool {
	if conf.CustomConf.LLM.Workflow.Enabled {
		return true
	}
	components.RenderJsonFail(ctx, components.ParamInvalidf("定时触发工作流未启用（llm.workflow.enabled），请先开启并执行建表"))
	return false
}

// CreateWorkflow 创建定义（trigger_type=cron 时注册调度 entry）。
func CreateWorkflow(ctx *gin.Context) {
	if !requireWorkflowEnabled(ctx) {
		return
	}
	var req workflowCreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("参数解析失败: %v", err))
		return
	}
	wf, err := workflowService.CreateWorkflowDef(ctx, req.WorkflowKey, toDefinitionInput(
		req.Name, req.Description, req.CallerKey, req.RouteValues, req.UserName, req.Prompt,
		req.TriggerType, req.CronExpr, req.Timezone, req.ModelKey, req.ModelVersion,
		req.MaxSteps, req.TimeoutSec, req.NotifyWebhookURL, req.RiskPatterns, req.Enabled))
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, gin.H{"workflow": wf})
}

// UpdateWorkflow 局部更新定义；cron/时区/启停变更即重注册 entry。
func UpdateWorkflow(ctx *gin.Context) {
	if !requireWorkflowEnabled(ctx) {
		return
	}
	var req workflowUpdateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("参数解析失败: %v", err))
		return
	}
	wf, err := workflowService.UpdateWorkflowDef(ctx, ctx.Param("key"), toDefinitionInput(
		req.Name, req.Description, req.CallerKey, req.RouteValues, req.UserName, req.Prompt,
		req.TriggerType, req.CronExpr, req.Timezone, req.ModelKey, req.ModelVersion,
		req.MaxSteps, req.TimeoutSec, req.NotifyWebhookURL, req.RiskPatterns, req.Enabled))
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, gin.H{"workflow": wf})
}

// ListWorkflows 定义列表（含 next_fire_at/last_fire_at/最近一次 run 状态）。
func ListWorkflows(ctx *gin.Context) {
	if !requireWorkflowEnabled(ctx) {
		return
	}
	rows, statuses, err := workflowService.ListWorkflowDefs(ctx)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for i := range rows {
		wf := &rows[i]
		items = append(items, gin.H{
			"workflow":      wf,
			"recentRunStatus": statuses[wf.ID],
		})
	}
	components.RenderJsonSucc(ctx, gin.H{"items": items, "total": len(items)})
}

// DeleteWorkflow 软删定义（置 deleted + 禁用 + 跳过 pending run），不物理删。
func DeleteWorkflow(ctx *gin.Context) {
	if !requireWorkflowEnabled(ctx) {
		return
	}
	if err := workflowService.DeleteWorkflowDef(ctx, ctx.Param("key")); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, gin.H{"deleted": true})
}

// DispatchWorkflow 手动触发一次 run（trigger_type=manual，验证入口）。
func DispatchWorkflow(ctx *gin.Context) {
	if !requireWorkflowEnabled(ctx) {
		return
	}
	var req workflowDispatchRequest
	// body 可选：dispatch 允许空请求体。
	_ = ctx.ShouldBindJSON(&req)
	run, err := workflowService.DispatchWorkflowDef(ctx, ctx.Param("key"), req.Note)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	zlog.Infof(ctx, "[workflow.http] 手动触发: key=%s runId=%d by=%s", ctx.Param("key"), run.ID, ctx.GetString("userName"))
	components.RenderJsonSucc(ctx, gin.H{"run": run})
}

// ListWorkflowRuns 运行历史分页（status 过滤 + 全量 status_counts）。
func ListWorkflowRuns(ctx *gin.Context) {
	if !requireWorkflowEnabled(ctx) {
		return
	}
	wf, err := model.GetWorkflowByKey(ctx, ctx.Param("key"))
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if wf == nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("workflow 不存在: %s", ctx.Param("key")))
		return
	}
	var req workflowRunsRequest
	// 查询参数兼容 query/body 两种传法。
	_ = ctx.ShouldBindJSON(&req)
	if req.Status == "" {
		req.Status = ctx.Query("status")
	}
	if req.Limit == 0 {
		if limit, err := strconv.Atoi(ctx.Query("limit")); err == nil {
			req.Limit = limit
		}
	}
	if req.Offset == 0 {
		if offset, err := strconv.Atoi(ctx.Query("offset")); err == nil {
			req.Offset = offset
		}
	}
	runs, err := model.ListWorkflowRunsByWorkflowID(ctx, wf.ID, req.Status, req.Limit, req.Offset)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	counts, err := model.CountWorkflowRunsByStatus(ctx, wf.ID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, gin.H{"runs": runs, "statusCounts": counts})
}

// toDefinitionInput 把控制器请求收敛为 service 层统一入参（创建/更新共用一套校验）。
func toDefinitionInput(name, description, callerKey string, routeValues []string, userName, prompt,
	triggerType, cronExpr, timezone, modelKey, modelVersion string, maxSteps, timeoutSec int,
	notifyWebhookURL, riskPatterns string, enabled *bool) workflowService.DefinitionInput {
	return workflowService.DefinitionInput{
		Name:             name,
		Description:      description,
		CallerKey:        callerKey,
		RouteValues:      routeValues,
		UserName:         userName,
		Prompt:           prompt,
		TriggerType:      triggerType,
		CronExpr:         cronExpr,
		Timezone:         timezone,
		ModelKey:         modelKey,
		ModelVersion:     modelVersion,
		MaxSteps:         maxSteps,
		TimeoutSec:       timeoutSec,
		NotifyWebhookURL: notifyWebhookURL,
		RiskPatterns:     riskPatterns,
		Enabled:          enabled,
	}
}
