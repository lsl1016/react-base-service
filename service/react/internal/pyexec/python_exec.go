// Package pyexec 承载 Runtime 分析执行域：python_exec 沙箱执行、inspect_data 结构探查与产物管理。
// 执行函数以 RunContext 显式传参替代引擎状态方法，由 react 门面在 meta tool 分发处构造调用。
package pyexec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"react-base-service/api/pythonexec"
	"react-base-service/components/metrics"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	core "react-base-service/service/react/internal/core"
	netcap "react-base-service/service/react/internal/netcap"
	"react-base-service/service/skillchatfile"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	pythonExecInputSourceToolResult = "tool_result"
	pythonExecInputSourceRawJSON    = "raw_json"
	pythonExecInputSourceText       = "text"
	pythonExecInputSourceExpr       = "expr"
	pythonExecInputSourceAttachment = "attachment"
	pythonExecInputSourceHTTP       = "http"

	// pythonExecInputMaxSourceBytes 是单个 attachment/http 输入源预取并解码后的上限；
	// 沙箱请求体硬上限 8MB，单源预取控制在 6MB 内，为 python 字段与 JSON 转义留余量。
	pythonExecInputMaxSourceBytes = 6 * 1024 * 1024
	// pythonExecInputMaxTotalBytes 是全部输入序列化后的总量上限，超限直接报错回给模型，
	// 避免请求发到沙箱才被 400 拒绝。
	pythonExecInputMaxTotalBytes = 7 * 1024 * 1024
)

type pythonExecInput struct {
	Python string                           `json:"python"`
	Inputs map[string]pythonExecInputSource `json:"inputs"`
}

type pythonExecInputSource struct {
	Type   string          `json:"type"`
	Ref    string          `json:"ref,omitempty"`
	FileID string          `json:"fileId,omitempty"`
	URL    string          `json:"url,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}

type pythonExecOutput struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut"`
	// Artifacts 只回填给模型产物描述符（类型/名称/大小/artifactId），不含 base64 字节，避免撑爆上下文。
	Artifacts []pythonExecArtifactDescriptor `json:"artifacts,omitempty"`
	// DisplayInstruction 是运行时指令：存在可展示产物时提醒模型调用 displayFiles 传 artifactId，
	// 只在需要时出现，比静态工具描述更精准。
	DisplayInstruction string `json:"displayInstruction,omitempty"`
}

// pythonExecArtifact 是解析脚本 stdout 产物的载体。沙箱只能产出文件内容（base64 或原始文本），
// 没有生成 uri 的能力，所以这里刻意没有 uri 字段——脚本 JSON 里即使带了 uri 也会被忽略，
// 无 content 的产物直接 dropped，不透传外部地址；下载地址统一由后端上传 COS 后铸造（见 ArtifactMetaItem）。
type pythonExecArtifact struct {
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

// ArtifactMetaItem 是单个产物给前端的 UI 旁路条目；uri 是后端上传 COS 后拼出的下载地址。
type ArtifactMetaItem struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	URI  string `json:"uri"`
}

// pythonExecArtifactDescriptor 是产物的轻量描述符，只让模型知道"产出了什么"，不含字节。
// ArtifactID 是产物的展示凭据：模型原样传给 displayFiles，由后端换成下载 uri 交前端渲染。
// 刻意不给模型 uri（曾试过让模型在 Markdown 里抄 uri，会被补协议改写成死链，已回滚勿复用）。
// Dropped=true 表示产物因超限未下发前端，DroppedReason 供模型据此缩小后重新生成。
type pythonExecArtifactDescriptor struct {
	Type          string `json:"type"`
	Name          string `json:"name,omitempty"`
	Bytes         int    `json:"bytes,omitempty"`
	ArtifactID    string `json:"artifactId,omitempty"`
	Dropped       bool   `json:"dropped,omitempty"`
	DroppedReason string `json:"droppedReason,omitempty"`
}

// ArtifactMeta 是产物的 UI 旁路载荷，随 toolMeta 落库并回放给前端渲染，不进模型上下文。
type ArtifactMeta struct {
	Artifacts []ArtifactMetaItem `json:"artifacts"`
}

