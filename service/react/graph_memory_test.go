package react

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
	graphmemory "react-base-service/service/graphmemory"
)

func graphMemoryTestFacts() []graphmemory.Fact {
	return []graphmemory.Fact{
		{
			Name:      "DEPLOYED_ON",
			Fact:      "service-A 部署在 cluster-02",
			ValidAt:   "2026-09-01T00:00:00Z",
			InvalidAt: "",
		},
		{
			Name:      "DEPLOYED_ON",
			Fact:      "service-A 曾部署在 cluster-01",
			ValidAt:   "2026-08-01T00:00:00Z",
			InvalidAt: "2026-09-01T00:00:00Z",
		},
		{
			Name: "READS_FROM",
			Fact: "job_123 读取 ods_order",
		},
	}
}

func TestRenderGraphMemoryContextEmpty(t *testing.T) {
	if rendered := renderGraphMemoryContext(nil, 8, 1200); rendered != "" {
		t.Fatalf("no facts should render empty, got %q", rendered)
	}
	if rendered := renderGraphMemoryContext(graphMemoryTestFacts(), 0, 1200); rendered != "" {
		t.Fatalf("maxFacts<=0 should render empty, got %q", rendered)
	}
}

func TestRenderGraphMemoryContextRendersWindow(t *testing.T) {
	rendered := renderGraphMemoryContext(graphMemoryTestFacts(), 8, 1200)
	if !strings.HasPrefix(rendered, "<graph_memory>") || !strings.HasSuffix(rendered, "</graph_memory>") {
		t.Fatalf("block delimiters missing: %q", rendered)
	}
	if !strings.Contains(rendered, "service-A 部署在 cluster-02（2026-09-01 ~ 未知时间）") {
		t.Fatalf("open-ended window should render 未知时间: %q", rendered)
	}
	if !strings.Contains(rendered, "service-A 曾部署在 cluster-01（2026-08-01 ~ 2026-09-01）") {
		t.Fatalf("closed window mismatch: %q", rendered)
	}
	if !strings.Contains(rendered, "job_123 读取 ods_order（未知时间 ~ 未知时间）") {
		t.Fatalf("missing valid_at should render 未知时间: %q", rendered)
	}
	if !strings.Contains(rendered, "以 <memory> 为准") {
		t.Fatalf("layer priority note missing: %q", rendered)
	}
}

func TestRenderGraphMemoryContextBudgetAndFactLimit(t *testing.T) {
	if rendered := renderGraphMemoryContext(graphMemoryTestFacts(), 1, 1200); strings.Contains(rendered, "cluster-01") {
		t.Fatalf("maxFacts=1 should drop the rest: %q", rendered)
	}
	rendered := renderGraphMemoryContext(graphMemoryTestFacts(), 8, 60)
	if !strings.Contains(rendered, "另有") || !strings.Contains(rendered, "graph_memory_search") {
		t.Fatalf("char budget should emit overflow hint: %q", rendered)
	}
}

func TestGraphMemoryToolDefinitionsFollowSwitch(t *testing.T) {
	original := conf.CustomConf.LLM.React.GraphMemory
	defer func() { conf.CustomConf.LLM.React.GraphMemory = original }()

	off := false
	on := true
	conf.CustomConf.LLM.React.GraphMemory = conf.ReactGraphMemoryConfig{Enabled: &on, Write: conf.ReactGraphMemoryWriteConfig{Enabled: &off}}
	names := make(map[string]bool)
	for _, def := range graphMemoryToolDefinitions() {
		names[def.Name] = true
	}
	if !names[metaToolGraphMemorySearch] {
		t.Fatalf("search tool missing when enabled")
	}
	if names[metaToolGraphMemoryWrite] {
		t.Fatalf("write tool must be absent when write.enabled=false")
	}

	conf.CustomConf.LLM.React.GraphMemory = conf.ReactGraphMemoryConfig{Enabled: &on, Write: conf.ReactGraphMemoryWriteConfig{Enabled: &on}}
	names = make(map[string]bool)
	for _, def := range graphMemoryToolDefinitions() {
		names[def.Name] = true
	}
	if !names[metaToolGraphMemorySearch] || !names[metaToolGraphMemoryWrite] {
		t.Fatalf("both tools expected when write.enabled=true: %v", names)
	}

	// 总注册表：总开关关闭时两个工具都不可见。
	conf.CustomConf.LLM.React.GraphMemory = conf.ReactGraphMemoryConfig{Enabled: &off, Write: conf.ReactGraphMemoryWriteConfig{Enabled: &on}}
	for _, def := range internalMetaToolDefinitions() {
		if def.Name == metaToolGraphMemorySearch || def.Name == metaToolGraphMemoryWrite {
			t.Fatalf("graph memory tools must be absent when disabled: %s", def.Name)
		}
	}
}

