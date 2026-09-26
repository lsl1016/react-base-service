package react

// delegate_agent：子 Agent 委派（服务端 AI 工作台改造方案 P1 核心）。
//
// 设计要点（与方案 4.1 节逐条对应）：
//   - 子 Agent 是注册类资源（tblLlmAgent），对主 Agent 呈现为一个 Meta Tool：
//     描述动态渲染 caller 可见的 agent 清单，主 LLM 由此"发现"子代理；
//   - 执行是复用现有引擎的隔离子 run：同 session 落库（parent_run_id/agent_path），
//     上下文 = agent.system_prompt + 子代理工具/Skill 索引 + 单条 user(task)，
//     不含父历史、不含父已加载工具、不注入长期记忆（隔离的三大来源）；
//   - 子 run 事件实时冒泡进父 WS 事件流（带 agentPath），但消息不进父的 LLM 历史
//     （GetOuterReactMessagesBySessionIDWithDB 已按外层 run 过滤）；
//   - 结束取子 run 最后一条 assistant 消息回填父循环（OH get_agent_final_response 等价物）；
//   - 嵌套 HITL：子 run 的 ask_question/client tool 走共享 readClient，父循环阻塞在
//     delegate 工具执行中，上行的 tool_use_answer 只会被子 run 读到，协议无需改动；
//   - 取消传播：子 runCtx 从父 runCtx 派生，父取消/断线级联取消子 run（复用 Cancel 传播）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/helpers"
	agentService "react-base-service/service/agent"
	model "react-base-service/models/llm"
	"react-base-service/service/workspace"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"react-base-service/golib/zlog"
)

// rootAgentPath 是多 Agent 事件树的逻辑根路径。
// 外层 Run 的事件默认省略 agentPath，前端按 main 渲染；真正委派后才出现 main/<agent>/...。
const rootAgentPath = "main"

// delegateAgentPath 计算子 run 的 agentPath：父路径为空（外层 run）时以 main 为根，
// 形如 main/ops-agent；嵌套委派继续拼接 main/ops-agent/dba-agent。
func delegateAgentPath(parentAgentPath, agentKey string) string {
	parentAgentPath = strings.TrimSpace(parentAgentPath)
	if parentAgentPath == "" {
		return rootAgentPath + "/" + agentKey
	}
	return parentAgentPath + "/" + agentKey
}

type delegateAgentInput struct {
	AgentKey string `json:"agent_key"`
	Task     string `json:"task"`
	Expect   string `json:"expect"`
	// Background=true 时异步启动（async_launched 等价物，A1）：立即返回启动回执，
	// 子 run 完成后经运行时通知邮箱（Q1 命令箱）回灌父 run。
	Background bool `json:"background"`
}

