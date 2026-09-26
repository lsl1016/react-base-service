package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/components/route"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	maxAgentTools  = 64
	maxAgentSkills = 32
)

// CreateAgent 创建子 Agent 定义；caller_key=default 是默认作用域伪 caller，不要求真实 caller 存在。
func CreateAgent(ctx *gin.Context, req *params.CreateAgentReq, createdBy string) (*model.Agent, error) {
	if err := validateAgentKey(req.AgentKey); err != nil {
		return nil, err
	}
	if !model.IsReservedCallerKey(req.CallerKey) {
		caller, err := model.GetActiveCallerByKey(ctx, req.CallerKey)
		if err != nil {
			return nil, err
		}
		if caller == nil {
			return nil, components.ErrorCallerNotFound.Sprintf(req.CallerKey)
		}
	}
	if err := validatePermissionMode(req.PermissionMode); err != nil {
		return nil, err
	}
	if err := validateReferenceList("tools", req.Tools, maxAgentTools); err != nil {
		return nil, err
	}
	if err := validateReferenceList("skills", req.Skills, maxAgentSkills); err != nil {
		return nil, err
	}

	rv := req.RouteValues
	if rv == nil {
		rv = []string{}
	}
	routeValues, _ := json.Marshal(rv)
	toolsJSON, _ := json.Marshal(normalizeReferenceList(req.Tools))
	skillsJSON, _ := json.Marshal(normalizeReferenceList(req.Skills))

	existing, err := model.GetAgentByCallerAndAgentKey(ctx, req.CallerKey, req.AgentKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, components.ErrorAgentDuplicate.Sprintf(req.CallerKey, req.AgentKey)
	}
	// uk_caller_agent 不含 deleted_at：软删行仍占用唯一键，同 key 重建走复活更新而非插入。
	if deleted, err := model.GetAgentByCallerAndAgentKeyUnscoped(ctx, req.CallerKey, req.AgentKey); err != nil {
		return nil, err
	} else if deleted != nil && deleted.DeletedAt != 0 {
		return reviveDeletedAgent(ctx, deleted, req, routeValues, toolsJSON, skillsJSON, createdBy)
	}

	status, err := resolveAgentStatus(req.Status)
	if err != nil {
		return nil, err
	}
	readOnly, err := resolveAgentReadOnly(req.ReadOnly)
	if err != nil {
		return nil, err
	}

	agent := &model.Agent{
		AgentID:         "agent_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		AgentKey:        req.AgentKey,
		Name:            req.Name,
		Description:     req.Description,
		CallerKey:       req.CallerKey,
		RouteValues:     string(routeValues),
		SystemPrompt:    req.SystemPrompt,
		ModelKey:        strings.TrimSpace(req.ModelKey),
		ModelVersion:    strings.TrimSpace(req.ModelVersion),
		ToolsJSON:       string(toolsJSON),
		SkillsJSON:      string(skillsJSON),
		MaxSteps:        req.MaxSteps,
		MaxTokensPerRun: req.MaxTokensPerRun,
		ReadOnly:        readOnly,
		PermissionMode:  normalizePermissionMode(req.PermissionMode),
		Status:          status,
		CreatedBy:       createdBy,
		UpdatedBy:       createdBy,
	}
	if err := model.CreateAgent(ctx, agent); err != nil {
		return nil, err
	}
	return agent, nil
}

// agentDefinitionUpdates 是「用文件定义覆盖 agent 行」的字段集（revive 与 bundle upsert 共用）；
// 不含 agent_id/caller_key（键位不可变）与 created_by（保留创建审计）。
func agentDefinitionUpdates(req *params.CreateAgentReq, routeValues, toolsJSON, skillsJSON []byte, updatedBy string) map[string]interface{} {
	status, _ := resolveAgentStatus(req.Status)
	readOnly, _ := resolveAgentReadOnly(req.ReadOnly)
	return map[string]interface{}{
		"name":               req.Name,
		"description":        req.Description,
		"route_values":       string(routeValues),
		"system_prompt":      req.SystemPrompt,
		"model_key":          strings.TrimSpace(req.ModelKey),
		"model_version":      strings.TrimSpace(req.ModelVersion),
		"tools_json":         string(toolsJSON),
		"skills_json":        string(skillsJSON),
		"max_steps":          req.MaxSteps,
		"max_tokens_per_run": req.MaxTokensPerRun,
		"read_only":          readOnly,
		"permission_mode":    normalizePermissionMode(req.PermissionMode),
		"status":             status,
		"updated_by":         updatedBy,
	}
}

