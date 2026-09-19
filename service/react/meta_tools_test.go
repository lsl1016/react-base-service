package react

import (
	"encoding/json"
	"strings"
	"testing"

	llm "react-base-service/api/llm"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"
)

func TestTodoWriteToolDefinitionRequiresStepHeadingBeforeExecution(t *testing.T) {
	description := todoWriteToolDefinition().Description
	for _, required := range []string{
		"最多只能有一个 in_progress todo",
		"assistant 正文中使用 Markdown 一级标题格式 `# <中文序号>、<完整 content>`",
		"中文序号按照当前完整 todo 列表顺序生成",
		"依次使用“一、二、三……十、十一……”",
		"例如 `# 一、检查现有实现`",
		"中文序号只用于标题展示，不得写入或修改 todo 的 content",
		"标题前后不得添加其他正文",
		"同一响应中调用本工具将该 todo 更新为 in_progress",
		"该响应不得同时调用其他实施工具",
		"下一轮再执行该步骤",
		"只在首次进入 in_progress 时输出一次步骤标题",
	} {
		if !strings.Contains(description, required) {
			t.Fatalf("todo_write description should contain %q, got: %s", required, description)
		}
	}
}

func TestBusinessToolDefinitionUsesASCIIName(t *testing.T) {
	tool := model.Tool{
		ToolID:      "tool_81a193580dcf4538b4414b9a120079ac",
		Name:        "按字段反查表列表",
		Description: "用户输入字段英文名或字段注释，查询有哪些表包含该字段",
	}

	def := businessToolDefinition(tool)
	if def.Name != tool.ToolID {
		t.Fatalf("expected function name %q, got %q", tool.ToolID, def.Name)
	}
	if !isValidToolFunctionName(def.Name) {
		t.Fatalf("function name should match provider regex: %q", def.Name)
	}
}

func TestBusinessToolDescriptionKeepsDisplayName(t *testing.T) {
	tool := model.Tool{
		ToolID:      "tool_demo",
		Name:        "按字段反查表列表",
		Description: "用户输入字段英文名或字段注释，查询有哪些表包含该字段",
	}

	description := businessToolDescription(tool)
	if description == tool.Description {
		t.Fatalf("description should include display name")
	}
	if description != tool.Description+"\n\n工具展示名："+tool.Name {
		t.Fatalf("unexpected description: %q", description)
	}
}

func TestToolDefinitionFingerprintStableAcrossPersistRoundTrip(t *testing.T) {
	tool := model.Tool{
		ToolID: "tool_fp_demo",
		Name:   "指纹测试工具",
		Config: `{"inputSchema":{"type":"object","properties":{"limit":{"type":"integer","maximum":100}},"required":["limit"]}}`,
	}
	def := businessToolDefinition(tool)
	fp := toolDefinitionFingerprint(def)
	if fp == "" {
		t.Fatalf("fingerprint should not be empty")
	}

	// 模拟 persistActiveTools 落库后下个 run 恢复：JSON 往返会把数字变成 float64，指纹必须保持一致。
	data, err := json.Marshal([]llm.ToolDefinition{def})
	if err != nil {
		t.Fatal(err)
	}
	var restored []llm.ToolDefinition
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if got := toolDefinitionFingerprint(restored[0]); got != fp {
		t.Fatalf("fingerprint changed across persist round trip: %q vs %q", got, fp)
	}
}

func TestToolDefinitionFingerprintDetectsSchemaChange(t *testing.T) {
	base := model.Tool{
		ToolID: "tool_fp_demo",
		Name:   "指纹测试工具",
		Config: `{"inputSchema":{"type":"object","properties":{"keyword":{"type":"string"}}}}`,
	}
	changed := base
	changed.Config = `{"inputSchema":{"type":"object","properties":{"keyword":{"type":"string"},"page":{"type":"integer"}}}}`

	if toolDefinitionFingerprint(businessToolDefinition(base)) == toolDefinitionFingerprint(businessToolDefinition(changed)) {
		t.Fatalf("fingerprint should change when inputSchema changes")
	}
}

