package caller

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

// CopyConfig 创建目标 Caller，并复制源 Caller 的全部配置资源。
func CopyConfig(ctx *gin.Context, req *params.CopyCallerConfigReq, operator string) (*params.CopyCallerConfigResp, error) {
	sourceCallerKey := strings.TrimSpace(req.SourceCallerKey)
	targetCallerKey := strings.TrimSpace(req.TargetCaller.CallerKey)
	targetName := strings.TrimSpace(req.TargetCaller.Name)
	targetPlatform := strings.TrimSpace(req.TargetCaller.Platform)
	if sourceCallerKey == "" || targetCallerKey == "" || targetName == "" || targetPlatform == "" {
		return nil, components.ErrorParamInvalid.Sprintf("sourceCallerKey、targetCaller.callerKey、name、platform 不能为空")
	}
	if sourceCallerKey == targetCallerKey {
		return nil, components.ErrorParamInvalid.Sprintf("源 Caller 与目标 Caller 不能相同")
	}

	resp := &params.CopyCallerConfigResp{TargetCallerKey: targetCallerKey}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, err := model.GetCallerByKeyWithDB(tx, sourceCallerKey)
		if err != nil {
			return err
		}
		if source == nil {
			return components.ErrorCallerNotFound.Sprintf(sourceCallerKey)
		}
		target, err := model.GetCallerByKeyUnscopedWithDB(tx, targetCallerKey)
		if err != nil {
			return err
		}
		if target != nil {
			return components.ErrorCallerDuplicate.Sprintf(targetCallerKey)
		}

		snapshot, err := model.LoadCallerConfigSnapshotWithDB(tx, sourceCallerKey)
		if err != nil {
			return err
		}
		cloneCallerConfigSnapshot(snapshot, targetCallerKey, operator)

		targetCaller := &model.Caller{
			CallerKey:   targetCallerKey,
			Name:        targetName,
			Description: req.TargetCaller.Description,
			Platform:    targetPlatform,
			// caller 级能力开关随配置复制（当前仅 allow_plan，三态原样拷贝）。
			AllowPlan: source.AllowPlan,
			Status:    1,
			CreatedBy: operator,
		}
		if err := model.CreateCallerConfigWithDB(tx, targetCaller, snapshot); err != nil {
			return err
		}

		resp.Copied = params.CallerResourceStats{
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

func cloneCallerConfigSnapshot(snapshot *model.CallerConfigSnapshot, targetCallerKey, operator string) {
	for i := range snapshot.Skills {
		skill := &snapshot.Skills[i]
		resetSkillForCopy(skill, targetCallerKey, operator)
	}
	for i := range snapshot.SystemPrompts {
		prompt := &snapshot.SystemPrompts[i]
		prompt.ID = 0
		prompt.CallerKey = targetCallerKey
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
		tool.CallerKey = targetCallerKey
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
		apiKey.CallerKey = targetCallerKey
		apiKey.CreatedBy = operator
		apiKey.UpdatedBy = operator
		apiKey.CreatedAt = time.Time{}
		apiKey.UpdatedAt = time.Time{}
		apiKey.DeletedAt = 0
	}
}

func resetSkillForCopy(skill *model.Skill, targetCallerKey, operator string) {
	skill.ID = 0
	skill.SkillID = newResourceID("skill")
	skill.CallerKey = targetCallerKey
	skill.CreatedBy = operator
	skill.UpdatedBy = operator
	skill.CreatedAt = time.Time{}
	skill.UpdatedAt = time.Time{}
	skill.DeletedAt = 0
}

func newResourceID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
