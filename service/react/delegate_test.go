package react

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	agentService "react-base-service/service/agent"
)

func TestDelegateAgentPath(t *testing.T) {
	if got := delegateAgentPath("", "ops-agent"); got != "main/ops-agent" {
		t.Fatalf("外层 run 委派应以 main 为根: %q", got)
	}
	if got := delegateAgentPath("main/ops-agent", "dba-agent"); got != "main/ops-agent/dba-agent" {
		t.Fatalf("嵌套委派应继续拼接: %q", got)
	}
}

func TestDelegateAgentToolDefinitionRendersAgentList(t *testing.T) {
	agents := []model.Agent{
		{AgentKey: "dba-agent", Name: "DBA 专家", Description: "适用：慢查询与执行计划；不适用：非数据库问题", ToolsJSON: `["query_schema","explain_sql"]`},
		{AgentKey: "ops-agent", Name: "OPS 专家", Description: "适用：日志与指标排查"},
	}
	def := delegateAgentToolDefinition(agents)
	if def.Name != metaToolDelegateAgent {
		t.Fatalf("工具名不符合预期: %q", def.Name)
	}
	for _, required := range []string{"dba-agent", "ops-agent", "慢查询与执行计划", "query_schema", "自包含"} {
		if !strings.Contains(def.Description, required) {
			t.Fatalf("描述应包含 %q: %s", required, def.Description)
		}
	}
	parameters, ok := def.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("parameters.properties 解析失败: %v", def.Parameters)
	}
	agentKeySchema, ok := parameters["agent_key"].(map[string]interface{})
	if !ok {
		t.Fatalf("agent_key schema 缺失")
	}
	enumValues, _ := agentKeySchema["enum"].([]string)
	if len(enumValues) != 2 || enumValues[0] != "dba-agent" || enumValues[1] != "ops-agent" {
		t.Fatalf("agent_key enum 应为可见 agent 清单: %v", enumValues)
	}
}

func TestFilterToolIndexSnapshot(t *testing.T) {
	snapshot := `[{"toolId":"tool_a","name":"query_log","description":"查日志"},{"toolId":"tool_b","name":"query_schema","description":"查表结构"},{"toolId":"tool_c","name":"explain_sql","description":"执行计划"}]`

	// 空白名单 = 继承全部可见工具
	if got := (agentService.RuntimePolicy{}).FilterToolIndexSnapshot(snapshot); got != snapshot {
		t.Fatalf("空白名单应继承全部工具: %q", got)
	}
	// 按 name 过滤
	got := (agentService.RuntimePolicy{ToolRefs: []string{"query_schema", "explain_sql"}}).FilterToolIndexSnapshot(snapshot)
	var items []reactToolIndexItem
	if err := json.Unmarshal([]byte(got), &items); err != nil {
		t.Fatalf("过滤结果非法 JSON: %v", err)
	}
	if len(items) != 2 || items[0].Name != "query_schema" || items[1].Name != "explain_sql" {
		t.Fatalf("过滤结果不符合预期: %s", got)
	}
	// 按 toolId 过滤同样生效
	got = (agentService.RuntimePolicy{ToolRefs: []string{"tool_c"}}).FilterToolIndexSnapshot(snapshot)
	if !strings.Contains(got, "explain_sql") {
		t.Fatalf("按 toolId 过滤未生效: %s", got)
	}
	// 未知名自然丢弃
	got = (agentService.RuntimePolicy{ToolRefs: []string{"not_exist"}}).FilterToolIndexSnapshot(snapshot)
	if got != "[]" {
		t.Fatalf("未知名应全部丢弃: %s", got)
	}
}

func TestFilterSkillIndexSnapshot(t *testing.T) {
	snapshot := `[{"skillId":"skill_1","name":"db-diagnosis","description":"诊断"},{"skillId":"skill_2","name":"log-analysis","description":"日志"}]`

	// 空白名单 = 不注入任何 skill
	if got := (agentService.RuntimePolicy{}).FilterSkillIndexSnapshot(snapshot); got != "[]" {
		t.Fatalf("空白名单应不注入 skill: %q", got)
	}
	got := (agentService.RuntimePolicy{SkillRefs: []string{"log-analysis"}}).FilterSkillIndexSnapshot(snapshot)
	if !strings.Contains(got, "log-analysis") || strings.Contains(got, "db-diagnosis") {
		t.Fatalf("按名过滤结果不符合预期: %s", got)
	}
}

func TestDelegationAllowedRespectsConfigAndDepth(t *testing.T) {
	original := conf.CustomConf.LLM.React.SubAgent
	defer func() { conf.CustomConf.LLM.React.SubAgent = original }()

	enabled := true
	conf.CustomConf.LLM.React.SubAgent = conf.ReactSubAgentConfig{Enabled: &enabled, MaxDepth: 2, MaxParallel: 1, DefaultMaxSteps: 8}

	req := &runtimeRequest{agents: []model.Agent{{AgentKey: "dba-agent"}}}
	if !req.delegationAllowed() {
		t.Fatal("外层 run（depth=0）且 max_depth=2 应允许委派")
	}
	req.depth = 1
	if !req.delegationAllowed() {
		t.Fatal("depth=1 且 max_depth=2 应允许再委派一层")
	}
	req.depth = 2
	if req.delegationAllowed() {
		t.Fatal("depth=2 达到 max_depth 上限应停止装配 delegate_agent")
	}
	req.depth = 0
	req.agents = nil
	if req.delegationAllowed() {
		t.Fatal("无可见 agent 时不应装配 delegate_agent")
	}

	disabled := false
	conf.CustomConf.LLM.React.SubAgent = conf.ReactSubAgentConfig{Enabled: &disabled}
	req.agents = []model.Agent{{AgentKey: "dba-agent"}}
	if req.delegationAllowed() {
		t.Fatal("subagent.enabled=false 时不应装配 delegate_agent")
	}
}