// reviveDeletedAgent 用本次定义覆盖软删行并恢复可见（清 deleted_at），沿用原 agent_id。
func reviveDeletedAgent(ctx *gin.Context, deleted *model.Agent, req *params.CreateAgentReq, routeValues, toolsJSON, skillsJSON []byte, updatedBy string) (*model.Agent, error) {
	if _, err := resolveAgentStatus(req.Status); err != nil {
		return nil, err
	}
	updates := agentDefinitionUpdates(req, routeValues, toolsJSON, skillsJSON, updatedBy)
	updates["deleted_at"] = 0
	if err := model.UpdateAgentByAgentIDUnscoped(ctx, deleted.AgentID, updates); err != nil {
		return nil, err
	}
	revived, err := model.GetAgentByAgentID(ctx, deleted.AgentID)
	if err != nil {
		return nil, err
	}
	if revived == nil {
		return nil, components.ErrorAgentNotFound.Sprintf(deleted.AgentID)
	}
	return revived, nil
}

func UpdateAgent(ctx *gin.Context, req *params.UpdateAgentReq) (*model.Agent, error) {
	existing, err := model.GetAgentByAgentID(ctx, req.AgentID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorAgentNotFound.Sprintf(req.AgentID)
	}
	if err := validateAgentKey(req.AgentKey); err != nil {
		return nil, err
	}
	if err := validateReferenceList("tools", req.Tools, maxAgentTools); err != nil {
		return nil, err
	}
	if err := validateReferenceList("skills", req.Skills, maxAgentSkills); err != nil {
		return nil, err
	}
	if req.AgentKey != existing.AgentKey {
		duplicate, err := model.GetAgentByCallerAndAgentKey(ctx, existing.CallerKey, req.AgentKey)
		if err != nil {
			return nil, err
		}
		if duplicate != nil {
			return nil, components.ErrorAgentDuplicate.Sprintf(existing.CallerKey, req.AgentKey)
		}
	}

	status, err := resolveAgentStatus(req.Status)
	if err != nil {
		return nil, err
	}
	rv := req.RouteValues
	if rv == nil {
		rv = []string{}
	}
	routeValues, _ := json.Marshal(rv)
	toolsJSON, _ := json.Marshal(normalizeReferenceList(req.Tools))
	skillsJSON, _ := json.Marshal(normalizeReferenceList(req.Skills))

	updates := map[string]interface{}{
		"agent_key":    req.AgentKey,
		"name":         req.Name,
		"description":  req.Description,
		"route_values": string(routeValues),
		"tools_json":   string(toolsJSON),
		"skills_json":  string(skillsJSON),
		"status":       status,
		"updated_by":   helpers.GetUserName(ctx),
	}
	if req.SystemPrompt != nil {
		updates["system_prompt"] = *req.SystemPrompt
	}
	if req.ModelKey != nil {
		updates["model_key"] = strings.TrimSpace(*req.ModelKey)
	}
	if req.ModelVersion != nil {
		updates["model_version"] = strings.TrimSpace(*req.ModelVersion)
	}
	if req.MaxSteps != nil {
		updates["max_steps"] = *req.MaxSteps
	}
	if req.MaxTokensPerRun != nil {
		updates["max_tokens_per_run"] = *req.MaxTokensPerRun
	}
	if req.ReadOnly != nil {
		readOnly, err := resolveAgentReadOnly(req.ReadOnly)
		if err != nil {
			return nil, err
		}
		updates["read_only"] = readOnly
	}
	if req.PermissionMode != nil {
		if err := validatePermissionMode(*req.PermissionMode); err != nil {
			return nil, err
		}
		updates["permission_mode"] = normalizePermissionMode(*req.PermissionMode)
	}

	if err := model.UpdateAgentByAgentID(ctx, req.AgentID, updates); err != nil {
		return nil, err
	}
	updated, err := model.GetAgentByAgentID(ctx, req.AgentID)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, components.ErrorAgentNotFound.Sprintf(req.AgentID)
	}
	return updated, nil
}

