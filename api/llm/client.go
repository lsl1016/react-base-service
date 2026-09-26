package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"react-base-service/conf"
)

// LLMMessage 发送给大模型的消息
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// FilePayload 文件载荷（供独立文件入模接口使用，不影响通用 ChatStream）
type FilePayload struct {
	FileID    string
	FileName  string
	MediaType string
	ContentAs string // document/text
	Encoding  string // base64/text
	Data      string // base64 字符串
	Text      string // 纯文本内容
}

// StreamChunk 流式响应片段
type StreamChunk struct {
	Content            string
	ReasoningContent   string // 模型供应商返回的真实 reasoning/thinking 增量；未返回时为空
	ReasoningSignature string // Claude thinking block 的签名；用于多轮工具调用时原样回传
	Done               bool
	Cancelled          bool // 用户主动取消
	InputTokens        int
	OutputTokens       int
	CacheReadTokens    int
	CacheCreateTokens  int
	Error              error
	ReceivedDone       bool
	FinishReason       string
	TerminationReason  string
	StopReason         string     // "end_turn" / "tool_use"，仅 Done=true 时有意义
	ToolCalls          []ToolCall // StopReason="tool_use" 时携带工具调用列表
}

// ReasoningMode 思考程度三态：off 关闭 / auto 自适应默认 / custom 按档位或预算显式指定。
type ReasoningMode string

const (
	ReasoningModeOff    ReasoningMode = "off"
	ReasoningModeAuto   ReasoningMode = "auto"
	ReasoningModeCustom ReasoningMode = "custom"
)

// 合法 effort 档位（OpenAI 系 reasoning_effort；Anthropic 系按 EffortToThinkingBudget 折算预算）。
var validReasoningEfforts = []string{"minimal", "low", "medium", "high"}

// IsValidReasoningEffort 校验 effort 档位是否合法。
func IsValidReasoningEffort(effort string) bool {
	for _, valid := range validReasoningEfforts {
		if effort == valid {
			return true
		}
	}
	return false
}

// ReasoningOptions 控制单次 LLM 请求是否请求供应商暴露真实 reasoning/thinking 增量，
// 以及思考程度（三态）。由 WithReasoning 注入 context，各协议 client 自行翻译：
// GPT 系 → reasoning_effort 档位；Anthropic 系 → thinking.budget_tokens 预算。
type ReasoningOptions struct {
	Enabled      bool
	Mode         ReasoningMode // off / auto / custom；空值按 auto 处理
	Effort       string        // minimal / low / medium / high（custom 档位）
	BudgetTokens int           // Anthropic 系 thinking budget（custom 显式预算）
}

// NormalizeReasoningOptions 归一化 ReasoningOptions：补齐 Mode、按 Mode 推导 Enabled。
func NormalizeReasoningOptions(opts ReasoningOptions) ReasoningOptions {
	if opts.Mode == "" {
		opts.Mode = ReasoningModeAuto
	}
	if opts.Mode != ReasoningModeOff && opts.Mode != ReasoningModeCustom {
		opts.Mode = ReasoningModeAuto
	}
	opts.Enabled = opts.Mode != ReasoningModeOff
	return opts
}

// BudgetToReasoningEffort 按思考预算折算 GPT 系 effort 档位（前端只传预算时的协议回退）。
func BudgetToReasoningEffort(budget int) string {
	switch {
	case budget <= 0:
		return ""
	case budget <= 2048:
		return "low"
	case budget <= 12288:
		return "medium"
	default:
		return "high"
	}
}

// ReasoningEffortForGPT 把三态 ReasoningOptions 翻译为 OpenAI 系 reasoning_effort：
// off → 空（不发）；auto/未指定档位 → high（保持历史默认档）；custom 档位直接用；
// custom 仅预算时按预算折算档位。
func ReasoningEffortForGPT(opts ReasoningOptions) string {
	if !opts.Enabled || opts.Mode == ReasoningModeOff {
		return ""
	}
	if IsValidReasoningEffort(opts.Effort) {
		return opts.Effort
	}
	if opts.BudgetTokens > 0 {
		return BudgetToReasoningEffort(opts.BudgetTokens)
	}
	return "high"
}

