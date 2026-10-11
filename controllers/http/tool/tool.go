package tool

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	toolService "react-base-service/service/tool"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// RegisterTool godoc
// @Summary 注册工具
// @Description 注册一个新的工具，同一调用方下工具名称不能重复
// @Tags Tool
// @Accept json
// @Produce json
// @Param request body params.RegisterToolReq true "工具注册请求"
// @Success 200 {object} params.ToolResp "注册成功"
// @Failure 400 {object} components.DefaultRenderWithTrace "参数错误"
// @Failure 500 {object} components.DefaultRenderWithTrace "服务器错误或工具名称重复"
// @Router /tool/register [post]
func RegisterTool(ctx *gin.Context) {
	var req params.RegisterToolReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Tool.Register] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)
	t, err := toolService.RegisterTool(ctx, &req, userName)
	if err != nil {
		zlog.Errorf(ctx, "[Tool.Register] 注册失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, toolService.ToToolResp(t))
}

// UpdateTool godoc
// @Summary 更新工具
// @Description 根据toolId更新工具信息，只更新请求中提供的非空字段
// @Tags Tool
// @Accept json
// @Produce json
// @Param request body params.UpdateToolReq true "工具更新请求"
// @Success 200 {object} map[string]interface{} "更新成功"
// @Failure 400 {object} components.DefaultRenderWithTrace "参数错误"
// @Failure 404 {object} components.DefaultRenderWithTrace "工具不存在"
// @Failure 500 {object} components.DefaultRenderWithTrace "服务器错误"
// @Router /tool/update [post]
func UpdateTool(ctx *gin.Context) {
	var req params.UpdateToolReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Tool.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)
	if err := toolService.UpdateTool(ctx, &req, userName); err != nil {
		zlog.Errorf(ctx, "[Tool.Update] 更新失败: toolId=%s, err=%v", req.ToolID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// DeleteTool godoc
// @Summary 删除工具
// @Description 根据toolId软删除工具（不实际删除数据，设置deleted_at标记）
// @Tags Tool
// @Accept json
// @Produce json
// @Param request body params.DeleteToolReq true "工具删除请求"
// @Success 200 {object} map[string]interface{} "删除成功"
// @Failure 400 {object} components.DefaultRenderWithTrace "参数错误"
// @Failure 404 {object} components.DefaultRenderWithTrace "工具不存在"
// @Failure 500 {object} components.DefaultRenderWithTrace "服务器错误"
// @Router /tool/delete [post]
func DeleteTool(ctx *gin.Context) {
	var req params.DeleteToolReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Tool.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	if err := toolService.DeleteTool(ctx, req.ToolID); err != nil {
		zlog.Errorf(ctx, "[Tool.Delete] 删除失败: toolId=%s, err=%v", req.ToolID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// ListTools godoc
// @Summary 查询工具列表
// @Description 根据appKey查询该调用方下的所有工具列表
// @Tags Tool
// @Accept json
// @Produce json
// @Param request body params.ListToolsReq true "工具列表查询请求"
// @Success 200 {array} params.ToolResp "查询成功"
// @Failure 400 {object} components.DefaultRenderWithTrace "参数错误"
// @Failure 500 {object} components.DefaultRenderWithTrace "服务器错误"
// @Router /tool/list [post]
func ListTools(ctx *gin.Context) {
	var req params.ListToolsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Tool.List] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	tools, err := toolService.ListByAppAndRoute(ctx, req.AppKey, req.RouteValues)
	if err != nil {
		zlog.Errorf(ctx, "[Tool.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	resp := make([]params.ToolResp, 0, len(tools))
	for i := range tools {
		resp = append(resp, toolService.ToToolResp(&tools[i]))
	}
	components.RenderJsonSucc(ctx, resp)
}

// GetToolDetail 查询工具详情
// @Summary      查询工具详情
// @Description  根据 toolId 查询单个工具的详细配置
// @Tags         Tool
// @Accept       json
// @Produce      json
// @Param        req  body     params.ToolDetailReq  true  "查询工具详情请求体"
// @Success      200  {object} params.ToolResp        "成功返回工具详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败"
// @Failure      404  {object} components.DefaultRenderWithTrace  "工具不存在"
// @Failure      500  {object} components.DefaultRenderWithTrace  "服务器错误"
// @Router       /tool/detail [post]
func GetToolDetail(ctx *gin.Context) {
	var req params.ToolDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Tool.Detail] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	t, err := toolService.GetDetail(ctx, req.ToolID)
	if err != nil {
		zlog.Errorf(ctx, "[Tool.Detail] 查询失败: toolId=%s, err=%v", req.ToolID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, toolService.ToToolResp(t))
}
