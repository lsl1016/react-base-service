// Package bundle 实现 Agent Bundle 插件包安装（P3）：manifest + agents/ + skills/ + .mcp.json
// 打包展开写入各注册表；同名覆盖（重装 = 先卸载还原再装新），卸载按资源清单回滚
//（新建资源软删、覆盖资源按安装前快照整行恢复）。参照 OpenHands plugin loader 的
//「安装即注入多类资源」语义；来源限白名单前缀（内部 git / 本地路径），不做公网 marketplace。
package bundle

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	agentService "react-base-service/service/agent"
	"react-base-service/service/mcpclient"
	skillService "react-base-service/service/skill"

	"github.com/gin-gonic/gin"
)

// Install 安装 bundle：拉取解析 →（重装时先卸载旧装）→ 逐资源应用并记录快照 → 失败自动回滚清理。
func Install(ctx *gin.Context, req *params.BundleInstallReq, installedBy string) (*model.Bundle, error) {
	cfg := conf.GetReactRuntimeConfig().Bundle
	if !cfg.BundleEnabled() {
		return nil, components.ErrorParamInvalid.Sprintf("bundle 未启用（llm.react.bundle.enabled）")
	}
	if err := validateBundleSource(cfg, req.Source); err != nil {
		return nil, err
	}
	appKey := strings.TrimSpace(req.AppKey)
	if appKey == "" {
		return nil, components.ErrorParamInvalid.Sprintf("appKey 不能为空（bundle 内 agent/skill 的默认归属）")
	}

	content, err := fetchBundle(cfg, req.Source, req.Ref, req.RepoPath)
	if err != nil {
		return nil, err
	}

	// 重装同名：先卸载旧装（还原到旧装前状态），保证新装快照正确、旧包独有资源也被还原。
	if existing, err := model.GetBundleByName(ctx, content.Manifest.Name); err != nil {
		return nil, err
	} else if existing != nil {
		if err := Uninstall(ctx, &params.BundleUninstallReq{Name: content.Manifest.Name}); err != nil {
			return nil, components.ErrorBundleImportInvalid.Sprintf("重装前卸载旧版本失败: %v", err)
		}
	}

	// bundle 行（软删同名行占唯一键：复活复用该行）。
	bundleID := "bundle_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405")+"-"+fmt.Sprintf("%x", time.Now().UnixNano()), "-", "")
	bundleRow := &model.Bundle{
		BundleID:     bundleID,
		Name:         content.Manifest.Name,
		Version:      content.Manifest.Version,
		Source:       strings.TrimSpace(req.Source),
		ResolvedRef:  content.commit,
		ManifestJSON: content.ManifestRaw,
		InstalledBy:  installedBy,
	}
	if softDeleted, err := model.GetBundleByNameUnscoped(ctx, content.Manifest.Name); err != nil {
		return nil, err
	} else if softDeleted != nil && softDeleted.DeletedAt != 0 {
		if err := model.UpdateBundleByBundleIDUnscoped(ctx, softDeleted.BundleID, map[string]interface{}{
			"version":       bundleRow.Version,
			"source":        bundleRow.Source,
			"resolved_ref":  bundleRow.ResolvedRef,
			"manifest_json": bundleRow.ManifestJSON,
			"installed_by":  bundleRow.InstalledBy,
			"deleted_at":    0,
		}); err != nil {
			return nil, err
		}
		bundleRow.BundleID = softDeleted.BundleID
		if err := model.HardDeleteBundleResourcesByBundleID(ctx, bundleRow.BundleID); err != nil {
			return nil, err
		}
	} else if err := model.CreateBundle(ctx, bundleRow); err != nil {
		return nil, err
	}

	// 逐资源应用；任一失败 → 自动卸载本次安装（不留半装状态）。
	if err := applyBundleResources(ctx, bundleRow.BundleID, content, appKey, req, installedBy); err != nil {
		if uninstallErr := uninstallByBundleID(ctx, bundleRow.BundleID); uninstallErr != nil {
			zlog.Errorf(ctx, "[Bundle] 安装失败后的自动清理也失败: bundle=%s err=%v（原错误: %v）", bundleRow.BundleID, uninstallErr, err)
		}
		return nil, err
	}

	installed, err := model.GetBundleByName(ctx, content.Manifest.Name)
	if err != nil || installed == nil {
		return bundleRow, err
	}
	zlog.Infof(ctx, "[Bundle] 安装完成: name=%s version=%s bundleId=%s agents=%d skills=%d mcp=%t",
		content.Manifest.Name, content.Manifest.Version, bundleRow.BundleID, len(content.Agents), len(content.Skills), content.McpJSON != "")
	return installed, nil
}

