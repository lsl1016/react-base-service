// Package gateway 是多渠道接入网关的管理面：渠道配置 CRUD 与会话映射视图。
// 配置变更后调用 gateway.Reload() 重建渠道连接（见 service/gateway）。
package gateway

import (
	"encoding/json"
	"errors"
	"strings"

	"react-base-service/components"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	"react-base-service/service/gateway"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"react-base-service/golib/zlog"
)

type CreateChannelReq struct {
	Kind               string   `json:"kind" binding:"required"`
	Name               string   `json:"name" binding:"required"`
	AppID              string   `json:"appID"`
	AppSecret          string   `json:"appSecret" binding:"required"`
	Enabled            int      `json:"enabled"`
	DefaultAppKey      string   `json:"defaultAppKey"`
	DefaultRouteValues []string `json:"defaultRouteValues"`
	DefaultUserName    string   `json:"defaultUserName"`
}

type UpdateChannelReq struct {
	ChannelID          string   `json:"channelId" binding:"required"`
	Name               *string  `json:"name"`
	AppID              *string  `json:"appID"`
	// AppSecret 为空表示不修改（回显打码后前端不回传原值）。
	AppSecret          *string  `json:"appSecret"`
	Enabled            *int     `json:"enabled"`
	DefaultAppKey      *string  `json:"defaultAppKey"`
	DefaultRouteValues []string `json:"defaultRouteValues"`
	DefaultUserName    *string  `json:"defaultUserName"`
}

type DeleteChannelReq struct {
	ChannelID string `json:"channelId" binding:"required"`
}

