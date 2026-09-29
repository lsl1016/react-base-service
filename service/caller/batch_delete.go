package caller

import (
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// BatchDelete 在单事务内软删除指定 Caller 及其全部聚合配置资源。
func BatchDelete(ctx *gin.Context, callerKey string) (*params.DeleteCallerResp, error) {
	callerKey = strings.TrimSpace(callerKey)
	if callerKey == "" {
		return nil, components.ParamInvalidf("callerKey 不能为空")
	}

	resp := &params.DeleteCallerResp{CallerKey: callerKey}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		caller, err := model.GetCallerByKeyWithDB(tx, callerKey)
		if err != nil {
			return err
		}
		if caller == nil {
			return components.ErrorCallerNotFound.Sprintf(callerKey)
		}

		stats, err := model.SoftDeleteCallerConfigsWithDB(tx, callerKey)
		if err != nil {
			return err
		}
		resp.Deleted = params.CallerResourceStats{
			Callers:          stats["callers"],
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
