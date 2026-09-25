package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"react-base-service/conf"
)

type ClaudeClient struct {
	apiKey       string
	defaultModel string
	config       conf.EndpointConfig
}

func NewClaudeClient(apiKey, defaultModel string, config conf.EndpointConfig) *ClaudeClient {
	return &ClaudeClient{apiKey: apiKey, defaultModel: defaultModel, config: config}
}

// claudeRequest 为通用聊天请求体（BW/历史调用路径保持不变）。
type claudeRequest struct {
	Model     string                  `json:"model"`
	MaxTokens int                     `json:"max_tokens"`
	Stream    bool                    `json:"stream"`
	System    string                  `json:"system,omitempty"`
	Thinking  *claudeThinkingSettings `json:"thinking,omitempty"`
	Messages  []LLMMessage            `json:"messages"`
}

type claudeThinkingSettings struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// claudeFileRequest 为文件入模请求体，不影响通用 claudeRequest。
type claudeFileRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	Stream    bool                `json:"stream"`
	System    string              `json:"system,omitempty"`
	Messages  []claudeFileMessage `json:"messages"`
}

type claudeFileMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type claudeContentBlock struct {
	Type   string            `json:"type"`
	Text   string            `json:"text,omitempty"`
	Source *claudeFileSource `json:"source,omitempty"`
}

type claudeFileSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

type claudeStreamEvent struct {
	Type    string           `json:"type"`
	Delta   json.RawMessage  `json:"delta,omitempty"`
	Usage   *claudeUsage     `json:"usage,omitempty"`
	Message *claudeMessageSt `json:"message,omitempty"`
}

type claudeMessageSt struct {
	Usage *claudeUsage `json:"usage,omitempty"`
}

type claudeContentDelta struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type claudeUsage struct {
	InputTokens            int `json:"input_tokens"`
	OutputTokens           int `json:"output_tokens"`
	CacheCreateInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens   int `json:"cache_read_input_tokens"`
}

func claudeThinkingSettingsForModel(ctx context.Context, model string, maxTokens int) *claudeThinkingSettings {
	opts := reasoningOptionsFromContext(ctx)
	if !opts.Enabled {
		return nil
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if !strings.Contains(model, "claude") || !strings.Contains(model, "4") || maxTokens <= 2048 {
		return nil
	}
	budget := opts.BudgetTokens
	if budget <= 0 {
		budget = maxTokens / 4
	}
	if budget < 1024 {
		budget = 1024
	}
	if budget > 4096 {
		budget = 4096
	}
	if budget >= maxTokens {
		return nil
	}
	return &claudeThinkingSettings{Type: "enabled", BudgetTokens: budget}
}

func (c *ClaudeClient) ChatStream(ctx context.Context, messages []LLMMessage, model string) (<-chan StreamChunk, error) {
	if model == "" {
		model = c.defaultModel
	}
	maxTokens := c.config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = MaxOutputTokensForVersion(model)
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	thinking := claudeThinkingSettingsForModel(ctx, model, maxTokens)

	var systemPrompt string
	var apiMessages []LLMMessage
	for _, msg := range messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
		} else {
			apiMessages = append(apiMessages, msg)
		}
	}

	reqBody := claudeRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  apiMessages,
		Stream:    true,
		System:    systemPrompt,
		Thinking:  thinking,
	}

	bodyBytes, err := marshalRequestBodyGuard(ctx, reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	apiURL := c.config.ApiUrl + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if thinking != nil {
		req.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	}
	applyTraceHeadersFromContext(ctx, req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newAPIError("claude", resp.StatusCode, ParseRetryAfter(resp.Header.Get("Retry-After")), string(body))
	}

	return c.streamMessageResponse(ctx, resp.Body), nil
}

