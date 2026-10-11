package apikey

import (
	"encoding/json"
	"fmt"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/components/route"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

func RegisterApiKey(ctx *gin.Context, req *params.RegisterApiKeyReq, createdBy string) (*model.ApiKey, error) {
	app, err := model.GetActiveAppByKey(ctx, req.AppKey)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, components.ErrorAppNotFound.Sprintf(req.AppKey)
	}

	rv := req.RouteValues
	if rv == nil {
		rv = []string{}
	}
	routeValues, _ := json.Marshal(rv)
	routeValuesStr := string(routeValues)

	exists, err := model.ExistApiKeyByAppAndRoute(ctx, req.AppKey, routeValuesStr)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, components.ErrorApiKeyDuplicate.Sprintf(req.AppKey, routeValuesStr)
	}

	ak := &model.ApiKey{
		AppKey:   req.AppKey,
		RouteValues: routeValuesStr,
		Name:        req.Name,
		ApiKeyValue: req.ApiKey,
		Status:      1,
		CreatedBy:   createdBy,
		UpdatedBy:   createdBy,
	}
	if err := model.CreateApiKey(ctx, ak); err != nil {
		return nil, err
	}
	return ak, nil
}

func UpdateApiKey(ctx *gin.Context, req *params.UpdateApiKeyReq) (*model.ApiKey, error) {
	existing, err := model.GetApiKeyByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorApiKeyNotFound.Sprintf(fmt.Sprintf("%d", req.ID))
	}

	updatedBy := helpers.GetUserName(ctx)
	updates := map[string]interface{}{
		"updated_by": updatedBy,
	}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.ApiKey != "" {
		updates["api_key"] = req.ApiKey
	}
	if req.RouteValues != nil {
		updateRv := req.RouteValues
		if len(updateRv) == 0 {
			updateRv = []string{}
		}
		rv, _ := json.Marshal(updateRv)
		newRouteValues := string(rv)

		// 路由变更时检查新路由是否已被占用
		if newRouteValues != existing.RouteValues {
			exists, err := model.ExistApiKeyByAppAndRoute(ctx, existing.AppKey, newRouteValues)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, components.ErrorApiKeyDuplicate.Sprintf(existing.AppKey, newRouteValues)
			}
		}
		updates["route_values"] = newRouteValues
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if err := model.UpdateApiKeyByID(ctx, req.ID, updates); err != nil {
		return nil, err
	}

	updated, err := model.GetApiKeyByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, components.ErrorApiKeyNotFound.Sprintf(fmt.Sprintf("%d", req.ID))
	}
	return updated, nil
}

func DeleteApiKey(ctx *gin.Context, id uint) (*model.ApiKey, error) {
	existing, err := model.GetApiKeyByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorApiKeyNotFound.Sprintf(fmt.Sprintf("%d", id))
	}

	updatedBy := helpers.GetUserName(ctx)
	if err := model.UpdateApiKeyByID(ctx, id, map[string]interface{}{"updated_by": updatedBy}); err != nil {
		return nil, err
	}
	if err := model.SoftDeleteApiKeyByID(ctx, id); err != nil {
		return nil, err
	}

	return existing, nil
}

func GetDetail(ctx *gin.Context, id uint) (*model.ApiKey, error) {
	ak, err := model.GetApiKeyByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ak == nil {
		return nil, components.ErrorApiKeyNotFound.Sprintf(fmt.Sprintf("%d", id))
	}
	return ak, nil
}

func ListByAppAndRoute(ctx *gin.Context, appKey string, routeValues []string) ([]model.ApiKey, error) {
	return model.ListApiKeysByAppAndExactRoute(ctx, appKey, route.SerializeRouteValues(routeValues))
}

// ResolveApiKey 根据 appKey + routeValues 解析最佳匹配的 API Key
// 策略：前缀匹配 → 取 route_values 最长的记录
func ResolveApiKey(ctx *gin.Context, appKey string, routeValues []string) (string, error) {
	prefixes := route.BuildRoutePrefixes(routeValues)
	keys, err := model.FindApiKeysByAppAndRoutes(ctx, appKey, prefixes)
	if err != nil {
		zlog.Warnf(ctx, "[ResolveApiKey] 查询失败: appKey=%s, err=%v", appKey, err)
		return "", err
	}
	if len(keys) == 0 {
		return "", nil
	}

	best := keys[0]
	for _, k := range keys[1:] {
		if len(k.RouteValues) > len(best.RouteValues) {
			best = k
		}
	}
	return best.ApiKeyValue, nil
}

// ToApiKeyResp 将 model.ApiKey 转换为 params.ApiKeyResp
func ToApiKeyResp(ak *model.ApiKey) params.ApiKeyResp {
	var routeValues []string
	_ = json.Unmarshal([]byte(ak.RouteValues), &routeValues)

	return params.ApiKeyResp{
		ID:          ak.ID,
		AppKey:   ak.AppKey,
		RouteValues: routeValues,
		Name:        ak.Name,
		ApiKey:      "",
		Status:      ak.Status,
		CreatedBy:   ak.CreatedBy,
		UpdatedBy:   ak.UpdatedBy,
		CreatedAt:   formatTime(ak.CreatedAt),
		UpdatedAt:   formatTime(ak.UpdatedAt),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
