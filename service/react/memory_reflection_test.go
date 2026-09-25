package react

import (
	"strings"
	"testing"
	"time"

	"react-base-service/conf"
	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"
)

func TestNormalizeSessionTypeAcceptsReflection(t *testing.T) {
	if normalizeSessionType("reflection") != model.ReactSessionTypeReflection {
		t.Fatalf("reflection should be preserved")
	}
	if normalizeSessionType("chat") != model.ReactSessionTypeChat {
		t.Fatalf("chat should be preserved")
	}
	if normalizeSessionType("weird") != model.ReactSessionTypeChat {
		t.Fatalf("unknown type should collapse to chat")
	}
}

func TestReflectionExecutionProfileOnlyAllowsMemory(t *testing.T) {
	profile := reflectionExecutionProfile()
	if !profile.AllowMemory {
		t.Fatalf("reflection profile must allow memory tools")
	}
	if profile.AllowTodo || profile.AllowPlan || profile.AllowDynamicTools || profile.AllowSkills || profile.AllowUserQuestion || profile.AllowAsyncTaskTools {
		t.Fatalf("reflection profile must disable everything except memory: %+v", profile)
	}
	for _, tool := range []string{metaToolMemoryList, metaToolMemoryRead, metaToolMemoryWrite} {
		if !profile.allowsInternalTool(tool) {
			t.Fatalf("reflection profile should allow %s", tool)
		}
	}
	for _, tool := range []string{metaToolGetTool, metaToolExecuteTool, metaToolTodoWrite, metaToolPythonExec, metaToolCreatePlan, metaToolGetSkill} {
		if profile.allowsInternalTool(tool) {
			t.Fatalf("reflection profile should deny %s", tool)
		}
	}
}

func TestInternalMetaToolDefinitionsForReflection(t *testing.T) {
	original := conf.CustomConf.LLM.React.Memory
	defer func() { conf.CustomConf.LLM.React.Memory = original }()

	on := true
	conf.CustomConf.LLM.React.Memory = conf.ReactMemoryConfig{Enabled: &on}
	defs := internalMetaToolDefinitionsForType(model.ReactSessionTypeReflection)
	if len(defs) != 3 {
		t.Fatalf("reflection should expose exactly 3 memory tools, got %d", len(defs))
	}
	for _, def := range defs {
		switch def.Name {
		case metaToolMemoryList, metaToolMemoryRead, metaToolMemoryWrite:
		default:
			t.Fatalf("unexpected tool in reflection set: %s", def.Name)
		}
	}

	off := false
	conf.CustomConf.LLM.React.Memory = conf.ReactMemoryConfig{Enabled: &off}
	if defs := internalMetaToolDefinitionsForType(model.ReactSessionTypeReflection); len(defs) != 0 {
		t.Fatalf("reflection with memory disabled should expose no tools, got %d", len(defs))
	}
	if defs := internalMetaToolDefinitionsForType(model.ReactSessionTypeChat); len(defs) == 0 {
		t.Fatalf("chat session should still expose full meta tools")
	}
}

func TestRenderMemoryReflectionTranscriptTailAndCap(t *testing.T) {
	// 用"甲乙丙"避免与省略标注文字（含"早"字）冲突。
	messages := []llm.ChatMessage{
		{Role: "user", Content: strings.Repeat("甲", 50)},
		{Role: "assistant", Content: strings.Repeat("乙", 50)},
		{Role: "user", Content: strings.Repeat("丙", 50)},
	}
	// 预算只够最近两条：最旧一条被省略并标注。
	got := renderMemoryReflectionTranscript(messages, 120)
	if !strings.Contains(got, "更早的 1 条消息已省略") {
		t.Fatalf("omission note missing:\n%s", got)
	}
	if !strings.Contains(got, "丙") || !strings.Contains(got, "乙") {
		t.Fatalf("recent messages must survive:\n%s", got)
	}
	if strings.Contains(got, "甲") {
		t.Fatalf("oldest message should be dropped:\n%s", got)
	}
	if renderMemoryReflectionTranscript(nil, 100) != "(无)" {
		t.Fatalf("empty transcript expected placeholder")
	}
}

func TestRenderMemoryReflectionTranscriptTrimsSingleMessage(t *testing.T) {
	long := strings.Repeat("字", 2000)
	got := renderMemoryReflectionTranscript([]llm.ChatMessage{{Role: "user", Content: long}}, 100000)
	if !strings.Contains(got, "…(截断)") {
		t.Fatalf("single overlong message should be trimmed:\n%.100s", got)
	}
}

func TestBuildMemoryReflectionPromptContainsPhasesAndRules(t *testing.T) {
	cfg := conf.GetReactRuntimeConfig().Memory
	got := buildMemoryReflectionPrompt("摘要内容", []llm.ChatMessage{{Role: "user", Content: "原文内容"}}, cfg)
	for _, expected := range []string{
		"Investigate", "Extract", "Update", "Review", "Commit",
		"摘要内容", "原文内容",
		"locked",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("prompt missing %q", expected)
		}
	}
	if !strings.Contains(got, "本次最多执行") {
		t.Fatalf("write limit should appear in prompt")
	}
}

func TestAcquireMemoryReflectionCooldownWindow(t *testing.T) {
	// 独立冷却表操作走包级单例，测试用极短窗口验证语义。
	key := "test-session-cooldown"
	deleteReflectionCooldownForTest(key)
	if !memoryReflectionCooldown.acquire(key, 50*time.Millisecond) {
		t.Fatalf("first acquire should pass")
	}
	if memoryReflectionCooldown.acquire(key, 50*time.Millisecond) {
		t.Fatalf("second acquire within cooldown should fail")
	}
	time.Sleep(60 * time.Millisecond)
	if !memoryReflectionCooldown.acquire(key, 50*time.Millisecond) {
		t.Fatalf("acquire after cooldown should pass again")
	}
	deleteReflectionCooldownForTest(key)
}

func deleteReflectionCooldownForTest(sessionID string) {
	memoryReflectionCooldown.mu.Lock()
	defer memoryReflectionCooldown.mu.Unlock()
	delete(memoryReflectionCooldown.last, sessionID)
}


func TestPlanScopedRunDisablesCreatePlan(t *testing.T) {
	original := conf.CustomConf.LLM.React.AllowPlan
	defer func() { conf.CustomConf.LLM.React.AllowPlan = original }()

	on := true
	conf.CustomConf.LLM.React.AllowPlan = &on
	profile := executionProfileForRun(&runtimeRequest{
		runtimeRequestExecution: runtimeRequestExecution{agentPath: "plan/inspect_upstream"},
	})
	if profile.AllowPlan {
		t.Fatalf("plan scoped run must not expose create_plan")
	}
	if profile.allowsInternalTool(metaToolCreatePlan) {
		t.Fatalf("plan scoped run must deny create_plan at execution boundary")
	}
}