// ChatStreamWithFilePayloads 文件入模专用，不影响通用 ChatStream。
func (c *ClaudeClient) ChatStreamWithFilePayloads(
	ctx context.Context,
	messages []LLMMessage,
	model string,
	files []FilePayload,
) (<-chan StreamChunk, error) {
	if len(files) == 0 {
		return c.ChatStream(ctx, messages, model)
	}
	if model == "" {
		model = c.defaultModel
	}
	maxTokens := c.config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	var systemPrompt string
	var apiMessages []LLMMessage
	for _, msg := range messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
		} else {
			apiMessages = append(apiMessages, msg)
		}
	}

	fileMessages, err := buildClaudeFileMessages(apiMessages, files)
	if err != nil {
		return nil, err
	}
	reqBody := claudeFileRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  fileMessages,
		Stream:    true,
		System:    systemPrompt,
	}

	bodyBytes, err := marshalRequestBodyGuard(ctx, reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	apiURL := c.config.ApiUrl + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	applyTraceHeadersFromContext(ctx, req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newAPIError("claude", resp.StatusCode, ParseRetryAfter(resp.Header.Get("Retry-After")), string(body))
	}

	return c.streamMessageResponse(ctx, resp.Body), nil
}

func (c *ClaudeClient) streamMessageResponse(ctx context.Context, body io.ReadCloser) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)
	go func() {
		defer close(ch)
		defer body.Close()

		var (
			inputTokens       int
			outputTokens      int
			cacheReadTokens   int
			cacheCreateTokens int
			finishReason      string
			chunkCount        int
			lastChunkAt       time.Time
			terminationReason = StreamTerminationCompleted
		)

		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				terminationReason = StreamTerminationCancelled
				logStreamFinal(ctx, streamFinalState{
					provider:          "Claude",
					terminationReason: terminationReason,
					finishReason:      finishReason,
					chunkCount:        chunkCount,
					inputTokens:       inputTokens,
					outputTokens:      outputTokens,
					cacheReadTokens:   cacheReadTokens,
					cacheCreateTokens: cacheCreateTokens,
					lastChunkAt:       lastChunkAt,
				})
				ch <- StreamChunk{
					Done:              true,
					Cancelled:         true,
					InputTokens:       inputTokens,
					OutputTokens:      outputTokens,
					CacheReadTokens:   cacheReadTokens,
					CacheCreateTokens: cacheCreateTokens,
					FinishReason:      finishReason,
					TerminationReason: terminationReason,
				}
				return
			default:
			}

			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			chunkCount++
			lastChunkAt = time.Now()

			var event claudeStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				logStreamParseError(ctx, "Claude", chunkCount, data, err)
				continue
			}

			switch event.Type {
			case "message_start":
				if event.Message != nil && event.Message.Usage != nil {
					inputTokens = event.Message.Usage.InputTokens
					cacheReadTokens = event.Message.Usage.CacheReadInputTokens
					cacheCreateTokens = event.Message.Usage.CacheCreateInputTokens
				}
			case "content_block_delta":
				var delta claudeContentDelta
				if err := json.Unmarshal(event.Delta, &delta); err != nil {
					logStreamParseError(ctx, "Claude", chunkCount, string(event.Delta), err)
					continue
				}
				switch delta.Type {
				case "thinking_delta":
					if delta.Thinking != "" {
						ch <- StreamChunk{ReasoningContent: delta.Thinking}
					}
				case "signature_delta":
					if delta.Signature != "" {
						ch <- StreamChunk{ReasoningSignature: delta.Signature}
					}
				default:
					if delta.Text != "" {
						ch <- StreamChunk{Content: delta.Text}
					}
				}
			case "message_delta":
				if event.Usage != nil {
					outputTokens = event.Usage.OutputTokens
					if event.Usage.CacheReadInputTokens > 0 {
						cacheReadTokens = event.Usage.CacheReadInputTokens
					}
					if event.Usage.CacheCreateInputTokens > 0 {
						cacheCreateTokens = event.Usage.CacheCreateInputTokens
					}
				}
			case "message_stop":
				finishReason = "message_stop"
				logStreamFinal(ctx, streamFinalState{
					provider:          "Claude",
					terminationReason: terminationReason,
					finishReason:      finishReason,
					chunkCount:        chunkCount,
					inputTokens:       inputTokens,
					outputTokens:      outputTokens,
					cacheReadTokens:   cacheReadTokens,
					cacheCreateTokens: cacheCreateTokens,
					lastChunkAt:       lastChunkAt,
				})
				ch <- StreamChunk{
					Done:              true,
					InputTokens:       inputTokens,
					OutputTokens:      outputTokens,
					CacheReadTokens:   cacheReadTokens,
					CacheCreateTokens: cacheCreateTokens,
					FinishReason:      finishReason,
				}
				return
			case "error":
				terminationReason = StreamTerminationUpstreamErrorEvent
				logStreamFinal(ctx, streamFinalState{
					provider:          "Claude",
					terminationReason: terminationReason,
					finishReason:      finishReason,
					chunkCount:        chunkCount,
					inputTokens:       inputTokens,
					outputTokens:      outputTokens,
					cacheReadTokens:   cacheReadTokens,
					cacheCreateTokens: cacheCreateTokens,
					lastChunkAt:       lastChunkAt,
				})
				ch <- StreamChunk{
					Error:             fmt.Errorf("claude stream error: %s", string(event.Delta)),
					Done:              true,
					InputTokens:       inputTokens,
					OutputTokens:      outputTokens,
					CacheReadTokens:   cacheReadTokens,
					CacheCreateTokens: cacheCreateTokens,
					FinishReason:      finishReason,
					TerminationReason: terminationReason,
				}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			terminationReason = StreamTerminationUpstreamReadError
			logStreamFinal(ctx, streamFinalState{
				provider:          "Claude",
				terminationReason: terminationReason,
				finishReason:      finishReason,
				scannerErr:        err,
				chunkCount:        chunkCount,
				inputTokens:       inputTokens,
				outputTokens:      outputTokens,
				cacheReadTokens:   cacheReadTokens,
				cacheCreateTokens: cacheCreateTokens,
				lastChunkAt:       lastChunkAt,
			})
			ch <- StreamChunk{
				Error:             fmt.Errorf("read stream: %w", err),
				Done:              true,
				InputTokens:       inputTokens,
				OutputTokens:      outputTokens,
				CacheReadTokens:   cacheReadTokens,
				CacheCreateTokens: cacheCreateTokens,
				FinishReason:      finishReason,
				TerminationReason: terminationReason,
			}
			return
		}
		logReason := terminationReason
		if finishReason == "" {
			logReason = StreamTerminationUpstreamEOFWithoutDone
		}
		logStreamFinal(ctx, streamFinalState{
			provider:          "Claude",
			terminationReason: logReason,
			finishReason:      finishReason,
			chunkCount:        chunkCount,
			inputTokens:       inputTokens,
			outputTokens:      outputTokens,
			cacheReadTokens:   cacheReadTokens,
			cacheCreateTokens: cacheCreateTokens,
			lastChunkAt:       lastChunkAt,
		})
		ch <- StreamChunk{
			Done:              true,
			InputTokens:       inputTokens,
			OutputTokens:      outputTokens,
			CacheReadTokens:   cacheReadTokens,
			CacheCreateTokens: cacheCreateTokens,
			FinishReason:      finishReason,
		}
	}()

	return ch
}