func TestBuildToolDefinitionsKeepsBusinessToolsOutOfProviderTools(t *testing.T) {
	state := &reactEngineState{
		reactEngineConversationState: reactEngineConversationState{activeTools: map[string]model.Tool{
			"tool_demo": {
				ToolID:      "tool_demo",
				Name:        "演示业务工具",
				Description: "演示业务工具描述",
			},
		}},
	}

	defs := state.buildToolDefinitions()
	var hasExecuteTool bool
	for _, def := range defs {
		if def.Name == metaToolExecuteTool {
			hasExecuteTool = true
		}
		if def.Name == metaToolListSkills {
			t.Fatalf("list_skills should not be exposed as provider tool: %#v", defs)
		}
		if def.Name == "tool_demo" {
			t.Fatalf("business tool should not be exposed as provider tool: %#v", defs)
		}
	}
	if !hasExecuteTool {
		t.Fatalf("expected stable execute_tool meta tool in definitions: %#v", defs)
	}
}

func TestFlattenedExecuteToolInputKeepsBusinessArguments(t *testing.T) {
	input := json.RawMessage(`{"toolId":"tool_demo","scope_ids":[55],"page":1}`)
	got := flattenedExecuteToolInput(input)
	if string(got) != `{"page":1,"scope_ids":[55]}` {
		t.Fatalf("unexpected flattened input: %s", string(got))
	}
}

func TestNormalizeExecuteToolInputParsesJSONString(t *testing.T) {
	got, ok := normalizeExecuteToolInput(json.RawMessage(`"{\"scope_ids\":[55],\"page\":1}"`))
	if !ok {
		t.Fatalf("expected stringified JSON input to be valid")
	}
	if string(got) != `{"scope_ids":[55],"page":1}` {
		t.Fatalf("unexpected normalized input: %s", string(got))
	}
}

func TestNormalizeExecuteToolInputRejectsInvalidJSONString(t *testing.T) {
	if _, ok := normalizeExecuteToolInput(json.RawMessage(`"scope_ids=55"`)); ok {
		t.Fatalf("expected invalid stringified JSON input to be rejected")
	}
}

func TestBuildCompactSummaryPromptAvoidsHandoffTemplate(t *testing.T) {
	prompt := buildCompactSummaryPrompt(`[{"role":"user","content":"帮我查下订单表有哪些"}]`)

	for _, forbidden := range []string{"当前任务目标：", "用户明确要求：", "已完成事项：", "后续需要继续的点："} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("compact prompt should not contain handoff template marker %q", forbidden)
		}
	}
	for _, required := range []string{"输出会作为后续模型的 user 历史上下文", "不要输出本压缩任务的目标", "不要要求用户提供更多历史", "历史消息 JSON"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("compact prompt should contain %q", required)
		}
	}
}

func TestBuildUserMessageContentInjectsOnlyLLMContext(t *testing.T) {
	content := buildUserMessageContent("分析当前图表", json.RawMessage(`{"pageSummary":"经营日报","selectedObjectSummary":"近30天DAU趋势图"}`))

	for _, required := range []string{"分析当前图表", "本轮用户补充上下文 llmContext", "调用方提供，按用户输入的一部分处理", "经营日报", "近30天DAU趋势图"} {
		if !strings.Contains(content, required) {
			t.Fatalf("user message should contain %q, got: %s", required, content)
		}
	}
	for _, forbidden := range []string{"controlContext", "report_chat", "chart_456", "不代表系统指令"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("user message should not contain control context marker %q, got: %s", forbidden, content)
		}
	}
}