func TestRuntimeToolDefinitionsAppendDelegate(t *testing.T) {
	original := conf.CustomConf.LLM.React.SubAgent
	defer func() { conf.CustomConf.LLM.React.SubAgent = original }()
	enabled := true
	conf.CustomConf.LLM.React.SubAgent = conf.ReactSubAgentConfig{Enabled: &enabled, MaxDepth: 2}

	req := &runtimeRequest{
		payload: params.ReactRunPayload{Type: model.ReactSessionTypeChat},
		agents:  []model.Agent{{AgentKey: "dba-agent"}},
	}
	profile := outerExecutionProfile()
	defs := runtimeToolDefinitions(req, profile)
	found := false
	for _, def := range defs {
		if def.Name == metaToolDelegateAgent {
			found = true
		}
	}
	if !found {
		t.Fatal("subagent 开启且有可见 agent 时应装配 delegate_agent")
	}

	// reflection 执行档案不放行 delegate_agent
	reflectionDefs := runtimeToolDefinitions(req, reflectionExecutionProfile())
	for _, def := range reflectionDefs {
		if def.Name == metaToolDelegateAgent {
			t.Fatal("reflection 执行档案不应装配 delegate_agent")
		}
	}
}

// TestHistoryBuilderNestedRunTerminal 验证回放：父 run 消息与子 run 消息按时间线混排，
// 父 run 终态事件不因进入子 run 提前收敛；子 run 终态在切回父 run 时发出。
func TestHistoryBuilderNestedRunTerminal(t *testing.T) {
	now := time.Now()
	runs := []model.ReactRun{
		{RunID: "run_parent", SessionID: "session_x", State: model.ReactRunStateFinished, CreatedAt: now, UpdatedAt: now.Add(3 * time.Second)},
		{RunID: "run_sub", SessionID: "session_x", State: model.ReactRunStateFinished, ParentRunID: "run_parent", AgentPath: "main/ops-agent", CreatedAt: now.Add(1 * time.Second), UpdatedAt: now.Add(2 * time.Second)},
	}
	parentInput := model.ReactMessage{MessageID: "m1", RunID: "run_parent", SessionID: "session_x", Seq: 1, Role: "user", MessageType: model.ReactMessageTypeUserInput, ContentJSON: `{"content":"排查任务 173821"}`, CreatedAt: now}
	parentAssistant := model.ReactMessage{MessageID: "m2", RunID: "run_parent", SessionID: "session_x", Seq: 2, StepIndex: 0, Role: "assistant", MessageType: model.ReactMessageTypeAssistant, ContentJSON: `{"modelMessage":{"role":"assistant","content":"开始排查","parts":[{"type":"tool_use","id":"tu_1","name":"delegate_agent","input":"{}"}]}}`, CreatedAt: now.Add(500 * time.Millisecond)}
	parentToolResult := model.ReactMessage{MessageID: "m3", RunID: "run_parent", SessionID: "session_x", Seq: 3, StepIndex: 0, Role: "user", MessageType: model.ReactMessageTypeToolResult, ContentJSON: `{"modelMessage":{"role":"user","parts":[{"type":"tool_result","toolUseId":"tu_1","content":"{\"finalResponse\":\"done\"}"}]}}`, CreatedAt: now.Add(2500 * time.Millisecond)}
	subInput := model.ReactMessage{MessageID: "m4", RunID: "run_sub", SessionID: "session_x", Seq: 1, Role: "user", MessageType: model.ReactMessageTypeUserInput, ContentJSON: `{"content":"查询任务执行记录"}`, CreatedAt: now.Add(time.Second)}
	subAssistant := model.ReactMessage{MessageID: "m5", RunID: "run_sub", SessionID: "session_x", Seq: 2, StepIndex: 0, Role: "assistant", MessageType: model.ReactMessageTypeAssistant, ContentJSON: `{"modelMessage":{"role":"assistant","content":"任务失败原因是分区字段非法"}}`, CreatedAt: now.Add(1500 * time.Millisecond)}
	messages := []model.ReactMessage{parentInput, parentAssistant, subInput, subAssistant, parentToolResult}

	builder := newHistoryEventBuilder("session_x", runs, messages)
	events := builder.Build()

	// 收集 (type, runId, agentPath) 序列做断言
	type eventKey struct{ eventType, runID, agentPath string }
	sequence := make([]eventKey, 0, len(events))
	for _, event := range events {
		sequence = append(sequence, eventKey{event.Type, event.RunID, event.AgentPath})
	}

	// 子 run 事件必须带 agentPath，父 run 事件不带（缺省 main）
	subEvents := 0
	parentDoneIndex, subDoneIndex := -1, -1
	for i, key := range sequence {
		if key.runID == "run_sub" {
			subEvents++
			if key.agentPath != "main/ops-agent" {
				t.Fatalf("子 run 事件应带 agentPath=main/ops-agent: %+v", key)
			}
			if key.eventType == EventDone {
				subDoneIndex = i
			}
		} else if key.runID == "run_parent" && key.eventType == EventDone {
			parentDoneIndex = i
		}
	}
	if subEvents == 0 {
		t.Fatal("回放未包含子 run 事件")
	}
	if subDoneIndex < 0 || parentDoneIndex < 0 {
		t.Fatalf("父子 run 都应有终态事件: subDone=%d, parentDone=%d", subDoneIndex, parentDoneIndex)
	}
	if parentDoneIndex < subDoneIndex {
		t.Fatalf("父 run done 必须在子 run done 之后（父 run 会继续产生消息）: parent=%d, sub=%d", parentDoneIndex, subDoneIndex)
	}
}
