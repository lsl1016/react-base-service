package react

import (
	"context"
	"sync/atomic"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// runtimeRequest 是“进入执行循环前已经解析完成”的 Run 快照。
//
// 它与 reactEngineState 的区别：
//   - runtimeRequest 描述本次 Run 要以什么身份、模型、能力和历史开始执行；
//   - reactEngineState 描述 executeReactLoop 正在变化的运行态。
//
// 子结构采用匿名嵌入，既明确状态归属，又保持 req.userName / req.agentPath 等已有访问方式不变。
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
	// callerAllowPlan 是 caller 级 allow_plan 覆盖快照（tblLlmCaller.allow_plan 三态）：
	// nil=未覆盖（跟随全局配置），false=caller 级强制关，true=caller 级强制开。
	// 在 prepareRuntimeRequest 解析一次，执行档案与 Plan 入口闸门共用同一口径。
	callerAllowPlan *bool
}

// runtimeRequestModel 是模型路由与凭证快照。
type runtimeRequestModel struct {
	apiKey               string
	resolvedModelKey     string
	resolvedModelVersion string
	// reasoning 是本次 run 的思考程度三态快照（off/auto/custom），随每轮模型调用注入。
	reasoning llm.ReasoningOptions
	// 以下三项来自用户模型（modelHash 路径）的自带配置；0 值=回退全局端点/模型目录。
	userModelApiURL          string
	userModelMaxOutputTokens int
	userModelContextTokens   int
	// connProtocol 是 LLM 连接（tblLlmConnection）的协议（openai/anthropic）；
	// 非空表示本 run 的凭证+端点来自连接：主模型 client 按协议构建、端点用
	// userModelApiURL 携带的连接 base url。空=走旧推导链。
	connProtocol string
	// caps* 是用户模型（ModelHash 路径）的能力声明；capsKnown=false（无 ModelHash
	// 或平台模型）时运行时不门禁，按目录/默认处理。声明关闭 tools 时主模型轮
	// 不下发工具 schema；关闭 thinking 时不下发思考参数；关闭 vision 时拒绝图片载荷。
	capsKnown    bool
	capsTools    bool
	capsVision   bool
	capsThinking bool
}

// runtimeRequestCapabilities 是 Run 初始化阶段解析出的能力快照。
type runtimeRequestCapabilities struct {
	systemPrompt            string
	skillsIndexSnapshotJSON string
	toolsIndexSnapshotJSON  string
	memoryContext           string
	graphMemoryContext      string
	agents                  []model.Agent
	// visibleTools 是 run 装配时的全量可见业务工具（WP2）：agent 工具白名单的 @readonly
	// token 需要按工具 config 判定只读，快照索引里没有 config，故随 request 传递。
	visibleTools []model.Tool
}