type ChannelResp struct {
	ChannelID          string   `json:"channelId"`
	Kind               string   `json:"kind"`
	Name               string   `json:"name"`
	AppID              string   `json:"appID"`
	AppSecretMasked    string   `json:"appSecretMasked"`
	Enabled            int      `json:"enabled"`
	DefaultAppKey      string   `json:"defaultAppKey"`
	DefaultRouteValues []string `json:"defaultRouteValues"`
	DefaultUserName    string   `json:"defaultUserName"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

type ListChannelSessionsReq struct {
	ChannelID string `json:"channelId"`
	Limit     int    `json:"limit"`
}

// CreateChannel 创建渠道配置
// @Summary      创建渠道配置
// @Description  创建一个多渠道网关渠道（M1：飞书自建应用；凭证 + 默认路由）。
// @Tags         gateway
// @Accept       json
// @Produce      json
// @Param        req  body     CreateChannelReq  true  "创建渠道请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=ChannelResp}  "成功返回渠道详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或创建失败"
// @Router       /gateway/channel/create [post]
func CreateChannel(ctx *gin.Context) {
	var req CreateChannelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	if req.Kind != model.ChannelKindFeishu && req.Kind != model.ChannelKindTelegram {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("kind 仅支持 %s / %s", model.ChannelKindFeishu, model.ChannelKindTelegram))
		return
	}
	// 按渠道类型校验凭证形态：
	//   飞书——AppID/AppSecret 成对（开放平台自建应用）；
	//   Telegram——单个 Bot Token 存 AppSecret，AppID 可选存 @username。
	if req.Kind == model.ChannelKindFeishu && strings.TrimSpace(req.AppID) == "" {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("飞书渠道 appID 不能为空"))
		return
	}
	if strings.TrimSpace(req.DefaultUserName) == "" {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("defaultUserName 不能为空（M1 单用户身份映射）"))
		return
	}

	routeJSON := marshalRouteValues(req.DefaultRouteValues)
	row := &model.ChannelConfig{
		ChannelID:          "gwch_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		Kind:               req.Kind,
		Name:               strings.TrimSpace(req.Name),
		AppID:              strings.TrimSpace(req.AppID),
		AppSecret:          strings.TrimSpace(req.AppSecret),
		Enabled:            normalizeEnabled(req.Enabled),
		DefaultAppKey:      strings.TrimSpace(req.DefaultAppKey),
		DefaultRouteValues: routeJSON,
		DefaultUserName:    strings.TrimSpace(req.DefaultUserName),
		CreatedBy:          helpers.GetUserName(ctx),
		UpdatedBy:          helpers.GetUserName(ctx),
	}
	if err := model.CreateChannelConfig(ctx, row); err != nil {
		zlog.Errorf(ctx, "[Gateway.CreateChannel] 创建失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	if err := gateway.Reload(); err != nil {
		zlog.Warnf(ctx, "[Gateway.CreateChannel] 渠道热重载失败(下次启动生效): %v", err)
	}
	components.RenderJsonSucc(ctx, toChannelResp(row))
}

// UpdateChannel 更新渠道配置
// @Summary      更新渠道配置
// @Description  按 channelId 更新渠道（AppSecret 为空表示不修改）。
// @Tags         gateway
// @Accept       json
// @Produce      json
// @Param        req  body     UpdateChannelReq  true  "更新渠道请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=ChannelResp}  "更新成功并返回渠道详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /gateway/channel/update [post]
func UpdateChannel(ctx *gin.Context) {
	var req UpdateChannelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	row, err := model.GetChannelConfigByChannelID(ctx, req.ChannelID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if row == nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("渠道不存在: %s", req.ChannelID))
		return
	}

	updates := map[string]interface{}{"updated_by": helpers.GetUserName(ctx)}
	if req.Name != nil {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.AppID != nil {
		updates["app_id"] = strings.TrimSpace(*req.AppID)
	}
	if req.AppSecret != nil && strings.TrimSpace(*req.AppSecret) != "" {
		updates["app_secret"] = strings.TrimSpace(*req.AppSecret)
	}
	if req.Enabled != nil {
		updates["enabled"] = normalizeEnabled(*req.Enabled)
	}
	if req.DefaultAppKey != nil {
		updates["default_app_key"] = strings.TrimSpace(*req.DefaultAppKey)
	}
	if req.DefaultRouteValues != nil {
		updates["default_route_values"] = marshalRouteValues(req.DefaultRouteValues)
	}
	if req.DefaultUserName != nil {
		if strings.TrimSpace(*req.DefaultUserName) == "" {
			components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("defaultUserName 不能为空"))
			return
		}
		updates["default_user_name"] = strings.TrimSpace(*req.DefaultUserName)
	}
	if err := model.UpdateChannelConfigByChannelID(ctx, req.ChannelID, updates); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	updated, err := model.GetChannelConfigByChannelID(ctx, req.ChannelID)
	if err != nil || updated == nil {
		components.RenderJsonFail(ctx, components.ErrorDbSelect.Wrap(errors.New("回读失败")))
		return
	}
	if err := gateway.Reload(); err != nil {
		zlog.Warnf(ctx, "[Gateway.UpdateChannel] 渠道热重载失败(下次启动生效): %v", err)
	}
	components.RenderJsonSucc(ctx, toChannelResp(updated))
}

// DeleteChannel 删除渠道配置
// @Summary      删除渠道配置
// @Description  按 channelId 软删除渠道（存量会话映射保留可查）。
// @Tags         gateway
// @Accept       json
// @Produce      json
// @Param        req  body     DeleteChannelReq  true  "删除渠道请求体"
// @Success      200  {object} components.DefaultRenderWithTrace  "删除成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或删除失败"
// @Router       /gateway/channel/delete [post]
func DeleteChannel(ctx *gin.Context) {
	var req DeleteChannelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	if err := model.DeleteChannelConfigByChannelID(ctx, req.ChannelID); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if err := gateway.Reload(); err != nil {
		zlog.Warnf(ctx, "[Gateway.DeleteChannel] 渠道热重载失败(下次启动生效): %v", err)
	}
	components.RenderJsonSucc(ctx, nil)
}

// ListChannels 渠道配置列表
// @Summary      渠道配置列表
// @Description  全量渠道配置（AppSecret 打码）。
// @Tags         gateway
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]ChannelResp}  "成功返回渠道列表"
// @Router       /gateway/channel/list [post]
func ListChannels(ctx *gin.Context) {
	rows, err := model.ListChannelConfigs(ctx, false)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	resp := make([]ChannelResp, 0, len(rows))
	for i := range rows {
		resp = append(resp, toChannelResp(&rows[i]))
	}
	components.RenderJsonSucc(ctx, resp)
}

// ListSessions 渠道会话映射列表
// @Summary      渠道会话映射列表
// @Description  平台 chat ↔ ReAct 会话绑定视图（管理面板「渠道」tab 可视）。
// @Tags         gateway
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace  "成功返回映射列表"
// @Router       /gateway/session/list [post]
func ListSessions(ctx *gin.Context) {
	var req ListChannelSessionsReq
	_ = ctx.ShouldBindJSON(&req)
	rows, err := model.ListChannelSessions(ctx, strings.TrimSpace(req.ChannelID), req.Limit)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	type sessionResp struct {
		ChannelID    string `json:"channelId"`
		ChatID       string `json:"chatId"`
		SessionID    string `json:"sessionId"`
		UserName     string `json:"userName"`
		AppKey       string `json:"appKey"`
		LastMessage  string `json:"lastMessage"`
		LastActiveAt string `json:"lastActiveAt"`
	}
	resp := make([]sessionResp, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, sessionResp{
			ChannelID:    row.ChannelID,
			ChatID:       row.ChatID,
			SessionID:    row.SessionID,
			UserName:     row.UserName,
			AppKey:       row.AppKey,
			LastMessage:  row.LastMessage,
			LastActiveAt: row.LastActiveAt.Format("2006-01-02 15:04:05"),
		})
	}
	components.RenderJsonSucc(ctx, resp)
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

func normalizeEnabled(v int) int {
	if v == 1 {
		return 1
	}
	return 0
}

func marshalRouteValues(values []string) string {
	if values == nil {
		values = []string{}
	}
	out, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(out)
}

func toChannelResp(row *model.ChannelConfig) ChannelResp {
	routeValues := []string{}
	_ = json.Unmarshal([]byte(row.DefaultRouteValues), &routeValues)
	return ChannelResp{
		ChannelID:          row.ChannelID,
		Kind:               row.Kind,
		Name:               row.Name,
		AppID:              row.AppID,
		AppSecretMasked:    maskSecret(row.AppSecret),
		Enabled:            row.Enabled,
		DefaultAppKey:      row.DefaultAppKey,
		DefaultRouteValues: routeValues,
		DefaultUserName:    row.DefaultUserName,
		CreatedAt:          row.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:          row.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

// maskSecret 凭证回显打码：保留首尾各 2 字符，中间星号（过短全星号）。
func maskSecret(secret string) string {
	runes := []rune(strings.TrimSpace(secret))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 6 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:2]) + strings.Repeat("*", 6) + string(runes[len(runes)-2:])
}
