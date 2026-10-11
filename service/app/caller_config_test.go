package app

import (
	"testing"

	model "react-base-service/models/llm"

	"github.com/stretchr/testify/require"
)

func TestCloneAppConfigSnapshotRebuildsResourceRelations(t *testing.T) {
	snapshot := &model.AppConfigSnapshot{
		Skills:           []model.Skill{{SkillID: "skill_old", AppKey: "source", Name: "skill"}},
		SystemPrompts:    []model.SystemPrompt{{ID: 10, AppKey: "source", Name: "prompt"}},
		Tools:            []model.Tool{{ToolID: "tool_old", AppKey: "source", Name: "tool"}},
		ToolUserPolicies: []model.ToolUserPolicy{{ID: 11, ToolID: "tool_old", WhiteUserList: `["alice"]`, BlackUserList: `["bob"]`}},
		ApiKeys:          []model.ApiKey{{ID: 12, AppKey: "source", Name: "key", ApiKeyValue: "secret"}},
	}

	cloneAppConfigSnapshot(snapshot, "target", "operator")

	require.Equal(t, "target", snapshot.Skills[0].AppKey)
	require.NotEqual(t, "skill_old", snapshot.Skills[0].SkillID)
	require.Equal(t, "target", snapshot.SystemPrompts[0].AppKey)
	require.Zero(t, snapshot.SystemPrompts[0].ID)
	require.Equal(t, "target", snapshot.Tools[0].AppKey)
	require.NotEqual(t, "tool_old", snapshot.Tools[0].ToolID)
	require.Equal(t, snapshot.Tools[0].ToolID, snapshot.ToolUserPolicies[0].ToolID)
	require.Equal(t, `["alice"]`, snapshot.ToolUserPolicies[0].WhiteUserList)
	require.Equal(t, `["bob"]`, snapshot.ToolUserPolicies[0].BlackUserList)
	require.Equal(t, "target", snapshot.ApiKeys[0].AppKey)
	require.Equal(t, "secret", snapshot.ApiKeys[0].ApiKeyValue)
	require.Equal(t, "operator", snapshot.ToolUserPolicies[0].CreatedBy)
}
