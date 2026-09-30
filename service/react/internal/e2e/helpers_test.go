package e2e

import (
	"net/http"
	"net/http/httptest"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
)

// newHeadlessGinContext 构造无前端请求的 gin.Context（与 react 门面 memory_reflection.go
// 的同名生产构造器等义）：e2e 独立包不可见包内私有符号，自带副本；模型层按 ctx 取用户名，
// 必须显式注入。
func newHeadlessGinContext(userName string) *gin.Context {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/internal/e2e", nil)
	helpers.SetUserName(ginCtx, userName)
	return ginCtx
}
