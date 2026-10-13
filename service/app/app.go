package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"react-base-service/components"
	model "react-base-service/models/llm"
	skillService "react-base-service/service/skill"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"react-base-service/golib/zlog"
)

func RegisterApp(ctx *gin.Context, appKey, name, description, platform string, allowPlan *int, bindDefaultCapabilities *bool, defaultModelHash string, createdBy string) (*model.App, error) {
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

	defaultModelHash = strings.TrimSpace(defaultModelHash)
	if defaultModelHash != "" {
		if err := validateDefaultModelHash(ctx, defaultModelHash); err != nil {
			return nil, err
		}
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
		DefaultModelHash: defaultModelHash,
		BindDefaultCapabilities: bindDefault,
		Status:  1,
		CreatedBy: createdBy,
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
	// 默认模型非 nil 即更新（含空串=清除）；配置期校验存在性与连接启用状态，
	// 运行期引用失效则回退连接解析（见 llmmodel.ResolveAppDefaultModel）。
	if raw, ok := updates["default_model_hash"]; ok {
		hash, _ := raw.(string)
		hash = strings.TrimSpace(hash)
		if hash != "" {
			if err := validateDefaultModelHash(ctx, hash); err != nil {
				return err
			}
		}
		updates["default_model_hash"] = hash
	}
	return model.UpdateAppByKey(ctx, appKey, updates)
}

// validateDefaultModelHash 校验默认模型可解析：用户模型存在，且引用连接（若有）启用中。
func validateDefaultModelHash(ctx *gin.Context, hash string) error {
	userModel, err := model.GetUserModelByHash(ctx, hash)
	if err != nil {
		return err
	}
	if userModel == nil {
		return components.ErrorUserModelNotFound.Sprintf(hash)
	}
	if userModel.ConnectionID > 0 {
		conn, err := model.GetConnectionByID(ctx, userModel.ConnectionID)
		if err != nil {
			return err
		}
		if conn == nil || conn.Status != 1 {
			return components.ErrorConnectionNotFound.Sprintf(
				fmt.Sprintf("默认模型引用的连接(id=%d)已删除或停用", userModel.ConnectionID))
		}
	}
	return nil
}

func ListAll(ctx *gin.Context) ([]model.App, error) {
	return model.ListApps(ctx)
}
