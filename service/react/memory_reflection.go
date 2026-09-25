package react

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// 记忆自动整理（reflection）：压缩事件后异步派生受限子 run，审阅被压缩的近期对话并整理记忆。
//
// 设计约束（见 docs/memory.md 第 7 节）：
//   - 触发：compact_end 后判定（开关 + 冷却 + 防自触发），绝不阻塞主会话
//   - 子 run：独立 session（type=reflection），工具集仅 memory 三工具，走统一写核心（source=reflection）
//   - 安全阀：max_writes_per_run 限额、locked 标签条目只读、失败静默重试一次
//   - 可观测：react_memory_reflection_total{status}，事件流照常持久化、回放页可查

const (
	// memoryReflectionMaxSteps 是 reflection run 的最大推理轮次：五阶段 + 分批写操作需要的空间。
	memoryReflectionMaxSteps = 12
	// memoryReflectionMessageCharLimit 是转录中单条消息的截断长度，防止单条超长工具结果吃光预算。
	memoryReflectionMessageCharLimit = 800
)

// reflectionExecutionProfile 是 reflection run 的受限执行档案：只放行记忆工具，其余全部关闭。
func reflectionExecutionProfile() ExecutionProfile {
	return ExecutionProfile{AllowMemory: true}
}

// executionProfileForSessionType 按会话类型选择执行档案。
func executionProfileForSessionType(sessionType string) ExecutionProfile {
	if sessionType == model.ReactSessionTypeReflection {
		return reflectionExecutionProfile()
	}
	return outerExecutionProfile()
}

// executionProfileForRun 按运行请求选择执行档案：delegate_agent 子 run 用受限的外层档案
// （不注入会话级异步任务提醒），其余按会话类型；req 为 nil（单测最小状态）按外层对话处理。
func executionProfileForRun(req *runtimeRequest) ExecutionProfile {
	if req == nil {
		return outerExecutionProfile()
	}
	if strings.HasPrefix(strings.TrimSpace(req.agentPath), "plan/") {
		// Plan Step 已由上层 Plan Runtime 负责计划编排，Scoped ReAct 只执行当前 Step。
		// 禁止再次暴露 create_plan，避免 Step 内递归生成第二套计划。
		profile := subAgentExecutionProfile()
		profile.AllowPlan = false
		return profile
	}
	if req.agentPath != "" {
		return subAgentExecutionProfile()
	}
	return executionProfileForSessionType(req.payload.Type)
}

// internalMetaToolDefinitionsForType 按会话类型裁剪内置工具声明：
// reflection 只暴露记忆三工具（执行侧由 ExecutionProfile 双重拦截）。
func internalMetaToolDefinitionsForType(sessionType string) []llm.ToolDefinition {
	if sessionType == model.ReactSessionTypeReflection {
		if conf.CustomConf.LLM.React.Memory.MemoryEnabled() {
			return memoryToolDefinitions()
		}
		return nil
	}
	return internalMetaToolDefinitions()
}

// scopedCooldownTable 是进程内冷却表：key → 最近一次触发时间。
// 进程重启丢失冷却状态是可接受的（最坏情况重启后多触发一次）。
type scopedCooldownTable struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// acquire 原子判定并占用冷却窗口；冷却未到返回 false。
func (t *scopedCooldownTable) acquire(key string, cooldown time.Duration) bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[key]; ok && now.Sub(last) < cooldown {
		return false
	}
	t.last[key] = now
	// 冷却表懒清理：超阈值时剔除已过期条目，避免长生命周期进程无限增长。
	if len(t.last) > 1024 {
		for id, ts := range t.last {
			if now.Sub(ts) >= cooldown {
				delete(t.last, id)
			}
		}
	}
	return true
}

// memoryReflectionCooldown 是 reflection 的冷却表（sessionID 维度）。
var memoryReflectionCooldown = &scopedCooldownTable{last: make(map[string]time.Time)}