func buildClaudeFileMessages(messages []LLMMessage, files []FilePayload) ([]claudeFileMessage, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages is empty")
	}
	targetIdx := findLatestUserMessageIndex(messages)
	if targetIdx < 0 {
		targetIdx = len(messages) - 1
	}

	out := make([]claudeFileMessage, 0, len(messages))
	for idx, msg := range messages {
		if idx != targetIdx {
			out = append(out, claudeFileMessage{Role: msg.Role, Content: msg.Content})
			continue
		}

		blocks := make([]claudeContentBlock, 0, len(files)+1)
		if strings.TrimSpace(msg.Content) != "" {
			blocks = append(blocks, claudeContentBlock{Type: "text", Text: msg.Content})
		}
		for _, f := range files {
			if strings.EqualFold(strings.TrimSpace(f.ContentAs), "text") {
				if strings.TrimSpace(f.Text) == "" {
					return nil, fmt.Errorf("claude text file content is empty, file=%s", f.FileName)
				}
				blocks = append(blocks, claudeContentBlock{
					Type: "text",
					Text: f.Text,
				})
				continue
			}

			if strings.ToLower(strings.TrimSpace(f.Encoding)) != "base64" {
				return nil, fmt.Errorf("claude file encoding only supports base64, file=%s", f.FileName)
			}
			if strings.TrimSpace(f.Data) == "" {
				return nil, fmt.Errorf("claude file data is empty, file=%s", f.FileName)
			}
			blocks = append(blocks, claudeContentBlock{
				Type: "document",
				Source: &claudeFileSource{
					Type:      "base64",
					MediaType: normalizeClaudeDocumentMediaType(f.MediaType),
					Data:      f.Data,
				},
			})
		}
		out = append(out, claudeFileMessage{
			Role:    msg.Role,
			Content: blocks,
		})
	}
	return out, nil
}