// applyBundleResources 按序应用资源并即时落资源清单行（失败清理依赖这些行）。
func applyBundleResources(ctx *gin.Context, bundleID string, content *bundleContent, appKey string, req *params.BundleInstallReq, installedBy string) error {
	record := func(resourceType, resourceKey, resourceID, previousJSON string) error {
		return model.CreateBundleResources(ctx, []model.BundleResource{{
			BundleID: bundleID, ResourceType: resourceType, ResourceKey: resourceKey,
			ResourceID: resourceID, PreviousStateJSON: previousJSON,
		}})
	}

	for _, file := range content.Agents {
		agent, previous, err := agentService.UpsertFromMarkdownForBundle(ctx, &params.ImportAgentReq{
			AppKey:   appKey,
			RouteValues: req.RouteValues,
			Markdown:    file.Markdown,
		}, installedBy)
		if err != nil {
			return components.ErrorBundleImportInvalid.Sprintf("agents/%s.md 应用失败: %v", file.Name, err)
		}
		if err := record(model.BundleResourceTypeAgent, agent.AgentKey, agent.AgentID, previous); err != nil {
			return err
		}
	}

	for _, file := range content.Skills {
		skill, previous, err := skillService.UpsertFromMarkdownForBundle(ctx, &params.ImportSkillReq{
			AppKey:   appKey,
			RouteValues: req.RouteValues,
			Markdown:    file.Markdown,
		}, installedBy)
		if err != nil {
			return components.ErrorBundleImportInvalid.Sprintf("skills/%s/SKILL.md 应用失败: %v", file.Name, err)
		}
		if err := record(model.BundleResourceTypeSkill, skill.Name, skill.SkillID, previous); err != nil {
			return err
		}
	}

	if content.McpJSON != "" {
		servers, err := parseBundleMcpServers(content.McpJSON)
		if err != nil {
			return err
		}
		for _, server := range servers {
			registered, previous, err := registerBundleMcpServer(ctx, server, appKey, installedBy)
			if err != nil {
				return components.ErrorBundleImportInvalid.Sprintf(".mcp.json 服务器 %s 注册失败: %v", server.Name, err)
			}
			if err := record(model.BundleResourceTypeMcpServer, registered.Name, registered.ServerID, previous); err != nil {
				return err
			}
		}
	}
	return nil
}

// Uninstall 卸载 bundle：按资源清单逆序回滚（新建软删 / 覆盖按快照恢复），最后软删 bundle 行。
func Uninstall(ctx *gin.Context, req *params.BundleUninstallReq) error {
	name := strings.TrimSpace(req.Name)
	bundleRow, err := model.GetBundleByName(ctx, name)
	if err != nil {
		return err
	}
	if bundleRow == nil {
		return components.ErrorBundleNotFound.Sprintf(name)
	}
	if err := uninstallByBundleID(ctx, bundleRow.BundleID); err != nil {
		return err
	}
	zlog.Infof(ctx, "[Bundle] 卸载完成: name=%s bundleId=%s", name, bundleRow.BundleID)
	return nil
}

func uninstallByBundleID(ctx *gin.Context, bundleID string) error {
	resources, err := model.ListBundleResourcesByBundleID(ctx, bundleID)
	if err != nil {
		return err
	}
	// 逆序回滚：后应用的资源先还原（mcp → skills → agents，与安装顺序相反）。
	for i := len(resources) - 1; i >= 0; i-- {
		resource := resources[i]
		var rollbackErr error
		switch resource.ResourceType {
		case model.BundleResourceTypeAgent:
			rollbackErr = rollbackAgentResource(ctx, resource)
		case model.BundleResourceTypeSkill:
			rollbackErr = rollbackSkillResource(ctx, resource)
		case model.BundleResourceTypeMcpServer:
			rollbackErr = rollbackMcpServerResource(ctx, resource)
		default:
			rollbackErr = components.ErrorBundleImportInvalid.Sprintf("未知资源类型: %s", resource.ResourceType)
		}
		if rollbackErr != nil {
			return components.ErrorBundleImportInvalid.Sprintf("回滚 %s[%s] 失败: %v", resource.ResourceType, resource.ResourceKey, rollbackErr)
		}
	}
	return model.SoftDeleteBundleByBundleID(ctx, bundleID)
}

