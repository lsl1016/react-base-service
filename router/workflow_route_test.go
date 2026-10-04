package router

import (
	"testing"

	"react-base-service/conf"

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
