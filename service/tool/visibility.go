package tool

import (
	"encoding/json"
	"strings"

	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// FindVisibleToolsByAppAndRoutes 在原有启用状态和路由查询结果上，再按当前用户名过滤受限工具。
func FindVisibleToolsByAppAndRoutes(
	ctx *gin.Context,
	scopeKeys []string,
	routePrefixes []string,
	userName string,
) ([]model.Tool, error) {
	tools, err := model.FindToolsByAppAndRoutes(ctx, scopeKeys, routePrefixes)
	if err != nil || len(tools) == 0 {
		return tools, err
	}

	toolIDs := make([]string, 0, len(tools))
	for i := range tools {
		toolIDs = append(toolIDs, tools[i].ToolID)
	}
	policies, err := model.ListToolUserPoliciesByToolIDs(ctx, toolIDs)
	if err != nil {
		return nil, err
	}

	visible, filteredCount := filterToolsByUserName(ctx, tools, policies, strings.TrimSpace(userName))
	if filteredCount > 0 {
		zlog.Infof(ctx, "[ToolUserPolicy.Filter] scopeKeys=%v, candidateCount=%d, policyCount=%d, filteredCount=%d",
			scopeKeys, len(tools), len(policies), filteredCount)
	}
	return visible, nil
}

func filterToolsByUserName(
	ctx *gin.Context,
	tools []model.Tool,
	policies []model.ToolUserPolicy,
	userName string,
) ([]model.Tool, int) {
	policyByToolID := make(map[string]model.ToolUserPolicy, len(policies))
	for i := range policies {
		policyByToolID[policies[i].ToolID] = policies[i]
	}

	visible := make([]model.Tool, 0, len(tools))
	filteredCount := 0
	for i := range tools {
		policy, restricted := policyByToolID[tools[i].ToolID]
		if !restricted {
			visible = append(visible, tools[i])
			continue
		}

		var whiteUserList []string
		if err := json.Unmarshal([]byte(policy.WhiteUserList), &whiteUserList); err != nil {
			filteredCount++
			zlog.Warnf(ctx, "[ToolUserPolicy.InvalidWhiteList] toolId=%s, reason=invalid_json", tools[i].ToolID)
			continue
		}
		normalized, err := normalizeToolUserList(whiteUserList)
		if err != nil {
			filteredCount++
			zlog.Warnf(ctx, "[ToolUserPolicy.InvalidWhiteList] toolId=%s, reason=invalid_or_empty_list", tools[i].ToolID)
			continue
		}

		allowed := false
		for _, allowedUser := range normalized {
			if allowedUser == userName {
				allowed = true
				break
			}
		}
		if allowed {
			visible = append(visible, tools[i])
			continue
		}
		filteredCount++
	}
	return visible, filteredCount
}
