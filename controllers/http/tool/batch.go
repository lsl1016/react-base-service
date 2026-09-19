package tool

import (
	"encoding/json"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	toolService "react-base-service/service/tool"
	"react-base-service/service/mcpgateway/toolconfig"

	"github.com/gin-gonic/gin"
)

type batchRegisterToolReq struct {
	// Tools 是待注册的工具清单（结构与单条 /tool/register 完全一致），
	// 逐条独立校验与落库，单条失败不影响其余条目。
	Tools []params.RegisterToolReq `json:"tools" binding:"required"`
}

type batchRegisterToolItem struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	ToolID string `json:"toolId,omitempty"`
	Error  string `json:"error,omitempty"`
}

// BatchRegisterTool 批量注册工具（对齐 mcp-server 管理台的批量注册交互）。
//
// 每条独立处理：http 工具的 outputSchema 声明 x-output-projection 时先校验投影
// 语法（MCP 网关按该 schema 裁剪响应并渲染字段说明），再复用单条注册逻辑落库；
// 返回逐条成败，全部条目处理完毕（部分成功是正常态）。
// @Summary      批量注册工具
// @Description  逐条注册工具（结构同 /tool/register），返回逐条成败；outputSchema 含投影时校验投影语法
// @Tags         Tool
// @Accept       json
// @Produce      json
// @Param        request body batchRegisterToolReq true "批量注册请求"
// @Success      200 {object} map[string]interface{} "逐条结果"
// @Router       /tool/batch_register [post]
func BatchRegisterTool(ctx *gin.Context) {
	var req batchRegisterToolReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Tool.BatchRegister] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	if len(req.Tools) == 0 {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("tools 不能为空"))
		return
	}
	if len(req.Tools) > 50 {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("单次批量注册不能超过 50 条"))
		return
	}

	userName := helpers.GetUserName(ctx)
	results := make([]batchRegisterToolItem, 0, len(req.Tools))
	created, failed := 0, 0
	for index, item := range req.Tools {
		entry := batchRegisterToolItem{Index: index, Name: item.Name}
		if err := validateBatchToolItem(&item); err != nil {
			entry.Error = err.Error()
			failed++
			results = append(results, entry)
			continue
		}
		registered, err := toolService.RegisterTool(ctx, &item, userName)
		if err != nil {
			entry.Error = err.Error()
			failed++
			results = append(results, entry)
			continue
		}
		entry.ToolID = registered.ToolID
		created++
		results = append(results, entry)
	}
	zlog.Infof(ctx, "[Tool.BatchRegister] 批量注册完成: total=%d created=%d failed=%d", len(req.Tools), created, failed)
	components.RenderJsonSucc(ctx, gin.H{
		"total":   len(req.Tools),
		"created": created,
		"failed":  failed,
		"results": results,
	})
}

// validateBatchToolItem 批量注册的前置校验：http 工具的 outputSchema 存在时
// 校验对象 schema 与投影语法（与 MCP 网关 buildToolDefinition 的要求一致）。
func validateBatchToolItem(item *params.RegisterToolReq) error {
	if item.ToolType != "http" {
		return nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(item.Config, &config); err != nil {
		return components.ErrorParamInvalid.Sprintf("config 不是合法 JSON 对象")
	}
	raw, ok := config["outputSchema"]
	if !ok || len(raw) == 0 {
		return nil
	}
	if err := toolconfig.ValidateOutputSchema(string(raw), false); err != nil {
		return components.ErrorParamInvalid.Sprintf("outputSchema 无效: %s", err.Error())
	}
	return nil
}
