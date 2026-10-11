package systemprompt

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	systempromptService "react-base-service/service/systemprompt"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// RegisterSystemPrompt 注册系统提示词
// @Summary      注册系统提示词
// @Description  在指定 app 的 routeValues 下注册一条系统提示词，同一 app+routeValues 组合不允许重复。
// @Tags         system-prompt
// @Accept       json
// @Produce      json
// @Param        req  body     params.RegisterSystemPromptReq  true  "注册系统提示词请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SystemPromptResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /system-prompt/register [post]
func RegisterSystemPrompt(ctx *gin.Context) {
	var req params.RegisterSystemPromptReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Register] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)
	sp, err := systempromptService.RegisterSystemPrompt(ctx, &req, userName)
	if err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Register] 注册失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, systempromptService.ToSystemPromptResp(sp))
}

// UpdateSystemPrompt 更新系统提示词
// @Summary      更新系统提示词
// @Description  根据 id 更新系统提示词。
// @Tags         system-prompt
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateSystemPromptReq  true  "更新系统提示词请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SystemPromptResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /system-prompt/update [post]
func UpdateSystemPrompt(ctx *gin.Context) {
	var req params.UpdateSystemPromptReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	sp, err := systempromptService.UpdateSystemPrompt(ctx, &req)
	if err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Update] 更新失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, systempromptService.ToSystemPromptResp(sp))
}

// DeleteSystemPrompt 删除系统提示词
// @Summary      删除系统提示词
// @Description  根据 id 删除指定系统提示词。
// @Tags         system-prompt
// @Accept       json
// @Produce      json
// @Param        req  body     params.DeleteSystemPromptReq  true  "删除系统提示词请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SystemPromptResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /system-prompt/delete [post]
func DeleteSystemPrompt(ctx *gin.Context) {
	var req params.DeleteSystemPromptReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	sp, err := systempromptService.DeleteSystemPrompt(ctx, req.ID)
	if err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Delete] 删除失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, systempromptService.ToSystemPromptResp(sp))
}

// ListSystemPrompts 查询系统提示词列表
// @Summary      查询系统提示词列表
// @Description  根据 appKey 查询该调用方下的系统提示词列表。
// @Tags         system-prompt
// @Accept       json
// @Produce      json
// @Param        req  body     params.ListSystemPromptsReq  true  "查询系统提示词列表请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.SystemPromptResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /system-prompt/list [post]
func ListSystemPrompts(ctx *gin.Context) {
	var req params.ListSystemPromptsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.List] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	prompts, err := systempromptService.ListByAppAndRoute(ctx, req.AppKey, req.RouteValues)
	if err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	resp := make([]params.SystemPromptResp, 0, len(prompts))
	for i := range prompts {
		resp = append(resp, systempromptService.ToSystemPromptResp(&prompts[i]))
	}
	components.RenderJsonSucc(ctx, resp)
}

// GetSystemPromptDetail 查询系统提示词详情
// @Summary      查询系统提示词详情
// @Description  根据 id 查询单条系统提示词的详情。
// @Tags         system-prompt
// @Accept       json
// @Produce      json
// @Param        req  body     params.SystemPromptDetailReq  true  "查询系统提示词详情请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SystemPromptResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /system-prompt/detail [post]
func GetSystemPromptDetail(ctx *gin.Context) {
	var req params.SystemPromptDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Detail] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	sp, err := systempromptService.GetDetail(ctx, req.ID)
	if err != nil {
		zlog.Errorf(ctx, "[SystemPrompt.Detail] 查询失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, systempromptService.ToSystemPromptResp(sp))
}
