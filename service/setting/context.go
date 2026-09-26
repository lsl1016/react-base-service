// context.go 实现管理面板「上下文与压缩」运行时设置：与 subagent 同一套 DB 覆盖机制
//（tblLlmRuntimeSetting 单行 JSON、字段级覆盖、写后本进程立即生效、TTL 拉平多实例）。
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

// SettingKeyContext 是上下文压缩策略在 tblLlmRuntimeSetting 中的设置键。
const SettingKeyContext = "context"

// 校验范围：与 service/llmmodel 参数 schema、conf 默认值口径一致（写侧强校验）。
const (
	contextTokenTriggerMin        = 8192
	contextTokenTriggerMax        = 2_000_000
	contextTokenTargetMin         = 4096
	contextTokenTargetMax         = 2_000_000
	contextSummaryLimitMin        = 1000
	contextSummaryLimitMax        = 64000
	contextOutputReserveMin       = 1024
	contextOutputReserveMax       = 200_000
	contextBufferMin              = 0
	contextBufferMax              = 100_000
	contextMicrocompactKeepMin    = 1
	contextMicrocompactKeepMax    = 50
)

// contextCompactOverrideJSON 是 value_json 的存储结构；字段语义与
// conf.ContextCompactSettingOverride 完全一致（指针为 null = 未覆盖）。
type contextCompactOverrideJSON struct {
	Enabled                *bool `json:"enabled,omitempty"`
	TokenTrigger           *int  `json:"tokenTrigger,omitempty"`
	TokenTarget            *int  `json:"tokenTarget,omitempty"`
	SummaryLimit           *int  `json:"summaryLimit,omitempty"`
	OutputReserveTokens    *int  `json:"outputReserveTokens,omitempty"`
	BufferTokens           *int  `json:"bufferTokens,omitempty"`
	MicrocompactEnabled    *bool `json:"microcompactEnabled,omitempty"`
	MicrocompactKeepRecent *int  `json:"microcompactKeepRecent,omitempty"`
}

// GetContextCompactSetting 返回上下文压缩策略的生效视图。
func GetContextCompactSetting(ctx *gin.Context) (*params.ContextCompactSettingResp, error) {
	override, updatedBy, updatedAt, err := loadContextCompactOverride(ctx)
	if err != nil {
		return nil, err
	}
	return buildContextCompactSettingResp(override, updatedBy, updatedAt), nil
}

// UpdateContextCompactSetting 更新上下文压缩策略：校验 → 合并现状 → 整行写回 → 立即刷新覆盖快照。
func UpdateContextCompactSetting(ctx *gin.Context, req *params.UpdateContextCompactSettingReq, userName string) (*params.ContextCompactSettingResp, error) {
	if err := validateContextCompactUpdate(req); err != nil {
		return nil, err
	}

	current, _, _, err := loadContextCompactOverride(ctx)
	if err != nil {
		return nil, err
	}
	merged := mergeContextCompactUpdate(current, req)

	valueJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, components.ErrorRuntimeSettingInvalid.Wrap(err)
	}
	if err := model.UpsertRuntimeSetting(ctx, &model.RuntimeSetting{
		SettingKey: SettingKeyContext,
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
	zlog.Infof(ctx, "[Setting] 上下文压缩策略已更新: operator=%s, value=%s", userName, string(valueJSON))
	return buildContextCompactSettingResp(merged, updatedBy, updatedAt), nil
}

// loadContextCompactOverride 读 DB 覆盖原文；行缺失/JSON 解析失败按「无覆盖」处理并告警。
func loadContextCompactOverride(ctx *gin.Context) (override *contextCompactOverrideJSON, updatedBy, updatedAt string, err error) {
	row, err := model.GetRuntimeSettingByKey(ctx, SettingKeyContext)
	if err != nil {
		return nil, "", "", err
	}
	if row == nil {
		return nil, "", "", nil
	}
	parsed := &contextCompactOverrideJSON{}
	if strings.TrimSpace(row.ValueJSON) != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), parsed); err != nil {
			zlog.Warnf(ctx, "[Setting] context 覆盖 JSON 解析失败(按无覆盖处理): err=%v", err)
			return nil, "", "", nil
		}
	}
	return parsed, row.UpdatedBy, row.UpdatedAt.Format("2006-01-02 15:04:05"), nil
}