const (
	// pythonExecArtifactMaxFileBytes 是单份产物解码后的大小上限；超过则不上传 COS、标记 dropped
	// 让模型缩小后重生。护住沙箱 stdout 内存与上传体积，与 chat 附件上限一致。
	pythonExecArtifactMaxFileBytes = 10 * 1024 * 1024
	// pythonExecArtifactCOSSubdir 是产物在 COS 顶层前缀下的子目录，与 chat 附件区分。
	pythonExecArtifactCOSSubdir = "react-artifacts"
	// ArtifactURLPrefix 是后端下载接口前缀；前端拿到的 uri 由此 + artifactID 组成，
	// 走本服务自有域名 + IPS 登录态读取（COS 私有桶不直接暴露给前端），资源生命周期由后端自管。
	ArtifactURLPrefix = "/react-base-service/react/artifact/"
)

// pythonExecArtifactUpload 承载单个产物上传所需的全部上下文。
type pythonExecArtifactUpload struct {
	Ctx       *gin.Context
	SessionID string
	RunID     string
	ToolUseID string
	UserName  string
	AppKey string
	Artifact  pythonExecArtifact
	Data      []byte
}

// pythonExecArtifactUploader 上传产物字节到 COS、落库产物元数据，返回 artifactID；抽成变量便于单测注入。
// 下载 uri 由调用方按 ArtifactURLPrefix + artifactID 拼出，铸造点唯一。
var pythonExecArtifactUploader = uploadPythonExecArtifactToCOS

func ExecutePythonExec(rc RunContext, toolUseID string, input json.RawMessage) (string, json.RawMessage, bool, error) {
	var req pythonExecInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", nil, true, fmt.Errorf("python_exec input must be valid JSON: %w", err)
	}
	python := strings.TrimSpace(req.Python)
	if python == "" {
		return "", nil, true, fmt.Errorf("python_exec python is required")
	}
	data, err := resolvePythonExecData(rc, req)
	if err != nil {
		return "", nil, true, err
	}
	// 沙箱强校验 body 里的 logId 非空，缺失会被拒（logId is required）；优先用链路 logID，回退 runID 保证非空。
	logID, _ := rc.RunCtx.Value("logID").(string)
	if strings.TrimSpace(logID) == "" {
		logID = rc.RunID
	}
	// P0-3：沙箱不需要用户 Cookie，一律不透传——减少沙箱侧凭证暴露面。
	resp, err := pythonexec.Execute(rc.RunCtx, &pythonexec.ExecuteRequest{
		LogID:  logID,
		Python: python,
		Data:   data,
	})
	if err != nil {
		zlog.Errorf(rc.GinCtx, "[python_exec] 调用沙箱失败: runId=%s, toolUseId=%s, logId=%s, err=%v", rc.RunID, toolUseID, logID, err)
		return "", nil, true, err
	}
	if resp.ExitCode != 0 || resp.TimedOut {
		if resp.TimedOut {
			metrics.PythonExecTimeoutsTotal.Inc()
		}
		// 执行失败时把 exitCode / timedOut / stderr / stdout 打进日志，否则线上排查什么都看不到。
		zlog.Errorf(rc.GinCtx, "[python_exec] 执行失败: runId=%s, toolUseId=%s, logId=%s, exitCode=%d, timedOut=%t\n--- stderr ---\n%s\n--- stdout ---\n%s",
			rc.RunID, toolUseID, logID, resp.ExitCode, resp.TimedOut, resp.Stderr, resp.Stdout)
	}
	cleanStdout, artifacts := extractPythonExecArtifacts(resp.Stdout)
	descriptors, meta := processPythonExecArtifacts(rc, toolUseID, artifacts)
	out := pythonExecOutput{
		ExitCode:  resp.ExitCode,
		Stdout:    scrubPythonExecBase64(cleanStdout),
		Stderr:    scrubPythonExecBase64(resp.Stderr),
		TimedOut:  resp.TimedOut,
		Artifacts: descriptors,
	}
	for _, d := range descriptors {
		if d.ArtifactID != "" {
			out.DisplayInstruction = "Call displayFiles with the artifactIds of every artifact that is not dropped. Pass each artifactId unchanged."
			break
		}
	}
	body, _ := json.Marshal(out)
	return string(body), meta, resp.ExitCode != 0 || resp.TimedOut, nil
}