// ---------- 资源回滚（新建软删 / 覆盖按快照整行恢复） ----------

func rollbackAgentResource(ctx *gin.Context, resource model.BundleResource) error {
	if resource.PreviousStateJSON == "" {
		return model.SoftDeleteAgentByAgentID(ctx, resource.ResourceID)
	}
	var restored model.Agent
	if err := json.Unmarshal([]byte(resource.PreviousStateJSON), &restored); err != nil {
		return err
	}
	updates := map[string]interface{}{
		"agent_key":         restored.AgentKey,
		"name":              restored.Name,
		"description":       restored.Description,
		"app_key":        restored.AppKey,
		"route_values":      restored.RouteValues,
		"system_prompt":     restored.SystemPrompt,
		"model_key":         restored.ModelKey,
		"model_version":     restored.ModelVersion,
		"tools_json":        restored.ToolsJSON,
		"skills_json":       restored.SkillsJSON,
		"max_steps":         restored.MaxSteps,
		"max_tokens_per_run": restored.MaxTokensPerRun,
		"permission_mode":   restored.PermissionMode,
		"status":            restored.Status,
		"created_by":        restored.CreatedBy,
		"updated_by":        restored.UpdatedBy,
		"deleted_at":        restored.DeletedAt,
	}
	return model.UpdateAgentByAgentIDUnscoped(ctx, resource.ResourceID, updates)
}

func rollbackSkillResource(ctx *gin.Context, resource model.BundleResource) error {
	if resource.PreviousStateJSON == "" {
		return model.SoftDeleteSkillBySkillID(ctx, resource.ResourceID)
	}
	var restored model.Skill
	if err := json.Unmarshal([]byte(resource.PreviousStateJSON), &restored); err != nil {
		return err
	}
	updates := map[string]interface{}{
		"name":               restored.Name,
		"description":        restored.Description,
		"trigger_condition":  restored.TriggerCondition,
		"forbidden_condition": restored.ForbiddenCondition,
		"execution_steps":    restored.ExecutionSteps,
		"business_context":   restored.BusinessContext,
		"prompt_supplement":  restored.PromptSupplement,
		"triggers_json":      restored.TriggersJSON,
		"content":            restored.Content,
		"app_key":         restored.AppKey,
		"route_values":       restored.RouteValues,
		"is_default":         restored.IsDefault,
		"status":             restored.Status,
		"created_by":         restored.CreatedBy,
		"updated_by":         restored.UpdatedBy,
		"deleted_at":         restored.DeletedAt,
	}
	return model.UpdateSkillBySkillIDUnscoped(ctx, resource.ResourceID, updates)
}

func rollbackMcpServerResource(ctx *gin.Context, resource model.BundleResource) error {
	if resource.PreviousStateJSON == "" {
		// 本次安装新建：与 /react/mcp 删除同款清理（停客户端 + 清注册工具 + 软删行）。
		_ = mcpclient.RemoveServer(resource.ResourceKey)
		if _, err := mcpclient.RemoveRegistryTools(ctx, resource.ResourceKey); err != nil {
			return err
		}
		return model.SoftDeleteMcpServerByServerID(ctx, resource.ResourceID)
	}
	var restored model.McpServer
	if err := json.Unmarshal([]byte(resource.PreviousStateJSON), &restored); err != nil {
		return err
	}
	updates := map[string]interface{}{
		"name":               restored.Name,
		"kind":               restored.Kind,
		"endpoint":           restored.Endpoint,
		"headers":            restored.Headers,
		"env":                restored.Env,
		"timeout_ms":         restored.TimeoutMs,
		"description":        restored.Description,
		"status":             restored.Status,
		"last_check_status":  restored.LastCheckStatus,
		"last_check_message": restored.LastCheckMessage,
		"app_key":         restored.AppKey,
		"created_by":         restored.CreatedBy,
		"updated_by":         restored.UpdatedBy,
		"deleted_at":         restored.DeletedAt,
	}
	if err := model.UpdateMcpServerByServerIDUnscoped(ctx, resource.ResourceID, updates); err != nil {
		return err
	}
	// 快照是软删行 → 回到软删态，停客户端并清理注册工具；活跃行 → 按快照重连。
	if restored.DeletedAt != 0 {
		_ = mcpclient.RemoveServer(restored.Name)
		if _, err := mcpclient.RemoveRegistryTools(ctx, restored.Name); err != nil {
			return err
		}
		return nil
	}
	return reconnectMcpServer(ctx, &restored)
}

