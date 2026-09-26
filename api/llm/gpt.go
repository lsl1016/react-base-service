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

type GPTClient struct {
	apiKey       string
	defaultModel string
	config       conf.EndpointConfig
}

func NewGPTClient(apiKey, defaultModel string, config conf.EndpointConfig) *GPTClient {
	return &GPTClient{apiKey: apiKey, defaultModel: defaultModel, config: config}
}

type gptStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// gptRequest 为通用聊天请求体（BW/历史调用路径保持不变）。
type gptRequest struct {
	Model               string            `json:"model"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	Stream              bool              `json:"stream"`
	StreamOptions       *gptStreamOptions `json:"stream_options,omitempty"`
	Messages            []LLMMessage      `json:"messages"`
}

// gptFileRequest 为文件入模请求体，不影响通用 gptRequest。
type gptFileRequest struct {
	Model               string            `json:"model"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	Stream              bool              `json:"stream"`
	StreamOptions       *gptStreamOptions `json:"stream_options,omitempty"`
	Messages            []gptFileMessage  `json:"messages"`
}

type gptFileMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type gptContentBlock struct {
	Type string         `json:"type"`
	Text string         `json:"text,omitempty"`
	File *gptFileSource `json:"file,omitempty"`
}

type gptFileSource struct {
	FileData string `json:"file_data"`
	FileName string `json:"filename,omitempty"`
}

type gptStreamChunk struct {
	Choices []gptChoice `json:"choices"`
	Usage   *gptUsage   `json:"usage,omitempty"`
}