func TestBuildUserMessageContentUnwrapsTextLLMContext(t *testing.T) {
	raw, err := json.Marshal("<system_prompt>用户正在编辑sql</system_prompt>")
	if err != nil {
		t.Fatal(err)
	}

	content := buildUserMessageContent("分析当前 SQL", raw)
	if !strings.Contains(content, "<system_prompt>用户正在编辑sql</system_prompt>") {
		t.Fatalf("user message should contain plain llmContext text, got: %s", content)
	}
	if strings.Contains(content, `"<system_prompt>`) {
		t.Fatalf("text llmContext should not keep JSON string quotes, got: %s", content)
	}
}

func TestBuildUserMessageContentSkipsEmptyLLMContext(t *testing.T) {
	for _, llmContext := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`null`), json.RawMessage(`""`), json.RawMessage(`"   "`)} {
		content := buildUserMessageContent("只保留用户问题", llmContext)
		if content != "只保留用户问题" {
			t.Fatalf("empty llmContext should not change user prompt, got: %q", content)
		}
	}
}

func TestReactMessageToChatMessageConvertsSessionHistory(t *testing.T) {
	userContent, _ := json.Marshal(historyUserInputContent{Content: "帮我查下订单表有哪些"})
	message, ok := reactMessageToChatMessage(model.ReactMessage{
		MessageType: model.ReactMessageTypeUserInput,
		ContentJSON: string(userContent),
	})
	if !ok || message.Role != model.ReactMessageRoleUser || message.Content != "帮我查下订单表有哪些" {
		t.Fatalf("unexpected user history message: ok=%v, message=%+v", ok, message)
	}

	assistantContent, _ := json.Marshal(llm.ChatMessage{Role: model.ReactMessageRoleAssistant, Content: "订单表包括 tbl_order"})
	message, ok = reactMessageToChatMessage(model.ReactMessage{
		MessageType: model.ReactMessageTypeAssistant,
		ContentJSON: string(assistantContent),
	})
	if !ok || message.Role != model.ReactMessageRoleAssistant || message.Content != "订单表包括 tbl_order" {
		t.Fatalf("unexpected assistant history message: ok=%v, message=%+v", ok, message)
	}

	partialContent, _ := json.Marshal(map[string]any{"modelMessage": llm.ChatMessage{Role: model.ReactMessageRoleAssistant, Parts: []llm.ContentPart{{Type: "thinking", Thinking: "未完成的推理片段"}}}})
	message, ok = reactMessageToChatMessage(model.ReactMessage{
		MessageType: model.ReactMessageTypeAssistantPartial,
		ContentJSON: string(partialContent),
	})
	if ok {
		t.Fatalf("assistant partial should not enter model context, got: %+v", message)
	}

	compactContent, _ := json.Marshal(map[string]string{"summary": "用户已询问订单表，已回答 tbl_order"})
	message, ok = reactMessageToChatMessage(model.ReactMessage{
		MessageType: model.ReactMessageTypeCompactSummary,
		ContentJSON: string(compactContent),
	})
	if !ok || message.Role != model.ReactMessageRoleUser || message.Content != "用户已询问订单表，已回答 tbl_order" {
		t.Fatalf("unexpected compact history message: ok=%v, message=%+v", ok, message)
	}
}

