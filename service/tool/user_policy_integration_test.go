//go:build integration

package tool

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestToolUserPolicyCRUDAndVisibilityWithLocalMySQL(t *testing.T) {
	originalWorkingDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir("../.."))
	defer func() {
		require.NoError(t, os.Chdir(originalWorkingDir))
	}()

	helpers.PreInit()
	defer helpers.Clear()
	helpers.InitMysql()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	testSuffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	toolID := "codex_tool_policy_" + testSuffix
	appKey := "ctp_" + testSuffix[:24]
	testTool := &model.Tool{
		ToolID:      toolID,
		Name:        "codex_tool_policy_test",
		Description: "tool user policy integration test",
		ToolType:    ToolTypeClient,
		AppKey:   appKey,
		RouteValues: "[]",
		Config:      "{}",
		Status:      0,
		CreatedBy:   "codex",
		UpdatedBy:   "codex",
	}
	require.NoError(t, model.CreateTool(ctx, testTool))
	require.NoError(t, model.UpdateToolByToolID(ctx, toolID, map[string]interface{}{"status": 0}))
	t.Cleanup(func() {
		helpers.MysqlClientLLM.Unscoped().Where("tool_id = ?", toolID).Delete(&model.ToolUserPolicy{})
		helpers.MysqlClientLLM.Unscoped().Where("tool_id = ?", toolID).Delete(&model.Tool{})
	})

	created, err := CreateToolUserPolicy(ctx, createPolicyReq(toolID, " user_b ", "user_a", "user_a"), "codex")
	require.NoError(t, err)
	createdPolicyID := created.ID
	createdResp, err := ToToolUserPolicyResp(created)
	require.NoError(t, err)
	require.Equal(t, []string{"user_a", "user_b"}, createdResp.WhiteUserList)

	_, err = CreateToolUserPolicy(ctx, createPolicyReq(toolID, "user_a"), "codex")
	require.Error(t, err)
	require.True(t, components.ErrorToolUserPolicyDuplicate.Equal(err))

	listed, err := ListToolUserPolicies(ctx, []string{"missing", toolID, toolID})
	require.NoError(t, err)
	require.Len(t, listed, 1)

	require.NoError(t, model.UpdateToolByToolID(ctx, toolID, map[string]interface{}{"status": 1}))
	// 可见性会并入「默认作用域」（app_key=default）下其他用例登记的工具，
	// 断言只关注本用例 app 名下的工具。
	ownVisible := func(tools []model.Tool) []model.Tool {
		own := make([]model.Tool, 0, len(tools))
		for i := range tools {
			if tools[i].AppKey == appKey {
				own = append(own, tools[i])
			}
		}
		return own
	}
	visible, err := FindVisibleToolsByAppAndRoutes(ctx, appKey, []string{"[]"}, "user_a")
	require.NoError(t, err)
	require.Len(t, ownVisible(visible), 1)
	visible, err = FindVisibleToolsByAppAndRoutes(ctx, appKey, []string{"[]"}, "user_c")
	require.NoError(t, err)
	require.Empty(t, ownVisible(visible))

	_, err = UpdateToolUserPolicy(ctx, updatePolicyReq(toolID, "user_c"), "codex")
	require.Error(t, err)
	require.True(t, components.ErrorToolUserPolicyRequiresDisabled.Equal(err))

	require.NoError(t, model.UpdateToolByToolID(ctx, toolID, map[string]interface{}{"status": 0}))
	updated, err := UpdateToolUserPolicy(ctx, updatePolicyReq(toolID, "user_c"), "codex")
	require.NoError(t, err)
	updatedResp, err := ToToolUserPolicyResp(updated)
	require.NoError(t, err)
	require.Equal(t, []string{"user_c"}, updatedResp.WhiteUserList)

	require.NoError(t, helpers.MysqlClientLLM.Model(&model.ToolUserPolicy{}).
		Where("tool_id = ?", toolID).
		Update("black_user_list", `["user_c"]`).Error)
	require.NoError(t, model.UpdateToolByToolID(ctx, toolID, map[string]interface{}{"status": 1}))
	visible, err = FindVisibleToolsByAppAndRoutes(ctx, appKey, []string{"[]"}, "user_c")
	require.NoError(t, err)
	require.Len(t, ownVisible(visible), 1, "black_user_list is reserved and must not affect this release")

	require.NoError(t, DeleteToolUserPolicy(ctx, toolID, "codex"))
	visible, err = FindVisibleToolsByAppAndRoutes(ctx, appKey, []string{"[]"}, "user_a")
	require.NoError(t, err)
	require.Len(t, ownVisible(visible), 1, "deleting the policy must restore unrestricted visibility")

	require.NoError(t, model.UpdateToolByToolID(ctx, toolID, map[string]interface{}{"status": 0}))
	restored, err := CreateToolUserPolicy(ctx, createPolicyReq(toolID, "user_b"), "codex")
	require.NoError(t, err)
	require.Equal(t, createdPolicyID, restored.ID, "soft-deleted policy must be restored instead of inserting a duplicate")
}

func createPolicyReq(toolID string, users ...string) *params.CreateToolUserPolicyReq {
	return &params.CreateToolUserPolicyReq{ToolID: toolID, WhiteUserList: users}
}

func updatePolicyReq(toolID string, users ...string) *params.UpdateToolUserPolicyReq {
	return &params.UpdateToolUserPolicyReq{ToolID: toolID, WhiteUserList: users}
}