// delegateAgentToolDefinition 构造 delegate_agent 工具声明，描述动态渲染可用子 Agent 清单。
// 子代理数量少、schema 固定，不走 get_tool/execute_tool 两段式，直接暴露。
func delegateAgentToolDefinition(agents []model.Agent) llm.ToolDefinition {
	agentKeys := make([]string, 0, len(agents))
	var sb strings.Builder
	sb.WriteString("把一个自包含的子任务委派给专家子 Agent 隔离执行，默认阻塞等待其结论后返回；")
	sb.WriteString("background=true 时立即返回（后台执行），完成通知自动回灌当前运行，适合耗时较长、无需阻塞当前对话的独立子任务。")
	sb.WriteString("子 Agent 看不到当前对话历史，task 必须包含完成子任务所需的全部背景、已知信息与期望产出；")
	sb.WriteString("需要多个子 Agent 配合时分别委派，不要把多个目标塞进一次调用。\n可用子 Agent：\n")
	for _, agent := range agents {
		agentKeys = append(agentKeys, agent.AgentKey)
		sb.WriteString(fmt.Sprintf("- agent_key: %s\n  name: %s\n  description: %s\n", agent.AgentKey, agent.Name, strings.TrimSpace(agent.Description)))
		policy := agentService.DefaultRuntime().Policy(agent)
		if len(policy.ToolRefs) > 0 {
			sb.WriteString(fmt.Sprintf("  tools: %s\n", strings.Join(policy.ToolRefs, ", ")))
		}
	}
	return llm.ToolDefinition{
		Name:        metaToolDelegateAgent,
		Description: strings.TrimSpace(sb.String()),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次委派目的，名词短语，用于前端展示。"),
				"agent_key": map[string]interface{}{
					"type":        "string",
					"enum":        agentKeys,
					"description": "目标子 Agent 的 agent_key。",
				},
				"task":   stringSchema("完整自包含的任务描述。子 Agent 看不到当前对话，必须写明任务目标、全部已知信息（ID、时间范围、报错原文等）与需要产出什么。"),
				"expect": stringSchema("期望返回什么（可选），例如：根因结论 + 关键证据。"),
				"background": map[string]interface{}{
					"type":        "boolean",
					"description": "true=后台委派：立即返回，子 Agent 完成后以系统通知自动回灌当前运行（适合耗时长、无需阻塞对话的子任务）；缺省 false=阻塞等待结论。",
				},
			},
			"required":             []string{"description", "agent_key", "task"},
			"additionalProperties": false,
		},
	}
}

