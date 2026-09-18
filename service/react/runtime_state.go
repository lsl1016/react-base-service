package react

import (
	"context"
	"sync/atomic"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// runtimeRequest 只保留入口 payload 与 Runtime 依赖在顶层，其余按职责分组。
// 子结构采用匿名嵌入，既明确状态归属，又保持 req.userName / req.agentPath 等现有读取方式不变。
type runtimeRequest struct {
	payload  params.ReactRunPayload
	services runtimeServices

	runtimeRequestIdentity
	runtimeRequestModel
	runtimeRequestCapabilities
	runtimeRequestExecution
	runtimeRequestConversation
}

// runtimeRequestIdentity 是本次 Run 的调用身份与会话归属。
type runtimeRequestIdentity struct {
	inputSessionID       string
	userName             string
	routeValuesJSON      string
	callerRuntimeContext components.CallerRuntimeContext
}

// runtimeRequestModel 是模型路由与凭证快照。
type runtimeRequestModel struct {
	apiKey               string
	resolvedModelKey     string
	resolvedModelVersion string
}

// runtimeRequestCapabilities 是 Run 初始化阶段解析出的能力快照。
type runtimeRequestCapabilities struct {
	systemPrompt            string
	skillsIndexSnapshotJSON string
	toolsIndexSnapshotJSON  string
	memoryContext           string
	graphMemoryContext      string
	agents                  []model.Agent
}

// runtimeRequestExecution 是多 Agent / HITL 等执行期策略。
type runtimeRequestExecution struct {
	agentPath           string
	depth               int
	clientHub           *clientMessageHub
	agentPermissionMode string
	tokenBudget         int
}

// runtimeRequestConversation 是历史、当前输入和可恢复运行态。
type runtimeRequestConversation struct {
	historyMessages        []llm.ChatMessage
	historyMessageRefs     [][]reactMessageRef
	modelUserMessage       llm.ChatMessage
	modelUserMessageRef    reactMessageRef
	attachments            []reactAttachmentSnapshot
	todoStateJSON          string
	prevActiveToolIDsJSON  string
	prevActiveToolDefsJSON string
}

// reactEngineState 顶层只保留执行上下文、run identity、事件通道和跨能力依赖。
// 模型、消息、异步任务、token 计量分别收口到独立子状态；匿名嵌入保持现有字段访问语义。
type reactEngineState struct {
	ctx       *gin.Context
	runCtx    context.Context
	req       *runtimeRequest
	profile   ExecutionProfile
	runID     string
	sessionID string

	emitter    *runEventEmitter
	readClient ClientMessageReader
	services   runtimeServices
	clientHub  *clientMessageHub

	agentPath           string
	depth               int
	agentPermissionMode string

	reactEngineModelState
	reactEngineConversationState
	reactEngineAsyncState
	reactEngineUsageState
}

type reactEngineModelState struct {
	client         llm.LLMClient
	currentModel   reactModelTarget
	failoverModels []reactModelTarget
}

type reactEngineConversationState struct {
	messages    []llm.ChatMessage
	messageRefs [][]reactMessageRef
	activeTools map[string]model.Tool

	// prevToolDefFingerprint 记录上一个 run 已加载工具的 definition 指纹（callName -> 指纹）。
	prevToolDefFingerprint map[string]string
	loadedSkillID          map[string]bool
	todoStateJSON          string
}

type reactEngineAsyncState struct {
	// memoryWrites 是当前 run 内成功执行的记忆写操作数，用于 reflection 的单次写入限额。
	memoryWrites int
	// pendingAsyncTasks 保存当前 Session 的未完结异步任务，仅在外层 ReAct 中注入模型上下文。
	pendingAsyncTasks        []model.ReactAsyncTask
	pendingAsyncTasksHasMore bool
}

type reactEngineUsageState struct {
	inputTokens              int
	outputTokens             int
	runBaseInputTokens       int
	runBaseOutputTokens      int
	cacheReadTokens          int
	cacheCreateTokens        int
	runBaseCacheReadTokens   int
	runBaseCacheCreateTokens int
	// lastInputTokens 最近一次模型调用真实 input_tokens，用作压缩触发的主锚点。
	lastInputTokens  int
	lastOutputTokens int

	// delegatedInput/OutputTokens 是委派子 run 消耗的内存镜像，并行委派并发累加用原子操作。
	delegatedInputTokens  atomic.Int64
	delegatedOutputTokens atomic.Int64
}

// newReactEngineState 集中构造 ReAct 引擎状态，避免主循环感知各子状态的初始化细节。
func newReactEngineState(
	ctx *gin.Context,
	runCtx context.Context,
	req *runtimeRequest,
	runID, sessionID string,
	client llm.LLMClient,
	currentModel reactModelTarget,
	failoverModels []reactModelTarget,
	emitter *runEventEmitter,
	readClient ClientMessageReader,
	messages []llm.ChatMessage,
) *reactEngineState {
	return &reactEngineState{
		ctx:                 ctx,
		runCtx:              runCtx,
		req:                 req,
		profile:             executionProfileForRun(req),
		runID:               runID,
		sessionID:           sessionID,
		emitter:             emitter,
		readClient:          readClient,
		services:            req.services,
		clientHub:           req.clientHub,
		agentPath:           req.agentPath,
		depth:               req.depth,
		agentPermissionMode: req.agentPermissionMode,
		reactEngineModelState: reactEngineModelState{
			client:         client,
			currentModel:   currentModel,
			failoverModels: failoverModels,
		},
		reactEngineConversationState: reactEngineConversationState{
			messages:               messages,
			messageRefs:            req.historyMessageRefs,
			activeTools:            make(map[string]model.Tool),
			prevToolDefFingerprint: make(map[string]string),
			loadedSkillID:           make(map[string]bool),
			todoStateJSON:           req.todoStateJSON,
		},
	}
}