type gptChoice struct {
	Delta        gptDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

type gptDelta struct {
	Content          string             `json:"content"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	Reasoning        string             `json:"reasoning,omitempty"`
	ToolCalls        []gptToolCallDelta `json:"tool_calls,omitempty"`
}

// --- GPT Tool Use 内部类型 ---

// gptToolDef GPT 格式的工具定义
type gptToolDef struct {
	Type     string      `json:"type"`
	Function gptFunction `json:"function"`
}

type gptFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// gptToolCallDelta 流式中的 tool_calls 增量
type gptToolCallDelta struct {
	Index    int           `json:"index"`
	ID       string        `json:"id,omitempty"`
	Type     string        `json:"type,omitempty"`
	Function *gptFuncDelta `json:"function,omitempty"`
}

type gptFuncDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// gptToolRequest 携带工具定义的 GPT 请求体
type gptToolRequest struct {
	Model               string            `json:"model"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	Stream              bool              `json:"stream"`
	StreamOptions       *gptStreamOptions `json:"stream_options,omitempty"`
	Tools               []gptToolDef      `json:"tools,omitempty"`
	ToolChoice          ToolChoice        `json:"tool_choice,omitempty"`
	Messages            []gptAnyMsg       `json:"messages"`
}

// gptAnyMsg content 统一发送字符串；纯工具调用的 assistant 消息使用空字符串兼容必填校验。
type gptAnyMsg struct {
	Role             string           `json:"role"`
	Content          interface{}      `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []gptToolCallMsg `json:"tool_calls,omitempty"`   // assistant 消息
	ToolCallID       string           `json:"tool_call_id,omitempty"` // tool 角色消息
}

type gptToolCallMsg struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function gptFuncCall `json:"function"`
}

type gptFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toGPTTools(tools []ToolDefinition) []gptToolDef {
	out := make([]gptToolDef, len(tools))
	for i, t := range tools {
		out[i] = gptToolDef{
			Type: "function",
			Function: gptFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		}
	}
	return out
}

type gptUsage struct {
	CompletionTokens    int                    `json:"completion_tokens"`
	PromptTokens        int                    `json:"prompt_tokens"`
	TotalTokens         int                    `json:"total_tokens"`
	PromptTokensDetails gptPromptTokensDetails `json:"prompt_tokens_details"`
}

type gptPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func (g *GPTClient) resolveMaxCompletionTokens(model string) int {
	maxTokens := g.config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	limit := conf.GetModelVersionLimit(model)
	if limit != nil && limit.MaxCompletionTokens > 0 && maxTokens > limit.MaxCompletionTokens {
		return limit.MaxCompletionTokens
	}

	return maxTokens
}

func (g *GPTClient) ChatStream(ctx context.Context, messages []LLMMessage, model string) (<-chan StreamChunk, error) {
	if model == "" {
		model = g.defaultModel
	}

	maxTokens := g.resolveMaxCompletionTokens(model)

	reqBody := gptRequest{
		Model:               model,
		Messages:            messages,
		MaxCompletionTokens: maxTokens,
		Stream:              true,
		StreamOptions:       &gptStreamOptions{IncludeUsage: true},
	}
	if reasoning := reasoningOptionsFromContext(ctx); reasoning.Enabled {
		reqBody.ReasoningEffort = ReasoningEffortForGPT(reasoning)
	}

	bodyBytes, err := marshalRequestBodyGuard(ctx, reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	apiURL := g.config.ApiUrl + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	applyTraceHeadersFromContext(ctx, req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newAPIError("gpt", resp.StatusCode, ParseRetryAfter(resp.Header.Get("Retry-After")), string(body))
	}

	return g.streamChatCompletionResponse(ctx, resp.Body), nil
}

// ChatStreamWithFilePayloads 文件入模专用，不影响通用 ChatStream。
func (g *GPTClient) ChatStreamWithFilePayloads(
	ctx context.Context,
	messages []LLMMessage,
	model string,
	files []FilePayload,
) (<-chan StreamChunk, error) {
	if len(files) == 0 {
		return g.ChatStream(ctx, messages, model)
	}
	if model == "" {
		model = g.defaultModel
	}

	maxTokens := g.resolveMaxCompletionTokens(model)

	apiMessages, err := buildGPTFileMessages(messages, files)
	if err != nil {
		return nil, err
	}
	reqBody := gptFileRequest{
		Model:               model,
		Messages:            apiMessages,
		MaxCompletionTokens: maxTokens,
		Stream:              true,
		StreamOptions:       &gptStreamOptions{IncludeUsage: true},
	}

	bodyBytes, err := marshalRequestBodyGuard(ctx, reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	apiURL := g.config.ApiUrl + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	applyTraceHeadersFromContext(ctx, req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newAPIError("gpt", resp.StatusCode, ParseRetryAfter(resp.Header.Get("Retry-After")), string(body))
	}

	return g.streamChatCompletionResponse(ctx, resp.Body), nil
}

func (g *GPTClient) streamChatCompletionResponse(ctx context.Context, body io.ReadCloser) <-chan StreamChunk {
	ch := make(chan StreamChunk, 64)
	go func() {
		defer close(ch)
		defer body.Close()

		var (
			inputTokens       int
			outputTokens      int
			cacheReadTokens   int
			cacheCreateTokens int
			receivedDone      bool
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
					provider:          "GPT",
					terminationReason: terminationReason,
					receivedDone:      receivedDone,
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
					ReceivedDone:      receivedDone,
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
			if data == "[DONE]" {
				receivedDone = true
				lastChunkAt = time.Now()
				logStreamFinal(ctx, streamFinalState{
					provider:          "GPT",
					terminationReason: terminationReason,
					receivedDone:      receivedDone,
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
					ReceivedDone:      true,
					FinishReason:      finishReason,
				}
				return
			}

			chunkCount++
			lastChunkAt = time.Now()

			var chunk gptStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				logStreamParseError(ctx, "GPT", chunkCount, data, err)
				continue
			}

			if chunk.Usage != nil {
				inputTokens = chunk.Usage.PromptTokens
				outputTokens = chunk.Usage.CompletionTokens
				cacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}

			for _, choice := range chunk.Choices {
				if choice.FinishReason != nil {
					finishReason = strings.TrimSpace(*choice.FinishReason)
				}
				if reasoning := firstNonEmpty(choice.Delta.ReasoningContent, choice.Delta.Reasoning); reasoning != "" {
					ch <- StreamChunk{ReasoningContent: reasoning}
				}
				if choice.Delta.Content != "" {
					ch <- StreamChunk{Content: choice.Delta.Content}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			terminationReason = StreamTerminationUpstreamReadError
			logStreamFinal(ctx, streamFinalState{
				provider:          "GPT",
				terminationReason: terminationReason,
				receivedDone:      receivedDone,
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
		if !receivedDone {
			switch {
			case finishReason == "":
				logReason = StreamTerminationUpstreamEOFWithoutDone
			case isAcceptedFinishReason(finishReason):
				logReason = StreamTerminationCompleted
			default:
				logReason = finishReasonTerminationReason(finishReason)
			}
		}
		logStreamFinal(ctx, streamFinalState{
			provider:          "GPT",
			terminationReason: logReason,
			receivedDone:      receivedDone,
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
			ReceivedDone:      receivedDone,
		}
	}()

	return ch
}

func buildGPTFileMessages(messages []LLMMessage, files []FilePayload) ([]gptFileMessage, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages is empty")
	}
	targetIdx := findLatestUserMessageIndex(messages)
	if targetIdx < 0 {
		targetIdx = len(messages) - 1
	}

	out := make([]gptFileMessage, 0, len(messages))
	for idx, msg := range messages {
		if idx != targetIdx {
			out = append(out, gptFileMessage{Role: msg.Role, Content: msg.Content})
			continue
		}

		blocks := make([]gptContentBlock, 0, len(files)+1)
		if strings.TrimSpace(msg.Content) != "" {
			blocks = append(blocks, gptContentBlock{Type: "text", Text: msg.Content})
		}
		for _, f := range files {
			if strings.EqualFold(strings.TrimSpace(f.ContentAs), "text") || strings.EqualFold(strings.TrimSpace(f.Encoding), "text") {
				if strings.TrimSpace(f.Text) == "" {
					return nil, fmt.Errorf("gpt text file content is empty, file=%s", f.FileName)
				}
				blocks = append(blocks, gptContentBlock{
					Type: "text",
					Text: buildGPTTextFileBlock(f.FileName, f.Text),
				})
				continue
			}
			if strings.ToLower(strings.TrimSpace(f.Encoding)) != "base64" {
				return nil, fmt.Errorf("gpt file encoding only supports base64 for non-text file, file=%s", f.FileName)
			}
			if strings.TrimSpace(f.Data) == "" {
				return nil, fmt.Errorf("gpt file_data is empty, file=%s", f.FileName)
			}
			if mt := strings.ToLower(strings.TrimSpace(f.MediaType)); mt != "" && mt != "application/pdf" {
				return nil, fmt.Errorf("gpt non-text file currently only supports pdf, file=%s", f.FileName)
			}
			pdfDataURL, err := buildGPTPDFDataURL(f.Data)
			if err != nil {
				return nil, fmt.Errorf("gpt pdf file_data invalid, file=%s: %w", f.FileName, err)
			}
			blocks = append(blocks, gptContentBlock{
				Type: "file",
				File: &gptFileSource{
					FileData: pdfDataURL,
					FileName: strings.TrimSpace(f.FileName),
				},
			})
		}
		out = append(out, gptFileMessage{
			Role:    msg.Role,
			Content: blocks,
		})
	}
	return out, nil
}

func findLatestUserMessageIndex(messages []LLMMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "user") {
			return i
		}
	}
	return -1
}

func buildGPTPDFDataURL(base64Data string) (string, error) {
	data := strings.TrimSpace(base64Data)
	if strings.HasPrefix(data, "data:") {
		lower := strings.ToLower(data)
		if !strings.HasPrefix(lower, "data:application/pdf;") {
			return "", fmt.Errorf("data url mime must be application/pdf")
		}
		return data, nil
	}
	return fmt.Sprintf("data:application/pdf;base64,%s", data), nil
}

func buildGPTTextFileBlock(fileName, text string) string {
	name := strings.TrimSpace(fileName)
	content := strings.TrimSpace(text)
	if content == "" {
		return ""
	}
	if name == "" {
		return content
	}
	return fmt.Sprintf("【文件 %s 内容】\n%s", name, content)
}

// ChatStreamWithTools 支持 tool_use 的 GPT 流式对话
func (g *GPTClient) ChatStreamWithTools(
	ctx context.Context,
	messages []ChatMessage,
	model string,
	tools []ToolDefinition,
) (<-chan StreamChunk, error) {
	if model == "" {
		model = g.defaultModel
	}
	maxTokens := g.resolveMaxCompletionTokens(model)

	apiMessages := buildGPTToolMessages(messages)

	reqBody := gptToolRequest{
		Model:               model,
		Messages:            apiMessages,
		MaxCompletionTokens: maxTokens,
		Stream:              true,
		StreamOptions:       &gptStreamOptions{IncludeUsage: true},
		Tools:               toGPTTools(tools),
		ToolChoice:          toolChoiceFromContext(ctx),
	}
	if reasoning := reasoningOptionsFromContext(ctx); reasoning.Enabled {
		reqBody.ReasoningEffort = ReasoningEffortForGPT(reasoning)
	}

	bodyBytes, err := marshalRequestBodyGuard(ctx, reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	logLLMPrefixDebug(ctx, "GPT", bodyBytes)

	apiURL := g.config.ApiUrl + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	applyTraceHeadersFromContext(ctx, req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newAPIError("gpt", resp.StatusCode, ParseRetryAfter(resp.Header.Get("Retry-After")), string(body))
	}

	ch := make(chan StreamChunk, 64)
	go g.parseGPTToolStream(ctx, resp, ch)
	return ch, nil
}

// parseGPTToolStream 解析 GPT 带 tool_calls 的流式响应
func (g *GPTClient) parseGPTToolStream(ctx context.Context, resp *http.Response, ch chan<- StreamChunk) {
	defer close(ch)
	defer resp.Body.Close()

	var (
		inputTokens, outputTokens int
		cacheReadTokens           int
		cacheCreateTokens         int
		finishReason              string
		// 按 index 跟踪正在构建的 tool calls
		toolCallBuilders = make(map[int]*gptToolCallBuilder)
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
		if data == "[DONE]" {
			break
		}

		var chunk gptStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
			cacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}

		for _, choice := range chunk.Choices {
			if reasoning := firstNonEmpty(choice.Delta.ReasoningContent, choice.Delta.Reasoning); reasoning != "" {
				ch <- StreamChunk{ReasoningContent: reasoning}
			}
			if choice.Delta.Content != "" {
				ch <- StreamChunk{Content: choice.Delta.Content}
			}

			for _, tc := range choice.Delta.ToolCalls {
				b, ok := toolCallBuilders[tc.Index]
				if !ok {
					b = &gptToolCallBuilder{}
					toolCallBuilders[tc.Index] = b
				}
				if tc.ID != "" {
					b.id = tc.ID
				}
				if tc.Function != nil {
					if tc.Function.Name != "" {
						b.name = tc.Function.Name
					}
					b.argsBuf.WriteString(tc.Function.Arguments)
				}
			}

			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamChunk{Error: fmt.Errorf("read stream: %w", err), Done: true}
		return
	}

	// 构建最终 StreamChunk
	var stopReason string
	var toolCalls []ToolCall

	if hasGPTToolCallBuilders(toolCallBuilders) || isToolCallFinishReason(finishReason) {
		stopReason = "tool_use" // 统一为 tool_use
		for i := 0; i < len(toolCallBuilders); i++ {
			if b, ok := toolCallBuilders[i]; ok {
				toolCalls = append(toolCalls, ToolCall{
					ID:    b.id,
					Name:  b.name,
					Input: canonicalToolInput(json.RawMessage(b.argsBuf.String())),
				})
			}
		}
	} else if normalizeFinishReason(finishReason) == "length" {
		// finish_reason=length 透传为 max_tokens：引擎据此自动续写，而不是把截断回答当最终答案。
		stopReason = "max_tokens"
	} else {
		stopReason = "end_turn" // 统一为 end_turn
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

type gptToolCallBuilder struct {
	id      string
	name    string
	argsBuf strings.Builder
}

func hasGPTToolCallBuilders(builders map[int]*gptToolCallBuilder) bool {
	for _, b := range builders {
		if b != nil && (strings.TrimSpace(b.id) != "" || strings.TrimSpace(b.name) != "" || b.argsBuf.Len() > 0) {
			return true
		}
	}
	return false
}

// buildGPTToolMessages 将统一 ChatMessage 转换为 GPT 格式消息
func buildGPTToolMessages(messages []ChatMessage) []gptAnyMsg {
	out := make([]gptAnyMsg, 0, len(messages))

	for _, msg := range messages {
		if len(msg.Parts) == 0 {
			out = append(out, gptAnyMsg{Role: msg.Role, Content: msg.Content})
			continue
		}

		switch msg.Role {
		case "assistant":
			am := gptAnyMsg{Role: "assistant", Content: ""}
			var textParts []string
			var reasoningParts []string
			for _, p := range msg.Parts {
				switch p.Type {
				case "thinking":
					if strings.TrimSpace(p.Thinking) != "" {
						reasoningParts = append(reasoningParts, p.Thinking)
					}
				case "text":
					textParts = append(textParts, p.Text)
				case "tool_use":
					am.ToolCalls = append(am.ToolCalls, gptToolCallMsg{
						ID:   p.ID,
						Type: "function",
						Function: gptFuncCall{
							Name:      p.Name,
							Arguments: string(canonicalToolInput(p.Input)),
						},
					})
				}
			}
			if len(reasoningParts) > 0 {
				am.ReasoningContent = strings.Join(reasoningParts, "")
			}
			if len(textParts) > 0 {
				am.Content = strings.Join(textParts, "")
			}
			out = append(out, am)

		case "user":
			for _, p := range msg.Parts {
				if p.Type == "tool_result" {
					out = append(out, gptAnyMsg{
						Role:       "tool",
						Content:    p.Content,
						ToolCallID: p.ToolUseID,
					})
				}
			}

		default:
			out = append(out, gptAnyMsg{Role: msg.Role, Content: msg.Content})
		}
	}

	return out
}
