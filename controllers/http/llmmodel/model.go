package llmmodel

import (
	"react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// GetModels 获取可用模型列表
// @Summary      获取可用模型列表
// @Description  返回当前支持的 LLM 模型及版本信息
// @Tags         llm
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ModelsResp}  "成功返回模型列表"
// @Failure      500  {object} components.DefaultRenderWithTrace  "服务内部错误"
// @Router       /models [get]
func GetModels(ctx *gin.Context) {
	zlog.Debugf(ctx, "[GetModels] 获取可用模型列表")

	metas := llm.GetSupportedModels()
	var models []params.ModelInfo
	for _, m := range metas {
		models = append(models, params.ModelInfo{
			Key:            m.Key,
			Versions:       m.Versions,
			DefaultVersion: m.DefaultVersion,
		})
	}

	components.RenderJsonSucc(ctx, params.ModelsResp{
		Models: models,
		Vendors: llm.ValidModelKeys(),
	})
}