func TestBuildInitialChatMessagesKeepsSystemFirstAndCurrentUserLast(t *testing.T) {
	currentUserRef := reactMessageRef{RunID: "run_new", MessageID: "msg_new", Seq: 1}
	req := &runtimeRequest{
		runtimeRequestCapabilities: runtimeRequestCapabilities{
			systemPrompt: "当前系统提示",
		},
		runtimeRequestConversation: runtimeRequestConversation{
			historyMessages: []llm.ChatMessage{
				{Role: model.ReactMessageRoleUser, Content: "第一轮用户"},
				{Role: model.ReactMessageRoleAssistant, Content: "第一轮回答"},
				{Role: model.ReactMessageRoleUser, Content: "历史摘要"},
			},
			historyMessageRefs: [][]reactMessageRef{
				{{RunID: "run_old", MessageID: "msg_1", Seq: 1}},
				{{RunID: "run_old", MessageID: "msg_2", Seq: 2}},
				{{RunID: "run_old", MessageID: "msg_3", Seq: 3}},
			},
			modelUserMessage:    llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: "第二轮用户"},
			modelUserMessageRef: currentUserRef,
		},
	}

	messages, err := buildInitialChatMessages(nil, req, "run_new", "session_demo")
	if err != nil {
		t.Fatalf("build initial messages failed: %v", err)
	}
	if len(messages) != 5 {
		t.Fatalf("unexpected message count: %d", len(messages))
	}
	if messages[0].Role != model.ReactMessageRoleSystem {
		t.Fatalf("current system message should be first, got: %+v", messages[0])
	}
	if messages[0].Content != "当前系统提示" {
		t.Fatalf("current system message should stay unchanged, got: %+v", messages[0])
	}
	if messages[1].Content != "第一轮用户" || messages[2].Content != "第一轮回答" || messages[3].Content != "历史摘要" || messages[4].Content != "第二轮用户" {
		t.Fatalf("history messages should stay before current user, got: %+v", messages)
	}
	if len(req.historyMessageRefs) != len(messages) {
		t.Fatalf("message refs should align with messages")
	}
	if refs := req.historyMessageRefs[3]; len(refs) != 1 || refs[0].MessageID != "msg_3" {
		t.Fatalf("history summary refs should stay on history summary message, got: %+v", refs)
	}
	if refs := req.historyMessageRefs[4]; len(refs) != 1 || refs[0] != currentUserRef {
		t.Fatalf("current user ref should stay on last message, got: %+v", refs)
	}
}

func TestBuildInitialChatMessagesInjectsSkillSummary(t *testing.T) {
	currentUserRef := reactMessageRef{RunID: "run_new", MessageID: "msg_new", Seq: 1}
	skillsIndexSnapshotJSON := buildSkillIndexSnapshotJSON([]model.Skill{
		{
			SkillID:            "skill_report",
			Name:               "报表分析",
			Description:        "用于分析业务报表",
			TriggerCondition:   "用户需要分析报表时触发",
			ForbiddenCondition: "用户只闲聊时禁用",
			IsDefault:          0,
			RouteValues:        `[]`,
		},
	})
	req := &runtimeRequest{
		runtimeRequestCapabilities: runtimeRequestCapabilities{
			systemPrompt:            "当前系统提示",
			skillsIndexSnapshotJSON: skillsIndexSnapshotJSON,
		},
		runtimeRequestConversation: runtimeRequestConversation{
			modelUserMessage:    llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: "分析一下报表"},
			modelUserMessageRef: currentUserRef,
		},
	}

	messages, err := buildInitialChatMessages(nil, req, "run_new", "session_demo")
	if err != nil {
		t.Fatalf("build initial messages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("unexpected message count: %d", len(messages))
	}
	systemContent := messages[0].Content
	for _, required := range []string{"当前系统提示", "当前 run 可用 Skill 摘要索引", "skill_report", "报表分析", "必须调用 get_skill", "forbiddenCondition"} {
		if !strings.Contains(systemContent, required) {
			t.Fatalf("system content should contain %q, got: %s", required, systemContent)
		}
	}
	if messages[1].Content != "分析一下报表" {
		t.Fatalf("current user should stay last, got: %+v", messages)
	}
}

func TestCompactDisabledSkipsTrigger(t *testing.T) {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	disabled := false
	compactCfg.Enabled = &disabled
	if shouldCompactContext(compactCfg.TokenTrigger*2, 0, compactCfg) {
		t.Fatalf("compact should be skipped when disabled")
	}
}

