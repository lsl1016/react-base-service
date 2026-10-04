package workflow

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	cron "github.com/robfig/cron/v3"
)

// definitionService 提供工作流定义的校验与 CRUD：所有变更先落 DB，
// 再经 manager 单点同步内存 entry（SyncRegister/SyncRemove），防内存与 DB 漂移。

// DefinitionInput 是创建/更新定义的统一入参（控制器侧字段的业务校验在这里收敛）。
type DefinitionInput struct {
	Name             string
	Description      string
	CallerKey        string
	RouteValues      []string
	UserName         string
	Prompt           string
	TriggerType      string
	CronExpr         string
	Timezone         string
	ModelKey         string
	ModelVersion     string
	MaxSteps         int
	TimeoutSec       int
	NotifyWebhookURL string
	RiskPatterns     string
	Enabled          *bool // nil=不修改（仅更新路径有意义）
}

var workflowKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}[a-zA-Z0-9]$|^[a-zA-Z0-9]{1}$`)

// CreateWorkflowDef 创建定义；trigger_type=cron 时注册调度 entry。
func CreateWorkflowDef(ctx context.Context, workflowKey string, input DefinitionInput) (*model.Workflow, error) {
	workflowKey = strings.TrimSpace(workflowKey)
	if !workflowKeyPattern.MatchString(workflowKey) {
		return nil, components.ParamInvalidf("workflowKey 仅允许字母/数字/中划线/下划线（1-64 位，字母或数字开头）")
	}
	if existing, err := model.GetWorkflowByKey(ctx, workflowKey); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, components.ParamInvalidf("workflowKey 已存在: %s", workflowKey)
	}
	wf, err := buildWorkflowRow(workflowKey, input)
	if err != nil {
		return nil, err
	}
	if err := model.CreateWorkflow(ctx, wf); err != nil {
		return nil, err
	}
	manager.SyncRegister(wf)
	zlog.Infof(nil, "[workflow.definition] 创建 workflow: key=%s trigger=%s expr=%s caller=%s",
		wf.WorkflowKey, wf.TriggerType, wf.CronExpr, wf.CallerKey)
	return wf, nil
}

// UpdateWorkflowDef 局部更新定义；cron/时区/启停变更经 SyncRegister 重建 entry。
// 零值字段不更新（全量替换语义由「先删再建」表达，PATCH 只收敛变更）。
func UpdateWorkflowDef(ctx context.Context, workflowKey string, input DefinitionInput) (*model.Workflow, error) {
	existing, err := model.GetWorkflowByKey(ctx, strings.TrimSpace(workflowKey))
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ParamInvalidf("workflow 不存在: %s", workflowKey)
	}
	merged := *existing
	updates := map[string]any{}

	if input.Name != "" {
		if err := validateWorkflowName(input.Name); err != nil {
			return nil, err
		}
		merged.Name = input.Name
		updates["name"] = input.Name
	}
	if input.Description != "" {
		merged.Description = input.Description
		updates["description"] = input.Description
	}
	if input.CallerKey != "" {
		merged.CallerKey = strings.TrimSpace(input.CallerKey)
		updates["caller_key"] = merged.CallerKey
	}
	if input.RouteValues != nil {
		routeJSON, err := marshalRouteValues(input.RouteValues)
		if err != nil {
			return nil, err
		}
		merged.RouteValues = routeJSON
		updates["route_values"] = routeJSON
	}
	if input.UserName != "" {
		merged.UserName = strings.TrimSpace(input.UserName)
		updates["user_name"] = merged.UserName
	}
	if input.Prompt != "" {
		if strings.TrimSpace(input.Prompt) == "" {
			return nil, components.ParamInvalidf("prompt 不能为空")
		}
		merged.Prompt = input.Prompt
		updates["prompt"] = merged.Prompt
	}
	if input.TriggerType != "" {
		if err := validateTriggerType(input.TriggerType); err != nil {
			return nil, err
		}
		merged.TriggerType = input.TriggerType
		updates["trigger_type"] = merged.TriggerType
	}
	if input.CronExpr != "" {
		merged.CronExpr = strings.TrimSpace(input.CronExpr)
		updates["cron_expr"] = merged.CronExpr
	}
	if input.Timezone != "" {
		if err := validateTimezone(input.Timezone); err != nil {
			return nil, err
		}
		merged.Timezone = strings.TrimSpace(input.Timezone)
		updates["timezone"] = merged.Timezone
	}
	if input.ModelKey != "" {
		merged.ModelKey = strings.TrimSpace(input.ModelKey)
		updates["model_key"] = merged.ModelKey
	}
	if input.ModelVersion != "" {
		merged.ModelVersion = strings.TrimSpace(input.ModelVersion)
		updates["model_version"] = merged.ModelVersion
	}
	if input.MaxSteps > 0 {
		merged.MaxSteps = input.MaxSteps
		updates["max_steps"] = input.MaxSteps
	}
	if input.TimeoutSec > 0 {
		merged.TimeoutSec = input.TimeoutSec
		updates["timeout_sec"] = input.TimeoutSec
	}
	if input.NotifyWebhookURL != "" {
		if err := validateWebhookURL(input.NotifyWebhookURL); err != nil {
			return nil, err
		}
		merged.NotifyWebhookURL = strings.TrimSpace(input.NotifyWebhookURL)
		updates["notify_webhook_url"] = merged.NotifyWebhookURL
	}
	if input.RiskPatterns != "" {
		if err := validateRiskPatterns(input.RiskPatterns); err != nil {
			return nil, err
		}
		merged.RiskPatterns = input.RiskPatterns
		updates["risk_patterns"] = merged.RiskPatterns
	}
	if input.Enabled != nil {
		enabled := 0
		if *input.Enabled {
			enabled = 1
		}
		merged.Enabled = enabled
		updates["enabled"] = enabled
	}

	// 变更后的整行定义再走一次完整性校验（如 trigger_type=cron 但 cron_expr 缺失）。
	if err := validateWorkflowRow(&merged); err != nil {
		return nil, err
	}
	if err := model.UpdateWorkflowByKey(ctx, existing.WorkflowKey, updates); err != nil {
		return nil, err
	}
	manager.SyncRegister(&merged)
	zlog.Infof(nil, "[workflow.definition] 更新 workflow: key=%s fields=%d", existing.WorkflowKey, len(updates))
	return &merged, nil
}

// DeleteWorkflowDef 软删定义（置 deleted + 跳过 pending run）并摘除调度 entry。
func DeleteWorkflowDef(ctx context.Context, workflowKey string) error {
	workflowKey = strings.TrimSpace(workflowKey)
	existing, err := model.GetWorkflowByKey(ctx, workflowKey)
	if err != nil {
		return err
	}
	if existing == nil {
		return components.ParamInvalidf("workflow 不存在: %s", workflowKey)
	}
	if err := model.SoftDeleteWorkflow(ctx, workflowKey); err != nil {
		return err
	}
	manager.SyncRemove(workflowKey)
	zlog.Infof(nil, "[workflow.definition] 软删 workflow: key=%s", workflowKey)
	return nil
}

// DispatchWorkflowDef 手动触发一次 run（trigger_type=manual；新建 workflow 的验证入口）。
// 手动触发 planned_fire_at=NULL，与 cron 防重索引天然兼容，可重复触发。
func DispatchWorkflowDef(ctx context.Context, workflowKey, note string) (*model.WorkflowRun, error) {
	wf, err := model.GetWorkflowByKey(ctx, strings.TrimSpace(workflowKey))
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, components.ParamInvalidf("workflow 不存在: %s", workflowKey)
	}
	if wf.Enabled != 1 {
		return nil, components.ParamInvalidf("workflow 已停用，请先启用再触发: %s", wf.WorkflowKey)
	}
	run := &model.WorkflowRun{
		WorkflowID:  wf.ID,
		TriggerType: model.WorkflowTriggerTypeManual,
		TriggerNote: strings.TrimSpace(note),
		Status:      model.WorkflowRunStatusPending,
	}
	created, err := model.CreateWorkflowRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if !created {
		return nil, components.ErrorSystemError.Sprintf("运行历史插入失败，请重试")
	}
	metrics.WorkflowTriggerTotal.WithLabelValues(wf.WorkflowKey, "manual").Inc()
	// 手动触发不做并发闸门（操作者显式动作，验证入口语义）；同样纳入停机收敛跟踪。
	manager.trackGo(wf, run)
	return run, nil
}

// ListWorkflowDefs 返回定义列表（附 next_fire_at/last_fire_at/最近一次 run 状态）。
func ListWorkflowDefs(ctx context.Context) ([]model.Workflow, map[uint64]string, error) {
	rows, err := model.ListWorkflows(ctx)
	if err != nil {
		return nil, nil, err
	}
	statuses, err := model.LatestWorkflowRunStatuses(ctx)
	if err != nil {
		return nil, nil, err
	}
	return rows, statuses, nil
}

// buildWorkflowRow 组装并校验创建入参对应的定义行。
func buildWorkflowRow(workflowKey string, input DefinitionInput) (*model.Workflow, error) {
	if err := validateWorkflowName(input.Name); err != nil {
		return nil, err
	}
	if err := validateTriggerType(input.TriggerType); err != nil {
		return nil, err
	}
	if err := validateRiskPatterns(input.RiskPatterns); err != nil {
		return nil, err
	}
	if input.NotifyWebhookURL != "" {
		if err := validateWebhookURL(input.NotifyWebhookURL); err != nil {
			return nil, err
		}
	}
	if input.Timezone != "" {
		if err := validateTimezone(input.Timezone); err != nil {
			return nil, err
		}
	}
	routeJSON := "[]"
	if input.RouteValues != nil {
		value, err := marshalRouteValues(input.RouteValues)
		if err != nil {
			return nil, err
		}
		routeJSON = value
	}
	enabled := 1
	if input.Enabled != nil && !*input.Enabled {
		enabled = 0
	}
	wf := &model.Workflow{
		WorkflowKey:      workflowKey,
		Name:             strings.TrimSpace(input.Name),
		Description:      input.Description,
		CallerKey:        strings.TrimSpace(input.CallerKey),
		RouteValues:      routeJSON,
		UserName:         strings.TrimSpace(input.UserName),
		Prompt:           input.Prompt,
		TriggerType:      input.TriggerType,
		CronExpr:         strings.TrimSpace(input.CronExpr),
		Timezone:         firstNonEmpty(strings.TrimSpace(input.Timezone), "Asia/Shanghai"),
		ModelKey:         strings.TrimSpace(input.ModelKey),
		ModelVersion:     strings.TrimSpace(input.ModelVersion),
		MaxSteps:         input.MaxSteps,
		TimeoutSec:       input.TimeoutSec,
		NotifyWebhookURL: strings.TrimSpace(input.NotifyWebhookURL),
		RiskPatterns:     input.RiskPatterns,
		Enabled:          enabled,
	}
	if err := validateWorkflowRow(wf); err != nil {
		return nil, err
	}
	return wf, nil
}

// validateWorkflowRow 整行完整性校验：prompt/caller 必填；cron 型必须有合法表达式与时区。
func validateWorkflowRow(wf *model.Workflow) error {
	if err := validateWorkflowName(wf.Name); err != nil {
		return err
	}
	if strings.TrimSpace(wf.CallerKey) == "" {
		return components.ParamInvalidf("callerKey 不能为空（触发的 run 依赖 caller 解析提示词/工具/Skill）")
	}
	if strings.TrimSpace(wf.Prompt) == "" {
		return components.ParamInvalidf("prompt 不能为空")
	}
	if err := validateTriggerType(wf.TriggerType); err != nil {
		return err
	}
	if wf.TriggerType == model.WorkflowTriggerTypeCron {
		if strings.TrimSpace(wf.CronExpr) == "" {
			return components.ParamInvalidf("cron 型 workflow 必须配置 cronExpr")
		}
		if _, err := cron.ParseStandard(wf.CronExpr); err != nil {
			return components.ParamInvalidf("cronExpr 非法（标准 5 段表达式）: %v", err)
		}
		if err := validateTimezone(wf.Timezone); err != nil {
			return err
		}
	}
	if err := validateRiskPatterns(wf.RiskPatterns); err != nil {
		return err
	}
	return nil
}

func validateWorkflowName(name string) error {
	if strings.TrimSpace(name) == "" {
		return components.ParamInvalidf("name 不能为空")
	}
	return nil
}

func validateTriggerType(triggerType string) error {
	switch triggerType {
	case model.WorkflowTriggerTypeCron, model.WorkflowTriggerTypeManual:
		return nil
	case "":
		return components.ParamInvalidf("triggerType 不能为空（cron | manual）")
	default:
		return components.ParamInvalidf("triggerType 仅支持 cron | manual（webhook/event 三期预留）")
	}
}

// validateTimezone 校验 IANA 时区名（保存时拒绝，不静默禁用——比 misfire 排查便宜得多）。
func validateTimezone(name string) error {
	if _, err := time.LoadLocation(strings.TrimSpace(name)); err != nil {
		return components.ParamInvalidf("timezone 非法（IANA 名称，如 Asia/Shanghai）: %v", err)
	}
	return nil
}

// validateRiskPatterns 校验判级正则逐行可编译。
func validateRiskPatterns(patterns string) error {
	for _, line := range strings.Split(patterns, "\n") {
		pattern := strings.TrimSpace(line)
		if pattern == "" {
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return components.ParamInvalidf("riskPatterns 含非法正则 %q: %v", pattern, err)
		}
	}
	return nil
}

func validateWebhookURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return components.ParamInvalidf("notifyWebhookUrl 非法")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return components.ParamInvalidf("notifyWebhookUrl 仅支持 http/https")
	}
	return nil
}

func marshalRouteValues(values []string) (string, error) {
	if values == nil {
		return "[]", nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", components.ParamInvalidf("routeValues 序列化失败: %v", err)
	}
	return string(data), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
