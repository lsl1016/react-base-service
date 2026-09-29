//go:build integration

// 环境依赖测试：需要可用 MySQL（TestMain 会初始化全量资源）。
// 本地运行：go test -tags integration ./router/...
package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"react-base-service/conf"
	"react-base-service/helpers"

	"react-base-service/golib/env"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	env.SetRootPath("../")
	if err := os.MkdirAll("log", 0o755); err != nil {
		panic(err)
	}
	helpers.PreInit()
	helpers.InitResource(nil)
	os.Exit(m.Run())
}

func TestReactPlaygroundReferencesSDKAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	req := httptest.NewRequest(http.MethodGet, "/react-base-service/react/playground", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	body := rec.Body.String()
	require.Contains(t, body, "/react-base-service/react/ips.js")
	require.Contains(t, body, "/react-base-service/react/sdk/0.0.1.css")
	require.Contains(t, body, "/react-base-service/react/index.css")
	require.NotContains(t, body, `src="/react-base-service/react/sdk/0.0.1.js"`)
	require.NotContains(t, body, `src="/react-base-service/react/index.js"`)
	require.NotContains(t, body, "<style>")
	require.NotContains(t, body, "window.REACT_AGENT_HOST_CONFIG")
}

func TestReactReplayReferencesReadOnlyAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	req := httptest.NewRequest(http.MethodGet, "/react-base-service/react/replay", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	body := rec.Body.String()
	require.Contains(t, body, `data-app-url="/react-base-service/react/replay.js"`)
	require.Contains(t, body, "/react-base-service/react/sdk/0.0.1.css")
	require.Contains(t, body, "/react-base-service/react/replay.css")
	require.Contains(t, body, "/react-base-service/react/ips.js")
	require.Contains(t, body, "会话重放")
}

func TestReactReplayPublishesPlanDetailLoaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	req := httptest.NewRequest(http.MethodGet, "/react-base-service/react/replay.js", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "loadPlanExecutionDetail: async")
	require.Contains(t, body, "manager.getPlanExecutionDetail")
	require.Contains(t, body, "reducer.applyPlanDetail")
	require.Contains(t, body, "loadPlanStepEvents: async")
	require.Contains(t, body, "manager.getPlanStepEvents")
	require.Contains(t, body, "reducer.applyPlanStepEvents")
	require.Contains(t, body, "replayClient.setReducer(reducer)")
}

func TestReactSDKDirectoryRouteIsRemoved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	req := httptest.NewRequest(http.MethodGet, "/react-base-service/react/sdk/", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestReactPlaygroundPublishesTreeSelectMock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	req := httptest.NewRequest(http.MethodGet, "/react-base-service/react/index.js", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "analysis-theme-select")
	require.Contains(t, body, "kind: 'tree-select'")
	require.Contains(t, body, "loadMockAnalysisThemeOptions")
	require.Contains(t, body, "tag: 'user_select_analysis_theme'")
}

func TestReactPlaygroundPublishesCallerApiKeyAndPlanTemplateManagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	for _, tt := range []struct {
		path     string
		contains []string
	}{
		{
			path: "/react-base-service/react/playground",
			contains: []string{
				`id="copy-caller"`,
				`id="delete-caller"`,
				`id="copy-caller-modal-mask"`,
				`id="delete-caller-modal-mask"`,
			},
		},
		{
			path: "/react-base-service/react/index.js",
			contains: []string{
				"title: 'API Key 管理'",
				"await resource.loadDetail(item, this.config(), resource)",
				"type === 'codeTextarea'",
				"/caller/copy_config",
				"/caller/batch_delete",
				"type=\"password\"",
			},
		},
		{
			path: "/react-base-service/react/index.js",
			contains: []string{
				"title: 'MCP 连接管理'",
				"listPath: '/react/mcp/list'",
				"detailPath: '/react/mcp/detail'",
				"createPath: '/react/mcp/create'",
				"updatePath: '/react/mcp/update'",
				"deletePath: '/react/mcp/delete'",
				"post('/react/mcp/connect'",
				"rp-mcp-badge",
				"renderMcpCards()",
				// 工具页 MCP 工具保护：禁改配置 JSON / 禁删除 + 按连接名筛选
				"mcpFilter: true",
				"readonlyTextarea",
				"item.toolType !== 'mcp'",
				"refreshMcpConnectionOptions()",
				"rp-mcp-kind-badge",
				// Caller 筛选（全部/默认作用域/各 caller）+ 归属 caller 列
				"callerFilter: true",
				"loadCallerFilterOptions()",
				"callerFilterValue()",
				"createTargetCallerKey()",
				// MCP 连接绑定 caller
				"boundCallersText",
				"boundCallers: splitRouteText(draft.boundCallersText)",
			},
		},
		{
			path: "/react-base-service/react/playground",
			contains: []string{
				`id="management-cards"`,
				`id="management-mcp-filter"`,
				`id="management-caller-filter"`,
			},
		},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, tt.path)
		for _, expected := range tt.contains {
			require.Contains(t, rec.Body.String(), expected, tt.path)
		}
	}
}