// executeDelegateAgent 执行 delegate_agent：解析目标 agent → 组装隔离子 run → 复用引擎执行 →
// 取子 run 最终回复作为工具结果回填父循环。返回值遵循 executeInternalToolContent 约定：
// err 仅在中断（取消/断连）时非 nil 并向上传播，其余失败以 isError 结果回给模型自行调整。
// executeDelegateAgent 执行一次子 Agent 委派。
//
// 子 Agent 不是另一套执行引擎，而是“Agent 配置 + 独立 ReactRun + 同一个 executeReactLoop()”。
// 子 Run 拥有自己的模型上下文、Tool/Skill 白名单、token budget、agentPath 和持久化记录，
// 但继承父 Run 的 runtimeServices 与 clientHub。因此父子上下文隔离，同时仍能共享 Tool Runtime、
// Memory Runtime、Client Tool/HITL 通道和取消传播机制。
func (s *reactEngineState) executeDelegateAgent(call llm.ToolCall, step int) (string, bool, error) {
	var input delegateAgentInput
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return "delegate_agent input must be a valid JSON object", true, nil
	}
	input.AgentKey = strings.TrimSpace(input.AgentKey)
	input.Task = strings.TrimSpace(input.Task)
	input.Expect = strings.TrimSpace(input.Expect)
	if input.AgentKey == "" || input.Task == "" {
		return "delegate_agent requires agent_key and task", true, nil
	}

	cfg := conf.GetReactRuntimeConfig().SubAgent
	if s.depth+1 > cfg.MaxDepth {
		metrics.ReactDelegationsTotal.WithLabelValues(input.AgentKey, "depth_limited").Inc()
		return fmt.Sprintf("delegation depth limit reached (max_depth=%d), handle this task yourself instead of delegating", cfg.MaxDepth), true, nil
	}
	// 委派时实时解析 agent 定义：管理面板修改/新建的 agent 从下一次委派起立即生效
	//（delegate_agent 工具描述清单仍是 run 装配快照，随下一个外层 run 刷新）。
	agent, ok := s.resolveAgentForKey(input.AgentKey)
	if !ok {
		metrics.ReactDelegationsTotal.WithLabelValues(input.AgentKey, "agent_not_found").Inc()
		return fmt.Sprintf("agent %s is not available, available agents: %s", input.AgentKey, strings.Join(s.visibleAgentKeys(), ", ")), true, nil
	}

	// A1 后台委派：注册隔离子 run 后立即返回启动回执，完成通知经 Q1 命令箱回灌。
	if input.Background {
		return s.launchBackgroundDelegate(agent, input)
	}

	subReq, subRunID, err := s.buildSubAgentRuntimeRequest(agent, input.Task, input.Expect)
	if err != nil {
		return "", true, err
	}

	subCtx, cancel := context.WithCancelCause(s.runCtx)
	registerReactRunCancel(subRunID, cancel)
	registerReactRunSoftLanding(subRunID)
	watchdog := startSubAgentWatchdog(cancel)
	metrics.RunsActive.Inc()
	defer func() {
		if watchdog != nil {
			watchdog.Stop()
		}
		cancel(nil)
		unregisterReactRunCancel(subRunID)
		unregisterReactRunSoftLanding(subRunID)
		metrics.RunsActive.Dec()
		// 子 run 的工作区随子 run 终态释放（幂等；未分配为空操作）。
		workspace.Default().ReleaseRun(s.ctx, subRunID)
	}()

	subEmitter := &runEventEmitter{runID: subRunID, sessionID: s.sessionID, agentPath: subReq.agentPath, write: s.emitter.write}
	zlog.Infof(s.ctx, "[React.Delegate] 子Agent run启动: parentRun=%s, subRun=%s, agentKey=%s, agentPath=%s", s.runID, subRunID, agent.AgentKey, subReq.agentPath)

	loopErr := executeReactLoop(s.ctx, subCtx, subReq, subRunID, s.sessionID, subEmitter, s.readClient)
	// 委派计量（OH delegate:{id} 口径的等价物）：子 run 终态后把其 token 消耗（含其自身委派的
	// 间接消耗，递归口径）原子累加进父 run 的 delegated 列，父级报表/预算口径由此闭环。
	s.accumulateDelegatedTokens(s.ctx, subRunID, agent.AgentKey)
	if loopErr != nil {
		s.finalizeSubAgentRunError(s.ctx, subRunID, loopErr)
		if IsReactRunCancelled(loopErr) || IsReactClientDisconnected(loopErr) || errors.Is(loopErr, context.DeadlineExceeded) {
			metrics.ReactDelegationsTotal.WithLabelValues(agent.AgentKey, "cancelled").Inc()
			return "", false, loopErr
		}
		// 子 run 失败（模型故障、超步数上限等）：软错误回给父模型，可自行调整后重试或换路。
		metrics.ReactDelegationsTotal.WithLabelValues(agent.AgentKey, "error").Inc()
		return fmt.Sprintf("子 Agent %s 执行失败: %s", agent.AgentKey, loopErr.Error()), true, nil
	}

	finalResponse, err := subAgentFinalResponse(s.ctx, subRunID)
	if err != nil {
		metrics.ReactDelegationsTotal.WithLabelValues(agent.AgentKey, "no_response").Inc()
		return fmt.Sprintf("子 Agent %s 未产生最终回复: %v", agent.AgentKey, err), true, nil
	}
	metrics.ReactDelegationsTotal.WithLabelValues(agent.AgentKey, "success").Inc()
	zlog.Infof(s.ctx, "[React.Delegate] 子Agent run完成: parentRun=%s, subRun=%s, agentKey=%s", s.runID, subRunID, agent.AgentKey)
	resultPayload, _ := json.Marshal(map[string]string{
		"agentKey":      agent.AgentKey,
		"runId":         subRunID,
		"finalResponse": finalResponse,
	})
	return string(resultPayload), false, nil
}

// backgroundDelegateOutcome 是后台子 run 终态的投递输入（完成监视 goroutine 汇总）。
type backgroundDelegateOutcome struct {
	AgentKey  string
	AgentPath string
	SubRunID  string
	LoopErr   error
}

