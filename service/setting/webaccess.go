// webaccess.go 实现管理面板「网页检索」运行时设置：web_fetch / web_search 两个内置联网
// 工具的共用总开关在线覆盖。与 subagent/context/memory 同一套 DB 覆盖机制
//（tblLlmRuntimeSetting 单行 JSON、字段级覆盖、写后本进程立即生效、TTL 拉平多实例）。
//
// 一枚开关同时启停两个工具（状态天然同步，写入同一覆盖值）；面板只暴露总开关：
// web_fetch 的抓取预算/超时、web_search 的搜索服务连接参数（kind/base_url/api_key）
// 仍归 custom.yaml 管。web_search 处于「开关开启但连接参数不完整」的半配置状态时
// 该工具不会注册（web_fetch 不受影响），生效视图以 configured 字段显式暴露供面板警示。
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

// SettingKeyWeb 是联网能力（web_fetch + web_search 共用总开关）在 tblLlmRuntimeSetting
// 中的设置键（同时是 /setting/web 路由段）。
const SettingKeyWeb = "web"

// webAccessOverrideJSON 是 value_json 的存储结构（当前只有总开关）。
type webAccessOverrideJSON struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// GetWebSetting 返回联网能力的生效视图：effective（合并 yaml/默认值后的引擎实际消费口径，
// 两工具共用同一开关值）+ 逐字段 source + DB 覆盖原文。
func GetWebSetting(ctx *gin.Context) (*params.WebSettingResp, error) {
	override, updatedBy, updatedAt, err := loadWebAccessOverride(ctx)
	if err != nil {
		return nil, err
	}
	return buildWebSettingResp(override, updatedBy, updatedAt), nil
}

// UpdateWebSetting 更新联网总开关（同时作用于 web_fetch 与 web_search）：校验 → 合并现状
// → 整行写回 → 立即刷新覆盖快照（本进程下一个 run 即生效）。clearFields 用于把开关清回
// yaml/默认值（两工具一并回落各自的 yaml 开关）。
func UpdateWebSetting(ctx *gin.Context, req *params.UpdateWebSettingReq, userName string) (*params.WebSettingResp, error) {
	if err := validateWebAccessUpdate(req.ClearFields); err != nil {
		return nil, err
	}
	current, _, _, err := loadWebAccessOverride(ctx)
	if err != nil {
		return nil, err
	}
	merged := mergeWebAccessUpdate(current, req.Enabled, req.ClearFields)

	valueJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, components.ErrorRuntimeSettingInvalid.Wrap(err)
	}
	if err := model.UpsertRuntimeSetting(ctx, &model.RuntimeSetting{
		SettingKey: SettingKeyWeb,
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
	zlog.Infof(ctx, "[Setting] 联网总开关已更新(web_fetch+web_search): operator=%s, value=%s", userName, string(valueJSON))
	return buildWebSettingResp(merged, updatedBy, updatedAt), nil
}

// loadWebAccessOverride 读 DB 覆盖原文；行缺失/JSON 解析失败按「无覆盖」处理并告警。
func loadWebAccessOverride(ctx *gin.Context) (override *webAccessOverrideJSON, updatedBy, updatedAt string, err error) {
	row, err := model.GetRuntimeSettingByKey(ctx, SettingKeyWeb)
	if err != nil {
		return nil, "", "", err
	}
	if row == nil {
		return nil, "", "", nil
	}
	parsed := &webAccessOverrideJSON{}
	if strings.TrimSpace(row.ValueJSON) != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), parsed); err != nil {
			zlog.Warnf(ctx, "[Setting] %s 覆盖 JSON 解析失败(按无覆盖处理): err=%v", SettingKeyWeb, err)
			return nil, "", "", nil
		}
	}
	return parsed, row.UpdatedBy, row.UpdatedAt.Format("2006-01-02 15:04:05"), nil
}

// validateWebAccessUpdate 校验更新请求：clearFields 仅允许 enabled。
func validateWebAccessUpdate(clearFields []string) error {
	for _, field := range clearFields {
		if strings.TrimSpace(field) != "enabled" {
			return components.RuntimeSettingInvalidf("clearFields 含未知字段: %s", field)
		}
	}
	return nil
}

// mergeWebAccessUpdate 把请求合并进现状：clear 优先清空，否则覆盖非 nil 值。
// 布尔字段无法区分「未传」与「关闭」，显式传 false 即关闭；清除走 clearFields。
func mergeWebAccessUpdate(current *webAccessOverrideJSON, enabled *bool, clearFields []string) *webAccessOverrideJSON {
	merged := &webAccessOverrideJSON{}
	if current != nil {
		*merged = *current
	}
	clearing := false
	for _, field := range clearFields {
		if strings.TrimSpace(field) == "enabled" {
			clearing = true
		}
	}
	if clearing {
		merged.Enabled = nil
	} else if enabled != nil {
		merged.Enabled = enabled
	}
	return merged
}

// buildWebSettingResp 组装联网能力生效视图：effective.enabled 经 conf.EffectiveWebFetchConfig
// 计算（两工具共用同一覆盖，开关值恒一致，取任一即为合成口径）；configured 标识 web_search
// 连接参数（kind=searxng + base_url）完整性，半配置状态原样透出供面板警示。
// source 判定 override > yaml > default（两工具 yaml 开关可能不同步，source 按 web_fetch 基线）。
func buildWebSettingResp(override *webAccessOverrideJSON, updatedBy, updatedAt string) *params.WebSettingResp {
	effective := conf.EffectiveWebFetchConfig(toConfWebAccessOverride(override))
	resp := &params.WebSettingResp{Sources: map[string]params.SubAgentSettingFieldResp{}}
	resp.Effective.Enabled = effective.WebFetchEnabled()
	resp.Effective.Configured = conf.EffectiveWebSearchConfig(toConfWebAccessOverride(override)).WebSearchConfigured()

	resp.Sources["enabled"] = webAccessFieldSource(override, conf.CustomConf.LLM.React.WebFetch.Enabled, resp.Effective.Enabled)
	if override != nil {
		resp.Override = &struct {
			Enabled *bool `json:"enabled"`
		}{override.Enabled}
	}
	resp.UpdatedBy = updatedBy
	resp.UpdatedAt = updatedAt
	return resp
}

// webAccessFieldSource 判定总开关来源（override > yaml > default）；baseline 是清除覆盖后的
// 回落值（yaml 开关 > 默认 false），供面板预览「关掉覆盖再保存」的结果。
func webAccessFieldSource(override *webAccessOverrideJSON, yamlEnabled *bool, effectiveValue bool) params.SubAgentSettingFieldResp {
	source := "default"
	if yamlEnabled != nil {
		source = "yaml"
	}
	if override != nil && override.Enabled != nil {
		source = "override"
	}
	baseline := false
	if yamlEnabled != nil {
		baseline = *yamlEnabled
	}
	return params.SubAgentSettingFieldResp{Value: effectiveValue, Source: source, Baseline: baseline}
}

// toConfWebAccessOverride 把存储层覆盖结构转换为 conf 内存覆盖结构（nil 保持 nil）。
func toConfWebAccessOverride(override *webAccessOverrideJSON) *conf.WebAccessSettingOverride {
	if override == nil {
		return nil
	}
	return &conf.WebAccessSettingOverride{Enabled: override.Enabled}
}