func TestCompactTokenTriggerWithLastInputTokens(t *testing.T) {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	// lastInputTokens 高于 TokenTrigger → 触发
	if !shouldCompactContext(compactCfg.TokenTrigger+1, 0, compactCfg) {
		t.Fatalf("expected lastInputTokens > TokenTrigger to trigger compact")
	}
	// lastInputTokens 低于 TokenTrigger → 不触发
	if shouldCompactContext(compactCfg.TokenTrigger-1, 0, compactCfg) {
		t.Fatalf("should not trigger when lastInputTokens below TokenTrigger")
	}
}

func TestCompactColdStartUsesEstimatedTokens(t *testing.T) {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	// lastInputTokens=0（冷启动），估算 token 超阈值 → 触发
	if !shouldCompactContext(0, compactCfg.TokenTrigger+1, compactCfg) {
		t.Fatalf("expected estimated tokens to trigger compact at cold start")
	}
	if shouldCompactContext(0, compactCfg.TokenTrigger-1, compactCfg) {
		t.Fatalf("should not trigger when both anchors below TokenTrigger")
	}
}

func TestSelectCompactKeepCountUsesTokenBudget(t *testing.T) {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	compactCfg.SummaryLimit = 0
	messages := makeReactTestMessages(5, "短消息")

	compactCfg.TokenTarget = 1
	if keepCount := selectCompactKeepCount(messages, 5000, compactCfg); keepCount != 1 {
		t.Fatalf("keepCount should keep latest message when budget is exhausted, got %d", keepCount)
	}

	compactCfg.TokenTarget = 1000000
	if keepCount := selectCompactKeepCount(messages, 0, compactCfg); keepCount != len(messages)-1 {
		t.Fatalf("keepCount should keep all compactable tail messages when budget is enough, got %d", keepCount)
	}
}

func TestHistoryExecuteToolNameUsesToolIndexDisplayName(t *testing.T) {
	builder := newHistoryEventBuilder("session_1", []model.ReactRun{{
		RunID:                 "run_1",
		ToolIndexSnapshotJSON: `[{"toolId":"tool_abc","name":"按字段反查表列表","description":"演示工具"}]`,
	}}, nil)
	part := llm.ContentPart{
		Type:  "tool_use",
		ID:    "call_1",
		Name:  metaToolExecuteTool,
		Input: json.RawMessage(`{"description":"字段查询","toolId":"tool_abc","input":{"keyword":"uid"}}`),
	}

	input := builder.buildToolUseStartInput("run_1", part)
	if input.ToolName != "按字段反查表列表" {
		t.Fatalf("expected display name, got %q", input.ToolName)
	}
	if string(input.ToolInput) != `{"keyword":"uid"}` {
		t.Fatalf("unexpected tool input: %s", string(input.ToolInput))
	}
}

func TestInternalToolDefinitionsExposeDataAndPythonTools(t *testing.T) {
	defs := internalMetaToolDefinitions()
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		seen[def.Name] = true
	}
	for _, name := range []string{metaToolInspectData, metaToolPythonExec, metaToolReadToolResult, metaToolTodoWrite, metaToolDisplayFiles} {
		if !seen[name] {
			t.Fatalf("expected internal tool %s to be exposed, got %#v", name, defs)
		}
		if !isInternalMetaTool(name) {
			t.Fatalf("expected %s to be treated as internal meta tool", name)
		}
	}
}

