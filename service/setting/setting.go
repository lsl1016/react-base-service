// Package setting 实现管理面板「运行时配置」：把 custom.yaml 中需要运营期在线调整的
// 策略参数（当前为 subagent 委派策略）落到 tblLlmRuntimeSetting 单行 JSON，并刷新进
// conf 内存覆盖快照供 GetReactRuntimeConfig 合并。
//
// 取值优先级（字段级）：DB 覆盖 > custom.yaml > 内置默认值。
// 失效语义：yaml 始终是兜底基线（DB 行缺失/字段未覆盖即回落），删除 DB 行 = 回到纯 yaml 行为。
// 生效时机：写路径落库后本进程立即刷新；多实例部署靠 TTL 刷新拉平（策略参数无需毫秒级一致，
// 与「下一个 run / 下一次委派生效」的既有语义对齐，运行中 run 不受影响）。
package setting

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// SettingKeySubAgent 是 subagent 委派策略在 tblLlmRuntimeSetting 中的设置键。
const SettingKeySubAgent = "subagent"

// 校验范围：与引擎语义和 conf 默认值对齐（写侧强校验，conf 侧不重复校验）。
const (
	subAgentMaxParallelMin     = 1
	subAgentMaxParallelMax     = 8
	subAgentDefaultMaxStepsMin = 1
	subAgentDefaultMaxStepsMax = 64
	subAgentMaxDepthMin        = 1
	subAgentMaxDepthMax        = 4
)

// refreshInterval 是 DB 覆盖快照的兜底刷新周期（拉平多实例漂移与人工改库）。
const refreshInterval = 10 * time.Second

// subAgentOverrideJSON 是 value_json 的存储结构；字段语义与 conf.SubAgentSettingOverride
// 完全一致（指针为 null = 未覆盖），此处独立声明以隔离存储格式与 conf 内存结构。
type subAgentOverrideJSON struct {
	Enabled         *bool `json:"enabled,omitempty"`
	MaxParallel     *int  `json:"maxParallel,omitempty"`
	DefaultMaxSteps *int  `json:"defaultMaxSteps,omitempty"`
	MaxDepth        *int  `json:"maxDepth,omitempty"`
}

// GetSubAgentSetting 返回 subagent 委派策略的生效视图：effective（合并 yaml/默认值后的
// 引擎实际消费口径）+ 逐字段 source + DB 覆盖原文。
func GetSubAgentSetting(ctx *gin.Context) (*params.SubAgentSettingResp, error) {
	override, updatedBy, updatedAt, err := loadSubAgentOverride(ctx)
	if err != nil {
		return nil, err
	}
	return buildSubAgentSettingResp(override, updatedBy, updatedAt), nil
}

// UpdateSubAgentSetting 更新 subagent 委派策略：校验 → 合并现状 → 整行写回 → 立即刷新
// conf 覆盖快照（本进程下一个 run 即生效）。clearFields 用于把指定字段清回 yaml/默认值。
func UpdateSubAgentSetting(ctx *gin.Context, req *params.UpdateSubAgentSettingReq, userName string) (*params.SubAgentSettingResp, error) {
	if err := validateSubAgentUpdate(req); err != nil {
		return nil, err
	}

	current, _, _, err := loadSubAgentOverride(ctx)
	if err != nil {
		return nil, err
	}
	merged := mergeSubAgentUpdate(current, req)

	valueJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, components.ErrorRuntimeSettingInvalid.Wrap(err)
	}
	if err := model.UpsertRuntimeSetting(ctx, &model.RuntimeSetting{
		SettingKey: SettingKeySubAgent,
		ValueJSON:  string(valueJSON),
		UpdatedBy:  userName,
	}); err != nil {
		return nil, err
	}

	updatedBy, updatedAt := userName, time.Now().Format("2006-01-02 15:04:05")
	// 写后立即刷新本进程快照；失败仅告警（TTL 刷新兜底），不回滚已落库的设置。
	if err := refreshOverrideFromDB(ctx); err != nil {
		zlog.Warnf(ctx, "[Setting] 写后刷新覆盖快照失败(等待TTL兜底): err=%v", err)
		updatedBy, updatedAt = "", ""
	}
	zlog.Infof(ctx, "[Setting] subagent 委派策略已更新: operator=%s, value=%s", userName, string(valueJSON))
	return buildSubAgentSettingResp(merged, updatedBy, updatedAt), nil
}

// loadSubAgentOverride 读 DB 覆盖原文；行缺失/JSON 解析失败按「无覆盖」处理并告警
// （解析失败回落 yaml 而非报错，避免脏数据把管理面与引擎一起打挂）。
func loadSubAgentOverride(ctx *gin.Context) (override *subAgentOverrideJSON, updatedBy, updatedAt string, err error) {
	row, err := model.GetRuntimeSettingByKey(ctx, SettingKeySubAgent)
	if err != nil {
		return nil, "", "", err
	}
	if row == nil {
		return nil, "", "", nil
	}
	parsed := &subAgentOverrideJSON{}
	if strings.TrimSpace(row.ValueJSON) != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), parsed); err != nil {
			zlog.Warnf(ctx, "[Setting] subagent 覆盖 JSON 解析失败(按无覆盖处理): err=%v", err)
			return nil, "", "", nil
		}
	}
	return parsed, row.UpdatedBy, row.UpdatedAt.Format("2006-01-02 15:04:05"), nil
}