func normalizeClaudeDocumentMediaType(mediaType string) string {
	if strings.EqualFold(strings.TrimSpace(mediaType), "application/pdf") {
		return "application/pdf"
	}
	return "text/plain"
}

// ---- Tool Use 支持 ----

// claudeToolRequest 携带工具定义的请求体
type claudeToolRequest struct {
	Model     string                  `json:"model"`
	MaxTokens int                     `json:"max_tokens"`
	Stream    bool                    `json:"stream"`
	System    string                  `json:"system,omitempty"`
	Tools     []claudeToolDef         `json:"tools,omitempty"`
	Thinking  *claudeThinkingSettings `json:"thinking,omitempty"`
	Messages  []claudeAnyMsg          `json:"messages"`
}

type claudeCacheControl struct {
	Type string `json:"type"`
}

// claudeToolDef Claude API 要求工具 schema 字段为 input_schema
type claudeToolDef struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"input_schema"`
	CacheControl *claudeCacheControl    `json:"cache_control,omitempty"`
}

func toClaudeTools(tools []ToolDefinition) []claudeToolDef {
	out := make([]claudeToolDef, len(tools))
	for i, t := range tools {
		out[i] = claudeToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		}
	}
	if len(out) > 0 {
		out[len(out)-1].CacheControl = &claudeCacheControl{Type: "ephemeral"}
	}
	return out
}

// claudeAnyMsg content 可以是 string 或 []claudeContentPart
type claudeAnyMsg struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// claudeContentPart 统一内容块（text / thinking / tool_use / tool_result）
type claudeContentPart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   interface{}     `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// CacheControl 是 prompt 缓存断点标记（ephemeral）；由 applyClaudeCacheAnchor 统一放置。
	CacheControl *claudeCacheControl `json:"cache_control,omitempty"`
}

// applyClaudeCacheAnchor 把 prompt 缓存锚点放到最后一条稳定消息的最后一个内容块上。
// 跳过 ctx 声明的尾部临时消息数（WithCacheAnchorSkip），避免锚点落在下一轮就消失的
// 合成提醒上导致缓存断点每轮移动。thinking 块不承担锚点（部分 provider 禁止）。
func applyClaudeCacheAnchor(ctx context.Context, messages []claudeAnyMsg) {
	index := len(messages) - 1 - cacheAnchorSkipFromContext(ctx)
	if index < 0 || index >= len(messages) {
		return
	}
	message := &messages[index]
	switch content := message.Content.(type) {
	case []claudeContentPart:
		for i := len(content) - 1; i >= 0; i-- {
			if content[i].Type == "thinking" {
				continue
			}
			content[i].CacheControl = &claudeCacheControl{Type: "ephemeral"}
			return
		}
	case string:
		if content != "" {
			message.Content = []claudeContentPart{{Type: "text", Text: content, CacheControl: &claudeCacheControl{Type: "ephemeral"}}}
		}
	}
}

