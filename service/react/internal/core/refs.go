package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	"react-base-service/service/skillchatfile"

	"github.com/gin-gonic/gin"
)

// ReadResultRef 只允许当前 session/run 读取自己的大结果，过期或不存在统一按未命中处理。
// read_tool_result / inspect_data / python_exec 三个分析类工具共用的大结果读取原语。
func ReadResultRef(ctx *gin.Context, sessionID, runID, resultRef string) (string, bool, error) {
	resultRef = strings.TrimSpace(resultRef)
	if resultRef == "" {
		return "", false, nil
	}
	stored, err := model.GetReactToolResultByResultRef(ctx, resultRef)
	if err != nil {
		return "", false, err
	}
	if stored == nil {
		return "", false, nil
	}
	if stored.SessionID != sessionID {
		return "", false, fmt.Errorf("resultRef forbidden")
	}
	if stored.ExpireAt != nil && time.Now().After(*stored.ExpireAt) {
		return "", false, nil
	}
	return stored.Content, true, nil
}

// SliceResultContent 按 rune 维度切分 resultRef 内容，避免中文等多字节字符被截断。
func SliceResultContent(content string, offset, limit int) (string, bool, int) {
	toolResultCfg := conf.GetReactRuntimeConfig().ToolResult
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = toolResultCfg.ReadLimit
	}
	if limit > toolResultCfg.MaxReadLimit {
		limit = toolResultCfg.MaxReadLimit
	}
	runes := []rune(content)
	if offset >= len(runes) {
		return "", false, len(runes)
	}
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[offset:end]), end < len(runes), end
}

// ResolveAttachmentRecord 按 fileId 读取并校验附件记录（归属、状态、类型、大小），供注入校验、
// read_attachment、inspect_attachment 和 python_exec 附件源共用。
func ResolveAttachmentRecord(ctx context.Context, owner, fileID string) (*model.ChatFileRecord, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, components.ErrorParamInvalid.Sprintf("attachments.fileId 不能为空")
	}
	record, err := model.GetChatFileRecordByFileIDWithContext(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, components.ErrorParamInvalid.Sprintf("附件不存在: %s", fileID)
	}
	if strings.TrimSpace(record.Owner) != strings.TrimSpace(owner) {
		return nil, components.ErrorUserNameMismatch
	}
	if record.Status != model.ChatFileStatusParsed {
		return nil, components.ErrorParamInvalid.Sprintf("附件状态不可用: %s", fileID)
	}
	if !skillchatfile.IsSupportedChatFileExtension(record.Ext) {
		return nil, components.ErrorParamInvalid.Sprintf("仅支持 csv / md / txt 文件")
	}
	if record.Size > skillchatfile.ChatFileUploadMaxBytes {
		return nil, components.ParamInvalidf("文件大小不能超过 50MB")
	}
	return record, nil
}

// RequestCookies 提取当前 HTTP 请求 Cookie，透传给后端 HTTP 工具保持调用态一致。
func RequestCookies(ctx *gin.Context) map[string]string {
	cookies := make(map[string]string)
	for _, cookie := range ctx.Request.Cookies() {
		cookies[cookie.Name] = cookie.Value
	}
	return cookies
}

// CanViewReactSessionHistory 判定历史回放可见性：会话本人始终可见，
// 跨用户查看需登录用户在 playground 白名单内。
func CanViewReactSessionHistory(loginUserName, sessionUserName string) bool {
	loginUserName = strings.TrimSpace(loginUserName)
	if loginUserName != "" && loginUserName == strings.TrimSpace(sessionUserName) {
		return true
	}
	return conf.IsReactPlaygroundWhitelisted(loginUserName)
}

// HistoryTimeFormat 是历史事件时间的统一序列化格式。
const HistoryTimeFormat = "2006-01-02 15:04:05"

// FormatHistoryTime 空时间返回空串，其余按 HistoryTimeFormat 格式化。
func FormatHistoryTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(HistoryTimeFormat)
}
