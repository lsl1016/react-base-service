package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"react-base-service/conf"
	"react-base-service/web"

	"github.com/gin-gonic/gin"
)

// TestInitLLMRouterWorkflowEnabled 验证 llm.workflow.enabled=true 时管理接口可注册：
// gin 同一位置静态段（/workflow/list）与参数段（/:key/runs）共存，冲突会在注册时 panic。
func TestInitLLMRouterWorkflowEnabled(t *testing.T) {
	origin := conf.CustomConf.LLM.Workflow.Enabled
	conf.CustomConf.LLM.Workflow.Enabled = true
	defer func() { conf.CustomConf.LLM.Workflow.Enabled = origin }()

	engine := gin.New()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("workflow 路由注册 panic: %v", r)
		}
	}()
	InitLLMRouter(engine.Group("/react-base-service"))

	paths := map[string]bool{}
	for _, route := range engine.Routes() {
		paths[route.Path] = true
	}
	for _, expect := range []string{
		"/react-base-service/react/workflow",
		"/react-base-service/react/workflow/list",
		"/react-base-service/react/workflow/:key",
		"/react-base-service/react/workflow/:key/dispatch",
		"/react-base-service/react/workflow/:key/runs",
	} {
		if !paths[expect] {
			t.Errorf("缺路由: %s", expect)
		}
	}
}

// TestWorkflowAdminPageEmbedded 验证管理面板页面已嵌入且路由可达。
func TestWorkflowAdminPageEmbedded(t *testing.T) {
	if _, err := web.FS.ReadFile("react/workflow-admin.html"); err != nil {
		t.Fatalf("管理面板页面未嵌入 web.FS: %v", err)
	}
	engine := gin.New()
	engine.GET("/react/workflow/admin", serveReactEmbedWorkflowAdmin)
	req := httptest.NewRequest(http.MethodGet, "/react/workflow/admin", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("管理面板页面应 200，got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "定时工作流管理台") {
		t.Fatal("管理面板页面内容缺失")
	}
}
