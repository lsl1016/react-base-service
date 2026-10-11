package app

import (
	"encoding/json"
	"strings"

	"react-base-service/components"
	model "react-base-service/models/llm"
	skillService "react-base-service/service/skill"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"react-base-service/golib/zlog"
)

func RegisterApp(ctx *gin.Context, appKey, name, description, platform string, allowPlan *int, bindDefaultCapabilities *bool, createdBy string) (*model.App, error) {
	// "default" 是默认作用域伪 app 的保留字，不允许注册为真实 app。
	if model.IsReservedAppKey(appKey) {
		return nil, components.ErrorAppDuplicate.Sprintf(appKey)
	}
	existing, err := model.GetAppByKeyUnscoped(ctx, appKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, components.ErrorAppDuplicate.Sprintf(appKey)
	}

	// bindDefaultCapabilities 缺省为绑定（1）：新 app 默认继承 default 伪 app 的公共能力池；
	// 显式传 false 的 app 只解析自身名下工具与 Skill。
	bindDefault := 1
	if bindDefaultCapabilities != nil && !*bindDefaultCapabilities {
		bindDefault = 0
	}
	app := &model.App{
		AppKey:   appKey,
		Name:        name,
		Description: description,
		Platform:    platform,
		AllowPlan:   allowPlan,
		BindDefaultCapabilities: bindDefault,
		Status:      1,
		CreatedBy:   createdBy,
	}
	if err := model.CreateApp(ctx, app); err != nil {
		return nil, err
	}

	// 内置技能包幂等导入（WP3）：新 app 立即获得官方基线技能；单条失败不阻断 app 创建。
	if created, err := skillService.EnsureBundledSkillsForApp(ctx, appKey, createdBy); err != nil {
		zlog.Warnf(ctx, "[App] 内置技能导入部分失败(忽略): app=%s, err=%v", appKey, err)
	} else if created > 0 {
		zlog.Infof(ctx, "[App] 内置技能已导入: app=%s, count=%d", appKey, created)
	}

	return app, nil
}

func createDefaultSkill(ctx *gin.Context, appKey, createdBy string) error {
	skillID := "skill_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	routeValues, _ := json.Marshal([]string{})

	skill := &model.Skill{
		SkillID:          skillID,
		Name:             appKey + "-默认对话",
		Description:      "通用对话技能，当用户问题不匹配任何具体技能时使用",
		TriggerCondition: "用户的问题不属于任何具体技能的范围",
		ExecutionSteps:   "直接基于上下文回答用户问题",
		AppKey:        appKey,
		RouteValues:      string(routeValues),
		IsDefault:        1,
		Status:           1,
		CreatedBy:        createdBy,
	}
	return model.CreateSkill(ctx, skill)
}

func GetByKey(ctx *gin.Context, appKey string) (*model.App, error) {
	app, err := model.GetActiveAppByKey(ctx, appKey)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, components.ErrorAppNotFound.Sprintf(appKey)
	}
	return app, nil
}

func UpdateApp(ctx *gin.Context, appKey string, updates map[string]interface{}) error {
	existing, err := model.GetAppByKey(ctx, appKey)
	if err != nil {
		return err
	}
	if existing == nil {
		return components.ErrorAppNotFound.Sprintf(appKey)
	}
	return model.UpdateAppByKey(ctx, appKey, updates)
}

func ListAll(ctx *gin.Context) ([]model.App, error) {
	return model.ListApps(ctx)
}
