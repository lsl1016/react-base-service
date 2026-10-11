package apikey

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	apikeyService "react-base-service/service/apikey"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// RegisterApiKey 注册 API Key
// @Summary      注册 API Key
// @Description  在指定 app 的 routeValues 下注册一个 API Key，同一 app+routeValues 组合不允许重复。
// @Tags         apikey
// @Accept       json
// @Produce      json
// @Param        req  body     params.RegisterApiKeyReq  true  "注册 API Key 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ApiKeyResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /apikey/register [post]
func RegisterApiKey(ctx *gin.Context) {
	var req params.RegisterApiKeyReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[ApiKey.Register] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)
	ak, err := apikeyService.RegisterApiKey(ctx, &req, userName)
	if err != nil {
		zlog.Errorf(ctx, "[ApiKey.Register] 注册失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, apikeyService.ToApiKeyResp(ak))
}

// UpdateApiKey 更新 API Key
// @Summary      更新 API Key
// @Description  根据 id 更新 API Key 的配置。
// @Tags         apikey
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateApiKeyReq  true  "更新 API Key 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ApiKeyResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /apikey/update [post]
func UpdateApiKey(ctx *gin.Context) {
	var req params.UpdateApiKeyReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[ApiKey.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	ak, err := apikeyService.UpdateApiKey(ctx, &req)
	if err != nil {
		zlog.Errorf(ctx, "[ApiKey.Update] 更新失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, apikeyService.ToApiKeyResp(ak))
}

// DeleteApiKey 删除 API Key
// @Summary      删除 API Key
// @Description  根据 id 删除指定 API Key。
// @Tags         apikey
// @Accept       json
// @Produce      json
// @Param        req  body     params.DeleteApiKeyReq  true  "删除 API Key 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ApiKeyResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /apikey/delete [post]
func DeleteApiKey(ctx *gin.Context) {
	var req params.DeleteApiKeyReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[ApiKey.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	ak, err := apikeyService.DeleteApiKey(ctx, req.ID)
	if err != nil {
		zlog.Errorf(ctx, "[ApiKey.Delete] 删除失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, apikeyService.ToApiKeyResp(ak))
}

// ListApiKeys 查询 API Key 列表
// @Summary      查询 API Key 列表
// @Description  根据 appKey 查询该调用方下的 API Key 列表。
// @Tags         apikey
// @Accept       json
// @Produce      json
// @Param        req  body     params.ListApiKeysReq  true  "查询 API Key 列表请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.ApiKeyResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /apikey/list [post]
func ListApiKeys(ctx *gin.Context) {
	var req params.ListApiKeysReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[ApiKey.List] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	keys, err := apikeyService.ListByAppAndRoute(ctx, req.AppKey, req.RouteValues)
	if err != nil {
		zlog.Errorf(ctx, "[ApiKey.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	resp := make([]params.ApiKeyResp, 0, len(keys))
	for i := range keys {
		resp = append(resp, apikeyService.ToApiKeyResp(&keys[i]))
	}
	components.RenderJsonSucc(ctx, resp)
}

// GetApiKeyDetail 查询 API Key 详情
// @Summary      查询 API Key 详情
// @Description  根据 id 查询单个 API Key 的详情。
// @Tags         apikey
// @Accept       json
// @Produce      json
// @Param        req  body     params.ApiKeyDetailReq  true  "查询 API Key 详情请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ApiKeyResp}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /apikey/detail [post]
func GetApiKeyDetail(ctx *gin.Context) {
	var req params.ApiKeyDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[ApiKey.Detail] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	ak, err := apikeyService.GetDetail(ctx, req.ID)
	if err != nil {
		zlog.Errorf(ctx, "[ApiKey.Detail] 查询失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, apikeyService.ToApiKeyResp(ak))
}