func TestPythonExecToolDefinitionSupportsMultipleInputSourceTypes(t *testing.T) {
	def := pythonExecToolDefinition()
	if strings.Contains(def.Description, "data_ref") || strings.Contains(def.Description, "dataRef") {
		t.Fatalf("python_exec description should not mention data_ref/dataRef: %s", def.Description)
	}
	for _, required := range []string{"inspect_data", "可选", "空输入容器", "tool_result", "raw_json", "text", "expr", "stdin", "alias", "后端会读取"} {
		if !strings.Contains(def.Description, required) {
			t.Fatalf("python_exec description should contain %q, got: %s", required, def.Description)
		}
	}
	params := def.Parameters
	properties, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in parameters: %#v", params)
	}
	if _, ok := properties["dataRef"]; ok {
		t.Fatalf("python_exec should not expose top-level dataRef after inputs protocol: %#v", properties)
	}
	inputs, ok := properties["inputs"].(map[string]interface{})
	if !ok {
		t.Fatalf("python_exec should expose inputs object, got: %#v", properties)
	}
	if desc, _ := inputs["description"].(string); strings.Contains(desc, "data_ref") || strings.Contains(desc, "dataRef") {
		t.Fatalf("inputs description should not mention data_ref/dataRef: %s", desc)
	}
	for _, required := range []string{"可选", "不需要外部输入", "后端会读取", "raw_json", "text", "expr"} {
		desc, _ := inputs["description"].(string)
		if !strings.Contains(desc, required) {
			t.Fatalf("inputs description should contain %q, got: %s", required, desc)
		}
	}
	inputSource, ok := inputs["additionalProperties"].(map[string]interface{})
	if !ok {
		t.Fatalf("inputs should declare source spec additionalProperties, got: %#v", inputs)
	}
	sourceProperties, ok := inputSource["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("input source should expose properties, got: %#v", inputSource)
	}
	typeSchema, ok := sourceProperties["type"].(map[string]interface{})
	if !ok {
		t.Fatalf("input source type should expose schema, got: %#v", sourceProperties)
	}
	enumValues, ok := typeSchema["enum"].([]string)
	if !ok {
		t.Fatalf("input source type should expose enum, got: %#v", typeSchema)
	}
	for _, expected := range []string{"tool_result", "raw_json", "text", "expr"} {
		if !stringSliceContains(enumValues, expected) {
			t.Fatalf("input source type should allow %s, got: %#v", expected, typeSchema)
		}
	}
	if _, ok := sourceProperties["ref"]; !ok {
		t.Fatalf("input source should expose ref, got: %#v", sourceProperties)
	}
	if _, ok := sourceProperties["value"]; !ok {
		t.Fatalf("input source should expose value, got: %#v", sourceProperties)
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("expected required string slice, got: %#v", params["required"])
	}
	if stringSliceContains(required, "inputs") {
		t.Fatalf("python_exec inputs should be optional, got required: %#v", required)
	}
	for _, expected := range []string{"description", "python"} {
		if !stringSliceContains(required, expected) {
			t.Fatalf("python_exec should require %s, got: %#v", expected, required)
		}
	}
}

func TestInspectDataToolDefinitionGuidesPythonExecPreflight(t *testing.T) {
	def := inspectDataToolDefinition()
	if strings.Contains(def.Description, "data_ref") || strings.Contains(def.Description, "dataRef") {
		t.Fatalf("inspect_data description should not mention data_ref/dataRef: %s", def.Description)
	}
	for _, required := range []string{"python_exec", "不是 python_exec 的必选前置步骤", "截断", "多个工具结果", "resultRef", "alias"} {
		if !strings.Contains(def.Description, required) {
			t.Fatalf("inspect_data description should contain %q, got: %s", required, def.Description)
		}
	}
	params := def.Parameters
	properties, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in parameters: %#v", params)
	}
	source, ok := properties["source"].(map[string]interface{})
	if !ok {
		t.Fatalf("inspect_data should expose source object, got: %#v", properties)
	}
	sourceProperties, ok := source["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("source should expose properties, got: %#v", source)
	}
	typeSchema, ok := sourceProperties["type"].(map[string]interface{})
	if !ok {
		t.Fatalf("source type should expose schema, got: %#v", sourceProperties)
	}
	enumValues, ok := typeSchema["enum"].([]string)
	if !ok || len(enumValues) != 1 || enumValues[0] != "tool_result" {
		t.Fatalf("inspect_data source type should only allow tool_result, got: %#v", typeSchema)
	}
}