// validateSubAgentUpdate 校验更新请求：覆盖值范围 + clearFields 字段名合法。
// 同字段同时出现在 clearFields 与覆盖值时以 clear 为准（覆盖值不参与校验与合并）。
func validateSubAgentUpdate(req *params.UpdateSubAgentSettingReq) error {
	clearing := make(map[string]bool, len(req.ClearFields))
	for _, field := range req.ClearFields {
		switch strings.TrimSpace(field) {
		case "enabled", "maxParallel", "defaultMaxSteps", "maxDepth":
			clearing[strings.TrimSpace(field)] = true
		default:
			return components.ErrorRuntimeSettingInvalid.Sprintf("clearFields 含未知字段: %s", field)
		}
	}
	if req.MaxParallel != nil && !clearing["maxParallel"] {
		if *req.MaxParallel < subAgentMaxParallelMin || *req.MaxParallel > subAgentMaxParallelMax {
			return components.ErrorRuntimeSettingInvalid.Sprintf("maxParallel 取值范围 %d-%d", subAgentMaxParallelMin, subAgentMaxParallelMax)
		}
	}
	if req.DefaultMaxSteps != nil && !clearing["defaultMaxSteps"] {
		if *req.DefaultMaxSteps < subAgentDefaultMaxStepsMin || *req.DefaultMaxSteps > subAgentDefaultMaxStepsMax {
			return components.ErrorRuntimeSettingInvalid.Sprintf("defaultMaxSteps 取值范围 %d-%d", subAgentDefaultMaxStepsMin, subAgentDefaultMaxStepsMax)
		}
	}
	if req.MaxDepth != nil && !clearing["maxDepth"] {
		if *req.MaxDepth < subAgentMaxDepthMin || *req.MaxDepth > subAgentMaxDepthMax {
			return components.ErrorRuntimeSettingInvalid.Sprintf("maxDepth 取值范围 %d-%d", subAgentMaxDepthMin, subAgentMaxDepthMax)
		}
	}
	return nil
}

// mergeSubAgentUpdate 把请求合并进现状：逐字段「clear 优先清空，否则覆盖非 nil 值」，其余保留现状。
func mergeSubAgentUpdate(current *subAgentOverrideJSON, req *params.UpdateSubAgentSettingReq) *subAgentOverrideJSON {
	merged := &subAgentOverrideJSON{}
	if current != nil {
		*merged = *current
	}
	clearing := make(map[string]bool, len(req.ClearFields))
	for _, field := range req.ClearFields {
		clearing[strings.TrimSpace(field)] = true
	}
	// enabled：布尔字段无法区分「未传」与「关闭」，显式传 false 即关闭；清除走 clearFields。
	if clearing["enabled"] {
		merged.Enabled = nil
	} else if req.Enabled != nil {
		merged.Enabled = req.Enabled
	}
	if clearing["maxParallel"] {
		merged.MaxParallel = nil
	} else if req.MaxParallel != nil {
		merged.MaxParallel = req.MaxParallel
	}
	if clearing["defaultMaxSteps"] {
		merged.DefaultMaxSteps = nil
	} else if req.DefaultMaxSteps != nil {
		merged.DefaultMaxSteps = req.DefaultMaxSteps
	}
	if clearing["maxDepth"] {
		merged.MaxDepth = nil
	} else if req.MaxDepth != nil {
		merged.MaxDepth = req.MaxDepth
	}
	return merged
}

// buildSubAgentSettingResp 组装生效视图：effective 经 conf.EffectiveSubAgentConfig 计算
// （与引擎消费口径一致，且不依赖全局快照的实时性）；source 逐字段判定（override > yaml > default）。
func buildSubAgentSettingResp(override *subAgentOverrideJSON, updatedBy, updatedAt string) *params.SubAgentSettingResp {
	effective := conf.EffectiveSubAgentConfig(toConfOverride(override))
	resp := &params.SubAgentSettingResp{Sources: map[string]params.SubAgentSettingFieldResp{}}
	resp.Effective.Enabled = effective.SubAgentEnabled()
	resp.Effective.MaxParallel = effective.MaxParallel
	resp.Effective.DefaultMaxSteps = effective.DefaultMaxSteps
	resp.Effective.MaxDepth = effective.MaxDepth

	yamlCfg := conf.CustomConf.LLM.React.SubAgent
	resp.Sources["enabled"] = subAgentFieldSource(override != nil && override.Enabled != nil, yamlCfg.Enabled != nil, effective.SubAgentEnabled())
	resp.Sources["maxParallel"] = subAgentNumericFieldSource(overrideFieldInt(override, "maxParallel"), yamlCfg.MaxParallel, effective.MaxParallel)
	resp.Sources["defaultMaxSteps"] = subAgentNumericFieldSource(overrideFieldInt(override, "defaultMaxSteps"), yamlCfg.DefaultMaxSteps, effective.DefaultMaxSteps)
	resp.Sources["maxDepth"] = subAgentNumericFieldSource(overrideFieldInt(override, "maxDepth"), yamlCfg.MaxDepth, effective.MaxDepth)

	if override != nil {
		resp.Override = &struct {
			Enabled         *bool `json:"enabled"`
			MaxParallel     *int  `json:"maxParallel"`
			DefaultMaxSteps *int  `json:"defaultMaxSteps"`
			MaxDepth        *int  `json:"maxDepth"`
		}{override.Enabled, override.MaxParallel, override.DefaultMaxSteps, override.MaxDepth}
	}
	resp.UpdatedBy = updatedBy
	resp.UpdatedAt = updatedAt
	return resp
}