// maybeTriggerMemoryReflection 在 compact_end 成功发出后调用：判定通过则异步派生 reflection run。
// compacted 是本轮被压缩掉的原始消息（内存中直接持有，无需回库取）。
func maybeTriggerMemoryReflection(s *reactEngineState, summary string, compacted []llm.ChatMessage) {
	defer func() {
		// 触发链路自身绝不能影响主 run：panic 只记日志。
		if r := recover(); r != nil {
			zlog.Errorf(s.ctx, "[react.maybeTriggerMemoryReflection] 触发 panic: runId=%s err=%v", s.runID, r)
		}
	}()

	cfg := conf.GetReactRuntimeConfig().Memory
	if !cfg.MemoryEnabled() || !cfg.Reflection.ReflectionEnabled() {
		return
	}
	// 防自触发：reflection 会话自身的压缩不再派生 reflection。
	if s.req.payload.Type == model.ReactSessionTypeReflection {
		return
	}
	if strings.TrimSpace(summary) == "" {
		return
	}
	if !memoryReflectionCooldown.acquire(s.sessionID, time.Duration(cfg.Reflection.CooldownMinutes)*time.Minute) {
		metrics.MemoryReflectionTotal.WithLabelValues("cooldown_skipped").Inc()
		return
	}
	metrics.MemoryReflectionTotal.WithLabelValues("triggered").Inc()

	payload := params.ReactRunPayload{
		CallerKey:    s.req.payload.CallerKey,
		RouteValues:  s.req.payload.RouteValues,
		Type:         model.ReactSessionTypeReflection,
		UserPrompt:   buildMemoryReflectionPrompt(summary, compacted, cfg),
		ModelKey:     s.currentModel.ModelKey,
		ModelVersion: s.currentModel.ModelVersion,
		MaxSteps:     memoryReflectionMaxSteps,
	}
	userName := s.req.userName
	callerKey := s.req.payload.CallerKey

	go func() {
		// 父 context 用 Background：reflection 生命周期独立于触发它的 WS 连接，
		// 优雅停机由 run() 内部的注册/注销机制统一覆盖。
		runMemoryReflection(payload, userName, callerKey)
	}()
}

// runMemoryReflection 执行一次 reflection run；失败静默重试一次，终态（success/error）统一打点。
func runMemoryReflection(payload params.ReactRunPayload, userName, callerKey string) {
	status := "success"
	defer func() {
		metrics.MemoryReflectionTotal.WithLabelValues(status).Inc()
	}()
	for attempt := 0; attempt < 2; attempt++ {
		ginCtx := newHeadlessGinContext(userName)
		result, err := RunWithClientReaderContext(ginCtx, context.Background(), payload, "", headlessEventWriter(ginCtx), nil)
		if err == nil {
			zlog.Infof(ginCtx, "[react.runMemoryReflection] 整理完成: session=%s run=%s", result.SessionID, result.RunID)
			return
		}
		if IsReactRunCancelled(err) {
			// 停机取消属正常收敛，不计为 error。
			return
		}
		zlog.Warnf(ginCtx, "[react.runMemoryReflection] 整理失败(第%d次): caller=%s err=%v", attempt+1, callerKey, err)
		status = "error"
	}
}

// newHeadlessGinContext 构造无前端请求的 gin.Context：reflection 在后台 goroutine 运行，
// 不能复用触发请求的 ctx（连接断开即失效）；模型层按 ctx 取用户名，必须显式注入。
func newHeadlessGinContext(userName string) *gin.Context {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/internal/memory-reflection", nil)
	helpers.SetUserName(ginCtx, userName)
	return ginCtx
}

// headlessEventWriter 丢弃实时事件（reflection 无前端连接），仅把 error 事件落日志；
// 事件流持久化不受影响——回放页从消息存储还原，无需实时事件。
func headlessEventWriter(ctx *gin.Context) EventWriter {
	return func(event params.ReactEvent) error {
		if event.Type == EventError {
			if payload, ok := event.Payload.(params.ReactErrorPayload); ok {
				zlog.Errorf(ctx, "[react.headlessEventWriter] reflection run 错误事件: run=%s err=%s", event.RunID, payload.ErrMsg)
			}
		}
		return nil
	}
}

