// 身份绑定 CRUD（平台用户 → 基座 userName，跨端投递的寻址地基；
// 入站身份解析实时查库，变更无需触达渠道连接）。
package gateway

import (
	"regexp"
	"strings"

	"react-base-service/components"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"react-base-service/golib/zlog"
)

// platformUserIDPattern 平台用户标识字符集：Telegram 数字 ID、飞书 open_id（ou_/on_ 前缀）公共超集。
var platformUserIDPattern = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,128}$`)

type CreateIdentityReq struct {
	Kind           string `json:"kind" binding:"required"`
	PlatformUserID string `json:"platformUserId" binding:"required"`
	UserName       string `json:"userName" binding:"required"`
	DisplayName    string `json:"displayName"`
}

type UpdateIdentityReq struct {
	IdentityID     string  `json:"identityId" binding:"required"`
	PlatformUserID *string `json:"platformUserId"`
	UserName       *string `json:"userName"`
	DisplayName    *string `json:"displayName"`
}

type DeleteIdentityReq struct {
	IdentityID string `json:"identityId" binding:"required"`
}

type IdentityResp struct {
	IdentityID     string `json:"identityId"`
	Kind           string `json:"kind"`
	PlatformUserID string `json:"platformUserId"`
	UserName       string `json:"userName"`
	DisplayName    string `json:"displayName"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// validateIdentity 校验平台/平台用户标识合法性（kind 不可变更，创建时校验一次）。
func validateIdentity(kind, platformUserID, userName string) error {
	if kind != model.ChannelKindFeishu && kind != model.ChannelKindTelegram {
		return components.ErrorParamInvalid.Sprintf("kind 仅支持 %s / %s", model.ChannelKindFeishu, model.ChannelKindTelegram)
	}
	platformUserID = strings.TrimSpace(platformUserID)
	if !platformUserIDPattern.MatchString(platformUserID) {
		return components.ErrorParamInvalid.Sprintf("platformUserId 非法（允许字母/数字/_/-，≤128 字符）: %q", platformUserID)
	}
	if strings.TrimSpace(userName) == "" || len(userName) > 64 {
		return components.ErrorParamInvalid.Sprintf("userName 不能为空且 ≤64 字符")
	}
	return nil
}

