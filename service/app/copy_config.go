package app

import (
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CopyConfig 创建目标 App，并复制源 App 的全部配置资源。
func CopyConfig(ctx *gin.Context, req *params.CopyAppConfigReq, operator string) (*params.CopyAppConfigResp, error) {
	sourceAppKey := strings.TrimSpace(req.SourceAppKey)
	targetAppKey := strings.TrimSpace(req.TargetApp.AppKey)
	targetName := strings.TrimSpace(req.TargetApp.Name)
	targetPlatform := strings.TrimSpace(req.TargetApp.Platform)
	if sourceAppKey == "" || targetAppKey == "" || targetName == "" || targetPlatform == "" {
		return nil, components.ErrorParamInvalid.Sprintf("sourceAppKey、targetApp.appKey、name、platform 不能为空")
	}
	if sourceAppKey == targetAppKey {
		return nil, components.ErrorParamInvalid.Sprintf("源 App 与目标 App 不能相同")
	}

	resp := &params.CopyAppConfigResp{TargetAppKey: targetAppKey}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, err := model.GetAppByKeyWithDB(tx, sourceAppKey)
		if err != nil {
			return err
		}
		if source == nil {
			return components.ErrorAppNotFound.Sprintf(sourceAppKey)
		}
		target, err := model.GetAppByKeyUnscopedWithDB(tx, targetAppKey)
		if err != nil {
			return err
		}
		if target != nil {
			return components.ErrorAppDuplicate.Sprintf(targetAppKey)
		}

		snapshot, err := model.LoadAppConfigSnapshotWithDB(tx, sourceAppKey)
		if err != nil {
			return err
		}
		cloneAppConfigSnapshot(snapshot, targetAppKey, operator)

		targetApp := &model.App{
			AppKey:   targetAppKey,
			Name:        targetName,
			Description: req.TargetApp.Description,
			Platform:    targetPlatform,
			// app 级能力开关随配置复制（allow_plan 三态原样拷贝；默认能力池绑定开关同步继承）。
			AllowPlan:               source.AllowPlan,
			BindDefaultCapabilities: source.BindDefaultCapabilities,
			Status:                  1,
			CreatedBy:               operator,
		}
		if err := model.CreateAppConfigWithDB(tx, targetApp, snapshot); err != nil {
			return err
		}

		resp.Copied = params.AppResourceStats{
			Skills:           int64(len(snapshot.Skills)),
			SystemPrompts:    int64(len(snapshot.SystemPrompts)),
			Tools:            int64(len(snapshot.Tools)),
			ToolUserPolicies: int64(len(snapshot.ToolUserPolicies)),
			ApiKeys:          int64(len(snapshot.ApiKeys)),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func cloneAppConfigSnapshot(snapshot *model.AppConfigSnapshot, targetAppKey, operator string) {
	for i := range snapshot.Skills {
		skill := &snapshot.Skills[i]
		resetSkillForCopy(skill, targetAppKey, operator)
	}
	for i := range snapshot.SystemPrompts {
		prompt := &snapshot.SystemPrompts[i]
		prompt.ID = 0
		prompt.AppKey = targetAppKey
		prompt.CreatedBy = operator
		prompt.UpdatedBy = operator
		prompt.CreatedAt = time.Time{}
		prompt.UpdatedAt = time.Time{}
		prompt.DeletedAt = 0
	}

	toolIDMapping := make(map[string]string, len(snapshot.Tools))
	for i := range snapshot.Tools {
		tool := &snapshot.Tools[i]
		oldToolID := tool.ToolID
		tool.ID = 0
		tool.ToolID = newResourceID("tool")
		tool.AppKey = targetAppKey
		tool.CreatedBy = operator
		tool.UpdatedBy = operator
		tool.CreatedAt = time.Time{}
		tool.UpdatedAt = time.Time{}
		tool.DeletedAt = 0
		toolIDMapping[oldToolID] = tool.ToolID
	}
	for i := range snapshot.ToolUserPolicies {
		policy := &snapshot.ToolUserPolicies[i]
		policy.ID = 0
		policy.ToolID = toolIDMapping[policy.ToolID]
		policy.CreatedBy = operator
		policy.UpdatedBy = operator
		policy.CreatedAt = time.Time{}
		policy.UpdatedAt = time.Time{}
		policy.DeletedAt = 0
	}
	for i := range snapshot.ApiKeys {
		apiKey := &snapshot.ApiKeys[i]
		apiKey.ID = 0
		apiKey.AppKey = targetAppKey
		apiKey.CreatedBy = operator
		apiKey.UpdatedBy = operator
		apiKey.CreatedAt = time.Time{}
		apiKey.UpdatedAt = time.Time{}
		apiKey.DeletedAt = 0
	}
}

func resetSkillForCopy(skill *model.Skill, targetAppKey, operator string) {
	skill.ID = 0
	skill.SkillID = newResourceID("skill")
	skill.AppKey = targetAppKey
	skill.CreatedBy = operator
	skill.UpdatedBy = operator
	skill.CreatedAt = time.Time{}
	skill.UpdatedAt = time.Time{}
	skill.DeletedAt = 0
}

func newResourceID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
