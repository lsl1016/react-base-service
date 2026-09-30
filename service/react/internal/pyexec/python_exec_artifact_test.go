package pyexec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var errStubUpload = errors.New("stub upload failure")

func TestExtractPythonExecArtifacts(t *testing.T) {
	pngBytes := []byte("fake-png-bytes")
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	stdout := `{"summary":"ok","artifacts":[{"type":"image/png","name":"chart.png","content":"` + b64 + `"}]}`

	clean, artifacts := extractPythonExecArtifacts(stdout)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0].Content != b64 || artifacts[0].Name != "chart.png" {
		t.Fatalf("unexpected artifact: %+v", artifacts[0])
	}
	// base64 必须从回填给模型的 stdout 中剥离。
	if strings.Contains(clean, b64) || strings.Contains(clean, "artifacts") {
		t.Fatalf("clean stdout should not contain base64 or artifacts key: %s", clean)
	}
	if !strings.Contains(clean, `"summary":"ok"`) {
		t.Fatalf("clean stdout should retain other fields: %s", clean)
	}
}

func TestScrubPythonExecBase64(t *testing.T) {
	longB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 4096)) // >2048 字符

	// 场景1：JSON 合法但 base64 放错字段（不在 artifacts 里）→ 必须被移除，其余字段保留。
	wrongField := `{"summary":"ok","chart":"` + longB64 + `"}`
	got := scrubPythonExecBase64(wrongField)
	if strings.Contains(got, longB64[:100]) {
		t.Fatalf("base64 in wrong field must be scrubbed: %s", got[:200])
	}
	if !strings.Contains(got, `"summary":"ok"`) || !strings.Contains(got, "已移除") {
		t.Fatalf("scrub should keep other fields and leave a hint: %s", got)
	}
	// 移除后仍是合法 JSON（提示文字不含引号/反斜杠，不破坏字符串结构）。
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("scrubbed output should stay valid JSON: %v\n%s", err, got)
	}

	// 场景2：stdout 混了调试输出导致剥离失败，base64 随原文回填 → 同样被移除。
	dirty := "处理完成\n" + `{"artifacts":[{"type":"image/png","name":"a.png","content":"` + longB64 + `"}]}`
	if got := scrubPythonExecBase64(dirty); strings.Contains(got, longB64[:100]) || !strings.Contains(got, "处理完成") {
		t.Fatalf("base64 after failed extraction must be scrubbed, prose kept: %s", got[:200])
	}

	// 场景3：base64.encodebytes 形态（每 76 字符插 \n，JSON 里成字面 \\n）→ 换行不打断计数，仍被移除。
	var wrapped strings.Builder
	for i := 0; i < len(longB64); i += 76 {
		end := i + 76
		if end > len(longB64) {
			end = len(longB64)
		}
		wrapped.WriteString(longB64[i:end])
		wrapped.WriteString(`\n`)
	}
	if got := scrubPythonExecBase64(`{"img":"` + wrapped.String() + `"}`); strings.Contains(got, longB64[:100]) {
		t.Fatalf("newline-wrapped base64 must be scrubbed: %s", got[:200])
	}

	// 场景4：正常内容不受影响——短 base64、普通 JSON、中文文本均原样返回。
	for _, s := range []string{
		`{"summary":"ok","rows":[1,2,3]}`,
		`{"token":"` + longB64[:500] + `"}`, // 短 base64（如哈希/ID）不误伤
		"分析完成，共处理 1024 条记录",
		"",
	} {
		if got := scrubPythonExecBase64(s); got != s {
			t.Fatalf("normal content must pass through unchanged: %q -> %q", s, got)
		}
	}
}

func TestExtractPythonExecArtifactsPassthrough(t *testing.T) {
	// 非 JSON 对象、无 artifacts 字段时原样返回，保持向后兼容。
	for _, stdout := range []string{"plain text output", `{"summary":"ok"}`, `[1,2,3]`, ``} {
		clean, artifacts := extractPythonExecArtifacts(stdout)
		if artifacts != nil {
			t.Fatalf("expected no artifacts for %q, got %+v", stdout, artifacts)
		}
		if clean != stdout {
			t.Fatalf("expected passthrough for %q, got %q", stdout, clean)
		}
	}
}