// subAgentFieldSource 判定布尔字段来源（override > yaml > default），value 返回生效值。
func subAgentFieldSource(hasOverride, hasYaml bool, effectiveValue bool) params.SubAgentSettingFieldResp {
	source := "default"
	if hasYaml {
		source = "yaml"
	}
	if hasOverride {
		source = "override"
	}
	return params.SubAgentSettingFieldResp{Value: effectiveValue, Source: source}
}

// subAgentNumericFieldSource 判定数字字段来源；指针/零值语义与 conf 归一化对齐。
func subAgentNumericFieldSource(hasOverride bool, yamlValue, effectiveValue int) params.SubAgentSettingFieldResp {
	source := "default"
	if yamlValue > 0 {
		source = "yaml"
	}
	if hasOverride {
		source = "override"
	}
	return params.SubAgentSettingFieldResp{Value: effectiveValue, Source: source}
}

func overrideFieldInt(override *subAgentOverrideJSON, field string) bool {
	if override == nil {
		return false
	}
	switch field {
	case "maxParallel":
		return override.MaxParallel != nil
	case "defaultMaxSteps":
		return override.DefaultMaxSteps != nil
	case "maxDepth":
		return override.MaxDepth != nil
	}
	return false
}

// toConfOverride 把存储层覆盖结构转换为 conf 内存覆盖结构（字段一一对应，nil 保持 nil）。
func toConfOverride(override *subAgentOverrideJSON) *conf.SubAgentSettingOverride {
	if override == nil {
		return nil
	}
	return &conf.SubAgentSettingOverride{
		Enabled:         override.Enabled,
		MaxParallel:     override.MaxParallel,
		DefaultMaxSteps: override.DefaultMaxSteps,
		MaxDepth:        override.MaxDepth,
	}
}

// refreshOverrideFromDB 读 DB 覆盖并注入 conf 内存快照。
func refreshOverrideFromDB(ctx *gin.Context) error {
	override, _, _, err := loadSubAgentOverride(ctx)
	if err != nil {
		return err
	}
	snapshot := conf.RuntimeSettingOverride{SubAgent: toConfOverride(override)}
	conf.SetRuntimeSettingOverride(snapshot)
	return nil
}

var refresherLifecycle = struct {
	sync.Mutex
	cancel  context.CancelFunc
	running bool
}{}

// Bootstrap 启动覆盖快照后台维护：启动即加载一次 + 周期刷新兜底（拉平多实例漂移与人工改库）。
// 刷新失败保留上一份快照并告警，不会把引擎打回 yaml（避免 DB 抖动引发策略闪断）。
func Bootstrap() {
	refresherLifecycle.Lock()
	defer refresherLifecycle.Unlock()
	if refresherLifecycle.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	refresherLifecycle.cancel = cancel
	refresherLifecycle.running = true

	// 后台无请求作用域，与 mcpclient 等后台任务一致使用空 gin.Context 承载 DB 查询。
	if err := refreshOverrideFromDB(&gin.Context{}); err != nil {
		zlog.Warnf(nil, "[Setting.Bootstrap] 运行时设置初次加载失败(暂无覆盖,回落yaml): err=%v", err)
	}
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := refreshOverrideFromDB(&gin.Context{}); err != nil {
					zlog.Warnf(nil, "[Setting] 运行时设置周期刷新失败(保留上一份快照): err=%v", err)
				}
			}
		}
	}()
	zlog.Infof(nil, "[Setting.Bootstrap] 运行时设置后台刷新已启动: interval=%s", refreshInterval)
}

// Shutdown 停止后台刷新（随 HTTP 服务退出调用）。
func Shutdown() {
	refresherLifecycle.Lock()
	defer refresherLifecycle.Unlock()
	if !refresherLifecycle.running {
		return
	}
	refresherLifecycle.cancel()
	refresherLifecycle.cancel = nil
	refresherLifecycle.running = false
}
