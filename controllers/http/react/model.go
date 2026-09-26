package react

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	llmmodelService "react-base-service/service/llmmodel"

	"github.com/gin-gonic/gin"
)

// catalogInfoForVersion 在模型目录中按版本查找容量与思考能力；未命中返回零值。
func catalogInfoForVersion(version string) (contextTokens, maxOutputTokens int, supportThinking bool) {
	if version == "" {
		return 0, 0, false
	}
	for _, catalog := range conf.CustomConf.LLM.Models {
		matched := catalog.DefaultVersion == version
		for _, item := range catalog.Versions {
			if item == version {
				matched = true
				break
			}
		}
		if matched {
			return catalog.MaxContextTokens, catalog.MaxOutputTokens,
				catalog.SupportsThinking != nil && *catalog.SupportsThinking
		}
	}
	return 0, 0, false
}

// buildReactModelInfo 组装带目录能力信息的模型条目。
func buildReactModelInfo(item conf.ReactModelConfig) params.ReactModelInfo {
	info := params.ReactModelInfo{
		ModelKey:     item.ModelKey,
		ModelVersion: item.ModelVersion,
		DisplayName:  item.DisplayName,
	}
	info.ContextTokens, info.MaxOutputTokens, info.SupportThinking = catalogInfoForVersion(item.ModelVersion)
	return info
}

// GetModels 返回 ReAct 前端可选择的模型列表和默认模型（含目录容量与思考能力）。
func GetModels(ctx *gin.Context) {
	cfg := conf.GetReactRuntimeConfig().Models
	models := make([]params.ReactModelInfo, 0, len(cfg.Available))
	for _, item := range cfg.Available {
		models = append(models, buildReactModelInfo(item))
	}
	components.RenderJsonSucc(ctx, params.ReactModelsResp{
		Models:       models,
		DefaultModel: buildReactModelInfo(cfg.Default),
	})
}

// GetConfigSchema 返回模型配置面板的参数 schema（声明式 min/max/step/default），
// 前端据此渲染数值控件，避免前后端各自硬编码范围。
func GetConfigSchema(ctx *gin.Context) {
	components.RenderJsonSucc(ctx, params.ConfigSchemaResp{
		Params: llmmodelService.BuildModelConfigParams(),
	})
}