// launchBackgroundDelegate 后台启动一次委派（A1，async_launched 等价物）：
// 组装与同步路径完全一致的隔离子 run（独立 run 行/历史清空/工具过滤/模型继承），
// 注册取消句柄后立即返回启动回执，子 run 在监视 goroutine 中执行到终态。
//
// 生命周期语义（与 ZCode 的有意分歧，方案 §4.3）：子 runCtx 从父 runCtx 派生——
// 父 run 取消/断连级联取消后台子 run，成本可控优先，避免用户取消后仍在烧 token；
// detach 存活语义如需支持后续加开关。
//
// gin ctx 用 headless 快照：后台子 run 可能比父 run/WS 连接活得久，原 gin ctx 归还
// 连接后会被 gin 池回收复用，后台 goroutine 不能再持有；runCtx 的取消级联不受影响。
// 父请求 Cookie 捕获进快照，HTTP 业务工具与同步委派保持同一调用态。
func (s *reactEngineState) launchBackgroundDelegate(agent model.Agent, input delegateAgentInput) (string, bool, error) {
	subReq, subRunID, err := s.buildSubAgentRuntimeRequest(agent, input.Task, input.Expect)
	if err != nil {
		return "", true, err
	}
	detached := newHeadlessGinContext(s.req.userName)
	if cookies := requestCookies(s.ctx); len(cookies) > 0 {
		inner := httptest.NewRequest(http.MethodPost, "/react/background-delegate", nil)
		for name, value := range cookies {
			inner.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		detached.Request = inner
	}

	subCtx, cancel := context.WithCancelCause(s.runCtx)
	registerReactRunCancel(subRunID, cancel)
	registerReactRunSoftLanding(subRunID)
	watchdog := startSubAgentWatchdog(cancel)
	metrics.RunsActive.Inc()
	subEmitter := &runEventEmitter{runID: subRunID, sessionID: s.sessionID, agentPath: subReq.agentPath, write: s.emitter.write}
	zlog.Infof(s.ctx, "[React.Delegate] 子Agent后台启动: parentRun=%s, subRun=%s, agentKey=%s, agentPath=%s", s.runID, subRunID, agent.AgentKey, subReq.agentPath)

	go func() {
		defer func() {
			if watchdog != nil {
				watchdog.Stop()
			}
			cancel(nil)
			unregisterReactRunCancel(subRunID)
			unregisterReactRunSoftLanding(subRunID)
			metrics.RunsActive.Dec()
			// 子 run 的工作区随子 run 终态释放（幂等；未分配为空操作）。
			workspace.Default().ReleaseRun(detached, subRunID)
		}()
		loopErr := executeReactLoop(detached, subCtx, subReq, subRunID, s.sessionID, subEmitter, s.readClient)
		if loopErr != nil {
			s.finalizeSubAgentRunError(detached, subRunID, loopErr)
		}
		s.accumulateDelegatedTokens(detached, subRunID, agent.AgentKey)
		s.deliverBackgroundNotification(detached, backgroundDelegateOutcome{
			AgentKey:  agent.AgentKey,
			AgentPath: subReq.agentPath,
			SubRunID:  subRunID,
			LoopErr:   loopErr,
		})
	}()

	metrics.ReactDelegationsTotal.WithLabelValues(agent.AgentKey, "background").Inc()
	return renderBackgroundDelegateLaunchedText(agent.AgentKey, subRunID), false, nil
}

// deliverBackgroundNotification 把后台子 run 终态封装为完成通知并投递父 run 命令箱（Q1 消费端）：
// 先读子 run 终态产物（最终回复/usage），再在 run 行锁事务内校验父 run 活跃并落账本行
// （kind=notification, status=admitted），提交后入箱。账本行是唯一持久痕迹；
// 父 run 已终态时不落账不入箱（结果留在子 run 记录里，绝不为通知复活 run）。
func (s *reactEngineState) deliverBackgroundNotification(ctx *gin.Context, outcome backgroundDelegateOutcome) {
	status := backgroundNotificationStatus(outcome.LoopErr)
	summary := ""
	if outcome.LoopErr != nil {
		summary = outcome.LoopErr.Error()
		if IsReactClientDisconnected(outcome.LoopErr) {
			summary = "client disconnected"
		}
	} else {
		final, err := subAgentFinalResponse(ctx, outcome.SubRunID)
		if err != nil {
			status = "error"
			summary = fmt.Sprintf("子 Agent 未产生最终回复: %v", err)
		} else {
			summary = final
		}
	}
	metrics.ReactDelegationsTotal.WithLabelValues(outcome.AgentKey, status).Inc()

	inputTokens, outputTokens := 0, 0
	if run, err := model.GetReactRunByRunID(ctx, outcome.SubRunID); err == nil && run != nil {
		inputTokens = run.TotalInputTokens + run.DelegatedInputTokens
		outputTokens = run.TotalOutputTokens + run.DelegatedOutputTokens
	}

	row := &model.ReactPendingInput{
		SessionID: s.sessionID,
		RunID:     s.runID,
		Kind:      model.ReactPendingKindNotification,
		Delivery:  model.ReactPendingDeliveryGuide,
		Status:    model.ReactPendingStatusAdmitted,
		Content:   renderTaskNotificationEnvelope(runtimeNotification{
			SubRunID:     outcome.SubRunID,
			AgentKey:     outcome.AgentKey,
			AgentPath:    outcome.AgentPath,
			Status:       status,
			Summary:      summary,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		}),
	}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 父 run 活跃校验与账本落账同事务（run 行锁）：与父 run 终态收敛
		//（先改 state 再结算）串行化，杜绝"父 run 恰好收尾"窗口产生无人消费的残留行。
		parentRun, err := model.GetReactRunByRunIDForUpdateWithDB(ctx, tx, s.runID)
		if err != nil {
			return err
		}
		if parentRun == nil || !shouldDeliverBackgroundNotification(parentRun.State) {
			return errBackgroundParentInactive
		}
		seq, err := model.GetReactPendingInputMaxSeqBySessionIDWithDB(ctx, tx, s.sessionID)
		if err != nil {
			return err
		}
		row.Seq = seq + 1
		return model.CreateReactPendingInputWithDB(ctx, tx, row)
	})
	if errors.Is(err, errBackgroundParentInactive) {
		zlog.Infof(ctx, "[React.Notify] 父 run 已终态，后台完成通知不回灌(结果见子 run 记录): parentRun=%s, subRun=%s, agent=%s, status=%s", s.runID, outcome.SubRunID, outcome.AgentKey, status)
		return
	}
	if err != nil {
		zlog.Warnf(ctx, "[React.Notify] 后台完成通知落账失败(忽略,结果见子 run 记录): parentRun=%s, subRun=%s, err=%v", s.runID, outcome.SubRunID, err)
		return
	}
	if s.commands != nil {
		s.commands.PushNotification(runtimeNotification{
			PendingInputID: row.ID,
			SubRunID:       outcome.SubRunID,
			AgentKey:       outcome.AgentKey,
			AgentPath:      outcome.AgentPath,
			Status:         status,
			Summary:        summary,
			InputTokens:    inputTokens,
			OutputTokens:   outputTokens,
		})
	}
	zlog.Infof(ctx, "[React.Notify] 后台委派完成通知已投递命令箱: parentRun=%s, subRun=%s, agent=%s, status=%s, pendingInputId=%s", s.runID, outcome.SubRunID, outcome.AgentKey, status, pendingInputID(row.ID))
}

