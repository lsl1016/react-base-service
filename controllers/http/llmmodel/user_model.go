package llmmodel

import (
	"encoding/json"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	llmmodelService "react-base-service/service/llmmodel"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// CheckConnectivity 连通性检测
// @Summary      模型连通性检测
// @Description  验证 API Key 与模型组合是否可用
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.ConnCheckReq  true  "连通性检测请求"
// @Success      200  {object} components.DefaultRenderWithTrace  "连通成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败"
// @Failure      500  {object} components.DefaultRenderWithTrace  "连通性检测失败"
// @Router       /model/check-connectivity [post]
func CheckConnectivity(ctx *gin.Context) {
	var req params.ConnCheckReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Model.CheckConnectivity] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	// 透传表单里的接入地址：检测环境必须与保存后的运行环境一致（空=走 api.yaml 全局端点）。
	if err := llmmodelService.CheckConnectivityWithEndpoint(ctx, req.ModelKey, req.ModelVersion, req.ApiKey, req.ApiURL); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// CreateUserModel 创建模型
// @Summary      创建模型
// @Description  用户创建自定义模型或平台默认模型（平台默认模型仅白名单用户可创建）；bizScenes 传 JSON 数组，如 ["标准SQL","智能分析"]
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.CreateUserModelReq  true  "创建模型请求"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.UserModelItem}  "成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败"
// @Router       /model/create [post]
func CreateUserModel(ctx *gin.Context) {
	var req params.CreateUserModelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Model.Create] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)

	m, err := llmmodelService.CreateUserModel(ctx, userName, req)
	if err != nil {
		zlog.Errorf(ctx, "[Model.Create] 创建失败: modelName=%s, err=%v", req.ModelName, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	var bizScenes []string
	_ = json.Unmarshal([]byte(m.BizScenes), &bizScenes)

	components.RenderJsonSucc(ctx, params.UserModelItem{
		ID:                m.ID,
		ModelHash:         m.ModelHash,
		UserName:          m.UserName,
		ModelName:         m.ModelName,
		ModelKey:          m.ModelKey,
		ModelVersion:      m.ModelVersion,
		ApiKey:            "****",
		BizScenes:         bizScenes,
		IsPlatformDefault: m.IsPlatformDefault,
		CreatedAt:         m.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:         m.UpdatedAt.Format("2006-01-02 15:04:05"),
	})
}

// UpdateUserModel 编辑模型
// @Summary      编辑模型
// @Description  编辑模型信息；普通用户仅可改自己的模型，平台默认模型仅白名单用户可编辑；白名单用户可通过 isPlatformDefault 字段变更模型类型（平台默认↔个人），变更时会同步更新 user_name 并校验名称唯一性
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateUserModelReq  true  "编辑模型请求"
// @Success      200  {object} components.DefaultRenderWithTrace  "编辑成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或权限不足"
// @Router       /model/update [post]
func UpdateUserModel(ctx *gin.Context) {
	var req params.UpdateUserModelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Model.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)

	if err := llmmodelService.UpdateUserModel(ctx, userName, req); err != nil {
		zlog.Errorf(ctx, "[Model.Update] 编辑失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// DeleteUserModel 删除模型
// @Summary      删除模型
// @Description  删除模型，创建者本人或白名单用户可删除
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.DeleteUserModelReq  true  "删除模型请求"
// @Success      200  {object} components.DefaultRenderWithTrace  "删除成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或权限不足"
// @Router       /model/delete [post]
func DeleteUserModel(ctx *gin.Context) {
	var req params.DeleteUserModelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Model.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)

	if err := llmmodelService.DeleteUserModel(ctx, userName, req.ID); err != nil {
		zlog.Errorf(ctx, "[Model.Delete] 删除失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// GetUserModelDetail 模型详情
// @Summary      获取模型详情
// @Description  根据ID查询模型完整信息（API Key 不脱敏，用于编辑回显）；白名单用户可查看任意模型，普通用户仅能查看平台默认模型或自己创建的模型；
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.GetUserModelDetailReq  true  "模型详情请求"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.UserModelItem}  "成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或权限不足"
// @Failure      500  {object} components.DefaultRenderWithTrace  "服务内部错误"
// @Router       /model/detail [post]
func GetUserModelDetail(ctx *gin.Context) {
	var req params.GetUserModelDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Model.Detail] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)

	item, err := llmmodelService.GetUserModelDetail(ctx, userName, req.ID)
	if err != nil {
		zlog.Errorf(ctx, "[Model.Detail] 查询失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, item)
}

// ListUserModels 模型列表
// @Summary      获取模型列表
// @Description  白名单用户返回全量模型，普通用户仅返回平台默认模型+自己创建的模型；API Key 始终脱敏；支持按模型名称模糊搜索和业务场景过滤；平台默认模型返回基础积分和赠送积分信息
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.ListUserModelsReq  false  "模型列表请求（可选参数：bizScene 业务场景过滤，modelName 模型名称模糊搜索）"
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.UserModelItem}  "成功返回模型列表"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败"
// @Failure      500  {object} components.DefaultRenderWithTrace  "服务内部错误"
// @Router       /model/list [post]
func ListUserModels(ctx *gin.Context) {
	var req params.ListUserModelsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Model.List] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)

	items, err := llmmodelService.ListUserModels(ctx, userName, req)
	if err != nil {
		zlog.Errorf(ctx, "[Model.List] 查询失败: userName=%s, err=%v", userName, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, items)
}