func TestReactSDKAssetsAreServedWithoutIPSLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	cases := []struct {
		path        string
		contentType string
	}{
		{path: "/react-base-service/react/ips.js", contentType: "application/javascript; charset=utf-8"},
		{path: "/react-base-service/react/index.css", contentType: "text/css; charset=utf-8"},
		{path: "/react-base-service/react/index.js", contentType: "application/javascript; charset=utf-8"},
		{path: "/react-base-service/react/replay.css", contentType: "text/css; charset=utf-8"},
		{path: "/react-base-service/react/replay.js", contentType: "application/javascript; charset=utf-8"},
		{path: "/react-base-service/react/sql-client-tools.js", contentType: "application/javascript; charset=utf-8"},
		{path: "/react-base-service/react/sdk/0.0.1.js", contentType: "application/javascript; charset=utf-8"},
		{path: "/react-base-service/react/sdk/0.0.1.css", contentType: "text/css; charset=utf-8"},
	}

	for _, tt := range cases {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, tt.path)
		require.Equal(t, tt.contentType, rec.Header().Get("Content-Type"), tt.path)
		require.Equal(t, "no-store, no-cache, must-revalidate, max-age=0", rec.Header().Get("Cache-Control"), tt.path)
		require.Equal(t, "no-cache", rec.Header().Get("Pragma"), tt.path)
		require.Equal(t, "0", rec.Header().Get("Expires"), tt.path)
		require.NotEmpty(t, rec.Body.Bytes(), tt.path)
	}
}

func TestReactPlaygroundAuthAnonymousWithoutLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Http(engine)

	req := httptest.NewRequest(http.MethodGet, "/react-base-service/react/playground/auth", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var response struct {
		ErrNo int `json:"errNo"`
		Data  struct {
			UserName string `json:"userName"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	// IPS 登录鉴权已移除：未登录访问回落到匿名用户，页面可用。
	require.Equal(t, 0, response.ErrNo)
	require.Equal(t, "anonymous", response.Data.UserName)
}

func TestReactPlaygroundAuthEnforcesWhitelist(t *testing.T) {
	originalWhitelist := conf.CustomConf.LLM.React.PlaygroundWhitelist
	defer func() { conf.CustomConf.LLM.React.PlaygroundWhitelist = originalWhitelist }()
	conf.CustomConf.LLM.React.PlaygroundWhitelist = []string{"alice"}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	helpers.SetUserName(ctx, "bob")
	checkReactPlaygroundAccess(ctx)

	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestReactPlaygroundWhitelistUsesTrimmedLoginUserName(t *testing.T) {
	originalWhitelist := conf.CustomConf.LLM.React.PlaygroundWhitelist
	defer func() { conf.CustomConf.LLM.React.PlaygroundWhitelist = originalWhitelist }()
	conf.CustomConf.LLM.React.PlaygroundWhitelist = []string{"alice"}

	require.True(t, isPlaygroundWhitelisted(" alice "))
	require.False(t, isPlaygroundWhitelisted("bob"))
}
