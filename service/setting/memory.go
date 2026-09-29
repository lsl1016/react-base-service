// memory.go 实现管理面板「长期记忆策略」运行时设置：与 subagent/context 同一套 DB 覆盖机制
//（tblLlmRuntimeSetting 单行 JSON、字段级覆盖、写后本进程立即生效、TTL 拉平多实例）。
// 暴露的是运营期可调子集（总开关/注入预算/软上限/作用域维度）；reflection/extractor 嵌套
// 策略仍归 custom.yaml 管。
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

// SettingKeyMemory 是长期记忆策略在 tblLlmRuntimeSetting 中的设置键。
const SettingKeyMemory = "memory"

// 校验范围：与引擎语义和 conf 默认值对齐（写侧强校验，conf 侧不重复校验）。
const (
	memoryResidentMaxItemsMin    = 1
	memoryResidentMaxItemsMax    = 64
	memoryResidentBudgetCharsMin = 100
	memoryResidentBudgetCharsMax = 32000
	memoryIndexMaxItemsMin       = 1
	memoryIndexMaxItemsMax       = 200
	memoryDetachedMaxItemsMin    = 10
	memoryDetachedMaxItemsMax    = 5000
)

// memoryOverrideJSON 是 value_json 的存储结构；字段语义与 conf.MemorySettingOverride
// 完全一致（指针为 null = 未覆盖），此处独立声明以隔离存储格式与 conf 内存结构。
type memoryOverrideJSON struct {
	Enabled             *bool `json:"enabled,omitempty"`
	ResidentMaxItems    *int  `json:"residentMaxItems,omitempty"`
	ResidentBudgetChars *int  `json:"residentBudgetChars,omitempty"`
	IndexMaxItems       *int  `json:"indexMaxItems,omitempty"`
	DetachedMaxItems    *int  `json:"detachedMaxItems,omitempty"`
	AllowUserScope      *bool `json:"allowUserScope,omitempty"`
}

// GetMemorySetting 返回长期记忆策略的生效视图：effective（合并 yaml/默认值后的引擎实际
// 消费口径）+ 逐字段 source + DB 覆盖原文。
func GetMemorySetting(ctx *gin.Context) (*params.MemorySettingResp, error) {
	override, updatedBy, updatedAt, err := loadMemoryOverride(ctx)
	if err != nil {
		return nil, err
	}
	return buildMemorySettingResp(override, updatedBy, updatedAt), nil
}

// UpdateMemorySetting 更新长期记忆策略：校验 → 合并现状 → 整行写回 → 立即刷新覆盖快照
//（本进程下一个 run 即生效）。clearFields 用于把指定字段清回 yaml/默认值。
func UpdateMemorySetting(ctx *gin.Context, req *params.UpdateMemorySettingReq, userName string) (*params.MemorySettingResp, error) {
	if err := validateMemoryUpdate(req); err != nil {
		return nil, err
	}

	current, _, _, err := loadMemoryOverride(ctx)
	if err != nil {
		return nil, err
	}
	merged := mergeMemoryUpdate(current, req)

	valueJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, components.ErrorRuntimeSettingInvalid.Wrap(err)
	}
	if err := model.UpsertRuntimeSetting(ctx, &model.RuntimeSetting{
		SettingKey: SettingKeyMemory,
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
	zlog.Infof(ctx, "[Setting] 长期记忆策略已更新: operator=%s, value=%s", userName, string(valueJSON))
	return buildMemorySettingResp(merged, updatedBy, updatedAt), nil
}

// loadMemoryOverride 读 DB 覆盖原文；行缺失/JSON 解析失败按「无覆盖」处理并告警。
func loadMemoryOverride(ctx *gin.Context) (override *memoryOverrideJSON, updatedBy, updatedAt string, err error) {
	row, err := model.GetRuntimeSettingByKey(ctx, SettingKeyMemory)
	if err != nil {
		return nil, "", "", err
	}
	if row == nil {
		return nil, "", "", nil
	}
	parsed := &memoryOverrideJSON{}
	if strings.TrimSpace(row.ValueJSON) != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), parsed); err != nil {
			zlog.Warnf(ctx, "[Setting] memory 覆盖 JSON 解析失败(按无覆盖处理): err=%v", err)
			return nil, "", "", nil
		}
	}
	return parsed, row.UpdatedBy, row.UpdatedAt.Format("2006-01-02 15:04:05"), nil
}