// ToolChoice 控制单次模型请求是否允许生成工具调用。
type ToolChoice string

const (
	ToolChoiceAuto ToolChoice = "auto"
	ToolChoiceNone ToolChoice = "none"
)

type reasoningContextKey struct{}
type toolChoiceContextKey struct{}

// WithToolChoice 为单次模型请求设置工具选择策略。
func WithToolChoice(ctx context.Context, choice ToolChoice) context.Context {
	if ctx == nil || choice == "" {
		return ctx
	}
	return context.WithValue(ctx, toolChoiceContextKey{}, choice)
}

func toolChoiceFromContext(ctx context.Context) ToolChoice {
	if ctx == nil {
		return ""
	}
	if choice, ok := ctx.Value(toolChoiceContextKey{}).(ToolChoice); ok {
		return choice
	}
	return ""
}

// WithReasoning 只用于需要展示真实思考过程的调用路径，例如 ReAct Runtime。
// 三态语义：off = 不向供应商请求 thinking（同时前端不展示思考流）；
// auto = 按协议自适应默认（GPT 系 high 档、Anthropic 系 maxTokens/4 预算）；
// custom = 按 Effort/BudgetTokens 显式指定。
func WithReasoning(ctx context.Context, opts ReasoningOptions) context.Context {
	opts = NormalizeReasoningOptions(opts)
	return context.WithValue(ctx, reasoningContextKey{}, opts)
}

func reasoningOptionsFromContext(ctx context.Context) ReasoningOptions {
	if ctx == nil {
		return ReasoningOptions{}
	}
	if opts, ok := ctx.Value(reasoningContextKey{}).(ReasoningOptions); ok {
		return opts
	}
	return ReasoningOptions{}
}

// LLMClient 大模型客户端接口
type LLMClient interface {
	ChatStream(ctx context.Context, messages []LLMMessage, model string) (<-chan StreamChunk, error)

	// ChatStreamWithTools 支持工具调用的流式对话
	// 当 StopReason="tool_use" 时，调用者应执行工具并将结果追加到 messages 后再次调用
	ChatStreamWithTools(ctx context.Context, messages []ChatMessage, model string, tools []ToolDefinition) (<-chan StreamChunk, error)
}

var (
	ErrModelNotSupported     = errors.New("llm model not supported")
	ErrApiKeyNotConfigured   = errors.New("llm api key not configured")
	ErrEndpointNotConfigured = errors.New("llm endpoint not configured")
	ErrCatalogNotConfigured  = errors.New("llm model catalog not configured")
)

func resolveApiKey(platformType string) string {
	return ResolveApiKey(platformType)
}

// ResolveApiKey 根据平台类型从配置中获取对应的 API Key
func ResolveApiKey(platformType string) string {
	apiCfg := conf.API.LLM
	if apiCfg.ApiKeys == nil {
		return ""
	}
	return strings.TrimSpace(apiCfg.ApiKeys[strings.TrimSpace(platformType)])
}

// GetClient 根据平台与模型 key 获取对应的 LLM 客户端
func GetClient(platformType, modelKey string) (LLMClient, error) {
	apiCfg := conf.API.LLM
	endpoint, ok := apiCfg.Endpoints[modelKey]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrEndpointNotConfigured, modelKey)
	}

	catalog, ok := conf.CustomConf.LLM.Models[modelKey]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCatalogNotConfigured, modelKey)
	}

	apiKey := resolveApiKey(platformType)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: %s", ErrApiKeyNotConfigured, platformType)
	}

	switch modelKey {
	case "claude", "deepseek":
		return NewClaudeClient(apiKey, catalog.DefaultVersion, endpoint), nil
	case "gpt":
		return NewGPTClient(apiKey, catalog.DefaultVersion, endpoint), nil
	case "minimax":
		return NewMiniMaxClient(apiKey, catalog.DefaultVersion, endpoint), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrModelNotSupported, modelKey)
	}
}