// validateContextCompactUpdate 校验更新请求：覆盖值范围 + clearFields 字段名合法。
func validateContextCompactUpdate(req *params.UpdateContextCompactSettingReq) error {
	clearing := make(map[string]bool, len(req.ClearFields))
	for _, field := range req.ClearFields {
		switch strings.TrimSpace(field) {
		case "enabled", "tokenTrigger", "tokenTarget", "summaryLimit",
			"outputReserveTokens", "bufferTokens", "microcompactEnabled", "microcompactKeepRecent":
			clearing[strings.TrimSpace(field)] = true
		default:
			return components.ErrorRuntimeSettingInvalid.Sprintf("clearFields 含未知字段: %s", field)
		}
	}
	check := func(field string, value, min, max int) error {
		if clearing[field] {
			return nil
		}
		if value < min || value > max {
			return components.ErrorRuntimeSettingInvalid.Sprintf("%s 取值范围 %d-%d", field, min, max)
		}
		return nil
	}
	if req.TokenTrigger != nil {
		if err := check("tokenTrigger", *req.TokenTrigger, contextTokenTriggerMin, contextTokenTriggerMax); err != nil {
			return err
		}
	}
	if req.TokenTarget != nil {
		if err := check("tokenTarget", *req.TokenTarget, contextTokenTargetMin, contextTokenTargetMax); err != nil {
			return err
		}
	}
	if req.SummaryLimit != nil {
		if err := check("summaryLimit", *req.SummaryLimit, contextSummaryLimitMin, contextSummaryLimitMax); err != nil {
			return err
		}
	}
	if req.OutputReserveTokens != nil {
		if err := check("outputReserveTokens", *req.OutputReserveTokens, contextOutputReserveMin, contextOutputReserveMax); err != nil {
			return err
		}
	}
	if req.BufferTokens != nil {
		if err := check("bufferTokens", *req.BufferTokens, contextBufferMin, contextBufferMax); err != nil {
			return err
		}
	}
	if req.MicrocompactKeepRecent != nil {
		if err := check("microcompactKeepRecent", *req.MicrocompactKeepRecent, contextMicrocompactKeepMin, contextMicrocompactKeepMax); err != nil {
			return err
		}
	}
	return nil
}

// mergeContextCompactUpdate 把请求合并进现状：clear 优先清空，否则覆盖非 nil 值。
func mergeContextCompactUpdate(current *contextCompactOverrideJSON, req *params.UpdateContextCompactSettingReq) *contextCompactOverrideJSON {
	merged := &contextCompactOverrideJSON{}
	if current != nil {
		*merged = *current
	}
	clearing := make(map[string]bool, len(req.ClearFields))
	for _, field := range req.ClearFields {
		clearing[strings.TrimSpace(field)] = true
	}
	// 布尔字段无法区分「未传」与「关闭」，显式传 false 即关闭；清除走 clearFields。
	if clearing["enabled"] {
		merged.Enabled = nil
	} else if req.Enabled != nil {
		merged.Enabled = req.Enabled
	}
	if clearing["microcompactEnabled"] {
		merged.MicrocompactEnabled = nil
	} else if req.MicrocompactEnabled != nil {
		merged.MicrocompactEnabled = req.MicrocompactEnabled
	}
	if clearing["tokenTrigger"] {
		merged.TokenTrigger = nil
	} else if req.TokenTrigger != nil {
		merged.TokenTrigger = req.TokenTrigger
	}
	if clearing["tokenTarget"] {
		merged.TokenTarget = nil
	} else if req.TokenTarget != nil {
		merged.TokenTarget = req.TokenTarget
	}
	if clearing["summaryLimit"] {
		merged.SummaryLimit = nil
	} else if req.SummaryLimit != nil {
		merged.SummaryLimit = req.SummaryLimit
	}
	if clearing["outputReserveTokens"] {
		merged.OutputReserveTokens = nil
	} else if req.OutputReserveTokens != nil {
		merged.OutputReserveTokens = req.OutputReserveTokens
	}
	if clearing["bufferTokens"] {
		merged.BufferTokens = nil
	} else if req.BufferTokens != nil {
		merged.BufferTokens = req.BufferTokens
	}
	if clearing["microcompactKeepRecent"] {
		merged.MicrocompactKeepRecent = nil
	} else if req.MicrocompactKeepRecent != nil {
		merged.MicrocompactKeepRecent = req.MicrocompactKeepRecent
	}
	return merged
}