// validateMemoryUpdate 校验更新请求：覆盖值范围 + clearFields 字段名合法。
// 同字段同时出现在 clearFields 与覆盖值时以 clear 为准（覆盖值不参与校验与合并）。
func validateMemoryUpdate(req *params.UpdateMemorySettingReq) error {
	clearing := make(map[string]bool, len(req.ClearFields))
	for _, field := range req.ClearFields {
		switch strings.TrimSpace(field) {
		case "enabled", "residentMaxItems", "residentBudgetChars",
			"indexMaxItems", "detachedMaxItems", "allowUserScope":
			clearing[strings.TrimSpace(field)] = true
		default:
			return components.RuntimeSettingInvalidf("clearFields 含未知字段: %s", field)
		}
	}
	check := func(field string, value, min, max int) error {
		if clearing[field] {
			return nil
		}
		if value < min || value > max {
			return components.RuntimeSettingInvalidf("%s 取值范围 %d-%d", field, min, max)
		}
		return nil
	}
	if req.ResidentMaxItems != nil {
		if err := check("residentMaxItems", *req.ResidentMaxItems, memoryResidentMaxItemsMin, memoryResidentMaxItemsMax); err != nil {
			return err
		}
	}
	if req.ResidentBudgetChars != nil {
		if err := check("residentBudgetChars", *req.ResidentBudgetChars, memoryResidentBudgetCharsMin, memoryResidentBudgetCharsMax); err != nil {
			return err
		}
	}
	if req.IndexMaxItems != nil {
		if err := check("indexMaxItems", *req.IndexMaxItems, memoryIndexMaxItemsMin, memoryIndexMaxItemsMax); err != nil {
			return err
		}
	}
	if req.DetachedMaxItems != nil {
		if err := check("detachedMaxItems", *req.DetachedMaxItems, memoryDetachedMaxItemsMin, memoryDetachedMaxItemsMax); err != nil {
			return err
		}
	}
	return nil
}

// mergeMemoryUpdate 把请求合并进现状：clear 优先清空，否则覆盖非 nil 值。
// 布尔字段无法区分「未传」与「关闭」，显式传 false 即关闭；清除走 clearFields。
func mergeMemoryUpdate(current *memoryOverrideJSON, req *params.UpdateMemorySettingReq) *memoryOverrideJSON {
	merged := &memoryOverrideJSON{}
	if current != nil {
		*merged = *current
	}
	clearing := make(map[string]bool, len(req.ClearFields))
	for _, field := range req.ClearFields {
		clearing[strings.TrimSpace(field)] = true
	}
	if clearing["enabled"] {
		merged.Enabled = nil
	} else if req.Enabled != nil {
		merged.Enabled = req.Enabled
	}
	if clearing["residentMaxItems"] {
		merged.ResidentMaxItems = nil
	} else if req.ResidentMaxItems != nil {
		merged.ResidentMaxItems = req.ResidentMaxItems
	}
	if clearing["residentBudgetChars"] {
		merged.ResidentBudgetChars = nil
	} else if req.ResidentBudgetChars != nil {
		merged.ResidentBudgetChars = req.ResidentBudgetChars
	}
	if clearing["indexMaxItems"] {
		merged.IndexMaxItems = nil
	} else if req.IndexMaxItems != nil {
		merged.IndexMaxItems = req.IndexMaxItems
	}
	if clearing["detachedMaxItems"] {
		merged.DetachedMaxItems = nil
	} else if req.DetachedMaxItems != nil {
		merged.DetachedMaxItems = req.DetachedMaxItems
	}
	if clearing["allowUserScope"] {
		merged.AllowUserScope = nil
	} else if req.AllowUserScope != nil {
		merged.AllowUserScope = req.AllowUserScope
	}
	return merged
}

