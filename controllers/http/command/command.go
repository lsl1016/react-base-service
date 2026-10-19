// Package command 自定义斜杠命令管理面 CRUD（docs/plan/20261013_斜杠命令支持设计方案.md §4.5）。
// 表结构与口径照 systemprompt/skill 样板：app_key + route_values 作用域 + status + 软删。
package command

import (
	"encoding/json"
	"regexp"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	commandService "react-base-service/service/command"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// customCommandNamePattern 自定义命令名：小写字母/数字/中划线/下划线，1-32 位，
// 字母或数字开头（与 service/command 的词法解析同源，保持同步）。
var customCommandNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

const maxCustomCommandContentBytes = 100 * 1024

// CreateCommand 创建自定义命令
// @Summary      创建自定义命令
// @Tags         command
// @Accept       json
// @Produce      json
// @Param        req  body     params.CommandCreateReq  true  "创建自定义命令请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.CommandResp}
// @Router       /command/create [post]
func CreateCommand(ctx *gin.Context) {
	var req params.CommandCreateReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Command.Create] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	userName := helpers.GetUserName(ctx)
	command, err := createCommand(ctx, &req, userName)
	if err != nil {
		zlog.Errorf(ctx, "[Command.Create] 创建失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, toCommandResp(command))
}

func createCommand(ctx *gin.Context, req *params.CommandCreateReq, userName string) (*model.Command, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !customCommandNamePattern.MatchString(name) {
		return nil, components.ErrorParamInvalid.Sprintf("命令名仅允许小写字母/数字/中划线/下划线（1-32 位，字母或数字开头）")
	}
	if commandService.IsReservedName(name) {
		return nil, components.ErrorParamInvalid.Sprintf("命令名 /%s 是保留名，不允许自定义命令使用", name)
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, components.ErrorParamInvalid.Sprintf("content 不能为空")
	}
	if len(content) > maxCustomCommandContentBytes {
		return nil, components.ErrorParamInvalid.Sprintf("content 超过上限 100KB")
	}
	routeValuesJSON, err := normalizeRouteValues(req.RouteValues)
	if err != nil {
		return nil, err
	}
	if exists, err := model.FindCommandsByAppAndRoutes(ctx, []string{req.AppKey}, []string{routeValuesJSON}); err != nil {
		return nil, err
	} else {
		for _, c := range exists {
			if c.Name == name {
				return nil, components.ErrorParamInvalid.Sprintf("命令 /%s 在该 app+route 作用域下已存在", name)
			}
		}
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = firstLine(content)
	}
	row := &model.Command{
		CommandKey:   "cmd_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		Name:         name,
		Description:  description,
		ArgumentHint: strings.TrimSpace(req.ArgumentHint),
		Content:      content,
		AppKey:       strings.TrimSpace(req.AppKey),
		RouteValues:  routeValuesJSON,
		Status:       1,
		CreatedBy:    userName,
		UpdatedBy:    userName,
	}
	if err := model.CreateCommand(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// UpdateCommand 更新自定义命令
// @Summary      更新自定义命令
// @Tags         command
// @Accept       json
// @Produce      json
// @Param        req  body     params.CommandUpdateReq  true  "更新自定义命令请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.CommandResp}
// @Router       /command/update [post]
func UpdateCommand(ctx *gin.Context) {
	var req params.CommandUpdateReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Command.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	userName := helpers.GetUserName(ctx)
	command, err := updateCommand(ctx, &req, userName)
	if err != nil {
		zlog.Errorf(ctx, "[Command.Update] 更新失败: commandKey=%s, err=%v", req.CommandKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, toCommandResp(command))
}

func updateCommand(ctx *gin.Context, req *params.CommandUpdateReq, userName string) (*model.Command, error) {
	existing, err := model.GetCommandByKey(ctx, req.CommandKey)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorParamInvalid.Sprintf("命令不存在: %s", req.CommandKey)
	}
	updates := map[string]any{"updated_by": userName}
	if name := strings.ToLower(strings.TrimSpace(req.Name)); name != "" {
		if !customCommandNamePattern.MatchString(name) {
			return nil, components.ErrorParamInvalid.Sprintf("命令名仅允许小写字母/数字/中划线/下划线（1-32 位，字母或数字开头）")
		}
		if commandService.IsReservedName(name) {
			return nil, components.ErrorParamInvalid.Sprintf("命令名 /%s 是保留名，不允许自定义命令使用", name)
		}
		if name != existing.Name {
			sameScope, err := model.FindCommandsByAppAndRoutes(ctx, []string{existing.AppKey}, []string{existing.RouteValues})
			if err != nil {
				return nil, err
			}
			for _, c := range sameScope {
				if c.Name == name {
					return nil, components.ErrorParamInvalid.Sprintf("命令 /%s 在该 app+route 作用域下已存在", name)
				}
			}
		}
		updates["name"] = name
	}
	if req.Description != nil {
		updates["description"] = strings.TrimSpace(*req.Description)
	}
	if req.ArgumentHint != nil {
		updates["argument_hint"] = strings.TrimSpace(*req.ArgumentHint)
	}
	if content := strings.TrimSpace(req.Content); content != "" {
		if len(content) > maxCustomCommandContentBytes {
			return nil, components.ErrorParamInvalid.Sprintf("content 超过上限 100KB")
		}
		updates["content"] = content
	}
	if req.Status != nil {
		if *req.Status != 0 && *req.Status != 1 {
			return nil, components.ErrorParamInvalid.Sprintf("status 仅允许 0/1")
		}
		updates["status"] = *req.Status
	}
	if err := model.UpdateCommandByKey(ctx, req.CommandKey, updates); err != nil {
		return nil, err
	}
	return model.GetCommandByKey(ctx, req.CommandKey)
}

// DeleteCommand 删除自定义命令（软删）
// @Summary      删除自定义命令
// @Tags         command
// @Accept       json
// @Produce      json
// @Param        req  body     params.CommandDeleteReq  true  "删除自定义命令请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=map[string]bool}
// @Router       /command/delete [post]
func DeleteCommand(ctx *gin.Context) {
	var req params.CommandDeleteReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Command.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	if err := model.DeleteCommandByKey(ctx, req.CommandKey); err != nil {
		zlog.Errorf(ctx, "[Command.Delete] 删除失败: commandKey=%s, err=%v", req.CommandKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, gin.H{"deleted": true})
}

// ListAllCommands 管理控制台命令列表
// @Summary      自定义命令列表
// @Description  AppKey 为空时返回全部 app（与 skill 管理口径一致）。
// @Tags         command
// @Accept       json
// @Produce      json
// @Param        req  body     params.CommandListReq  true  "命令列表请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.CommandResp}
// @Router       /command/list [post]
func ListAllCommands(ctx *gin.Context) {
	var req params.CommandListReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Command.List] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	commands, err := model.ListAllCommands(ctx, strings.TrimSpace(req.AppKey))
	if err != nil {
		zlog.Errorf(ctx, "[Command.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	items := make([]params.CommandResp, 0, len(commands))
	for i := range commands {
		items = append(items, toCommandResp(&commands[i]))
	}
	components.RenderJsonSucc(ctx, items)
}

// GetCommandDetail 查询单条自定义命令
// @Summary      自定义命令详情
// @Tags         command
// @Accept       json
// @Produce      json
// @Param        req  body     params.CommandDetailReq  true  "命令详情请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.CommandResp}
// @Router       /command/detail [post]
func GetCommandDetail(ctx *gin.Context) {
	var req params.CommandDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Command.Detail] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	command, err := model.GetCommandByKey(ctx, req.CommandKey)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if command == nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("命令不存在: %s", req.CommandKey))
		return
	}
	components.RenderJsonSucc(ctx, toCommandResp(command))
}

func normalizeRouteValues(routeValues []string) (string, error) {
	if routeValues == nil {
		routeValues = []string{}
	}
	data, err := json.Marshal(routeValues)
	if err != nil {
		return "", components.ErrorParamInvalid.Sprintf("routeValues 不合法: %v", err)
	}
	return string(data), nil
}

func firstLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func toCommandResp(c *model.Command) params.CommandResp {
	var routeValues []string
	_ = json.Unmarshal([]byte(c.RouteValues), &routeValues)
	return params.CommandResp{
		CommandKey:   c.CommandKey,
		Name:         c.Name,
		Description:  c.Description,
		ArgumentHint: c.ArgumentHint,
		AppKey:       c.AppKey,
		RouteValues:  routeValues,
		Content:      c.Content,
		Status:       c.Status,
		CreatedBy:    c.CreatedBy,
		UpdatedBy:    c.UpdatedBy,
		CreatedAt:    c.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:    c.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}
