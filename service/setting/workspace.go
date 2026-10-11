// workspace.go 实现管理面板「检索代码和工作区」运行时设置：与其它设置同一套 DB 覆盖
// 机制（tblLlmRuntimeSetting 单行 JSON、字段级覆盖、写后本进程立即生效、TTL 拉平多实例）。
// 区别：该开关全局生效、不提供 app 维度覆盖——内置工具不按 app 隔离，检索代码和工作区
// 能力（load_runtime_code 及其挂载的 ws_* 检索工具）对全部 app 统一可见或统一隐藏。
package setting

import (
	"encoding/json"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// SettingKeyWorkspace 是检索代码和工作区开关在 tblLlmRuntimeSetting 中的设置键（全局行）。
const SettingKeyWorkspace = "workspace"

// workspaceOverrideJSON 是 value_json 的存储结构；字段语义与 conf.WorkspaceSettingOverride
// 完全一致（指针为 null = 未覆盖），此处独立声明以隔离存储格式与 conf 内存结构。
type workspaceOverrideJSON struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// GetWorkspaceSetting 返回检索代码和工作区开关的生效视图。
func GetWorkspaceSetting(ctx *gin.Context) (*params.WorkspaceSettingResp, error) {
	override, updatedBy, updatedAt, err := loadWorkspaceOverride(ctx)
	if err != nil {
		return nil, err
	}
	return buildWorkspaceSettingResp(override, updatedBy, updatedAt), nil
}

// UpdateWorkspaceSetting 更新检索代码和工作区开关：校验 → 合并现状 → 整行写回 →
// 立即刷新 conf 覆盖快照（本进程下一个 run 即生效）。clearFields 用于清回 yaml/默认值。
func UpdateWorkspaceSetting(ctx *gin.Context, req *params.UpdateWorkspaceSettingReq, userName string) (*params.WorkspaceSettingResp, error) {
	for _, field := range req.ClearFields {
		if strings.TrimSpace(field) != "enabled" {
			return nil, components.RuntimeSettingInvalidf("clearFields 含未知字段: %s", field)
		}
	}

	current, _, _, err := loadWorkspaceOverride(ctx)
	if err != nil {
		return nil, err
	}
	merged := mergeWorkspaceUpdate(current, req)

	valueJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, components.ErrorRuntimeSettingInvalid.Wrap(err)
	}
	if err := model.UpsertRuntimeSetting(ctx, &model.RuntimeSetting{
		SettingKey: SettingKeyWorkspace,
		AppKey:     "",
		ValueJSON:  string(valueJSON),
		UpdatedBy:  userName,
	}); err != nil {
		return nil, err
	}

	updatedBy, updatedAt := userName, time.Now().Format("2006-01-02 15:04:05")
	if err := refreshOverrideFromDB(ctx); err != nil {
		zlog.Warnf(ctx, "[Setting] 写后刷新覆盖快照失败(等待TTL兜底): err=%v", err)
		updatedBy, updatedAt = "", ""
	}
	zlog.Infof(ctx, "[Setting] 检索代码和工作区开关已更新: operator=%s, value=%s", userName, string(valueJSON))
	return buildWorkspaceSettingResp(merged, updatedBy, updatedAt), nil
}

// loadWorkspaceOverride 读全局覆盖原文；行缺失/解析失败按「无覆盖」处理并告警。
func loadWorkspaceOverride(ctx *gin.Context) (override *workspaceOverrideJSON, updatedBy, updatedAt string, err error) {
	row, err := model.GetRuntimeSettingByKey(ctx, SettingKeyWorkspace)
	if err != nil {
		return nil, "", "", err
	}
	if row == nil {
		return nil, "", "", nil
	}
	parsed := &workspaceOverrideJSON{}
	if strings.TrimSpace(row.ValueJSON) != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), parsed); err != nil {
			zlog.Warnf(ctx, "[Setting] workspace 覆盖 JSON 解析失败(按无覆盖处理): err=%v", err)
			return nil, "", "", nil
		}
	}
	return parsed, row.UpdatedBy, row.UpdatedAt.Format("2006-01-02 15:04:05"), nil
}

// mergeWorkspaceUpdate 逐字段合并：clear 优先清空，否则覆盖非 nil 值，其余保留现状。
func mergeWorkspaceUpdate(current *workspaceOverrideJSON, req *params.UpdateWorkspaceSettingReq) *workspaceOverrideJSON {
	merged := &workspaceOverrideJSON{}
	if current != nil {
		*merged = *current
	}
	for _, field := range req.ClearFields {
		if strings.TrimSpace(field) == "enabled" {
			merged.Enabled = nil
		}
	}
	if req.Enabled != nil {
		merged.Enabled = req.Enabled
	}
	return merged
}

// buildWorkspaceSettingResp 组装生效视图：effective 与引擎消费口径
//（GetReactRuntimeConfig().Workspace）完全一致。
func buildWorkspaceSettingResp(override *workspaceOverrideJSON, updatedBy, updatedAt string) *params.WorkspaceSettingResp {
	effective := conf.GetReactRuntimeConfig().Workspace.WorkspaceEnabled()
	resp := &params.WorkspaceSettingResp{
		Effective: struct {
			Enabled             bool `json:"enabled"`
			ResolversConfigured bool `json:"resolversConfigured"`
		}{
			Enabled:             effective,
			ResolversConfigured: conf.CustomConf.LLM.React.Workspace.WorkspaceResolversConfigured(),
		},
		Sources: map[string]params.WorkspaceSettingFieldResp{},
	}
	yamlSet := conf.CustomConf.LLM.React.Workspace.Enabled != nil
	source := "default"
	if yamlSet {
		source = "yaml"
	}
	if override != nil && override.Enabled != nil {
		source = "override"
	}
	resp.Sources["enabled"] = params.WorkspaceSettingFieldResp{Value: effective, Source: source}
	if override != nil {
		resp.Override = &struct {
			Enabled *bool `json:"enabled"`
		}{override.Enabled}
	}
	resp.UpdatedBy = updatedBy
	resp.UpdatedAt = updatedAt
	return resp
}

// toConfWorkspaceOverride 把存储层覆盖结构转换为 conf 内存覆盖结构。
func toConfWorkspaceOverride(override *workspaceOverrideJSON) *conf.WorkspaceSettingOverride {
	if override == nil {
		return nil
	}
	return &conf.WorkspaceSettingOverride{
		Enabled: override.Enabled,
	}
}