// claudeToolStreamEvent 扩展的流式事件（支持 content_block_start 等）
type claudeToolStreamEvent struct {
	Type         string                  `json:"type"`
	Index        int                     `json:"index"`
	Delta        json.RawMessage         `json:"delta,omitempty"`
	ContentBlock *claudeContentBlockInfo `json:"content_block,omitempty"`
	Usage        *claudeUsage            `json:"usage,omitempty"`
	Message      *claudeMessageSt        `json:"message,omitempty"`
}

type claudeContentBlockInfo struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
}

type claudeDeltaPayload struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// ChatStreamWithTools 支持 tool_use 的流式对话
// messages 支持文本和工具内容混合，tools 为可用工具列表
func (c *ClaudeClient) ChatStreamWithTools(
	ctx context.Context,
	messages []ChatMessage,
	model string,
	tools []ToolDefinition,
) (<-chan StreamChunk, error) {
	if model == "" {
		model = c.defaultModel
	}
	maxTokens := c.config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = MaxOutputTokensForVersion(model)
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	thinking := claudeThinkingSettingsForModel(ctx, model, maxTokens)

	var systemPrompt string
	apiMessages := make([]claudeAnyMsg, 0, len(messages))

	for _, msg := range messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			continue
		}

		if len(msg.Parts) > 0 {
			parts := make([]claudeContentPart, 0, len(msg.Parts))
			for _, p := range msg.Parts {
				cp := claudeContentPart{Type: p.Type}
				switch p.Type {
				case "text":
					cp.Text = p.Text
				case "thinking":
					cp.Thinking = p.Thinking
					cp.Signature = p.Signature
				case "tool_use":
					cp.ID = p.ID
					cp.Name = p.Name
					cp.Input = normalizeClaudeToolInput(p.Input)
				case "tool_result":
					cp.ToolUseID = p.ToolUseID
					cp.Content = p.Content
					cp.IsError = p.IsError
				}
				parts = append(parts, cp)
			}
			apiMessages = append(apiMessages, claudeAnyMsg{Role: msg.Role, Content: parts})
		} else {
			apiMessages = append(apiMessages, claudeAnyMsg{Role: msg.Role, Content: msg.Content})
		}
	}

	// prompt 缓存锚点：放在最后一条稳定消息上（跳过尾部临时提醒），让下一轮请求增量命中缓存。
	applyClaudeCacheAnchor(ctx, apiMessages)

	reqBody := claudeToolRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  apiMessages,
		Stream:    true,
		System:    systemPrompt,
		Tools:     toClaudeTools(tools),
		Thinking:  thinking,
	}

	bodyBytes, err := marshalRequestBodyGuard(ctx, reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	logLLMPrefixDebug(ctx, "Claude", bodyBytes)

	apiURL := c.config.ApiUrl + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	var betaHeaders []string
	if thinking != nil {
		betaHeaders = append(betaHeaders, "interleaved-thinking-2025-05-14")
	}
	if len(tools) > 0 {
		betaHeaders = append(betaHeaders, "prompt-caching-2024-07-31")
	}
	if len(betaHeaders) > 0 {
		req.Header.Set("anthropic-beta", strings.Join(betaHeaders, ","))
	}
	applyTraceHeadersFromContext(ctx, req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newAPIError("claude", resp.StatusCode, ParseRetryAfter(resp.Header.Get("Retry-After")), string(body))
	}

	ch := make(chan StreamChunk, 64)
	go c.parseToolStream(ctx, resp, ch)
	return ch, nil
}

func normalizeClaudeToolInput(input json.RawMessage) json.RawMessage {
	if len(input) == 0 || !json.Valid(input) {
		return json.RawMessage(`{}`)
	}
	return canonicalToolInput(input)
}