// buildMemoryReflectionPrompt 构造五阶段整理提示词：压缩摘要 + 被压缩原文转录（截尾）+ 整理纪律。
func buildMemoryReflectionPrompt(summary string, compacted []llm.ChatMessage, cfg conf.ReactMemoryConfig) string {
	var sb strings.Builder
	sb.WriteString("<memory_reflection_task>\n")
	sb.WriteString("你是长期记忆的维护子任务，负责整理本 caller 作用域的记忆。当前记忆索引已注入上下文（<memory> 块），可用工具仅有 memory_list / memory_read / memory_write。\n\n")
	sb.WriteString("按五个阶段推进，每个阶段的结论先在正文中简述再进入下一阶段：\n")
	sb.WriteString("1. Investigate：用 memory_list（layer=all）与 memory_read 查看现有记忆全貌；阅读下方压缩摘要与近期对话原文。\n")
	sb.WriteString("2. Extract：列出候选动作——新事实（create）、表述更新（update）、重复项合并（保留更准确的一条，删除另一条）、过期项淘汰（delete）。没有价值就什么都不做，宁可少动不错动。\n")
	sb.WriteString("3. Update：逐条执行 memory_write，每条必须给 reason；时间敏感事实在 content 前加「截至 YYYY-MM-DD」。\n")
	sb.WriteString("4. Review：重新 memory_list 复查结果，检查矛盾、重复与常驻层预算。\n")
	sb.WriteString("5. Commit：用一段话总结本次整理（做了什么/为什么/放弃了什么），然后结束。\n\n")
	sb.WriteString("硬性纪律：\n")
	fmt.Fprintf(&sb, "- 本次最多执行 %d 次写操作（create/update/delete 合计）\n", cfg.Reflection.MaxWritesPerRun)
	sb.WriteString("- tags 含 locked 的条目只读，禁止修改或删除\n")
	sb.WriteString("- 不记录凭证、证件号、密钥等敏感信息（会被拦截）\n")
	sb.WriteString("- 只整理记忆，不回答对话中的问题\n\n")
	sb.WriteString("## 压缩摘要（近期对话的概括）\n")
	sb.WriteString(strings.TrimSpace(summary))
	sb.WriteString("\n\n## 被压缩的近期对话原文（可能截断）\n")
	sb.WriteString(renderMemoryReflectionTranscript(compacted, cfg.Reflection.TranscriptCharLimit))
	sb.WriteString("\n</memory_reflection_task>")
	return sb.String()
}

// renderMemoryReflectionTranscript 把被压缩的原始消息渲染为转录文本：
// 从最近往前累计，单条超长截断，总长超过 charLimit 时停止并在头部标注省略条数。
func renderMemoryReflectionTranscript(messages []llm.ChatMessage, charLimit int) string {
	if len(messages) == 0 {
		return "(无)"
	}
	if charLimit <= 0 {
		charLimit = defaultTranscriptFallbackChars
	}
	lines := make([]string, 0, len(messages))
	used := 0
	truncated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		if runes := []rune(content); len(runes) > memoryReflectionMessageCharLimit {
			content = string(runes[:memoryReflectionMessageCharLimit]) + "…(截断)"
		}
		line := fmt.Sprintf("[%s] %s", messages[i].Role, content)
		if used+len([]rune(line)) > charLimit && len(lines) > 0 {
			truncated = i + 1
			break
		}
		lines = append([]string{line}, lines...)
		used += len([]rune(line))
	}
	prefix := ""
	if truncated > 0 {
		prefix = fmt.Sprintf("(更早的 %d 条消息已省略)\n", truncated)
	}
	return prefix + strings.Join(lines, "\n")
}

const defaultTranscriptFallbackChars = 16000