// buildSubAgentRuntimeRequest 组装子 run 的运行请求：复用 prepareRuntimeRequest 完成 caller/apikey/
// 模型解析与全量索引装配，再覆盖为 agent 定义（系统提示词、工具/Skill 白名单、独立历史、继承或指定模型）。
// buildSubAgentRuntimeRequest 从父 Run 派生子 Agent 的运行快照。
//
// 这里刻意不复制父会话历史：主 Agent 必须把完成子任务所需的背景压缩到 task/expect 中，
// 防止父上下文递归膨胀，也避免子 Agent 获得无关信息。Tool/Skill 白名单、permissionMode、
// maxSteps、tokenBudget 等策略统一由 service/agent.RuntimePolicy 解释。
func (s *reactEngineState) buildSubAgentRuntimeRequest(agent model.Agent, task, expect string) (*runtimeRequest, string, error) {
	cfg := conf.GetReactRuntimeConfig().SubAgent

	modelKey := strings.TrimSpace(agent.ModelKey)
	modelVersion := strings.TrimSpace(agent.ModelVersion)
	if modelKey == "" {
		// 空 = 继承父 run 当前模型（含互备后的实际模型，OH 同款默认）。
		modelKey = s.currentModel.ModelKey
		modelVersion = s.currentModel.ModelVersion
	} else {
		modelVersion = llm.ResolveModelVersion(modelKey, modelVersion)
	}
	if strings.TrimSpace(modelVersion) == "" {
		return nil, "", components.ErrorModelNotSupported.Sprintf(modelKey)
	}

	policy := s.services.agentResolver().Policy(agent)
	maxSteps := policy.EffectiveMaxSteps(cfg.DefaultMaxSteps)

	taskContent := task
	if expect != "" {
		taskContent += "\n\n期望返回：" + expect
	}

	payload := params.ReactRunPayload{
		CallerKey:    s.req.payload.CallerKey,
		RouteValues:  s.req.payload.RouteValues,
		Type:         model.ReactSessionTypeChat,
		UserPrompt:   taskContent,
		ModelKey:     modelKey,
		ModelVersion: modelVersion,
		MaxSteps:     maxSteps,
	}
	base, err := prepareRuntimeRequestWithServices(s.ctx, payload, s.sessionID, s.services)
	if err != nil {
		return nil, "", err
	}

	// 隔离覆盖：不继承父历史与已加载工具，系统提示词/工具/Skill 换为 agent 定义，不注入长期记忆。
	base.historyMessages = nil
	base.historyMessageRefs = nil
	base.attachments = nil
	// 思考程度随父 run 继承（off/auto/custom）；agent 定义级的独立覆盖（P1-5）后续经
	// tblLlmAgent 扩展列接入，当前先保证父子 run 思考口径一致。
	base.reasoning = s.req.reasoning
	base.systemPrompt = agent.SystemPrompt
	base.toolsIndexSnapshotJSON = policy.FilterToolIndexSnapshot(base.toolsIndexSnapshotJSON)
	base.skillsIndexSnapshotJSON = policy.FilterSkillIndexSnapshot(base.skillsIndexSnapshotJSON)
	base.memoryContext = ""
	base.graphMemoryContext = ""
	base.modelUserMessage = llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: taskContent}
	base.todoStateJSON = ""
	base.prevActiveToolIDsJSON = ""
	base.prevActiveToolDefsJSON = ""
	base.agents = s.req.agents // 嵌套委派可见同一清单（是否装配 delegate_agent 由 MaxDepth 限制）
	base.agentPath = delegateAgentPath(s.agentPath, agent.AgentKey)
	base.depth = s.depth + 1
	base.clientHub = s.req.clientHub // 子 run 的交互等待经同一上行消息分发器认领
	// agent 级工具确认收紧（P2-3）：inherit/空 = 不收紧，confirm/confirm_risky 作为子 run
	// 内全部服务端工具的权限下限（与工具级取更严者）。
	base.agentPermissionMode = policy.PermissionMode
	// 子代理预算（P3）：agent 定义的递归 token 上限，engine 每轮模型调用后检查，超限终止子 run
	// 并经软错误通道回填父循环（OH max_budget_per_run 的 token 口径版）。
	base.tokenBudget = policy.MaxTokensPerRun

	subRunID := generateRunID()
	base.modelUserMessageRef = reactMessageRef{RunID: subRunID, MessageID: generateMessageID(), Seq: 1}

	// 入口 token 前置检查：system + tools + task 是子 run 的不可压缩部分。
	compactCfg := compactConfigForModel(modelKey)
	profile := executionProfileForRun(base)
	initialTools := runtimeToolDefinitions(base, profile)
	initialSystemContent := buildReactSystemContent(base.systemPrompt, renderToolIndexSummary(base.toolsIndexSnapshotJSON), renderSkillIndexSummary(base.skillsIndexSnapshotJSON), base.memoryContext, base.graphMemoryContext)
	if err := checkEntryInputTokens(initialSystemContent, base.modelUserMessage, initialTools, compactCfg.TokenTrigger); err != nil {
		return nil, "", err
	}

	if err := s.createSubAgentRunRecord(base, subRunID); err != nil {
		return nil, "", err
	}
	return base, subRunID, nil
}