// ---------- Bundle 内 MCP 注册（与 /react/mcp 同款约束：仅 url 形式 HTTP MCP） ----------

type bundleMcpDraft struct {
	Name    string
	URL     string
	Headers string // JSON 序列化后的请求头
}

// parseBundleMcpServers 解析 .mcp.json（{"mcpServers": {name: {url, headers}}}，
// 也接受去掉外层的单个映射）；command/args 形式直接拒绝。
func parseBundleMcpServers(raw string) ([]bundleMcpDraft, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, components.ErrorBundleImportInvalid.Sprintf(".mcp.json 不是合法 JSON: %v", err)
	}
	if serversRaw, ok := payload["mcpServers"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(serversRaw, &inner); err != nil {
			return nil, components.ErrorBundleImportInvalid.Sprintf("mcpServers 必须是 {名称: 配置} 映射: %v", err)
		}
		payload = inner
	}
	drafts := make([]bundleMcpDraft, 0, len(payload))
	for name, item := range payload {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			return nil, components.ErrorBundleImportInvalid.Sprintf("mcpServers 存在空名称条目")
		}
		var entry struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Command string            `json:"command"`
		}
		if err := json.Unmarshal(item, &entry); err != nil {
			return nil, components.ErrorBundleImportInvalid.Sprintf("服务器 %s 配置解析失败: %v", trimmedName, err)
		}
		if strings.TrimSpace(entry.Command) != "" {
			return nil, components.ErrorBundleImportInvalid.Sprintf("服务器 %s 是 command/args 形式配置：基座仅支持 url 形式的 HTTP MCP", trimmedName)
		}
		entry.URL = strings.TrimSpace(entry.URL)
		if !strings.HasPrefix(entry.URL, "http://") && !strings.HasPrefix(entry.URL, "https://") {
			return nil, components.ErrorBundleImportInvalid.Sprintf("服务器 %s 缺少 url 或 scheme 非 http/https", trimmedName)
		}
		headersJSON := ""
		if len(entry.Headers) > 0 {
			data, err := json.Marshal(entry.Headers)
			if err != nil {
				return nil, components.ErrorBundleImportInvalid.Sprintf("服务器 %s headers 序列化失败: %v", trimmedName, err)
			}
			if len(data) > 4<<10 {
				return nil, components.ErrorBundleImportInvalid.Sprintf("服务器 %s headers 超过 4KB 上限", trimmedName)
			}
			headersJSON = string(data)
		}
		drafts = append(drafts, bundleMcpDraft{Name: trimmedName, URL: entry.URL, Headers: headersJSON})
	}
	if len(drafts) == 0 {
		return nil, components.ErrorBundleImportInvalid.Sprintf(".mcp.json 中没有可登记的 MCP 服务器")
	}
	return drafts, nil
}