func DeleteAgent(ctx *gin.Context, agentID string) (*model.Agent, error) {
	existing, err := model.GetAgentByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorAgentNotFound.Sprintf(agentID)
	}
	if err := model.UpdateAgentByAgentID(ctx, agentID, map[string]interface{}{"updated_by": helpers.GetUserName(ctx)}); err != nil {
		return nil, err
	}
	if err := model.SoftDeleteAgentByAgentID(ctx, agentID); err != nil {
		return nil, err
	}
	deleted, err := model.GetAgentByAgentIDUnscoped(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if deleted == nil {
		return nil, components.ErrorAgentNotFound.Sprintf(agentID)
	}
	return deleted, nil
}

func GetDetail(ctx *gin.Context, agentID string) (*model.Agent, error) {
	agent, err := model.GetAgentByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, components.ErrorAgentNotFound.Sprintf(agentID)
	}
	return agent, nil
}

// ListByCallerAndRoute 管理列表：callerKey 为空跨 caller 全量；否则按路由前缀列出（含全部状态）。
func ListByCallerAndRoute(ctx *gin.Context, callerKey string, routeValues []string) ([]model.Agent, error) {
	if strings.TrimSpace(callerKey) == "" {
		return model.ListAllAgents(ctx)
	}
	exact, parents := route.SplitRouteExactAndParents(routeValues)
	prefixes := append([]string{exact}, parents...)
	return model.ListAgentsByCallerAndRoutes(ctx, callerKey, prefixes)
}

// ---------- Markdown 定义导入（OH 文件型 Agent 定义对应物） ----------

// agentMarkdownFrontmatter 是 SKILL.md 风格 Agent 定义文件的 frontmatter 字段。
type agentMarkdownFrontmatter struct {
	AgentKey        string   `yaml:"agent_key"`
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	CallerKey       string   `yaml:"caller_key"`
	RouteValues     []string `yaml:"route_values"`
	SystemPrompt    string   `yaml:"system_prompt"`
	ModelKey        string   `yaml:"model_key"`
	ModelVersion    string   `yaml:"model_version"`
	Tools           []string `yaml:"tools"`
	Skills          []string `yaml:"skills"`
	MaxSteps        int      `yaml:"max_steps"`
	MaxTokensPerRun int      `yaml:"max_tokens_per_run"`
	PermissionMode  string   `yaml:"permission_mode"`
}

// ImportFromMarkdown 解析「frontmatter + 正文」格式的 Agent 定义并入库：
// frontmatter 字段对应 CreateAgentReq（yaml 风格命名），正文（第二个 --- 之后）即 system_prompt；
// frontmatter 中的 caller_key / route_values 缺省时取请求体字段。
func ImportFromMarkdown(ctx *gin.Context, req *params.ImportAgentReq, createdBy string) (*model.Agent, error) {
	createReq, err := buildAgentCreateReqFromMarkdown(req)
	if err != nil {
		return nil, err
	}
	return CreateAgent(ctx, createReq, createdBy)
}

