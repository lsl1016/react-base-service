package llmmodel

import (
	"react-base-service/components"
	"react-base-service/components/params"
	llmmodelService "react-base-service/service/llmmodel"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
	"react-base-service/helpers"
)

// 连接管理接口：LLM 连接（协议 + base url + api key）是模型配置的主体。
// 设计与解析顺序见 docs/模型配置优化方案.md §3.2/§3.4。

// CreateConnection 创建连接
// @Summary      创建 LLM 连接
// @Description  创建协议 + 接入地址 + API Key 的自包含连接；同一 caller+路由组合不允许重复。
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.CreateConnectionReq  true  "创建连接请求"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ConnectionItem}
// @Failure      400  {object} components.DefaultRenderWithTrace
// @Router       /model/connection/create [post]
func CreateConnection(ctx *gin.Context) {
	var req params.CreateConnectionReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Connection.Create] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)
	conn, err := llmmodelService.CreateConnection(ctx, req, userName)
	if err != nil {
		zlog.Errorf(ctx, "[Connection.Create] 创建失败: name=%s, err=%v", req.Name, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{
		"id":          conn.ID,
		"name":        conn.Name,
		"protocol":    conn.Protocol,
		"baseUrl":     conn.BaseURL,
		"callerKey":   conn.CallerKey,
		"status":      conn.Status,
		"modelCount":  0,
	})
}

// UpdateConnection 更新连接
// @Summary      更新连接
// @Description  按 id 更新连接配置；apiKey 留空 = 保持原值。
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateConnectionReq  true  "更新连接请求"
// @Success      200  {object} components.DefaultRenderWithTrace
// @Router       /model/connection/update [post]
func UpdateConnection(ctx *gin.Context) {
	var req params.UpdateConnectionReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Connection.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)
	if err := llmmodelService.UpdateConnectionByID(ctx, req, userName); err != nil {
		zlog.Errorf(ctx, "[Connection.Update] 更新失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// DeleteConnection 删除连接
// @Summary      删除连接
// @Description  按 id 删除连接；仍被模型引用时拒绝。
// @Tags         llmmodel
// @Produce      json
// @Param        req  body     params.DeleteConnectionReq  true  "删除连接请求"
// @Success      200  {object} components.DefaultRenderWithTrace
// @Router       /model/connection/delete [post]
func DeleteConnection(ctx *gin.Context) {
	var req params.DeleteConnectionReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Connection.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	if err := llmmodelService.DeleteConnectionByID(ctx, req.ID); err != nil {
		zlog.Errorf(ctx, "[Connection.Delete] 删除失败: id=%d, err=%v", req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// ListConnections 连接列表
// @Summary      连接列表
// @Description  全量连接列表（key 脱敏 + 引用模型数）。
// @Tags         llmmodel
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.ConnectionItem}
// @Router       /model/connection/list [post]
func ListConnections(ctx *gin.Context) {
	items, err := llmmodelService.ListAllConnections(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[Connection.List] 查询失败: err=%v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, items)
}

// CheckConnection 连接级连通性检测
// @Summary      连接连通性检测
// @Description  先 /models 验证端点+凭证，再用首个模型发最小对话验证推理链路；错误透传上游真因。
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.ConnectionCheckReq  true  "检测请求"
// @Success      200  {object} components.DefaultRenderWithTrace
// @Router       /model/connection/check [post]
func CheckConnection(ctx *gin.Context) {
	var req params.ConnectionCheckReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Connection.Check] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	if err := llmmodelService.CheckConnection(ctx, req.ID); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, gin.H{})
}

// FetchConnectionModels 拉取连接可用模型列表
// @Summary      模型发现（服务端代理）
// @Description  代理调用上游 GET {base}/models，key 不出后端；返回可用模型 ID 列表。
// @Tags         llmmodel
// @Accept       json
// @Produce      json
// @Param        req  body     params.ConnectionModelsReq  true  "拉取请求"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ConnectionModelsResp}
// @Router       /model/connection/fetch_models [post]
func FetchConnectionModels(ctx *gin.Context) {
	var req params.ConnectionModelsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Connection.FetchModels] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	models, err := llmmodelService.FetchConnectionModels(ctx, req.ID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, params.ConnectionModelsResp{Models: models})
}