// parseToolStream 解析支持 tool_use 的 SSE 流
func (c *ClaudeClient) parseToolStream(ctx context.Context, resp *http.Response, ch chan<- StreamChunk) {
	defer close(ch)
	defer resp.Body.Close()

	var (
		inputTokens, outputTokens          int
		cacheReadTokens, cacheCreateTokens int
		stopReason                         string
		// 跟踪正在构建的 content blocks
		activeBlocks = make(map[int]*toolBlockBuilder)
		toolCalls    []ToolCall
	)

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- StreamChunk{
				Done:              true,
				Cancelled:         true,
				InputTokens:       inputTokens,
				OutputTokens:      outputTokens,
				CacheReadTokens:   cacheReadTokens,
				CacheCreateTokens: cacheCreateTokens,
			}
			return
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event claudeToolStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				inputTokens = event.Message.Usage.InputTokens
				cacheReadTokens = event.Message.Usage.CacheReadInputTokens
				cacheCreateTokens = event.Message.Usage.CacheCreateInputTokens
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				activeBlocks[event.Index] = &toolBlockBuilder{
					blockType: event.ContentBlock.Type,
					id:        event.ContentBlock.ID,
					name:      event.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			var delta claudeDeltaPayload
			if err := json.Unmarshal(event.Delta, &delta); err != nil {
				continue
			}
			switch delta.Type {
			case "text_delta":
				if delta.Text != "" {
					ch <- StreamChunk{Content: delta.Text}
				}
				if b, ok := activeBlocks[event.Index]; ok {
					b.textBuf.WriteString(delta.Text)
				}
			case "thinking_delta":
				if delta.Thinking != "" {
					ch <- StreamChunk{ReasoningContent: delta.Thinking}
				}
			case "signature_delta":
				if delta.Signature != "" {
					ch <- StreamChunk{ReasoningSignature: delta.Signature}
				}
			case "input_json_delta":
				if b, ok := activeBlocks[event.Index]; ok {
					b.jsonBuf.WriteString(delta.PartialJSON)
				}
			}

		case "content_block_stop":
			if b, ok := activeBlocks[event.Index]; ok {
				if b.blockType == "tool_use" {
					toolCalls = append(toolCalls, ToolCall{
						ID:    b.id,
						Name:  b.name,
						Input: normalizeClaudeToolInput(json.RawMessage(b.jsonBuf.String())),
					})
				}
				delete(activeBlocks, event.Index)
			}

		case "message_delta":
			var delta claudeDeltaPayload
			if err := json.Unmarshal(event.Delta, &delta); err == nil {
				if delta.StopReason != "" {
					stopReason = delta.StopReason
				}
			}
			if event.Usage != nil {
				outputTokens = event.Usage.OutputTokens
				if event.Usage.CacheReadInputTokens > 0 {
					cacheReadTokens = event.Usage.CacheReadInputTokens
				}
				if event.Usage.CacheCreateInputTokens > 0 {
					cacheCreateTokens = event.Usage.CacheCreateInputTokens
				}
			}

		case "message_stop":
			ch <- StreamChunk{
				Done:              true,
				InputTokens:       inputTokens,
				OutputTokens:      outputTokens,
				CacheReadTokens:   cacheReadTokens,
				CacheCreateTokens: cacheCreateTokens,
				StopReason:        stopReason,
				ToolCalls:         toolCalls,
			}
			return

		case "error":
			ch <- StreamChunk{
				Error:             fmt.Errorf("claude stream error: %s", string(event.Delta)),
				Done:              true,
				InputTokens:       inputTokens,
				OutputTokens:      outputTokens,
				CacheReadTokens:   cacheReadTokens,
				CacheCreateTokens: cacheCreateTokens,
			}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamChunk{
			Error:             fmt.Errorf("read stream: %w", err),
			Done:              true,
			InputTokens:       inputTokens,
			OutputTokens:      outputTokens,
			CacheReadTokens:   cacheReadTokens,
			CacheCreateTokens: cacheCreateTokens,
		}
		return
	}
	ch <- StreamChunk{
		Done:              true,
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		CacheReadTokens:   cacheReadTokens,
		CacheCreateTokens: cacheCreateTokens,
		StopReason:        stopReason,
		ToolCalls:         toolCalls,
	}
}

// toolBlockBuilder 跟踪正在构建的 content block
type toolBlockBuilder struct {
	blockType string // "text" or "tool_use"
	id        string
	name      string
	textBuf   strings.Builder
	jsonBuf   strings.Builder
}