func TestGraphMemoryReflectionProfileDeniesTools(t *testing.T) {
	profile := reflectionExecutionProfile()
	if profile.AllowGraphMemory {
		t.Fatalf("reflection profile must deny graph memory tools")
	}
	for _, tool := range []string{metaToolGraphMemorySearch, metaToolGraphMemoryWrite} {
		if profile.allowsInternalTool(tool) {
			t.Fatalf("reflection profile should deny %s", tool)
		}
	}
}

// TestGraphMemorySearchForcesServerSideGroups 验证工具路径的组隔离：
// group_ids 由服务端按 run 作用域注入（caller 组在前、user 组在后），模型入参不含组概念。
func TestGraphMemorySearchForcesServerSideGroups(t *testing.T) {
	original := conf.CustomConf.LLM.React.GraphMemory
	defer func() { conf.CustomConf.LLM.React.GraphMemory = original }()

	var gotBody struct {
		GroupIDs []string `json:"group_ids"`
		Query    string   `json:"query"`
		MaxFacts int      `json:"max_facts"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"facts":[]}`))
	}))
	defer server.Close()

	on := true
	conf.CustomConf.LLM.React.GraphMemory = conf.ReactGraphMemoryConfig{Enabled: &on, Endpoint: server.URL, TimeoutMs: 3000}

	state := &reactEngineState{
		ctx:    nil,
		runCtx: context.Background(),
		req: &runtimeRequest{
			payload: params.ReactRunPayload{CallerKey: "demo-app"},
			runtimeRequestIdentity: runtimeRequestIdentity{userName: "alice"},
		},
	}
	content, isError, err := state.executeGraphMemorySearch(json.RawMessage(`{"query":"service-A 和 cluster-01 什么关系","limit":5}`))
	if err != nil || isError {
		t.Fatalf("search failed: isErr=%v err=%v", isError, err)
	}
	wantScope := graphmemory.ResolveScope("demo-app", "alice", true)
	if len(gotBody.GroupIDs) != 2 || gotBody.GroupIDs[0] != wantScope.Groups[0] || gotBody.GroupIDs[1] != wantScope.Groups[1] {
		t.Fatalf("group_ids must be server-side resolved: got %v want %v", gotBody.GroupIDs, wantScope.Groups)
	}
	for _, group := range gotBody.GroupIDs {
		if strings.Contains(group, "-") {
			t.Fatalf("group id must not contain hyphen (FalkorDB fulltext incompatible): %q", group)
		}
	}
	if gotBody.Query != "service-A 和 cluster-01 什么关系" || gotBody.MaxFacts != 5 {
		t.Fatalf("query/limit mismatch: %+v", gotBody)
	}
	var payload struct {
		Facts []graphMemoryFactView `json:"facts"`
		Total int                   `json:"total"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if payload.Total != 0 {
		t.Fatalf("expected empty facts, got %d", payload.Total)
	}
}

func TestGraphMemorySearchRejectsEmptyQuery(t *testing.T) {
	state := &reactEngineState{
		runCtx: context.Background(),
		req:    &runtimeRequest{payload: params.ReactRunPayload{CallerKey: "demo-app"}, runtimeRequestIdentity: runtimeRequestIdentity{userName: "alice"}},
	}
	if _, _, err := state.executeGraphMemorySearch(json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Fatalf("empty query should be rejected")
	}
}