func TestInspectJSONValueBuildsPathSummary(t *testing.T) {
	var value interface{}
	if err := json.Unmarshal([]byte(`{"data":[{"id":1,"name":"a"},{"id":2,"extra":true}],"summary":{"total":2}}`), &value); err != nil {
		t.Fatalf("unmarshal fixture failed: %v", err)
	}
	acc := make(map[string]*jsonPathAccumulator)
	samples := make(map[string]interface{})
	inspectJSONValue(value, "$", acc, samples, 1)
	summaries := buildJSONPathSummaries(acc)
	byPath := make(map[string]jsonPathSummary, len(summaries))
	for _, item := range summaries {
		byPath[item.Path] = item
	}
	for _, path := range []string{"$", "$.data", "$.data[]", "$.data[].id", "$.data[].name", "$.data[].extra", "$.summary.total"} {
		if _, ok := byPath[path]; !ok {
			t.Fatalf("expected path %s in summaries: %#v", path, summaries)
		}
	}
	if got := byPath["$.data[].id"].Types; len(got) != 1 || got[0] != "number" {
		t.Fatalf("expected id number type, got %#v", got)
	}
	if sample, ok := samples["$.data[]"].([]interface{}); !ok || len(sample) != 1 {
		t.Fatalf("expected one sampled array item, got %#v", samples["$.data[]"])
	}
}

func TestNormalizedToolResultUsesRuneCoordinatesForPreviewAndRead(t *testing.T) {
	cfg := conf.GetReactRuntimeConfig().ToolResult
	repeatCount := cfg.InlineLimitBytes
	if repeatCount < cfg.PreviewLimit+16 {
		repeatCount = cfg.PreviewLimit + 16
	}
	content := " \n" + strings.Repeat("中", repeatCount)

	result := normalizeToolResult("tool_rune_coordinate", content, false, executedByInternal)
	if !result.Truncated {
		t.Fatalf("expected oversized content to be truncated")
	}

	contentRunes := []rune(content)
	previewRunes := []rune(result.Content)
	if len(previewRunes) != cfg.PreviewLimit {
		t.Fatalf("expected preview rune count %d, got %d", cfg.PreviewLimit, len(previewRunes))
	}
	if result.Content != string(contentRunes[:cfg.PreviewLimit]) {
		t.Fatalf("preview should preserve original rune coordinates")
	}
	if result.OmittedChars != len(contentRunes)-len(previewRunes) {
		t.Fatalf("expected omitted chars %d, got %d", len(contentRunes)-len(previewRunes), result.OmittedChars)
	}

	part, _, nextOffset := sliceResultContent(content, len(previewRunes), 8)
	if part != string(contentRunes[len(previewRunes):len(previewRunes)+8]) {
		t.Fatalf("read continuation should start immediately after preview, got %q", part)
	}
	if nextOffset != len(previewRunes)+8 {
		t.Fatalf("expected next offset %d, got %d", len(previewRunes)+8, nextOffset)
	}
}

func stringSliceContains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func makeReactTestMessages(count int, content string) []llm.ChatMessage {
	messages := make([]llm.ChatMessage, 0, count)
	for i := 0; i < count; i++ {
		messages = append(messages, llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: content})
	}
	return messages
}

func TestInternalMetaToolNamesAreReservedInToolService(t *testing.T) {
	names := []string{metaToolListTools, metaToolGetTool, metaToolExecuteTool, metaToolListSkills, metaToolGetSkill, metaToolReadToolResult, metaToolInspectData, metaToolPythonExec, metaToolTodoWrite, metaToolDisplayFiles}
	for _, name := range names {
		if !toolService.IsReservedToolName(name) {
			t.Fatalf("内置工具 %q 未加入 tool.reservedToolNames，注册校验会漏拦截", name)
		}
	}
	for _, def := range internalMetaToolDefinitions() {
		if !toolService.IsReservedToolName(def.Name) {
			t.Fatalf("内置工具定义 %q 未加入 tool.reservedToolNames，注册校验会漏拦截", def.Name)
		}
	}
}
