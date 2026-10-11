package systemprompt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/components/route"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

func RegisterSystemPrompt(ctx *gin.Context, req *params.RegisterSystemPromptReq, createdBy string) (*model.SystemPrompt, error) {
	// app_key=default 是默认作用域伪 app（全 app 可用），不要求真实 app 存在。
	if !model.IsReservedAppKey(req.AppKey) {
		app, err := model.GetActiveAppByKey(ctx, req.AppKey)
		if err != nil {
			return nil, err
		}
		if app == nil {
			return nil, components.ErrorAppNotFound.Sprintf(req.AppKey)
		}
	}

	rv := req.RouteValues
	if rv == nil {
		rv = []string{}
	}
	routeValues, _ := json.Marshal(rv)
	routeValuesStr := string(routeValues)

	exists, err := model.ExistSystemPromptByAppAndRoute(ctx, req.AppKey, routeValuesStr)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, components.ErrorSystemPromptDuplicate.Sprintf(req.AppKey, routeValuesStr)
	}

	sp := &model.SystemPrompt{
		AppKey:   req.AppKey,
		RouteValues: routeValuesStr,
		Name:        req.Name,
		Content:     req.Content,
		Status:      1,
		CreatedBy:   createdBy,
		UpdatedBy:   createdBy,
	}
	if err := model.CreateSystemPrompt(ctx, sp); err != nil {
		return nil, err
	}
	return sp, nil
}

func UpdateSystemPrompt(ctx *gin.Context, req *params.UpdateSystemPromptReq) (*model.SystemPrompt, error) {
	existing, err := model.GetSystemPromptByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorSystemPromptNotFound.Sprintf(fmt.Sprintf("%d", req.ID))
	}

	updatedBy := helpers.GetUserName(ctx)
	updates := map[string]interface{}{
		"updated_by": updatedBy,
	}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Content != "" {
		updates["content"] = req.Content
	}
	if req.RouteValues != nil {
		updateRv := req.RouteValues
		if len(updateRv) == 0 {
			updateRv = []string{}
		}
		rv, _ := json.Marshal(updateRv)
		newRouteValues := string(rv)

		if newRouteValues != existing.RouteValues {
			exists, err := model.ExistSystemPromptByAppAndRoute(ctx, existing.AppKey, newRouteValues)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, components.ErrorSystemPromptDuplicate.Sprintf(existing.AppKey, newRouteValues)
			}
		}
		updates["route_values"] = newRouteValues
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if err := model.UpdateSystemPromptByID(ctx, req.ID, updates); err != nil {
		return nil, err
	}

	updated, err := model.GetSystemPromptByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, components.ErrorSystemPromptNotFound.Sprintf(fmt.Sprintf("%d", req.ID))
	}
	return updated, nil
}

func DeleteSystemPrompt(ctx *gin.Context, id uint) (*model.SystemPrompt, error) {
	existing, err := model.GetSystemPromptByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorSystemPromptNotFound.Sprintf(fmt.Sprintf("%d", id))
	}

	updatedBy := helpers.GetUserName(ctx)
	if err := model.UpdateSystemPromptByID(ctx, id, map[string]interface{}{"updated_by": updatedBy}); err != nil {
		return nil, err
	}
	if err := model.SoftDeleteSystemPromptByID(ctx, id); err != nil {
		return nil, err
	}

	return existing, nil
}

func GetDetail(ctx *gin.Context, id uint) (*model.SystemPrompt, error) {
	sp, err := model.GetSystemPromptByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if sp == nil {
		return nil, components.ErrorSystemPromptNotFound.Sprintf(fmt.Sprintf("%d", id))
	}
	return sp, nil
}

func ListByAppAndRoute(ctx *gin.Context, appKey string, routeValues []string) ([]model.SystemPrompt, error) {
	// appKey 为空：管理控制台「全部」视图，跨 app 列出，忽略路由过滤。
	if strings.TrimSpace(appKey) == "" {
		return model.ListAllSystemPrompts(ctx)
	}
	return model.ListSystemPromptsByAppAndExactRoute(ctx, appKey, route.SerializeRouteValues(routeValues))
}

// ResolveSystemPrompt 根据 appKey + routeValues 解析所有匹配的系统提示词
// 策略：前缀匹配 → 默认作用域（default）优先，其余按 route_values 从短到长排序 → 全部拼接（从通用到具体）
func ResolveSystemPrompt(ctx *gin.Context, appKey string, routeValues []string) (string, error) {
	prefixes := route.BuildRoutePrefixes(routeValues)
	prompts, err := model.FindSystemPromptsByAppAndRoutes(ctx, appKey, prefixes)
	if err != nil {
		zlog.Warnf(ctx, "[ResolveSystemPrompt] 查询失败: appKey=%s, err=%v", appKey, err)
		return "", err
	}
	if len(prompts) == 0 {
		return "", nil
	}

	sort.Slice(prompts, func(i, j int) bool {
		iDefault, jDefault := model.IsReservedAppKey(prompts[i].AppKey), model.IsReservedAppKey(prompts[j].AppKey)
		if iDefault != jDefault {
			return iDefault // 默认作用域最通用，排最前
		}
		return len(prompts[i].RouteValues) < len(prompts[j].RouteValues)
	})

	var sb strings.Builder
	for i, p := range prompts {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(p.Content)
	}
	return sb.String(), nil
}

// ToSystemPromptResp 将 model.SystemPrompt 转换为 params.SystemPromptResp
func ToSystemPromptResp(sp *model.SystemPrompt) params.SystemPromptResp {
	var routeValues []string
	_ = json.Unmarshal([]byte(sp.RouteValues), &routeValues)

	return params.SystemPromptResp{
		ID:          sp.ID,
		AppKey:   sp.AppKey,
		RouteValues: routeValues,
		Name:        sp.Name,
		Content:     sp.Content,
		Status:      sp.Status,
		CreatedBy:   sp.CreatedBy,
		UpdatedBy:   sp.UpdatedBy,
		CreatedAt:   formatTime(sp.CreatedAt),
		UpdatedAt:   formatTime(sp.UpdatedAt),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