// UpsertFromMarkdownForBundle 是 Bundle 安装用的同名覆盖导入（P3）：
// 活跃同 caller+agent_key 行 → 覆盖定义字段（沿用 agent_id 与创建审计字段）；
// 软删行 → 复活覆盖（CreateAgent 内建路径）；不存在 → 全量校验新建。
// 返回覆盖前的整行快照 JSON（空串=本次新建），供 Bundle 卸载回滚。
// 普通 /agent/import 语义不变（同名仍报 ErrorAgentDuplicate）。
func UpsertFromMarkdownForBundle(ctx *gin.Context, req *params.ImportAgentReq, updatedBy string) (*model.Agent, string, error) {
	createReq, err := buildAgentCreateReqFromMarkdown(req)
	if err != nil {
		return nil, "", err
	}
	if existing, err := model.GetAgentByCallerAndAgentKey(ctx, createReq.CallerKey, createReq.AgentKey); err != nil {
		return nil, "", err
	} else if existing != nil {
		snapshot, _ := json.Marshal(existing)
		rv := createReq.RouteValues
		if rv == nil {
			rv = []string{}
		}
		routeValues, _ := json.Marshal(rv)
		toolsJSON, _ := json.Marshal(normalizeReferenceList(createReq.Tools))
		skillsJSON, _ := json.Marshal(normalizeReferenceList(createReq.Skills))
		if err := model.UpdateAgentByAgentID(ctx, existing.AgentID, agentDefinitionUpdates(createReq, routeValues, toolsJSON, skillsJSON, updatedBy)); err != nil {
			return nil, "", err
		}
		updated, err := model.GetAgentByAgentID(ctx, existing.AgentID)
		if err != nil {
			return nil, "", err
		}
		if updated == nil {
			return nil, "", components.ErrorAgentNotFound.Sprintf(existing.AgentID)
		}
		return updated, string(snapshot), nil
	}
	// 软删行占唯一键：CreateAgent 走复活；快照取软删行原值（含 deleted_at），卸载可还原为软删态。
	if deleted, err := model.GetAgentByCallerAndAgentKeyUnscoped(ctx, createReq.CallerKey, createReq.AgentKey); err != nil {
		return nil, "", err
	} else if deleted != nil && deleted.DeletedAt != 0 {
		snapshot, _ := json.Marshal(deleted)
		agent, err := CreateAgent(ctx, createReq, updatedBy)
		if err != nil {
			return nil, "", err
		}
		return agent, string(snapshot), nil
	}
	agent, err := CreateAgent(ctx, createReq, updatedBy)
	if err != nil {
		return nil, "", err
	}
	return agent, "", nil
}

// buildAgentCreateReqFromMarkdown 解析「frontmatter + 正文」为 CreateAgentReq
// （frontmatter 字段优先，caller_key/route_values/status 缺省回退请求体；正文即 system_prompt）。
func buildAgentCreateReqFromMarkdown(req *params.ImportAgentReq) (*params.CreateAgentReq, error) {
	frontmatter, body := splitAgentMarkdown(req.Markdown)
	if strings.TrimSpace(frontmatter) == "" {
		return nil, components.ErrorAgentImportInvalid.Sprintf("缺少 frontmatter（文件须以 --- 开头）")
	}
	var meta agentMarkdownFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return nil, components.ErrorAgentImportInvalid.Sprintf(fmt.Sprintf("frontmatter 解析失败: %v", err))
	}
	if strings.TrimSpace(body) == "" {
		return nil, components.ErrorAgentImportInvalid.Sprintf("正文为空（第二个 --- 之后应为子 Agent 系统提示词）")
	}

	callerKey := firstNonEmpty(meta.CallerKey, req.CallerKey)
	if callerKey == "" {
		return nil, components.ErrorAgentImportInvalid.Sprintf("caller_key 不能为空（frontmatter 或请求体至少提供一处）")
	}
	routeValues := meta.RouteValues
	if routeValues == nil {
		routeValues = req.RouteValues
	}
	status := req.Status
	if status == nil {
		enabled := 1
		status = &enabled
	}

	createReq := &params.CreateAgentReq{
		AgentKey:        meta.AgentKey,
		Name:            meta.Name,
		Description:     meta.Description,
		CallerKey:       callerKey,
		RouteValues:     routeValues,
		SystemPrompt:    strings.TrimSpace(body),
		ModelKey:        meta.ModelKey,
		ModelVersion:    meta.ModelVersion,
		Tools:           meta.Tools,
		Skills:          meta.Skills,
		MaxSteps:        meta.MaxSteps,
		MaxTokensPerRun: meta.MaxTokensPerRun,
		PermissionMode:  meta.PermissionMode,
		Status:          status,
	}
	if createReq.AgentKey == "" && createReq.Name != "" {
		createReq.AgentKey = sanitizeAgentKeyFromName(meta.Name)
	}
	return createReq, nil
}

// splitAgentMarkdown 拆分 frontmatter 与正文：文件以 --- 行开头，到下一个 --- 行结束。
func splitAgentMarkdown(markdown string) (frontmatter, body string) {
	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	trimmed := strings.TrimLeft(normalized, "\n")
	if !strings.HasPrefix(trimmed, "---\n") {
		return "", strings.TrimSpace(trimmed)
	}
	rest := trimmed[len("---\n"):]
	if idx := strings.Index(rest, "\n---"); idx >= 0 {
		frontmatter = rest[:idx]
		after := rest[idx+len("\n---"):]
		after = strings.TrimLeft(after, "\n")
		return frontmatter, after
	}
	return "", ""
}