// buildContextCompactSettingResp 组装生效视图：effective 经 conf.EffectiveContextCompactConfig
// 计算（与引擎消费口径一致）；source 逐字段判定（override > yaml > default）。
func buildContextCompactSettingResp(override *contextCompactOverrideJSON, updatedBy, updatedAt string) *params.ContextCompactSettingResp {
	effective := conf.EffectiveContextCompactConfig(toConfContextCompactOverride(override))
	resp := &params.ContextCompactSettingResp{Sources: map[string]params.SubAgentSettingFieldResp{}}
	resp.Effective.Enabled = effective.Enabled != nil && *effective.Enabled
	resp.Effective.TokenTrigger = effective.TokenTrigger
	resp.Effective.TokenTarget = effective.TokenTarget
	resp.Effective.SummaryLimit = effective.SummaryLimit
	resp.Effective.OutputReserveTokens = effective.OutputReserveTokens
	resp.Effective.BufferTokens = effective.BufferTokens
	resp.Effective.MicrocompactEnabled = effective.MicrocompactEnabled != nil && *effective.MicrocompactEnabled
	resp.Effective.MicrocompactKeepRecent = effective.MicrocompactKeepRecent

	yamlCfg := conf.CustomConf.LLM.React.ContextCompact
	setSource := func(field string, hasOverride, hasYaml bool, value interface{}) {
		source := "default"
		if hasYaml {
			source = "yaml"
		}
		if hasOverride {
			source = "override"
		}
		resp.Sources[field] = params.SubAgentSettingFieldResp{Value: value, Source: source}
	}
	setSource("enabled", override != nil && override.Enabled != nil, yamlCfg.Enabled != nil, resp.Effective.Enabled)
	setSource("tokenTrigger", overrideFieldHas(override, "tokenTrigger"), yamlCfg.TokenTrigger > 0, resp.Effective.TokenTrigger)
	setSource("tokenTarget", overrideFieldHas(override, "tokenTarget"), yamlCfg.TokenTarget > 0, resp.Effective.TokenTarget)
	setSource("summaryLimit", overrideFieldHas(override, "summaryLimit"), yamlCfg.SummaryLimit > 0, resp.Effective.SummaryLimit)
	setSource("outputReserveTokens", overrideFieldHas(override, "outputReserveTokens"), yamlCfg.OutputReserveTokens > 0, resp.Effective.OutputReserveTokens)
	setSource("bufferTokens", overrideFieldHas(override, "bufferTokens"), yamlCfg.BufferTokens > 0, resp.Effective.BufferTokens)
	setSource("microcompactEnabled", override != nil && override.MicrocompactEnabled != nil, yamlCfg.MicrocompactEnabled != nil, resp.Effective.MicrocompactEnabled)
	setSource("microcompactKeepRecent", overrideFieldHas(override, "microcompactKeepRecent"), yamlCfg.MicrocompactKeepRecent > 0, resp.Effective.MicrocompactKeepRecent)

	if override != nil {
		resp.Override = &struct {
			Enabled                *bool `json:"enabled"`
			TokenTrigger           *int  `json:"tokenTrigger"`
			TokenTarget            *int  `json:"tokenTarget"`
			SummaryLimit           *int  `json:"summaryLimit"`
			OutputReserveTokens    *int  `json:"outputReserveTokens"`
			BufferTokens           *int  `json:"bufferTokens"`
			MicrocompactEnabled    *bool `json:"microcompactEnabled"`
			MicrocompactKeepRecent *int  `json:"microcompactKeepRecent"`
		}{override.Enabled, override.TokenTrigger, override.TokenTarget, override.SummaryLimit,
			override.OutputReserveTokens, override.BufferTokens, override.MicrocompactEnabled, override.MicrocompactKeepRecent}
	}
	resp.UpdatedBy = updatedBy
	resp.UpdatedAt = updatedAt
	return resp
}

// overrideFieldHas 判断数字字段是否有 DB 覆盖。
func overrideFieldHas(override *contextCompactOverrideJSON, field string) bool {
	if override == nil {
		return false
	}
	switch field {
	case "tokenTrigger":
		return override.TokenTrigger != nil
	case "tokenTarget":
		return override.TokenTarget != nil
	case "summaryLimit":
		return override.SummaryLimit != nil
	case "outputReserveTokens":
		return override.OutputReserveTokens != nil
	case "bufferTokens":
		return override.BufferTokens != nil
	case "microcompactKeepRecent":
		return override.MicrocompactKeepRecent != nil
	}
	return false
}

// toConfContextCompactOverride 把存储层覆盖结构转换为 conf 内存覆盖结构。
func toConfContextCompactOverride(override *contextCompactOverrideJSON) *conf.ContextCompactSettingOverride {
	if override == nil {
		return nil
	}
	return &conf.ContextCompactSettingOverride{
		Enabled:                override.Enabled,
		TokenTrigger:           override.TokenTrigger,
		TokenTarget:            override.TokenTarget,
		SummaryLimit:           override.SummaryLimit,
		OutputReserveTokens:    override.OutputReserveTokens,
		BufferTokens:           override.BufferTokens,
		MicrocompactEnabled:    override.MicrocompactEnabled,
		MicrocompactKeepRecent: override.MicrocompactKeepRecent,
	}
}