// createSubAgentRunRecord 创建子 run 行并持久化委派任务为首条 user 消息。
// 与 createReactRunContext 的差异：不锁定 session 行（session 已存在且被父 run 持有）、
// 不做并发 run 检查（父 run 必然 active）、不更新会话摘要（外层 run 收敛时统一更新）。
// createSubAgentRunRecord 为子 Agent 创建独立 ReactRun 记录。
// parent_run_id 与 agent_path 是父子执行关系的权威持久化信息；前端多 Agent Lane、取消传播和
// token 归集都依赖这两个字段，不应仅依赖内存调用栈。
func (s *reactEngineState) createSubAgentRunRecord(req *runtimeRequest, subRunID string) error {
	run := &model.ReactRun{
		RunID:                   subRunID,
		SessionID:               s.sessionID,
		UserName:                req.userName,
		CallerKey:               req.payload.CallerKey,
		RouteValues:             req.routeValuesJSON,
		State:                   model.ReactRunStateRunning,
		StepIndex:               0,
		MaxSteps:                normalizeMaxSteps(req.payload.MaxSteps),
		ModelKey:                req.resolvedModelKey,
		ModelVersion:            req.resolvedModelVersion,
		ApiKey:                  req.apiKey,
		SkillsIndexSnapshotJSON: req.skillsIndexSnapshotJSON,
		ToolIndexSnapshotJSON:   req.toolsIndexSnapshotJSON,
		ParentRunID:             s.runID,
		AgentPath:               req.agentPath,
	}
	if err := model.CreateReactRun(s.ctx, run); err != nil {
		return err
	}
	return persistUserInput(s.ctx, helpers.MysqlClientLLM, req, subRunID, s.sessionID)
}

