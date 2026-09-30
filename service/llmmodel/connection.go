package llmmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/components/route"
	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// 连接管理：LLM 连接（协议 + base url + api key）是模型配置的主体，
// 设计与解析顺序见 docs/模型配置优化方案.md §3.2/§3.4。

const connectionFetchTimeout = 15 * time.Second

// CreateConnection 创建连接；同一 caller+routeValues 组合不允许重复。
func CreateConnection(ctx *gin.Context, req params.CreateConnectionReq, userName string) (*model.Connection, error) {
	protocol := strings.TrimSpace(req.Protocol)
	if !llm.IsValidProtocol(protocol) {
		return nil, components.ErrorConnectionProtocolInvalid.Sprintf(protocol)
	}

	routeValues, _ := json.Marshal(req.RouteValues)
	routeValuesStr := string(routeValues)
	callerKey := strings.TrimSpace(req.CallerKey)

	exists, err := model.ExistConnectionByCallerAndRoute(ctx, callerKey, routeValuesStr)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, components.ErrorConnectionDuplicate.Sprintf(fmt.Sprintf("%s %s", callerKey, routeValuesStr))
	}

	conn := &model.Connection{
		CallerKey:   callerKey,
		RouteValues: routeValuesStr,
		Name:        strings.TrimSpace(req.Name),
		Protocol:    protocol,
		BaseURL:     strings.TrimSpace(req.BaseURL),
		ApiKeyValue: strings.TrimSpace(req.ApiKey),
		Status:      1,
		CreatedBy:   userName,
		UpdatedBy:   userName,
	}
	if err := model.CreateConnection(ctx, conn); err != nil {
		return nil, err
	}
	return conn, nil
}

// UpdateConnectionByID 编辑连接；apiKey 留空 = 保持原值（脱敏显示配套语义）。
func UpdateConnectionByID(ctx *gin.Context, req params.UpdateConnectionReq, userName string) error {
	existing, err := model.GetConnectionByID(ctx, req.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return components.ErrorConnectionNotFound.Sprintf(fmt.Sprintf("%d", req.ID))
	}

	updates := map[string]interface{}{"updated_by": userName}
	if strings.TrimSpace(req.Name) != "" {
		updates["name"] = strings.TrimSpace(req.Name)
	}
	if strings.TrimSpace(req.Protocol) != "" {
		protocol := strings.TrimSpace(req.Protocol)
		if !llm.IsValidProtocol(protocol) {
			return components.ErrorConnectionProtocolInvalid.Sprintf(protocol)
		}
		updates["protocol"] = protocol
	}
	if strings.TrimSpace(req.BaseURL) != "" {
		updates["base_url"] = strings.TrimSpace(req.BaseURL)
	}
	if strings.TrimSpace(req.ApiKey) != "" {
		updates["api_key"] = model.EncryptAPIKey(strings.TrimSpace(req.ApiKey))
	}
	if strings.TrimSpace(req.CallerKey) != "" {
		updates["caller_key"] = strings.TrimSpace(req.CallerKey)
	}
	if req.RouteValues != nil {
		routeValues, _ := json.Marshal(req.RouteValues)
		newRouteValues := string(routeValues)
		if newRouteValues != existing.RouteValues || strings.TrimSpace(req.CallerKey) != existing.CallerKey {
			exists, err := model.ExistConnectionByCallerAndRoute(ctx, strings.TrimSpace(req.CallerKey), newRouteValues)
			if err != nil {
				return err
			}
			if exists {
				return components.ErrorConnectionDuplicate.Sprintf(fmt.Sprintf("%s %s", req.CallerKey, newRouteValues))
			}
		}
		updates["route_values"] = newRouteValues
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	return model.UpdateConnectionByID(ctx, req.ID, updates)
}

// DeleteConnectionByID 删除连接；仍被模型引用时拒绝。
func DeleteConnectionByID(ctx *gin.Context, id uint) error {
	existing, err := model.GetConnectionByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return components.ErrorConnectionNotFound.Sprintf(fmt.Sprintf("%d", id))
	}

	count, err := model.CountUserModelsByConnectionID(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return components.ErrorConnectionInUse.Sprintf(count)
	}

	return model.SoftDeleteConnectionByID(ctx, id)
}

// ListAllConnections 全量连接列表（配置面板用，key 脱敏 + 引用模型数）。
func ListAllConnections(ctx *gin.Context) ([]params.ConnectionItem, error) {
	conns, err := model.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]params.ConnectionItem, 0, len(conns))
	for i := range conns {
		conn := &conns[i]
		// api_key 未随列表解密（ListConnections 不解密），此处统一脱敏占位。
		count, err := model.CountUserModelsByConnectionID(ctx, conn.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, toConnectionItem(conn, count))
	}
	return items, nil
}

