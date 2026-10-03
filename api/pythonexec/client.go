package pythonexec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"react-base-service/conf"

	"react-base-service/golib/base"
	"github.com/gin-gonic/gin"
)

const pathExecute = "/api/pythonexec/execute"

// ExecuteRequest 是 python_exec 沙箱执行请求。
// 沙箱不需要用户凭证，故不含 Cookie 字段（P0-3：不再透传上游 Cookie）。
type ExecuteRequest struct {
	LogID  string `json:"logId"`
	Python string `json:"python"`
	Data   string `json:"data"`
}

type ExecuteResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut"`
}

type executeResponse struct {
	// python-exec 沙箱响应字段名为 message，早期误写成 msg 导致报错详情被丢弃；两者都兼容解析。
	Message string        `json:"message"`
	Msg     string        `json:"msg"`
	Code    int           `json:"code"`
	Data    ExecuteResult `json:"data"`
}

func isSuccessCode(code int) bool {
	return code == 0 || code == http.StatusOK
}

func Execute(ctx context.Context, reqBody *ExecuteRequest) (*ExecuteResult, error) {
	cfg := conf.API.PythonExec
	if strings.TrimSpace(cfg.Domain) == "" {
		return nil, fmt.Errorf("python_exec.domain 未配置")
	}

	headers := make(map[string]string, 2)
	if logID, _ := ctx.Value("logID").(string); logID != "" {
		headers["X-Log-Id"] = logID
	}
	if requestID, _ := ctx.Value("requestId").(string); requestID != "" {
		headers["Uber-Trace-Id"] = requestID
	}

	opt := base.HttpRequestOptions{
		RequestBody: reqBody,
		Encode:      base.EncodeJson,
		ContentType: "application/json",
		Headers:     headers,
	}

	resp, err := cfg.HttpPost(ensureGinContext(ctx), pathExecute, opt)
	if err != nil {
		return nil, fmt.Errorf("调用 Python RPC 失败: %w", err)
	}
	if resp.HttpCode < http.StatusOK || resp.HttpCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Python RPC HTTP %d: %s", resp.HttpCode, string(resp.Response))
	}

	var envelope executeResponse
	if err := json.Unmarshal(resp.Response, &envelope); err != nil {
		return nil, fmt.Errorf("解析 Python RPC 响应失败: %w", err)
	}
	if !isSuccessCode(envelope.Code) {
		msg := strings.TrimSpace(envelope.Message)
		if msg == "" {
			msg = strings.TrimSpace(envelope.Msg)
		}
		if msg == "" {
			msg = "Python RPC 返回非成功状态"
		}
		return nil, fmt.Errorf("%s(code=%d)", msg, envelope.Code)
	}
	return &envelope.Data, nil
}

func ensureGinContext(ctx context.Context) *gin.Context {
	if ginCtx, ok := ctx.(*gin.Context); ok {
		return ginCtx
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, pathExecute, nil).WithContext(ctx)
	ginCtx.Request = req

	if logID, _ := ctx.Value("logID").(string); logID != "" {
		ginCtx.Set("logID", logID)
	}
	if requestID, _ := ctx.Value("requestId").(string); requestID != "" {
		ginCtx.Set("requestId", requestID)
	}

	return ginCtx
}
