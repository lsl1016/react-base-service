package agent

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	agentService "react-base-service/service/agent"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// CreateAgent 创建子 Agent
// @Summary      创建子 Agent
// @Description  创建指定 app 下的子 Agent 定义（工具/Skill 白名单 + 系统提示词 + 模型）。
// @Tags         agent
// @Accept       json
// @Produce      json
// @Param        req  body     params.CreateAgentReq  true  "创建子 Agent 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.AgentResp}  "成功返回子 Agent 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或创建失败"
// @Router       /agent/create [post]
func CreateAgent(ctx *gin.Context) {
	var req params.CreateAgentReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Agent.Create] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	agent, err := agentService.CreateAgent(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Agent.Create] 创建失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, agentService.ToAgentResp(agent))
}

// UpdateAgent 更新子 Agent
// @Summary      更新子 Agent
// @Description  根据 agentId 更新子 Agent 的定义内容或状态。
// @Tags         agent
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateAgentReq  true  "更新子 Agent 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.AgentResp}  "更新成功并返回子 Agent 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /agent/update [post]
func UpdateAgent(ctx *gin.Context) {
	var req params.UpdateAgentReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Agent.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	agent, err := agentService.UpdateAgent(ctx, &req)
	if err != nil {
		zlog.Errorf(ctx, "[Agent.Update] 更新失败: agentId=%s, err=%v", req.AgentID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, agentService.ToAgentResp(agent))
}

// DeleteAgent 删除子 Agent
// @Summary      删除子 Agent
// @Description  根据 agentId 删除指定子 Agent（软删除）。
// @Tags         agent
// @Accept       json
// @Produce      json
// @Param        req  body     params.DeleteAgentReq  true  "删除子 Agent 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.AgentResp}  "删除成功并返回子 Agent 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或删除失败"
// @Router       /agent/delete [post]
func DeleteAgent(ctx *gin.Context) {
	var req params.DeleteAgentReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Agent.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	agent, err := agentService.DeleteAgent(ctx, req.AgentID)
	if err != nil {
		zlog.Errorf(ctx, "[Agent.Delete] 删除失败: agentId=%s, err=%v", req.AgentID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, agentService.ToAgentResp(agent))
}

// ListAgents 查询子 Agent 列表
// @Summary      查询子 Agent 列表
// @Description  按 appKey + routeValues 前缀查询子 Agent 列表；appKey 为空时跨 app 列出全部。
// @Tags         agent
// @Accept       json
// @Produce      json
// @Param        req  body     params.ListAgentsReq  true  "查询子 Agent 列表请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.AgentResp}  "成功返回子 Agent 列表"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或查询失败"
// @Router       /agent/list [post]
func ListAgents(ctx *gin.Context) {
	var req params.ListAgentsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Agent.List] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	agents, err := agentService.ListByAppAndRoute(ctx, req.AppKey, req.RouteValues)
	if err != nil {
		zlog.Errorf(ctx, "[Agent.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	resp := make([]params.AgentResp, 0, len(agents))
	for i := range agents {
		resp = append(resp, agentService.ToAgentResp(&agents[i]))
	}
	components.RenderJsonSucc(ctx, resp)
}

// GetAgentDetail 查询子 Agent 详情
// @Summary      查询子 Agent 详情
// @Description  根据 agentId 查询单个子 Agent 的完整定义。
// @Tags         agent
// @Accept       json
// @Produce      json
// @Param        req  body     params.AgentDetailReq  true  "查询子 Agent 详情请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.AgentResp}  "成功返回子 Agent 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或查询失败"
// @Router       /agent/detail [post]
func GetAgentDetail(ctx *gin.Context) {
	var req params.AgentDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Agent.Detail] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	agent, err := agentService.GetDetail(ctx, req.AgentID)
	if err != nil {
		zlog.Errorf(ctx, "[Agent.Detail] 查询失败: agentId=%s, err=%v", req.AgentID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, agentService.ToAgentResp(agent))
}

// ImportAgent 导入 Markdown 子 Agent 定义
// @Summary      导入 Markdown 子 Agent 定义
// @Description  解析「frontmatter + 正文」格式的 Agent 定义文件并入库；正文即子 Agent 系统提示词。
// @Tags         agent
// @Accept       json
// @Produce      json
// @Param        req  body     params.ImportAgentReq  true  "导入子 Agent 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.AgentResp}  "导入成功并返回子 Agent 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "定义文件非法或创建失败"
// @Router       /agent/import [post]
func ImportAgent(ctx *gin.Context) {
	var req params.ImportAgentReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Agent.Import] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	agent, err := agentService.ImportFromMarkdown(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Agent.Import] 导入失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, agentService.ToAgentResp(agent))
}