// extractPythonExecArtifacts 从脚本 stdout 中剥离 artifacts。约定脚本向 stdout 输出单个 JSON 对象，
// 若其中含 artifacts 数组则取出并从回填给模型的 stdout 中删除（base64 不进上下文）；
// 若 stdout 非 JSON 对象或无 artifacts，则原样返回，保持向后兼容。
func extractPythonExecArtifacts(stdout string) (string, []pythonExecArtifact) {
	trimmed := strings.TrimSpace(stdout)
	if !strings.HasPrefix(trimmed, "{") {
		return stdout, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return stdout, nil
	}
	artifactsRaw, ok := raw["artifacts"]
	if !ok || len(bytes.TrimSpace(artifactsRaw)) == 0 || string(bytes.TrimSpace(artifactsRaw)) == "null" {
		return stdout, nil
	}
	var artifacts []pythonExecArtifact
	if err := json.Unmarshal(artifactsRaw, &artifacts); err != nil {
		return stdout, nil
	}
	delete(raw, "artifacts")
	cleaned, err := json.Marshal(raw)
	if err != nil {
		return stdout, artifacts
	}
	return string(cleaned), artifacts
}

// pythonExecBase64RunThreshold 是判定"疑似文件内容"的连续 base64 字符数阈值。
// 正常文本/JSON 里的单词、数字、标识符远达不到 2048 连续 base64 字符；而任何有意义的
// 文件（哪怕最小的图片）编码后都远超此值，误伤与漏放的空间都很小。
const pythonExecBase64RunThreshold = 2048

// scrubPythonExecBase64 是 base64 进入模型上下文的最后防线：extractPythonExecArtifacts 是乐观解析，
// stdout 非干净单 JSON（混调试输出/双 JSON/被截断）或 base64 放错字段（不在 artifacts 里）时，
// 原始 base64 会随 stdout/stderr 原样回填模型，撑爆上下文还会被模型抄进回答内嵌 data URI。
// 这里对回填文本做逐字节扫描，把超阈值的连续 base64 段整段替换为提示文字，让模型改用 artifacts 重新生成。
// 连续段允许被换行（含 JSON 字符串里的字面 \n、\r 转义）打断而不重新计数，
// 兼容 base64.encodebytes 每 76 字符插换行的形态；替换文字不含引号/反斜杠，嵌在 JSON 字符串里不破坏结构。
func scrubPythonExecBase64(s string) string {
	if len(s) < pythonExecBase64RunThreshold {
		return s
	}
	var b strings.Builder
	lastFlush := 0
	for i := 0; i < len(s); {
		if !isBase64Byte(s[i]) {
			i++
			continue
		}
		// 进入疑似 base64 连续段：统计段内 base64 字符数，换行及其 JSON 转义不打断。
		start, count, j := i, 0, i
		for j < len(s) {
			switch c := s[j]; {
			case isBase64Byte(c):
				count++
				j++
			case c == '\n' || c == '\r':
				j++
			case c == '\\' && j+1 < len(s) && (s[j+1] == 'n' || s[j+1] == 'r'):
				j += 2
			default:
				goto runEnd
			}
		}
	runEnd:
		if count >= pythonExecBase64RunThreshold {
			b.WriteString(s[lastFlush:start])
			fmt.Fprintf(&b, "[已移除%d字符的疑似base64文件内容：文件必须放入 artifacts 数组的 content 字段，不要输出到 stdout/stderr 的其它位置，请修正脚本重新生成]", count)
			lastFlush = j
		}
		i = j
	}
	if lastFlush == 0 {
		return s
	}
	b.WriteString(s[lastFlush:])
	return b.String()
}

// isBase64Byte 判断字节是否属于标准 base64 字符集（含 padding）。
func isBase64Byte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '='
}