// MaxOutputTokensForVersion 在模型目录中按版本查找 max_output_tokens（含 default_version 命中）。
// 未配置返回 0，由调用方回退内置默认值。用于 endpoint 未配置 max_tokens 时消除硬编码输出上限。
func MaxOutputTokensForVersion(version string) int {
	version = strings.TrimSpace(version)
	if version == "" {
		return 0
	}
	for _, catalog := range conf.CustomConf.LLM.Models {
		if slices.Contains(catalog.Versions, version) || catalog.DefaultVersion == version {
			return catalog.MaxOutputTokens
		}
	}
	return 0
}

// GetClientWithKey 根据自定义 API Key 和模型 key 获取 LLM 客户端（Skill 管线使用）
func GetClientWithKey(apiKey, modelKey string) (LLMClient, error) {
	apiCfg := conf.API.LLM
	endpoint, ok := apiCfg.Endpoints[modelKey]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrEndpointNotConfigured, modelKey)
	}

	catalog, ok := conf.CustomConf.LLM.Models[modelKey]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCatalogNotConfigured, modelKey)
	}

	if apiKey == "" {
		return nil, fmt.Errorf("%w: empty apiKey", ErrApiKeyNotConfigured)
	}

	switch modelKey {
	case "claude", "deepseek":
		return NewClaudeClient(apiKey, catalog.DefaultVersion, endpoint), nil
	case "gpt":
		return NewGPTClient(apiKey, catalog.DefaultVersion, endpoint), nil
	case "minimax":
		return NewMiniMaxClient(apiKey, catalog.DefaultVersion, endpoint), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrModelNotSupported, modelKey)
	}
}

// ModelMeta 模型元信息
type ModelMeta struct {
	Key            string
	Versions       []string
	DefaultVersion string
}

// GetSupportedModels 获取所有支持的模型列表
func GetSupportedModels() []ModelMeta {
	catalog := conf.CustomConf.LLM.Models
	var models []ModelMeta
	for key, m := range catalog {
		models = append(models, ModelMeta{
			Key:            key,
			Versions:       m.Versions,
			DefaultVersion: m.DefaultVersion,
		})
	}
	return models
}

// modelKeyClientType 新枚举 model_key → client 类型（gpt/claude）
// OpenAI 兼容厂商统一走 gpt client，Anthropic 走 claude client
var modelKeyClientType = map[string]string{
	"OpenAI":             "gpt",
	"DeepSeek":           "gpt",
	"DeepSeek Anthropic": "claude",
	"xAI":                "gpt",
	"Moonshot":           "gpt",
	"通义千问":               "gpt",
	"智谱":                 "gpt",
	"火山方舟（字节）":           "gpt",
	"Google":             "gpt",
	"MiniMax":            "gpt",
	"文心一言":               "gpt",
	"Anthropic":          "claude",
	"自建网关":               "gpt",
}

// modelKeyEndpointOverrides 声明少数厂商枚举的专用 endpoint key。
// 例如 DeepSeek 同时有 OpenAI 兼容和 Anthropic 兼容两条接入面，不能只靠 gpt/claude-cn 推导。
var modelKeyEndpointOverrides = map[string]string{
	"deepseek":           "deepseek",
	"DeepSeek Anthropic": "deepseek",
}

// modelKeyIsCN 标记哪些厂商走国内代理，未出现在此 map 中的默认走国外代理
var modelKeyIsCN = map[string]bool{
	"DeepSeek": true,
	"Moonshot": true,
	"通义千问":     true,
	"智谱":       true,
	"火山方舟（字节）": true,
	"MiniMax":  true,
	"文心一言":     true,
	"自建网关":     true,
}

