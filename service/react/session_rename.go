package react

// 会话重命名：仅更新标题元数据，不触碰 runs/消息，因此不限制活跃 run
// （与 delete/fork 的数据级联操作不同——那两者需要避开正在写入的 run goroutine）。
//
// 口径与纪律：
//   - 归属校验复用 validateReactSessionContext 五元组——只能改自己的会话；
//   - 行锁读-校验-写同事务，与 delete/fork 共用「锁 session 行」的串行化点，
//     避免校验通过后会话已被并发删除的窗口；
//   - 标题归一化（去空白 + rune 截断）与 fork 的自定义标题共用同一上限；
//   - 幂等短路：标题未变化时不写库，避免无谓 bump updated_at（列表按 updated_at 排序）。

import (
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"react-base-service/golib/zlog"
)

// RenameReactSession 重命名一个会话（仅标题元数据）。
func RenameReactSession(ctx *gin.Context, req params.ReactSessionRenameReq) (params.ReactSessionRenameResp, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return params.ReactSessionRenameResp{}, components.ErrorParamInvalid.Sprintf("sessionId 不能为空")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return params.ReactSessionRenameResp{}, components.ErrorParamInvalid.Sprintf("title 不能为空")
	}
	title = truncateSessionTitle(title, customSessionTitleMaxLength)

	resp := params.ReactSessionRenameResp{}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行锁 + 存在性（与 delete/fork 同口径）
		session, err := model.GetReactSessionBySessionIDForUpdate(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if session == nil {
			return components.ErrorReactSessionNotFound.Sprintf(sessionID)
		}
		// 归属校验（与 list/events/delete/fork 同口径）
		if err := validateReactSessionContext(session, helpers.GetUserName(ctx), req.CallerKey, marshalRouteValuesForValidate(req.RouteValues), normalizeSessionType("")); err != nil {
			return err
		}
		// 幂等短路：标题未变化直接成功，不写库
		if session.Title == title {
			resp.Renamed = true
			resp.Title = title
			return nil
		}
		if err := model.UpdateReactSessionBySessionIDWithDB(ctx, tx, sessionID, map[string]any{"title": title}); err != nil {
			return err
		}
		resp.Renamed = true
		resp.Title = title
		return nil
	})
	if err != nil {
		return params.ReactSessionRenameResp{}, err
	}
	zlog.Infof(ctx, "[React.Session] 会话已重命名: sessionId=%s, title=%s", sessionID, title)
	return resp, nil
}