// startSubAgentWatchdog 启动子 run 墙钟看门狗（A2，SubAgent.MaxRunSeconds，0=不限）：
// 到点以 ErrReactRunTimeout 取消子 run——终态置 timeout，完成通知按失败（timeout）回灌。
// 对齐 ZCode 子代理看门狗的简化版：墙钟口径而非不活跃口径；cancel 在子 run 已终态后触发为空操作。
func startSubAgentWatchdog(cancel context.CancelCauseFunc) *time.Timer {
	d := conf.GetReactRuntimeConfig().SubAgent.SubAgentWatchdogDuration()
	if d <= 0 {
		return nil
	}
	return time.AfterFunc(d, func() { cancel(ErrReactRunTimeout) })
}

// finalizeSubAgentRunError 收敛子 run 的失败终态（run() 错误路径的子 run 对应物）。
// ctx 由调用方提供：后台监视 goroutine 必须传脱离 WS 连接的 headless ctx。
// 看门狗超时（A2）以独立的 timeout 终态收敛（对齐外层 run 的超时语义）。
func (s *reactEngineState) finalizeSubAgentRunError(ctx *gin.Context, subRunID string, loopErr error) {
	if IsReactRunCancelled(loopErr) {
		metrics.RunsTotal.WithLabelValues("cancelled").Inc()
		_ = model.UpdateReactRunByRunID(ctx, subRunID, map[string]any{"state": model.ReactRunStateCancelled})
		return
	}
	if IsReactRunTimeout(loopErr) {
		metrics.RunsTotal.WithLabelValues("timeout").Inc()
		_ = model.UpdateReactRunByRunID(ctx, subRunID, map[string]any{
			"state":         model.ReactRunStateTimeout,
			"error_message": fmt.Sprintf("subagent watchdog timeout: max_run_seconds=%d", conf.GetReactRuntimeConfig().SubAgent.MaxRunSeconds),
		})
		return
	}
	metrics.RunsTotal.WithLabelValues("error").Inc()
	errorMessage := loopErr.Error()
	if IsReactClientDisconnected(loopErr) {
		errorMessage = "client disconnected"
	}
	_ = model.UpdateReactRunByRunID(ctx, subRunID, map[string]any{
		"state":         model.ReactRunStateError,
		"error_message": errorMessage,
	})
}