// ValidModelKeys 返回所有支持的新枚举 model_key 列表
func ValidModelKeys() []string {
	keys := make([]string, 0, len(modelKeyClientType))
	for k := range modelKeyClientType {
		keys = append(keys, k)
	}
	return keys
}

// IsValidModelKey 校验 model_key 是否在新枚举列表内
func IsValidModelKey(modelKey string) bool {
	_, ok := modelKeyClientType[modelKey]
	return ok
}

// NormalizeModelKey 将旧枚举（gpt/claude/minimax）或新枚举统一映射到 clientType（gpt/claude）
func NormalizeModelKey(key string) string {
	// 旧枚举兼容
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "gpt":
		return "gpt"
	case "claude":
		return "claude"
	case "deepseek":
		return "claude"
	case "minimax":
		return "minimax" // 兼容历史枚举值
	}
	// 新枚举
	if clientType, ok := modelKeyClientType[key]; ok {
		return clientType
	}
	return ""
}

// GetClientWithUserModel 根据用户模型的 apiKey、modelKey（新枚举）构建 LLM 客户端
// endpoint 从 api.yaml 按 endpointKey 查找：国内厂商用 "<clientType>-cn"，国外用 "<clientType>"
func GetClientWithUserModel(apiKey, modelKey string) (LLMClient, error) {
	return GetClientWithUserModelEndpoint(apiKey, modelKey, "", 0)
}

// GetClientWithUserModelEndpoint 在 GetClientWithUserModel 基础上支持用户模型自带端点覆盖：
// apiURL 非空时跳过 api.yaml 端点查找（前端配置的自定义接入面优先），maxOutputTokens
// 作为该端点的 max_tokens（0=回退模型目录/内置默认）。连通性检测与 run 构建共用本入口。
func GetClientWithUserModelEndpoint(apiKey, modelKey, apiURL string, maxOutputTokens int) (LLMClient, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("%w: empty apiKey", ErrApiKeyNotConfigured)
	}

	clientType := NormalizeModelKey(modelKey)
	if clientType == "" {
		return nil, fmt.Errorf("%w: %s", ErrModelNotSupported, modelKey)
	}

	endpoint := conf.EndpointConfig{}
	if strings.TrimSpace(apiURL) != "" {
		// 用户模型自带端点：模型配置面板「厂商与密钥」填写的自定义接入面。
		endpoint.ApiUrl = strings.TrimSpace(apiURL)
		if maxOutputTokens > 0 {
			endpoint.MaxTokens = maxOutputTokens
		}
	} else {
		endpointKey := clientType
		if override, ok := modelKeyEndpointOverrides[modelKey]; ok {
			endpointKey = override
		} else if modelKeyIsCN[modelKey] {
			endpointKey = clientType + "-cn"
		}

		apiCfg := conf.API.LLM
		configured, ok := apiCfg.Endpoints[endpointKey]
		if !ok {
			return nil, fmt.Errorf("%w: endpointKey=%s", ErrEndpointNotConfigured, endpointKey)
		}
		endpoint = configured
		if maxOutputTokens > 0 {
			endpoint.MaxTokens = maxOutputTokens
		}
	}

	switch clientType {
	case "claude":
		return NewClaudeClient(apiKey, "", endpoint), nil
	case "minimax":
		return NewMiniMaxClient(apiKey, "", endpoint), nil
	default: // gpt（含所有 OpenAI 兼容厂商）
		return NewGPTClient(apiKey, "", endpoint), nil
	}
}

// ResolveModelVersion 解析最终生效的模型版本：
// 1) 优先使用请求显式传入版本；
// 2) 未传时回退到配置中的 default_version。
func ResolveModelVersion(modelKey, requested string) string {
	version := strings.TrimSpace(requested)
	if version != "" {
		return version
	}
	catalog, ok := conf.CustomConf.LLM.Models[modelKey]
	if !ok {
		return ""
	}
	return strings.TrimSpace(catalog.DefaultVersion)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