// processPythonExecArtifacts 逐个处理产物：解码 content → 校验大小 → 上传 COS 取 artifactID，
// 产出两份数据：给模型的描述符（类型/名称/大小/artifactId，不含 base64 也不含 URL，模型凭 artifactId
// 调 displayFiles 展示）和给前端的 UI 旁路 meta（类型/名称/uri）。超大或上传失败的产物标记 dropped + 原因，供模型据此重生。
func processPythonExecArtifacts(rc RunContext, toolUseID string, artifacts []pythonExecArtifact) ([]pythonExecArtifactDescriptor, json.RawMessage) {
	if len(artifacts) == 0 {
		return nil, nil
	}
	descriptors := make([]pythonExecArtifactDescriptor, 0, len(artifacts))
	kept := make([]ArtifactMetaItem, 0, len(artifacts))
	for _, a := range artifacts {
		d := pythonExecArtifactDescriptor{Type: a.Type, Name: a.Name}

		// 沙箱只能产出文件内容（base64/文本），没有生成 uri 的能力；content 为空即无效产物，直接丢弃。
		if strings.TrimSpace(a.Content) == "" {
			d.Dropped = true
			d.DroppedReason = "产物内容为空（沙箱须在 content 中输出文件内容）"
			descriptors = append(descriptors, d)
			continue
		}

		data, err := decodePythonExecArtifactContent(a.Content)
		if err != nil {
			d.Dropped = true
			d.DroppedReason = "产物内容无法解码（应为 base64 或 data URI）"
			descriptors = append(descriptors, d)
			continue
		}
		d.Bytes = len(data)
		if len(data) > pythonExecArtifactMaxFileBytes {
			d.Dropped = true
			d.DroppedReason = fmt.Sprintf("文件过大(%d 字节)超过 %d 字节上限，未保存；请缩小后重新生成", len(data), pythonExecArtifactMaxFileBytes)
			descriptors = append(descriptors, d)
			continue
		}

		artifactID, err := pythonExecArtifactUploader(pythonExecArtifactUpload{
			Ctx:       rc.GinCtx,
			SessionID: rc.SessionID,
			RunID:     rc.RunID,
			ToolUseID: toolUseID,
			UserName:  rc.UserName,
			AppKey: rc.AppKey,
			Artifact:  a,
			Data:      data,
		})
		if err != nil {
			d.Dropped = true
			d.DroppedReason = "文件保存失败，请重试"
			descriptors = append(descriptors, d)
			continue
		}
		d.ArtifactID = artifactID
		kept = append(kept, ArtifactMetaItem{Type: a.Type, Name: a.Name, URI: ArtifactURLPrefix + artifactID})
		descriptors = append(descriptors, d)
	}
	if len(kept) == 0 {
		return descriptors, nil
	}
	meta, err := json.Marshal(ArtifactMeta{Artifacts: kept})
	if err != nil {
		return descriptors, nil
	}
	return descriptors, meta
}

// decodePythonExecArtifactContent 将产物 content 解码为原始字节：优先按 base64（含 data URI）解码，
// 解码失败则回退当作原始文本字节（文本类产物允许直接放原文）。
func decodePythonExecArtifactContent(content string) ([]byte, error) {
	b64 := strings.TrimSpace(content)
	if b64 == "" {
		return nil, fmt.Errorf("empty content")
	}
	if strings.HasPrefix(b64, "data:") {
		if idx := strings.Index(b64, ","); idx >= 0 {
			b64 = b64[idx+1:]
		}
	}
	b64 = strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "").Replace(b64)
	if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
		return decoded, nil
	}
	// 非 base64：按原始文本字节返回，保持文本类产物内容完整（不裁剪首尾）。
	return []byte(content), nil
}

// uploadPythonExecArtifactToCOS 把产物字节上传到 COS（私有桶、不设有效期），落库产物元数据，
// 返回 artifactID。前端经下载接口（ArtifactURLPrefix + artifactID）读取，桶不直接对外暴露。
func uploadPythonExecArtifactToCOS(u pythonExecArtifactUpload) (string, error) {
	if err := helpers.EnsureCos(); err != nil {
		zlog.Warnf(u.Ctx, "[python_exec] 初始化 COS 失败: name=%s err=%v", u.Artifact.Name, err)
		return "", err
	}
	artifactID := strings.ReplaceAll(uuid.New().String(), "-", "")
	cosKey := buildPythonExecArtifactCosKey(u.SessionID, u.RunID, artifactID, u.Artifact.Name)
	mimeType := pythonExecArtifactContentType(u.Artifact.Type, u.Artifact.Name)
	if err := helpers.CosClient.UploadData(u.Ctx.Request.Context(), u.Data, cosKey, mimeType); err != nil {
		zlog.Warnf(u.Ctx, "[python_exec] 产物上传 COS 失败: name=%s bytes=%d cosKey=%s err=%v", u.Artifact.Name, len(u.Data), cosKey, err)
		return "", err
	}
	if err := model.CreateReactArtifact(u.Ctx, &model.ReactArtifact{
		ArtifactID: artifactID,
		SessionID:  u.SessionID,
		RunID:      u.RunID,
		ToolUseID:  u.ToolUseID,
		UserName:   u.UserName,
		AppKey:  u.AppKey,
		FileName:   u.Artifact.Name,
		MimeType:   mimeType,
		CosKey:     cosKey,
		SizeBytes:  len(u.Data),
	}); err != nil {
		zlog.Warnf(u.Ctx, "[python_exec] 产物元数据落库失败: name=%s cosKey=%s err=%v", u.Artifact.Name, cosKey, err)
		return "", err
	}
	return artifactID, nil
}