func sanitizeAgentKeyFromName(name string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
		}
	}
	return strings.ToLower(builder.String())
}

func validateAgentKey(agentKey string) error {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return components.ErrorParamInvalid.Sprintf("agentKey 不能为空")
	}
	for _, r := range agentKey {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return components.ErrorAgentKeyInvalid.Sprintf(agentKey)
	}
	return nil
}

func validatePermissionMode(mode string) error {
	switch strings.TrimSpace(mode) {
	case "", model.AgentPermissionModeInherit, model.AgentPermissionModeAuto, model.AgentPermissionModeConfirm, model.AgentPermissionModeConfirmRisky:
		return nil
	default:
		return components.ErrorParamInvalid.Sprintf("permissionMode 仅支持 inherit/auto/confirm/confirm_risky")
	}
}

func normalizePermissionMode(mode string) string {
	if mode = strings.TrimSpace(mode); mode == "" {
		return model.AgentPermissionModeInherit
	}
	return mode
}

// validateReferenceList 校验 tools/skills 白名单：非空、去重、无空白项、数量受限。
func validateReferenceList(field string, values []string, limit int) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return components.ErrorParamInvalid.Sprintf("%s 白名单不允许空项", field)
		}
		if seen[value] {
			return components.ErrorParamInvalid.Sprintf("%s 白名单存在重复项: %s", field, value)
		}
		seen[value] = true
	}
	if len(values) > limit {
		return components.ErrorParamInvalid.Sprintf("%s 白名单最多 %d 项", field, limit)
	}
	return nil
}

func normalizeReferenceList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func resolveAgentStatus(status *int) (int, error) {
	if status == nil {
		return 0, components.ErrorParamInvalid.Sprintf("status不能为空")
	}
	if *status != 0 && *status != 1 {
		return 0, components.ErrorParamInvalid.Sprintf("status 仅支持 0 或 1")
	}
	return *status, nil
}

// resolveAgentReadOnly 归一只读执行域声明：nil=未指定按 0（否）；仅接受 0/1。
func resolveAgentReadOnly(readOnly *int) (int, error) {
	if readOnly == nil {
		return 0, nil
	}
	if *readOnly != 0 && *readOnly != 1 {
		return 0, components.ErrorParamInvalid.Sprintf("readOnly 仅支持 0 或 1")
	}
	return *readOnly, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseAgentRouteValues(routeValuesRaw string) []string {
	var routeValues []string
	_ = json.Unmarshal([]byte(routeValuesRaw), &routeValues)
	if routeValues == nil {
		routeValues = []string{}
	}
	return routeValues
}

func parseAgentReferenceJSON(raw string) []string {
	var values []string
	_ = json.Unmarshal([]byte(raw), &values)
	if values == nil {
		values = []string{}
	}
	return values
}

func ToAgentResp(a *model.Agent) params.AgentResp {
	return params.AgentResp{
		AgentID:        a.AgentID,
		AgentKey:       a.AgentKey,
		Name:           a.Name,
		Description:    a.Description,
		CallerKey:      a.CallerKey,
		RouteValues:    parseAgentRouteValues(a.RouteValues),
		SystemPrompt:   a.SystemPrompt,
		ModelKey:       a.ModelKey,
		ModelVersion:   a.ModelVersion,
		Tools:          parseAgentReferenceJSON(a.ToolsJSON),
		Skills:         parseAgentReferenceJSON(a.SkillsJSON),
		MaxSteps:       a.MaxSteps,
		MaxTokensPerRun: a.MaxTokensPerRun,
		ReadOnly:       a.ReadOnly,
		PermissionMode: a.PermissionMode,
		Status:         a.Status,
		CreatedBy:      a.CreatedBy,
		UpdatedBy:      a.UpdatedBy,
		CreatedAt:      formatAgentTime(a.CreatedAt),
		UpdatedAt:      formatAgentTime(a.UpdatedAt),
	}
}

func formatAgentTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