// subAgentFinalResponse 取子 run 最后一条有正文的 assistant 消息作为最终回复。
func subAgentFinalResponse(ctx *gin.Context, subRunID string) (string, error) {
	messages, err := model.GetReactMessagesByRunID(ctx, subRunID)
	if err != nil {
		return "", err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].MessageType != model.ReactMessageTypeAssistant {
			continue
		}
		chatMessage := parseHistoryChatMessage(messages[i].ContentJSON)
		if content := strings.TrimSpace(extractAssistantContent(chatMessage)); content != "" {
			return content, nil
		}
	}
	return "", fmt.Errorf("no assistant message with content")
}

func (s *reactEngineState) findVisibleAgent(agentKey string) (model.Agent, bool) {
	for _, agent := range s.req.agents {
		if agent.AgentKey == agentKey {
			return agent, true
		}
	}
	return model.Agent{}, false
}

// resolveAgentForKey 实时查库解析 agent 定义（caller 作用域 + default 合并语义，与快照同源逻辑）：
// 委派执行取最新配置，管理面板变更从下一次委派起生效。
func (s *reactEngineState) resolveAgentForKey(agentKey string) (model.Agent, bool) {
	agent, err := s.services.agentResolver().Resolve(s.ctx, s.req.payload.CallerKey, s.req.payload.RouteValues, agentKey)
	if err != nil {
		zlog.Warnf(s.ctx, "[React.Delegate] 实时解析 agent 失败(回退 run 快照): runId=%s, agentKey=%s, err=%v", s.runID, agentKey, err)
		return s.findVisibleAgent(agentKey)
	}
	if agent == nil {
		return model.Agent{}, false
	}
	return *agent, true
}

// accumulateDelegatedTokens 把子 run 的 token 消耗（自身 total + 其 delegated 递归口径）
// 原子累加进父 run 行；并行委派下多个子 run 并发累加同一父行，SQL 自增天然安全。
// 同时累加父 state 的内存镜像，供 done 事件的 delegated 口径展示。
// ctx 由调用方提供：后台监视 goroutine 必须传脱离 WS 连接的 headless ctx。
func (s *reactEngineState) accumulateDelegatedTokens(ctx *gin.Context, subRunID, agentKey string) {
	subRun, err := model.GetReactRunByRunID(ctx, subRunID)
	if err != nil || subRun == nil {
		zlog.Warnf(ctx, "[React.Delegate] 读取子 run 计量失败(忽略): parentRun=%s, subRun=%s, err=%v", s.runID, subRunID, err)
		return
	}
	inputTokens := subRun.TotalInputTokens + subRun.DelegatedInputTokens
	outputTokens := subRun.TotalOutputTokens + subRun.DelegatedOutputTokens
	if inputTokens == 0 && outputTokens == 0 {
		return
	}
	if err := model.AccumulateReactRunDelegatedTokens(ctx, s.runID, inputTokens, outputTokens); err != nil {
		zlog.Warnf(ctx, "[React.Delegate] 委派计量累加失败(忽略): parentRun=%s, subRun=%s, err=%v", s.runID, subRunID, err)
	}
	s.delegatedInputTokens.Add(int64(inputTokens))
	s.delegatedOutputTokens.Add(int64(outputTokens))
}

func (s *reactEngineState) visibleAgentKeys() []string {
	keys := make([]string, 0, len(s.req.agents))
	for _, agent := range s.req.agents {
		keys = append(keys, agent.AgentKey)
	}
	return keys
}