// buildPythonExecArtifactCosKey 生成产物 COS key：顶层前缀/子目录/日期/session/run/artifactID-文件名，避免碰撞。
func buildPythonExecArtifactCosKey(sessionID, runID, artifactID, name string) string {
	prefix := strings.Trim(strings.TrimSpace(conf.RConf.Cos.PathPrefix), "/")
	if prefix == "" {
		prefix = "react-base/llm"
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s-%s",
		prefix, pythonExecArtifactCOSSubdir, time.Now().Format("2006-01-02"),
		sanitizePythonExecArtifactName(sessionID), sanitizePythonExecArtifactName(runID),
		artifactID, sanitizePythonExecArtifactName(name))
}

// sanitizePythonExecArtifactName 清洗 COS key 片段，去掉路径分隔符等不安全字符。
func sanitizePythonExecArtifactName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	return strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(name)
}

// pythonExecArtifactContentType 解析产物 Content-Type：优先用带 "/" 的完整 MIME；否则按扩展名推断；再兜底二进制流。
// pythonExecArtifactKnownTypes 内置扩展名优先表：mime.TypeByExtension 依赖系统注册表，
// Windows 上 .csv 会被映射成 Excel 类型，这里保证产物类型判定跨平台一致。
var pythonExecArtifactKnownTypes = map[string]string{
	".csv":  "text/csv",
	".txt":  "text/plain",
	".md":   "text/markdown",
	".json": "application/json",
	".svg":  "image/svg+xml",
	".html": "text/html",
	".htm":  "text/html",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".pdf":  "application/pdf",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xls":  "application/vnd.ms-excel",
	".zip":  "application/zip",
}

func pythonExecArtifactContentType(artifactType, name string) string {
	t := strings.TrimSpace(artifactType)
	if strings.Contains(t, "/") {
		return t
	}
	if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
		if byExt, ok := pythonExecArtifactKnownTypes[ext]; ok {
			return byExt
		}
		if byExt := mime.TypeByExtension(ext); byExt != "" {
			return byExt
		}
	}
	return "application/octet-stream"
}

// resolvePythonExecData 把全部输入源解析为平铺的 alias→value JSON，直接作为沙箱脚本的 stdin
// 输入容器（与工具描述的契约一致：脚本 json.loads(sys.stdin.read()) 后按 inputs[alias] 取值）。
// 沙箱自 P0-1 网络硬隔离后不可出网，attachment（COS）与 http（远端 URL）一律由引擎侧
// 预取并解码为 UTF-8 文本后内联，不再下发 multi-transport 信封（沙箱从未实现解包，
// 曾导致所有带 inputs 的脚本首调 KeyError，见 TEST-REPORT-2026-10-27 D-1）。
func resolvePythonExecData(rc RunContext, req pythonExecInput) (string, error) {
	inputs := make(map[string]interface{}, len(req.Inputs))
	for alias, source := range req.Inputs {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return "", fmt.Errorf("python_exec input alias is required")
		}
		value, err := resolvePythonExecInputSource(rc, alias, source)
		if err != nil {
			return "", err
		}
		inputs[alias] = value
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	if len(data) > pythonExecInputMaxTotalBytes {
		return "", fmt.Errorf("python_exec 输入序列化后共 %d 字节，超过 %d 字节上限；请减少输入内容（大文件建议拆分或改用 read_attachment 分页读取要点后自行构造分析数据）", len(data), pythonExecInputMaxTotalBytes)
	}
	return string(data), nil
}

// resolvePythonExecInputSource 把单个输入源解析为脚本内 inputs[alias] 直接可用的值：
// tool_result/raw_json → JSON 值；text/expr → 字符串；attachment → 附件全文文本（引擎侧取数）；
// http → 远端文本（引擎侧代取，复用 web_fetch 的 SSRF 防护与大小上限）。
func resolvePythonExecInputSource(rc RunContext, alias string, source pythonExecInputSource) (interface{}, error) {
	source.Type = strings.ToLower(strings.TrimSpace(source.Type))
	source.Ref = strings.TrimSpace(source.Ref)
	source.FileID = strings.TrimSpace(source.FileID)

	switch source.Type {
	case pythonExecInputSourceToolResult:
		if source.Ref == "" {
			return nil, fmt.Errorf("python_exec input %s ref is required", alias)
		}
		content, ok, err := core.ReadResultRef(rc.GinCtx, rc.SessionID, rc.RunID, source.Ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("python_exec input %s tool result ref not found or expired", alias)
		}
		return decodePythonExecJSONValue([]byte(content), alias)
	case pythonExecInputSourceRawJSON:
		if len(bytes.TrimSpace(source.Value)) == 0 {
			return nil, fmt.Errorf("python_exec input %s value is required", alias)
		}
		return decodePythonExecJSONValue(source.Value, alias)
	case pythonExecInputSourceText, pythonExecInputSourceExpr:
		if len(bytes.TrimSpace(source.Value)) == 0 {
			return nil, fmt.Errorf("python_exec input %s value is required", alias)
		}
		var text string
		if err := json.Unmarshal(source.Value, &text); err != nil {
			return nil, fmt.Errorf("python_exec input %s value must be a JSON string: %w", alias, err)
		}
		return text, nil
	case pythonExecInputSourceAttachment:
		return resolvePythonExecAttachmentInput(rc, alias, source.FileID)
	case pythonExecInputSourceHTTP:
		return resolvePythonExecHTTPInput(rc, alias, source.URL)
	default:
		return nil, fmt.Errorf("python_exec input %s unsupported source type: %s", alias, source.Type)
	}
}

