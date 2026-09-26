package caller

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

func RegisterCaller(ctx *gin.Context, callerKey, name, description, platform string, createdBy string) (*model.Caller, error) {
	// "default" 是默认作用域伪 caller 的保留字，不允许注册为真实 caller。
	if model.IsReservedCallerKey(callerKey) {
		return nil, components.ErrorCallerDuplicate.Sprintf(callerKey)
	}
	existing, err := model.GetCallerByKeyUnscoped(ctx, callerKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, components.ErrorCallerDuplicate.Sprintf(callerKey)
	}

	caller := &model.Caller{
		CallerKey:   callerKey,
		Name:        name,
		Description: description,
		Platform:    platform,
		Status:      1,
		CreatedBy:   createdBy,
	}
	if err := model.CreateCaller(ctx, caller); err != nil {
		return nil, err
	}

	// 内置技能包幂等导入（WP3）：新 caller 立即获得官方基线技能；单条失败不阻断 caller 创建。
	if created, err := skillService.EnsureBundledSkillsForCaller(ctx, callerKey, createdBy); err != nil {
		zlog.Warnf(ctx, "[Caller] 内置技能导入部分失败(忽略): caller=%s, err=%v", callerKey, err)
	} else if created > 0 {
		zlog.Infof(ctx, "[Caller] 内置技能已导入: caller=%s, count=%d", callerKey, created)
	}

	return caller, nil
}

func createDefaultSkill(ctx *gin.Context, callerKey, createdBy string) error {
	skillID := "skill_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	routeValues, _ := json.Marshal([]string{})

	skill := &model.Skill{
		SkillID:          skillID,
		Name:             callerKey + "-默认对话",
		Description:      "通用对话技能，当用户问题不匹配任何具体技能时使用",
		TriggerCondition: "用户的问题不属于任何具体技能的范围",
		ExecutionSteps:   "直接基于上下文回答用户问题",
		CallerKey:        callerKey,
		RouteValues:      string(routeValues),
		IsDefault:        1,
		Status:           1,
		CreatedBy:        createdBy,
	}
	return model.CreateSkill(ctx, skill)
}

func GetByKey(ctx *gin.Context, callerKey string) (*model.Caller, error) {
	caller, err := model.GetActiveCallerByKey(ctx, callerKey)
	if err != nil {
		return nil, err
	}
	if caller == nil {
		return nil, components.ErrorCallerNotFound.Sprintf(callerKey)
	}
	return caller, nil
}

func UpdateCaller(ctx *gin.Context, callerKey string, updates map[string]interface{}) error {
	existing, err := model.GetCallerByKey(ctx, callerKey)
	if err != nil {
		return err
	}
	if existing == nil {
		return components.ErrorCallerNotFound.Sprintf(callerKey)
	}
	return model.UpdateCallerByKey(ctx, callerKey, updates)
}

func ListAll(ctx *gin.Context) ([]model.Caller, error) {
	return model.ListCallers(ctx)
}