// CreateIdentity 创建身份绑定
// @Summary      创建身份绑定
// @Description  平台用户（Telegram 数字 ID / 飞书 open_id）→ 基座 userName 映射；跨端投递按 userName 寻址。
// @Tags         gateway
// @Accept       json
// @Produce      json
// @Param        req  body     CreateIdentityReq  true  "创建身份绑定请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=IdentityResp}  "成功返回绑定详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或创建失败"
// @Router       /gateway/identity/create [post]
func CreateIdentity(ctx *gin.Context) {
	var req CreateIdentityReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	req.Kind = strings.TrimSpace(req.Kind)
	req.PlatformUserID = strings.TrimSpace(req.PlatformUserID)
	req.UserName = strings.TrimSpace(req.UserName)
	if err := validateIdentity(req.Kind, req.PlatformUserID, req.UserName); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	// 同平台用户重复绑定给可读错误（唯一键兜底）。
	if existing, err := model.GetUserIdentity(ctx, req.Kind, req.PlatformUserID); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	} else if existing != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("平台用户 %s@%s 已绑定 %s，请直接编辑或删除重建", req.PlatformUserID, req.Kind, existing.IdentityID))
		return
	}
	row := &model.UserIdentity{
		IdentityID:     "idnt_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		Kind:           req.Kind,
		PlatformUserID: req.PlatformUserID,
		UserName:       req.UserName,
		DisplayName:    strings.TrimSpace(req.DisplayName),
		CreatedBy:      helpers.GetUserName(ctx),
		UpdatedBy:      helpers.GetUserName(ctx),
	}
	if err := model.CreateUserIdentity(ctx, row); err != nil {
		zlog.Errorf(ctx, "[Gateway.CreateIdentity] 创建失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	// 绑定是入站身份解析的实时数据源，无需触达渠道连接（不 Reload）。
	components.RenderJsonSucc(ctx, toIdentityResp(row))
}

// UpdateIdentity 更新身份绑定
// @Summary      更新身份绑定
// @Description  按 identityId 更新（kind 不可改；platformUserId/userName/displayName 可改）。
// @Tags         gateway
// @Accept       json
// @Produce      json
// @Param        req  body     UpdateIdentityReq  true  "更新身份绑定请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=IdentityResp}  "更新成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /gateway/identity/update [post]
func UpdateIdentity(ctx *gin.Context) {
	var req UpdateIdentityReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	row, err := model.GetUserIdentityByIdentityID(ctx, req.IdentityID)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	if row == nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("身份绑定不存在: %s", req.IdentityID))
		return
	}
	updates := map[string]interface{}{"updated_by": helpers.GetUserName(ctx)}
	if req.PlatformUserID != nil {
		platformUserID := strings.TrimSpace(*req.PlatformUserID)
		if err := validateIdentity(row.Kind, platformUserID, row.UserName); err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		if platformUserID != row.PlatformUserID {
			if existing, err := model.GetUserIdentity(ctx, row.Kind, platformUserID); err != nil {
				components.RenderJsonFail(ctx, err)
				return
			} else if existing != nil {
				components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("平台用户 %s@%s 已绑定 %s", platformUserID, row.Kind, existing.IdentityID))
				return
			}
		}
		updates["platform_user_id"] = platformUserID
	}
	if req.UserName != nil {
		userName := strings.TrimSpace(*req.UserName)
		if err := validateIdentity(row.Kind, row.PlatformUserID, userName); err != nil {
			components.RenderJsonFail(ctx, err)
			return
		}
		updates["user_name"] = userName
	}
	if req.DisplayName != nil {
		updates["display_name"] = strings.TrimSpace(*req.DisplayName)
	}
	if err := model.UpdateUserIdentityByIdentityID(ctx, req.IdentityID, updates); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	updated, err := model.GetUserIdentityByIdentityID(ctx, req.IdentityID)
	if err != nil || updated == nil {
		components.RenderJsonFail(ctx, components.ErrorDbSelect.Sprintf("回读失败"))
		return
	}
	components.RenderJsonSucc(ctx, toIdentityResp(updated))
}

// DeleteIdentity 删除身份绑定
// @Summary      删除身份绑定
// @Description  按 identityId 软删除；存量会话快照不受影响（粘滞），删除后新消息回落渠道默认 userName。
// @Tags         gateway
// @Produce      json
// @Param        req  body     DeleteIdentityReq  true  "删除身份绑定请求体"
// @Success      200  {object} components.DefaultRenderWithTrace  "删除成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或删除失败"
// @Router       /gateway/identity/delete [post]
func DeleteIdentity(ctx *gin.Context) {
	var req DeleteIdentityReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	if err := model.DeleteUserIdentityByIdentityID(ctx, req.IdentityID); err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, nil)
}

// ListIdentities 身份绑定列表
// @Summary      身份绑定列表
// @Description  全量身份绑定（可按 kind/userName 过滤）。
// @Tags         gateway
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]IdentityResp}  "成功返回绑定列表"
// @Router       /gateway/identity/list [post]
func ListIdentities(ctx *gin.Context) {
	var req struct {
		Kind     string `json:"kind"`
		UserName string `json:"userName"`
	}
	_ = ctx.ShouldBindJSON(&req)
	rows, err := model.ListUserIdentities(ctx, strings.TrimSpace(req.Kind), strings.TrimSpace(req.UserName))
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	resp := make([]IdentityResp, 0, len(rows))
	for i := range rows {
		resp = append(resp, toIdentityResp(&rows[i]))
	}
	components.RenderJsonSucc(ctx, resp)
}

func toIdentityResp(row *model.UserIdentity) IdentityResp {
	return IdentityResp{
		IdentityID:     row.IdentityID,
		Kind:           row.Kind,
		PlatformUserID: row.PlatformUserID,
		UserName:       row.UserName,
		DisplayName:    row.DisplayName,
		CreatedAt:      row.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:      row.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}