// registerBundleMcpServer 注册/覆盖一个 MCP 服务器（同名覆盖沿用 server_id），
// 返回覆盖前整行快照（空=新建）。仅同步属主 app 的工具副本，不建额外绑定。
func registerBundleMcpServer(ctx *gin.Context, draft bundleMcpDraft, appKey, installedBy string) (*model.McpServer, string, error) {
	updates := map[string]interface{}{
		"kind":        "http",
		"endpoint":    draft.URL,
		"headers":     draft.Headers,
		"timeout_ms":  30000,
		"description": "installed by agent bundle",
		"status":      1,
		"updated_by":  installedBy,
	}
	if existing, err := model.GetMcpServerByName(ctx, draft.Name); err != nil {
		return nil, "", err
	} else if existing != nil {
		snapshot, _ := json.Marshal(existing)
		if err := model.UpdateMcpServerByServerID(ctx, existing.ServerID, updates); err != nil {
			return nil, "", err
		}
		refreshed, err := model.GetMcpServerByServerID(ctx, existing.ServerID)
		if err != nil || refreshed == nil {
			refreshed = existing
		}
		if err := reconnectMcpServer(ctx, refreshed); err != nil {
			zlog.Warnf(ctx, "[Bundle] MCP 重连失败(不阻断安装): name=%s err=%v", draft.Name, err)
		}
		return refreshed, string(snapshot), nil
	}
	server := &model.McpServer{
		ServerID:    "mcpserver_" + draft.Name + "_" + fmt.Sprintf("%x", time.Now().UnixNano()),
		Name:        draft.Name,
		Kind:        "http",
		Endpoint:    draft.URL,
		Headers:     draft.Headers,
		TimeoutMs:   30000,
		Description: "installed by agent bundle",
		Status:      1,
		AppKey:   appKey,
		CreatedBy:   installedBy,
		UpdatedBy:   installedBy,
	}
	// 软删的同名行仍占 uk_name：复活复用该行。
	if softDeleted, err := model.GetMcpServerByNameUnscoped(ctx, draft.Name); err != nil {
		return nil, "", err
	} else if softDeleted != nil {
		snapshot, _ := json.Marshal(softDeleted)
		revive := map[string]interface{}{
			"server_id": server.ServerID, "kind": server.Kind, "endpoint": server.Endpoint,
			"headers": server.Headers, "env": server.Env, "timeout_ms": server.TimeoutMs,
			"description": server.Description, "status": server.Status, "app_key": server.AppKey,
			"created_by": server.CreatedBy, "updated_by": server.UpdatedBy,
			"last_check_status": "unknown", "last_check_message": "", "deleted_at": 0,
		}
		if err := model.ReviveMcpServerByName(ctx, draft.Name, revive); err != nil {
			return nil, "", err
		}
		revived, err := model.GetMcpServerByServerID(ctx, server.ServerID)
		if err != nil || revived == nil {
			revived = server
		}
		if err := reconnectMcpServer(ctx, revived); err != nil {
			zlog.Warnf(ctx, "[Bundle] MCP 重连失败(不阻断安装): name=%s err=%v", draft.Name, err)
		}
		return revived, string(snapshot), nil
	}
	if err := model.CreateMcpServer(ctx, server); err != nil {
		return nil, "", err
	}
	if err := reconnectMcpServer(ctx, server); err != nil {
		zlog.Warnf(ctx, "[Bundle] MCP 连接失败(不阻断安装): name=%s err=%v", draft.Name, err)
	}
	return server, "", nil
}

// reconnectMcpServer 拉起客户端并把工具同步到属主 app 名下（失败返回 error，调用方决定是否阻断）。
func reconnectMcpServer(ctx *gin.Context, server *model.McpServer) error {
	cfg, err := mcpclient.McpServerConfig(*server)
	if err != nil {
		return err
	}
	if _, err := mcpclient.EnsureServer(*cfg); err != nil {
		return err
	}
	_, err = mcpclient.SyncServerRegistryScoped(server.AppKey, server.Name, true)
	return err
}

// ---------- 列表 ----------

// ListInstalled 列出已安装 bundle 及资源计数。
func ListInstalled(ctx *gin.Context) ([]params.BundleListItemResp, error) {
	bundles, err := model.ListBundles(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]params.BundleListItemResp, 0, len(bundles))
	for i := range bundles {
		resources, err := model.ListBundleResourcesByBundleID(ctx, bundles[i].BundleID)
		if err != nil {
			return nil, err
		}
		counts := map[string]int{}
		for _, resource := range resources {
			counts[resource.ResourceType]++
		}
		items = append(items, params.BundleListItemResp{
			BundleID:         bundles[i].BundleID,
			Name:             bundles[i].Name,
			Version:          bundles[i].Version,
			Description:      bundleDescription(bundles[i].ManifestJSON),
			Source:           bundles[i].Source,
			ResolvedRef:      bundles[i].ResolvedRef,
			AgentCount:       counts[model.BundleResourceTypeAgent],
			SkillCount:       counts[model.BundleResourceTypeSkill],
			McpServerCount:   counts[model.BundleResourceTypeMcpServer],
			InstalledBy:      bundles[i].InstalledBy,
			InstalledAt:      bundles[i].CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	return items, nil
}

func bundleDescription(manifestJSON string) string {
	var manifest bundleManifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return ""
	}
	return manifest.Description
}