// runtimeRequestExecution 是多 Agent / HITL 等执行期策略。
type runtimeRequestExecution struct {
	agentPath           string
	depth               int
	clientHub           *clientMessageHub
	agentPermissionMode string
	tokenBudget         int
	// enforceReadOnlyTools 是只读执行域开关（WP2 只读子代理）：开启后本 run 内一切
	// 非 readOnly 声明的业务工具（含 client 工具）被硬拦截。单调收紧：子 run 取
	// 「父 run 开关 || 自身 agent 定义」，任何嵌套层级都不能放宽。
	enforceReadOnlyTools bool
	// agentToolRefs 是子代理业务工具白名单的原始 refs（含 @none/@readonly 约定 token）：
	// get_tool/自愈激活路径的硬检查依据（索引快照过滤只控制模型"看到什么"，这里控制"能加载什么"）。
	// nil = 不限制（外层 run / 白名单为空=继承全部的存量语义）。
	agentToolRefs []string
	// promotePendingInputID 是 Steering S2 自动续跑时待晋升的排队输入账本 ID（0=无）；
	// 在 createReactRunContext 事务内与 run 创建、用户消息落库一起原子置 guided。
	promotePendingInputID uint
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

// reactEngineState 是单个 executeReactLoop 的可变运行态，生命周期与当前 ReactRun 一致。
//
// 顶层只保留执行上下文、Run 标识、事件通道和跨能力依赖；模型、消息、异步任务和 token 计量
// 分别收口到独立子状态。它不负责 DB 模型定义，也不持有跨 Session 的全局状态。
// 子 Agent 会创建自己的 reactEngineState，因此父子 Run 的模型上下文和 activeTools 天然隔离。
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
	// commands 是本 run 的运行时命令箱（Q1 通知邮箱）：后台委派完成通知经完成监视
	// goroutine 投递进来，引擎在模型步边界吸收（见 command_box.go）。每个 run 独立一箱。
	commands *runtimeCommandBox

	// roundInterruptedResults 登记本轮被中断路径合成、待引擎收口统一落库的 tool_result
	//（toolUseID -> 结果）。Tool Runtime 闭合治理（Phase 1）：中断回填的唯一持久化点
	// 是引擎层的轮次结果落库，这里只登记不落库，保证每个 tool_use 恰好一条 tool_result。
	roundInterruptedResults map[string]llm.ToolResultContent

	agentPath           string
	depth               int
	agentPermissionMode string

	reactEngineModelState
	reactEngineConversationState
	reactEngineAsyncState
	reactEngineUsageState
	reactEngineBoundaryState
}

// reactEngineBoundaryState 是终止边界治理（软着陆/预算/异常守卫/输出续写/compact 熔断）的运行态。
// 生命周期与当前 run 一致；字段全部是进程内存态，不持久化。
type reactEngineBoundaryState struct {
	// runTimeout/runDeadline 是 run 级 wall-clock 边界（0 值=不限）。
	runTimeout  time.Duration
	runDeadline time.Time

	// 软着陆收尾：激活后 contextMessages 注入收尾提醒、工具执行限流到只读集合。
	softLandingActive bool
	softLandingReason string

	// 重复工具调用软守卫：同签名连续 streak 达到阈值后注入收束提醒（只提醒不阻断）。
	anomalyLastSignature string
	anomalyStreak        int
	anomalyLastTool      string
	anomalyInjections    int
	// anomalyReminderRendered 标记本轮渲染过提醒（幂等标记），轮末由 consumeAnomalyRender 结算。
	anomalyReminderRendered bool

	// 输出截断续写：pendingContinuation 待消费的续写标记；continuationCount 已续写次数。
	pendingContinuation bool
	continuationCount   int

	// lastEphemeralTail 是 contextMessages 尾部临时消息数，供 prompt 缓存锚点跳过。
	lastEphemeralTail int

	// lastAssistantContent 记录最近一轮 assistant 正文，软着陆耗尽收尾时用作最终回答兜底。
	lastAssistantContent string

	// compactLLMFailures 是 LLM 压缩连续失败计数（达到熔断阈值后本轮 run 直接走本地摘要）。
	compactLLMFailures int
	// compactStepHistory 记录最近发生压缩的 step 序号，用于 rapid-refill 防抖判定。
	compactStepHistory []int
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
	// lastContextBreakdownJSON 最近一次模型调用前的上下文分类估算（JSON），
	// 随 last_* 一并写回 ReactRun，供上下文容量看板按构成展示。
	lastContextBreakdownJSON string

	// delegatedInput/OutputTokens 是委派子 run 消耗的内存镜像，并行委派并发累加用原子操作。
	delegatedInputTokens  atomic.Int64
	delegatedOutputTokens atomic.Int64
}

// newReactEngineState 集中构造 ReAct 引擎状态。
// 新增运行字段时优先在这里统一初始化，避免 executeReactLoop 逐渐膨胀成大型 struct literal；
// 对需要从上一 Run 恢复的状态，只保存“可恢复引用/快照”，真正恢复动作仍由对应模块执行。
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
		commands:            newRuntimeCommandBox(),
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
			loadedSkillID:          make(map[string]bool),
			todoStateJSON:          req.todoStateJSON,
		},
	}
}
