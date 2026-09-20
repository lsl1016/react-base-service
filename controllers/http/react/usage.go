package react

import (
	"encoding/json"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	"react-base-service/helpers"
	"react-base-service/service/react"

	"github.com/gin-gonic/gin"
)

// usageCategoryOrder 是看板分类的固定展示顺序，与前端卡片一致。
var usageCategoryOrder = []string{
	react.ContextBreakdownMessages,
	react.ContextBreakdownMcpTools,
	react.ContextBreakdownSystemTools,
	react.ContextBreakdownSkills,
	react.ContextBreakdownSystemPrompt,
	react.ContextBreakdownOthers,
}

// GetUsageContext 获取会话上下文容量构成
// @Summary 上下文容量看板
// @Description 聚合会话最近一个外层 run 的上下文占用、缓存命中率与分类构成（分类为发送前估算口径）
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactUsageContextReq true "上下文容量请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactUsageContextResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/usage/context [post]
func GetUsageContext(ctx *gin.Context) {
	var req params.ReactUsageContextReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.GetUsageContext] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	run, err := model.GetLatestOuterReactRunBySessionIDWithDB(ctx, helpers.MysqlClientLLM, req.SessionID)
	if err != nil {
		zlog.Errorf(ctx, "[React.GetUsageContext] 查询最近 run 失败: sessionId=%s err=%v", req.SessionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	// 会话还没有任何 run：返回空构成而非报错，前端展示空态。
	if run == nil {
		components.RenderJsonSucc(ctx, buildUsageContextResp(nil))
		return
	}
	components.RenderJsonSucc(ctx, buildUsageContextResp(run))
}

// buildUsageContextResp 把 run 行聚合成看板响应；maxTokens 与事件侧口径一致取压缩触发阈值。
func buildUsageContextResp(run *model.ReactRun) params.ReactUsageContextResp {
	resp := params.ReactUsageContextResp{
		MaxTokens:  conf.GetReactRuntimeConfig().ContextCompact.TokenTrigger,
		Categories: make([]params.ReactUsageCategory, 0, len(usageCategoryOrder)),
	}
	if run == nil {
		return resp
	}
	resp.UsedTokens = run.LastInputTokens + run.LastOutputTokens
	if run.TotalInputTokens > 0 {
		resp.CacheHitRate = float64(run.CacheReadTokens) / float64(run.TotalInputTokens)
	}
	resp.UpdatedAt = run.UpdatedAt.UnixMilli()

	var breakdown react.ContextBreakdown
	if run.ContextBreakdownJSON != "" && json.Unmarshal([]byte(run.ContextBreakdownJSON), &breakdown) == nil {
		tokensByKey := map[string]int{
			react.ContextBreakdownSystemPrompt: breakdown.SystemPrompt,
			react.ContextBreakdownMessages:     breakdown.Messages,
			react.ContextBreakdownMcpTools:     breakdown.McpTools,
			react.ContextBreakdownSystemTools:  breakdown.SystemTools,
			react.ContextBreakdownSkills:       breakdown.Skills,
			react.ContextBreakdownOthers:       breakdown.Others,
		}
		for _, key := range usageCategoryOrder {
			if tokens := tokensByKey[key]; tokens > 0 {
				resp.Categories = append(resp.Categories, params.ReactUsageCategory{Key: key, Tokens: tokens})
			}
		}
		// 供应商可能把 input 全部记到缓存字段（last_input=0），此时 last 口径会显著
		// 低于真实上下文；取分类估算合计兜底，保证容量条与分类一致。
		if breakdownTotal := breakdown.Total(); breakdownTotal > resp.UsedTokens {
			resp.UsedTokens = breakdownTotal
		}
	}
	return resp
}