// resolvePythonExecHTTPInput 引擎侧代取远端文本（http 输入源）。URL 来自模型，走 web_fetch
// 同源的 SSRF 防护（拒绝内网/环回）与文本类 Content-Type 校验；内容受单源大小上限约束。
func resolvePythonExecHTTPInput(rc RunContext, alias, rawURL string) (interface{}, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("python_exec input %s url is required", alias)
	}
	text, err := netcap.FetchRawText(rc.RunCtx, rawURL, pythonExecInputMaxSourceBytes)
	if err != nil {
		return nil, fmt.Errorf("python_exec input %s http 源获取失败: %w", alias, err)
	}
	return text, nil
}

// resolvePythonExecAttachmentInput 引擎侧下载附件并解码为 UTF-8 文本注入（attachment 输入源）：
// 沙箱网络隔离后无法自行读取 COS，这里按 fileId 解析记录（含归属与状态校验）后预取内容。
// 超过单源上限的附件直接报错并给出替代路径，避免序列化后才在总量校验失败。
func resolvePythonExecAttachmentInput(rc RunContext, alias, fileID string) (interface{}, error) {
	if fileID == "" {
		return nil, fmt.Errorf("python_exec input %s fileId is required", alias)
	}
	record, err := core.ResolveAttachmentRecord(rc.GinCtx.Request.Context(), rc.UserName, fileID)
	if err != nil {
		return nil, err
	}
	if record.Size > pythonExecInputMaxSourceBytes {
		return nil, fmt.Errorf("python_exec input %s 附件 %s 大小 %d 字节超过单输入 %d 字节上限；请改用 read_attachment 分页读取要点，或让用户拆分文件", alias, fileID, record.Size, pythonExecInputMaxSourceBytes)
	}
	text, err := downloadDecodeAttachmentText(rc, record)
	if err != nil {
		return nil, err
	}
	if len(text) > pythonExecInputMaxSourceBytes {
		return nil, fmt.Errorf("python_exec input %s 附件 %s 解码后 %d 字节超过单输入 %d 字节上限", alias, fileID, len(text), pythonExecInputMaxSourceBytes)
	}
	return text, nil
}

// downloadDecodeAttachmentText 下载附件内容并按记录 charset 解码为 UTF-8 文本。
func downloadDecodeAttachmentText(rc RunContext, record *model.ChatFileRecord) (string, error) {
	if err := helpers.EnsureCos(); err != nil {
		return "", fmt.Errorf("python_exec 输入附件初始化 COS 失败: %w", err)
	}
	if helpers.CosClient == nil {
		return "", fmt.Errorf("python_exec 输入附件 COS 客户端不可用")
	}
	data, err := helpers.CosClient.DownloadData(rc.GinCtx.Request.Context(), record.CosKey)
	if err != nil {
		return "", fmt.Errorf("python_exec 输入附件下载失败: %s", fileIDOf(record))
	}
	text, err := skillchatfile.DecodeChatFileToUTF8(data, record.Charset)
	if err != nil {
		return "", fmt.Errorf("python_exec 输入附件内容解码失败: %s", fileIDOf(record))
	}
	return text, nil
}

func fileIDOf(record *model.ChatFileRecord) string {
	if record == nil {
		return ""
	}
	return record.FileID
}

func decodePythonExecJSONValue(raw []byte, alias string) (interface{}, error) {
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("python_exec input %s value is not valid JSON: %w", alias, err)
	}
	return value, nil
}
