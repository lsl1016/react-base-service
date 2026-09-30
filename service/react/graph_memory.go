package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/metrics"
	"react-base-service/conf"
	graphmemory "react-base-service/service/graphmemory"

	"github.com/gin-gonic/gin"
)

const (
	graphMemorySearchDefaultLimit = 10
	graphMemorySearchMaxLimit     = 30
	graphMemoryWriteMaxContent    = 4000
	// graphMemoryFactTimeLayout 事实时间窗的展示粒度：日期（图谱时间语义到天足够）。
	graphMemoryFactTimeLayout = "2006-01-02"
)

// graphMemoryConfig 解析当前生效的图谱记忆配置（含默认值填充）。
func graphMemoryConfig() conf.ReactGraphMemoryConfig {
	return conf.GetReactRuntimeConfig().GraphMemory
}

// graphMemorySharedClient 返回共享客户端；endpoint 未配置时返回 nil。
func graphMemorySharedClient() *graphmemory.Client {
	cfg := graphMemoryConfig()
	return graphmemory.SharedClient(graphmemory.Config{
		Endpoint:  cfg.Endpoint,
		TimeoutMs: cfg.TimeoutMs,
	})
}

// resolveGraphMemoryScope 解析当前 run 的图谱作用域（组即分区，服务端强制注入，不进模型入参）。
func resolveGraphMemoryScope(callerKey, userName string) graphmemory.Scope {
	cfg := graphMemoryConfig()
	return graphmemory.ResolveScope(callerKey, userName, cfg.GraphMemoryUserScope())
}

// graphMemoryToolDefinitions 声明图谱记忆工具；仅在 graph_memory.enabled 时注册，
// write 工具额外要求 write.enabled（灰度，默认关）。
func graphMemoryToolDefinitions() []llm.ToolDefinition {
	definitions := []llm.ToolDefinition{graphMemorySearchToolDefinition()}
	if graphMemoryConfig().Write.WriteEnabled() {
		definitions = append(definitions, graphMemoryWriteToolDefinition())
	}
	return definitions
}

func graphMemorySearchToolDefinition() llm.ToolDefinition {
	return objectTool(metaToolGraphMemorySearch,
		"检索长期事实图谱：实体间关系、历史事件、随时间变化的状态。适用于『A 和 B 什么关系』『过去发生过什么』『某个状态什么时候变的』这类需要关系与时间线的问题；偏好与约定看 <memory> 块（以它为准），文档原文用知识库检索。返回带时间窗的事实，可能包含已失效的历史事实（注意 valid/invalid 时间）。",
		map[string]interface{}{
			"query": stringSchema("自然语言查询，必填。"),
			"limit": numberSchema("返回事实条数上限，默认 10，最大 30。"),
		})
}

func graphMemoryWriteToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolGraphMemoryWrite,
		Description: "把一段重要事件/结论写入长期事实图谱（异步生效，数秒到数十秒后可检索到）。仅在用户明确要求沉淀、或发现值得长期记住的事实时使用；不写凭证、证件号等敏感信息。偏好类内容用 memory_write，不要用本工具。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
				"name":        stringSchema("事件短标题（≤64字），必填。"),
				"content":     stringSchema("事件或事实的自然语言描述（≤4000字），必填。写清涉及的对象、关系和时间。"),
			},
			"required":             []string{"description", "name", "content"},
			"additionalProperties": false,
		},
	}
}

type graphMemorySearchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type graphMemoryFactView struct {
	Fact      string `json:"fact"`
	Name      string `json:"name"`
	ValidAt   string `json:"validAt"`
	InvalidAt string `json:"invalidAt"`
}

// executeGraphMemorySearch 检索图谱事实。group_ids 由服务端按 run 作用域强制注入，
// 模型入参不暴露组概念（防跨组越权检索）。
func (s *reactEngineState) executeGraphMemorySearch(input json.RawMessage) (string, bool, error) {
	var req graphMemorySearchInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", true, fmt.Errorf("graph_memory_search input must be a valid JSON object")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return "", true, fmt.Errorf("query 不能为空")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = graphMemorySearchDefaultLimit
	}
	if limit > graphMemorySearchMaxLimit {
		limit = graphMemorySearchMaxLimit
	}

	client := graphMemorySharedClient()
	if client == nil {
		return "", true, fmt.Errorf("graph memory endpoint is not configured")
	}
	scope := resolveGraphMemoryScope(s.req.payload.CallerKey, s.req.userName)
	facts, err := client.Search(s.runCtx, scope.Groups, query, limit)
	if err != nil {
		metrics.GraphMemorySearchTotal.WithLabelValues("error").Inc()
		return "", true, err
	}
	metrics.GraphMemorySearchTotal.WithLabelValues("ok").Inc()

	views := make([]graphMemoryFactView, 0, len(facts))
	for _, fact := range facts {
		views = append(views, graphMemoryFactView{
			Fact:      fact.Fact,
			Name:      fact.Name,
			ValidAt:   graphMemoryFactDate(fact.ValidAt),
			InvalidAt: graphMemoryFactDate(fact.InvalidAt),
		})
	}
	data, _ := json.Marshal(map[string]interface{}{"facts": views, "total": len(views)})
	return string(data), false, nil
}

type graphMemoryWriteInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// executeGraphMemoryWrite 是模型侧 episode 写入（W1 显式沉淀）：组、来源描述、参考时间
// 全部由服务端补齐，模型只提供事件本身。Graphiti 受理即成功（202 异步抽取）。
func (s *reactEngineState) executeGraphMemoryWrite(input json.RawMessage) (string, bool, error) {
	var req graphMemoryWriteInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", true, fmt.Errorf("graph_memory_write input must be a valid JSON object")
	}
	name := strings.TrimSpace(req.Name)
	content := strings.TrimSpace(req.Content)
	if name == "" || content == "" {
		return "", true, fmt.Errorf("name 和 content 必填")
	}
	if len([]rune(content)) > graphMemoryWriteMaxContent {
		return "", true, fmt.Errorf("content 超过 %d 字上限，请提炼后重试", graphMemoryWriteMaxContent)
	}

	client := graphMemorySharedClient()
	if client == nil {
		return "", true, fmt.Errorf("graph memory endpoint is not configured")
	}
	scope := resolveGraphMemoryScope(s.req.payload.CallerKey, s.req.userName)
	err := client.AddEpisode(s.runCtx, scope.WriteGroup, graphmemory.EpisodeMessage{
		Content:           content,
		Name:              name,
		RoleType:          "user",
		Role:              s.req.userName,
		Timestamp:         time.Now(),
		SourceDescription: "react-base run " + s.runID,
	})
	if err != nil {
		metrics.GraphMemoryEpisodeTotal.WithLabelValues("error").Inc()
		return "", true, err
	}
	metrics.GraphMemoryEpisodeTotal.WithLabelValues("ok").Inc()
	data, _ := json.Marshal(map[string]interface{}{
		"queued": true,
		"note":   "已受理，图谱异步抽取中（通常数十秒内生效）；无需等待，也无需向用户确认写入结果。",
	})
	return string(data), false, nil
}

// graphMemoryFactDate 把 ISO 时间串裁到日期；空串原样返回。
func graphMemoryFactDate(iso string) string {
	trimmed := strings.TrimSpace(iso)
	if trimmed == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.Format(graphMemoryFactTimeLayout)
	}
	return trimmed
}

// buildGraphMemoryContextForRun 在 run 初始化阶段装配 <graph_memory> 注入块：
// 按本次用户输入检索相关事实。注入是增强不是依赖——检索失败/超时/空结果一律返回空串
// （token 零增量，绝不影响 run 启动）。
func buildGraphMemoryContextForRun(ctx *gin.Context, callerKey, userName, query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	client := graphMemorySharedClient()
	if client == nil {
		return ""
	}
	cfg := graphMemoryConfig()
	injectCtx, cancel := context.WithTimeout(ctx.Request.Context(), time.Duration(cfg.Inject.TimeoutMs)*time.Millisecond)
	defer cancel()

	scope := graphmemory.ResolveScope(callerKey, userName, cfg.GraphMemoryUserScope())
	facts, err := client.Search(injectCtx, scope.Groups, query, cfg.Inject.MaxFacts)
	if err != nil {
		metrics.GraphMemorySearchTotal.WithLabelValues("inject_error").Inc()
		metrics.GraphMemoryInjectFacts.WithLabelValues("error").Set(0)
		return ""
	}
	metrics.GraphMemorySearchTotal.WithLabelValues("inject_ok").Inc()
	rendered := renderGraphMemoryContext(facts, cfg.Inject.MaxFacts, cfg.Inject.MaxChars)
	metrics.GraphMemoryInjectFacts.WithLabelValues("ok").Set(float64(len(facts)))
	return rendered
}

// renderGraphMemoryContext 渲染注入块：每条事实一行（关系名 + 事实 + 时间窗），
// 条数/字符双预算截断。无事实返回空串（不注入）。
func renderGraphMemoryContext(facts []graphmemory.Fact, maxFacts, maxChars int) string {
	if len(facts) == 0 || maxFacts <= 0 || maxChars <= 0 {
		return ""
	}
	if len(facts) > maxFacts {
		facts = facts[:maxFacts]
	}

	var sb strings.Builder
	sb.WriteString("<graph_memory>\n")
	sb.WriteString("## 相关历史事实（自动检索，可能过时或已被新事实取代，注意时间窗；深挖用 graph_memory_search；与 <memory> 冲突时以 <memory> 为准）\n")
	used := 0
	truncated := 0
	for _, fact := range facts {
		line := fmt.Sprintf("- [%s] %s（%s ~ %s）\n",
			strings.TrimSpace(fact.Name),
			strings.TrimSpace(fact.Fact),
			graphMemoryFactWindowPart(graphMemoryFactDate(fact.ValidAt)),
			graphMemoryFactWindowPart(graphMemoryFactDate(fact.InvalidAt)))
		if used+len([]rune(line)) > maxChars {
			truncated++
			continue
		}
		sb.WriteString(line)
		used += len([]rune(line))
	}
	if truncated > 0 {
		sb.WriteString(fmt.Sprintf("（另有 %d 条相关事实超出预算未注入，可用 graph_memory_search 检索）\n", truncated))
	}
	sb.WriteString("</graph_memory>")
	return sb.String()
}

// graphMemoryFactWindowPart 渲染时间窗端点：空值显示"未知时间"。
func graphMemoryFactWindowPart(date string) string {
	if date == "" {
		return "未知时间"
	}
	return date
}