func TestDecodePythonExecArtifactContent(t *testing.T) {
	raw := []byte("hello-image-bytes")
	b64 := base64.StdEncoding.EncodeToString(raw)

	// 纯 base64
	if got, err := decodePythonExecArtifactContent(b64); err != nil || string(got) != string(raw) {
		t.Fatalf("base64 decode: got=%q err=%v", got, err)
	}
	// data URI
	if got, err := decodePythonExecArtifactContent("data:image/png;base64," + b64); err != nil || string(got) != string(raw) {
		t.Fatalf("data uri decode: got=%q err=%v", got, err)
	}
	// 原始文本（非 base64）回退为原始字节
	csv := "col1,col2\n1,2\n"
	if got, err := decodePythonExecArtifactContent(csv); err != nil || string(got) != csv {
		t.Fatalf("raw text fallback: got=%q err=%v", got, err)
	}
	// 空内容报错
	if _, err := decodePythonExecArtifactContent("   "); err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestPythonExecArtifactContentType(t *testing.T) {
	if got := pythonExecArtifactContentType("image/png", "a.png"); got != "image/png" {
		t.Fatalf("full mime should pass through, got %q", got)
	}
	if got := pythonExecArtifactContentType("png", "a.csv"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("expected csv type by ext, got %q", got)
	}
	if got := pythonExecArtifactContentType("", "noext"); got != "application/octet-stream" {
		t.Fatalf("expected octet-stream fallback, got %q", got)
	}
}

func TestBuildPythonExecArtifactCosKey(t *testing.T) {
	key := buildPythonExecArtifactCosKey("sess/1", "run 2", "artifact123", "../evil name.png")
	if strings.Contains(key, "..") || strings.Contains(key, " ") {
		t.Fatalf("cos key must be sanitized: %s", key)
	}
	if !strings.Contains(key, pythonExecArtifactCOSSubdir) {
		t.Fatalf("cos key should contain subdir: %s", key)
	}
	if !strings.Contains(key, "artifact123") {
		t.Fatalf("cos key should embed artifactID: %s", key)
	}
	if !strings.HasSuffix(key, "evil_name.png") {
		t.Fatalf("cos key should end with sanitized name: %s", key)
	}
}

func TestProcessPythonExecArtifactsUploadSuccess(t *testing.T) {
	orig := pythonExecArtifactUploader
	t.Cleanup(func() { pythonExecArtifactUploader = orig })

	var gotBytes []byte
	pythonExecArtifactUploader = func(u pythonExecArtifactUpload) (string, error) {
		gotBytes = u.Data
		return "art123", nil
	}

	rc := RunContext{SessionID: "sess", RunID: "run"}
	raw := []byte("chart-bytes")
	b64 := base64.StdEncoding.EncodeToString(raw)
	descriptors, meta := processPythonExecArtifacts(rc, "call_1", []pythonExecArtifact{
		{Type: "image/png", Name: "chart.png", Content: b64},
	})
	if string(gotBytes) != string(raw) {
		t.Fatalf("uploader should receive decoded bytes, got %q", gotBytes)
	}
	if len(descriptors) != 1 || descriptors[0].Dropped || descriptors[0].Bytes != len(raw) {
		t.Fatalf("unexpected descriptor: %+v", descriptors[0])
	}
	// 描述符携带 artifactId 供模型传给 displayFiles；不得携带 base64 或 URL（含下载路径前缀）。
	if descriptors[0].ArtifactID != "art123" {
		t.Fatalf("descriptor should carry artifactId, got %+v", descriptors[0])
	}
	descData, _ := json.Marshal(descriptors)
	if strings.Contains(string(descData), b64) || strings.Contains(string(descData), "http") || strings.Contains(string(descData), ArtifactURLPrefix) {
		t.Fatalf("descriptor must not carry base64 or url: %s", string(descData))
	}
	// meta 携带后端拼出的 uri 供前端渲染（meta 条目类型无 content 字段，结构上保证不含 base64）。
	var decoded ArtifactMeta
	if err := json.Unmarshal(meta, &decoded); err != nil {
		t.Fatalf("meta unmarshal: %v", err)
	}
	if len(decoded.Artifacts) != 1 || decoded.Artifacts[0].URI != ArtifactURLPrefix+"art123" {
		t.Fatalf("meta should carry backend-built uri: %+v", decoded.Artifacts)
	}
	if strings.Contains(string(meta), b64) {
		t.Fatalf("meta must not contain base64")
	}
}

func TestProcessPythonExecArtifactsOversizeDropped(t *testing.T) {
	orig := pythonExecArtifactUploader
	t.Cleanup(func() { pythonExecArtifactUploader = orig })
	uploaded := false
	pythonExecArtifactUploader = func(u pythonExecArtifactUpload) (string, error) {
		uploaded = true
		return "art-x", nil
	}

	rc := RunContext{SessionID: "sess", RunID: "run"}
	big := base64.StdEncoding.EncodeToString(make([]byte, pythonExecArtifactMaxFileBytes+1))
	descriptors, meta := processPythonExecArtifacts(rc, "call_1", []pythonExecArtifact{
		{Type: "application/pdf", Name: "big.pdf", Content: big},
	})
	if uploaded {
		t.Fatal("oversized artifact must not be uploaded")
	}
	if !descriptors[0].Dropped || descriptors[0].DroppedReason == "" {
		t.Fatalf("expected dropped with reason, got %+v", descriptors[0])
	}
	if meta != nil {
		t.Fatalf("expected nil meta when all dropped, got %s", string(meta))
	}
}

func TestProcessPythonExecArtifactsUploadFailureDropped(t *testing.T) {
	orig := pythonExecArtifactUploader
	t.Cleanup(func() { pythonExecArtifactUploader = orig })
	pythonExecArtifactUploader = func(u pythonExecArtifactUpload) (string, error) {
		return "", errStubUpload
	}

	rc := RunContext{SessionID: "sess", RunID: "run"}
	b64 := base64.StdEncoding.EncodeToString([]byte("ok"))
	descriptors, meta := processPythonExecArtifacts(rc, "call_1", []pythonExecArtifact{
		{Type: "image/png", Name: "a.png", Content: b64},
	})
	if !descriptors[0].Dropped || !strings.Contains(descriptors[0].DroppedReason, "保存失败") {
		t.Fatalf("expected dropped-on-upload-failure, got %+v", descriptors[0])
	}
	if meta != nil {
		t.Fatalf("expected nil meta on upload failure, got %s", string(meta))
	}
}

func TestProcessPythonExecArtifactsUriOnlyDropped(t *testing.T) {
	// 沙箱无法生成 uri，pythonExecArtifact 刻意不设 uri 字段：脚本 JSON 里带 uri 会在解析时被忽略，
	// 只给 uri、无 content 的产物视为无效，丢弃且不透传外部地址。
	orig := pythonExecArtifactUploader
	t.Cleanup(func() { pythonExecArtifactUploader = orig })
	uploaded := false
	pythonExecArtifactUploader = func(u pythonExecArtifactUpload) (string, error) {
		uploaded = true
		return "art-x", nil
	}

	stdout := `{"artifacts":[{"type":"text/csv","name":"b.csv","uri":"https://existing.example.com/b.csv"}]}`
	_, artifacts := extractPythonExecArtifacts(stdout)
	if len(artifacts) != 1 || artifacts[0].Content != "" {
		t.Fatalf("script-supplied uri should be ignored on parse, got %+v", artifacts)
	}

	rc := RunContext{SessionID: "sess", RunID: "run"}
	descriptors, meta := processPythonExecArtifacts(rc, "call_1", artifacts)
	if uploaded {
		t.Fatal("uri-only artifact must not trigger upload")
	}
	if !descriptors[0].Dropped {
		t.Fatalf("uri-only artifact should be dropped, got %+v", descriptors[0])
	}
	if meta != nil {
		t.Fatalf("expected nil meta for uri-only artifact, got %s", string(meta))
	}
}

func TestProcessPythonExecArtifactsEmpty(t *testing.T) {
	if descriptors, meta := processPythonExecArtifacts(RunContext{}, "call_1", nil); descriptors != nil || meta != nil {
		t.Fatalf("expected nil for no artifacts, got %+v / %s", descriptors, string(meta))
	}
}