// buildMemorySettingResp 组装生效视图：effective 经 conf.EffectiveMemoryConfig 计算
//（与引擎消费口径一致）；source 逐字段判定（override > yaml > default）。
func buildMemorySettingResp(override *memoryOverrideJSON, updatedBy, updatedAt string) *params.MemorySettingResp {
	effective := conf.EffectiveMemoryConfig(toConfMemoryOverride(override))
	resp := &params.MemorySettingResp{Sources: map[string]params.SubAgentSettingFieldResp{}}
	resp.Effective.Enabled = effective.MemoryEnabled()
	resp.Effective.ResidentMaxItems = effective.ResidentMaxItems
	resp.Effective.ResidentBudgetChars = effective.ResidentBudgetChars
	resp.Effective.IndexMaxItems = effective.IndexMaxItems
	resp.Effective.DetachedMaxItems = effective.DetachedMaxItems
	resp.Effective.AllowUserScope = effective.MemoryAllowUserScope()

	yamlCfg := conf.CustomConf.LLM.React.Memory
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
	setSource("residentMaxItems", memoryOverrideFieldHas(override, "residentMaxItems"), yamlCfg.ResidentMaxItems > 0, resp.Effective.ResidentMaxItems)
	setSource("residentBudgetChars", memoryOverrideFieldHas(override, "residentBudgetChars"), yamlCfg.ResidentBudgetChars > 0, resp.Effective.ResidentBudgetChars)
	setSource("indexMaxItems", memoryOverrideFieldHas(override, "indexMaxItems"), yamlCfg.IndexMaxItems > 0, resp.Effective.IndexMaxItems)
	setSource("detachedMaxItems", memoryOverrideFieldHas(override, "detachedMaxItems"), yamlCfg.DetachedMaxItems > 0, resp.Effective.DetachedMaxItems)
	setSource("allowUserScope", override != nil && override.AllowUserScope != nil, yamlCfg.AllowUserScope != nil, resp.Effective.AllowUserScope)
	for _, field := range []string{"enabled", "residentMaxItems", "residentBudgetChars", "indexMaxItems", "detachedMaxItems", "allowUserScope"} {
		src := resp.Sources[field]
		src.Baseline = memoryBaseline(override, field)
		resp.Sources[field] = src
	}

	if override != nil {
		resp.Override = &struct {
			Enabled             *bool `json:"enabled"`
			ResidentMaxItems    *int  `json:"residentMaxItems"`
			ResidentBudgetChars *int  `json:"residentBudgetChars"`
			IndexMaxItems       *int  `json:"indexMaxItems"`
			DetachedMaxItems    *int  `json:"detachedMaxItems"`
			AllowUserScope      *bool `json:"allowUserScope"`
		}{override.Enabled, override.ResidentMaxItems, override.ResidentBudgetChars,
			override.IndexMaxItems, override.DetachedMaxItems, override.AllowUserScope}
	}
	resp.UpdatedBy = updatedBy
	resp.UpdatedAt = updatedAt
	return resp
}

// memoryBaseline 计算单个字段清除覆盖后的回落值（yaml > 默认）：
// 复制覆盖并把该字段置 nil 后重算生效配置，供面板预览「关掉覆盖再保存」的结果。
func memoryBaseline(override *memoryOverrideJSON, field string) interface{} {
	stripped := &memoryOverrideJSON{}
	if override != nil {
		*stripped = *override
	}
	switch field {
	case "enabled":
		stripped.Enabled = nil
	case "residentMaxItems":
		stripped.ResidentMaxItems = nil
	case "residentBudgetChars":
		stripped.ResidentBudgetChars = nil
	case "indexMaxItems":
		stripped.IndexMaxItems = nil
	case "detachedMaxItems":
		stripped.DetachedMaxItems = nil
	case "allowUserScope":
		stripped.AllowUserScope = nil
	default:
		return nil
	}
	cfg := conf.EffectiveMemoryConfig(toConfMemoryOverride(stripped))
	switch field {
	case "enabled":
		return cfg.MemoryEnabled()
	case "residentMaxItems":
		return cfg.ResidentMaxItems
	case "residentBudgetChars":
		return cfg.ResidentBudgetChars
	case "indexMaxItems":
		return cfg.IndexMaxItems
	case "detachedMaxItems":
		return cfg.DetachedMaxItems
	case "allowUserScope":
		return cfg.MemoryAllowUserScope()
	}
	return nil
}

// memoryOverrideFieldHas 判断数字字段是否有 DB 覆盖。
func memoryOverrideFieldHas(override *memoryOverrideJSON, field string) bool {
	if override == nil {
		return false
	}
	switch field {
	case "residentMaxItems":
		return override.ResidentMaxItems != nil
	case "residentBudgetChars":
		return override.ResidentBudgetChars != nil
	case "indexMaxItems":
		return override.IndexMaxItems != nil
	case "detachedMaxItems":
		return override.DetachedMaxItems != nil
	}
	return false
}

// toConfMemoryOverride 把存储层覆盖结构转换为 conf 内存覆盖结构（字段一一对应，nil 保持 nil）。
func toConfMemoryOverride(override *memoryOverrideJSON) *conf.MemorySettingOverride {
	if override == nil {
		return nil
	}
	return &conf.MemorySettingOverride{
		Enabled:             override.Enabled,
		ResidentMaxItems:    override.ResidentMaxItems,
		ResidentBudgetChars: override.ResidentBudgetChars,
		IndexMaxItems:       override.IndexMaxItems,
		DetachedMaxItems:    override.DetachedMaxItems,
		AllowUserScope:      override.AllowUserScope,
	}
}