func toConnectionItem(conn *model.Connection, modelCount int64) params.ConnectionItem {
	var routeValues []string
	_ = json.Unmarshal([]byte(conn.RouteValues), &routeValues)
	return params.ConnectionItem{
		ID:          conn.ID,
		Name:        conn.Name,
		Protocol:    conn.Protocol,
		BaseURL:     conn.BaseURL,
		ApiKey:      "******",
		CallerKey:   conn.CallerKey,
		RouteValues: routeValues,
		Status:      conn.Status,
		CreatedAt:   conn.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:   conn.UpdatedAt.Format("2006-01-02 15:04:05"),
		ModelCount:  modelCount,
	}
}

// ResolveConnection 按 callerKey + 路由前缀匹配解析最佳连接：
// 先在 caller 精确命中的连接里取 route_values 最长者，没有再取空 caller（全局）兜底。
// 返回 nil 表示未配置连接（调用方回退旧 tblLlmApiKey 口径）。
func ResolveConnection(ctx *gin.Context, callerKey string, routeValues []string) (*model.Connection, error) {
	conns, err := model.FindConnectionsByCallerAndRoutes(ctx, callerKey, route.BuildRoutePrefixes(routeValues))
	if err != nil {
		zlog.Warnf(ctx, "[Connection.Resolve] 查询失败: callerKey=%s, err=%v", callerKey, err)
		return nil, err
	}
	bestExact, bestGlobal := (*model.Connection)(nil), (*model.Connection)(nil)
	for i := range conns {
		conn := &conns[i]
		if conn.Status != 1 {
			continue
		}
		if conn.CallerKey == callerKey {
			if bestExact == nil || len(conn.RouteValues) > len(bestExact.RouteValues) {
				bestExact = conn
			}
		} else if bestGlobal == nil || len(conn.RouteValues) > len(bestGlobal.RouteValues) {
			bestGlobal = conn
		}
	}
	if bestExact != nil {
		return bestExact, nil
	}
	return bestGlobal, nil
}

// GetConnectionByID 取连接（运行时路径复用；含 key 解密）。
func GetConnectionByID(ctx *gin.Context, id uint) (*model.Connection, error) {
	return model.GetConnectionByID(ctx, id)
}

// FetchConnectionModels 服务端代理拉取上游可用模型列表（GET {base}/models）：
// key 不出后端、无 CORS 问题；两个协议的 /models 与 chat 端点同目录层级。
// 返回条目附带本地能力目录自动填充的能力位（未命中目录时全 0，保守）。
func FetchConnectionModels(ctx *gin.Context, id uint) ([]params.ConnectionModelItem, error) {
	conn, err := model.GetConnectionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, components.ErrorConnectionNotFound.Sprintf(fmt.Sprintf("%d", id))
	}
	ids, err := fetchUpstreamModels(conn.BaseURL, conn.Protocol, conn.ApiKeyValue)
	if err != nil {
		return nil, err
	}
	items := make([]params.ConnectionModelItem, 0, len(ids))
	for _, id := range ids {
		caps := llm.LookupModelCapabilities(id)
		items = append(items, params.ConnectionModelItem{
			ID:               id,
			SupportTools:     boolToInt(caps.Known && caps.SupportTools),
			SupportThinking:  boolToInt(caps.Known && caps.SupportThinking),
			SupportVision:    boolToInt(caps.Known && caps.SupportVision),
			ContextTokens:    caps.ContextTokens,
			MaxOutputTokens:  caps.MaxOutputTokens,
		})
	}
	return items, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func fetchUpstreamModels(baseURL, protocol, apiKey string) ([]string, error) {
	modelsURL := llm.ModelsURL(baseURL)
	if modelsURL == "" {
		return nil, components.ErrorConnectionFetchModelsFailed.Sprintf("接入地址为空")
	}

	models, err := fetchModelsFromURL(modelsURL, apiKey)
	if err == nil {
		return models, nil
	}
	// 部分厂商的 Anthropic 兼容面挂在子路径（如 DeepSeek https://api.deepseek.com/anthropic）
	// 只实现 /v1/messages，模型发现仅在 OpenAI 面提供；首选 404 时回退同 host 根 /v1/models。
	// 首选能通（或非 404 类故障）的厂商不受影响。
	if fallbackURL := hostRootModelsURL(modelsURL); fallbackURL != "" && fallbackURL != modelsURL {
		if fallbackModels, fallbackErr := fetchModelsFromURL(fallbackURL, apiKey); fallbackErr == nil {
			return fallbackModels, nil
		}
	}
	return nil, components.ErrorConnectionFetchModelsFailed.Sprintf("%s", summarizeUpstreamError(err))
}

