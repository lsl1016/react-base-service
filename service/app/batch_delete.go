package app

import (
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// BatchDelete 在单事务内软删除指定 App 及其全部聚合配置资源。
func BatchDelete(ctx *gin.Context, appKey string) (*params.DeleteAppResp, error) {
	appKey = strings.TrimSpace(appKey)
	if appKey == "" {
		return nil, components.ParamInvalidf("appKey 不能为空")
	}

	resp := &params.DeleteAppResp{AppKey: appKey}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		app, err := model.GetAppByKeyWithDB(tx, appKey)
		if err != nil {
			return err
		}
		if app == nil {
			return components.ErrorAppNotFound.Sprintf(appKey)
		}

		stats, err := model.SoftDeleteAppConfigsWithDB(tx, appKey)
		if err != nil {
			return err
		}
		resp.Deleted = params.AppResourceStats{
			Apps:          stats["apps"],
			Skills:           stats["skills"],
			SystemPrompts:    stats["systemPrompts"],
			Tools:            stats["tools"],
			ToolUserPolicies: stats["toolUserPolicies"],
			ApiKeys:          stats["apiKeys"],
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}
