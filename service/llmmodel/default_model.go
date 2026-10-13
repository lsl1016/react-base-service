package llmmodel

import (
	"strings"

	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// app 默认模型（方案 §「app 级默认模型」）：把无 ModelHash 运行路径的兜底解析
// 从「选一条连接」升级为「选一个模型」——default_model_hash 指向某条用户模型，
// 厂商（连接）与模型一次选定，凭证/端点/协议/能力位同源，消除多厂商连接并存时
// 模型与接入点错配的问题。解析顺序见 runtime/workflow 调用方：
// app 默认模型 → 连接（app+路由前缀）→ 旧 app 级 API Key。

// ResolvedDefaultModel app 默认模型的解析结果：用户模型 + 其引用的启用中连接
//（自包含模型 ConnectionID=0 时 Connection 为 nil）。
type ResolvedDefaultModel struct {
	UserModel  *model.UserModel
	Connection *model.Connection
}

// ResolveAppDefaultModel 解析 app 的默认模型：
// 未配置 / 模型已删除 / 引用连接已删除或停用时返回 (nil, nil)，调用方回退下一级兜底
//（连接解析 → 旧 API Key），不因配置漂移（如连接被停用）阻断 run。
func ResolveAppDefaultModel(ctx *gin.Context, app *model.App) (*ResolvedDefaultModel, error) {
	if app == nil {
		return nil, nil
	}
	hash := strings.TrimSpace(app.DefaultModelHash)
	if hash == "" {
		return nil, nil
	}

	userModel, err := model.GetUserModelByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if userModel == nil {
		zlog.Warnf(ctx, "[DefaultModel] 默认模型不存在(回退连接解析): app=%s, hash=%s", app.AppKey, hash)
		return nil, nil
	}

	if userModel.ConnectionID == 0 {
		return &ResolvedDefaultModel{UserModel: userModel}, nil
	}
	conn, err := model.GetConnectionByID(ctx, userModel.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.Status != 1 {
		zlog.Warnf(ctx, "[DefaultModel] 默认模型引用的连接已删除或停用(回退连接解析): app=%s, hash=%s, connectionId=%d",
			app.AppKey, hash, userModel.ConnectionID)
		return nil, nil
	}
	return &ResolvedDefaultModel{UserModel: userModel, Connection: conn}, nil
}