// hostRootModelsURL 取 modelsURL 同 host 根下的 /v1/models（子路径兼容面的回退地址）。
func hostRootModelsURL(modelsURL string) string {
	parsed, err := url.Parse(modelsURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + "/v1/models"
}

// fetchModelsFromURL GET OpenAI 风格模型发现接口（GET /models），解析去重排序后的模型
// ID 列表。非 200 / 解析失败时返回携带 URL 的错误，供上层决定是否回退与透传真因。
func fetchModelsFromURL(modelsURL, apiKey string) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(context.Background(), connectionFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	// 鉴权头双发：OpenAI 兼容网关认 Authorization Bearer，Anthropic 兼容面认 x-api-key；
	// 同时携带不影响只认其一的一侧。
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d (%s): %s", resp.StatusCode, modelsURL, truncateBody(body))
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("响应解析失败 (%s): %s", modelsURL, err)
	}

	models := make([]string, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.Strings(models)
	return models, nil
}

func truncateBody(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}

// checkConnectivityByProtocol 按显式协议（连接路径）做一次最小对话连通性检测；
// 错误文案带上游真因，供用户模型注册（connectionId 模式）复用。
func checkConnectivityByProtocol(ctx *gin.Context, protocol, modelVersion, apiKey, apiURL string) error {
	client, err := llm.GetClientWithProtocol(apiKey, protocol, apiURL, 0)
	if err != nil {
		return components.ErrorUserModelConnectivityFailed.Sprintf(fmt.Sprintf("构建客户端失败: %s", summarizeUpstreamError(err)))
	}

	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	chunkCh, err := client.ChatStream(testCtx, []llm.LLMMessage{
		{Role: "user", Content: "hi"},
	}, modelVersion)
	if err != nil {
		return components.ErrorUserModelConnectivityFailed.Sprintf(fmt.Sprintf("请求失败: %s", summarizeUpstreamError(err)))
	}
	for chunk := range chunkCh {
		if chunk.Error != nil {
			return components.ErrorUserModelConnectivityFailed.Sprintf(fmt.Sprintf("响应错误: %s", summarizeUpstreamError(chunk.Error)))
		}
		if chunk.Content != "" {
			return nil
		}
	}
	return components.ErrorUserModelConnectivityFailed.Sprintf("未收到有效响应")
}
// 发一条最小对话验证推理链路。错误透传上游真因（401/404/超时可辨）。
// CheckConnection 连接级连通性检测：先 /models 验证端点+凭证，再用列表首个模型
// 发一条最小对话验证推理链路。错误透传上游真因（401/404/超时可辨）。
func CheckConnection(ctx *gin.Context, id uint) error {
	conn, err := model.GetConnectionByID(ctx, id)
	if err != nil {
		return err
	}
	if conn == nil {
		return components.ErrorConnectionNotFound.Sprintf(fmt.Sprintf("%d", id))
	}

	models, err := fetchUpstreamModels(conn.BaseURL, conn.Protocol, conn.ApiKeyValue)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return components.ErrorConnectionCheckFailed.Sprintf("模型列表为空，请确认接入地址与凭证")
	}

	client, err := llm.GetClientWithProtocol(conn.ApiKeyValue, conn.Protocol, conn.BaseURL, 0)
	if err != nil {
		zlog.Errorf(ctx, "[Connection.Check] 构建客户端失败: id=%d, err=%v", id, err)
		return components.ErrorConnectionCheckFailed.Sprintf(fmt.Sprintf("构建客户端失败: %s", summarizeUpstreamError(err)))
	}

	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	chunkCh, err := client.ChatStream(testCtx, []llm.LLMMessage{
		{Role: "user", Content: "hi"},
	}, models[0])
	if err != nil {
		zlog.Errorf(ctx, "[Connection.Check] ChatStream 请求失败: id=%d, model=%s, err=%v", id, models[0], err)
		return components.ErrorConnectionCheckFailed.Sprintf(fmt.Sprintf("请求失败(%s): %s", models[0], summarizeUpstreamError(err)))
	}
	for chunk := range chunkCh {
		if chunk.Error != nil {
			zlog.Errorf(ctx, "[Connection.Check] 响应错误: id=%d, model=%s, err=%v", id, models[0], chunk.Error)
			return components.ErrorConnectionCheckFailed.Sprintf(fmt.Sprintf("响应错误(%s): %s", models[0], summarizeUpstreamError(chunk.Error)))
		}
		if chunk.Content != "" {
			return nil
		}
	}
	return components.ErrorConnectionCheckFailed.Sprintf(fmt.Sprintf("未收到有效响应(%s)", models[0]))
}
