package react

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"react-base-service/components/params"
	"react-base-service/conf"

	"react-base-service/golib/zlog"
)

// errClientReaderRequired 与历史直读路径的报错语义一致：无 WS 读通道时交互类工具不可用。
var errClientReaderRequired = fmt.Errorf("client tool requires websocket reader")

// clientMessageHub 是外层 run 级的前端上行消息多路分发器，解决并行委派下的 HITL 路由：
// 多个等待者（父/子 run 的 ask_question、client tool 回填等待）同时阻塞时，pump 是唯一的
// readClient 消费方，按消息携带的 toolUseId 把消息投递给声明关心它的等待者——
// adk-go「按开放等待点 ID 匹配分发、一轮用户输入可回答多个等待者」的同款语义
// （eino Address 逐段匹配的简化版：基座 toolUseId 全局唯一，单键即足够寻址）。
//
// 分发规则：
//   - cancel 消息广播给全部等待者（各自返回 ErrReactRunCancelled，级联取消语义不变）；
//   - 可寻址消息（tool_use_answer / client_tool_use_end）优先按 toolUseId 匹配投递；
//     未匹配时进入待领缓冲——答复可能先于等待者注册到达（ask_question 的事件发射
//     与等待注册之间有窗口），等待者注册时先认领缓冲，避免错投或丢失；
//   - 非可寻址消息仅在恰好一个等待者时投给它（保持单等待者与历史直读一致的严格
//     校验语义：unexpected message 由等待方报错），多等待者时忽略并记日志。
//
// 生命周期：pump 在首个等待者注册时启动；readClient 返回错误（断连/WS 关闭）时向全部
// 等待者广播错误后退出。run 结束后 WS 层关闭消息通道，pump 随之收敛，无需显式停止。
// clientMessageHub 是一棵 Run 树共享的“单读者、多等待者”上行消息分发器。
//
// WebSocket 连接应只有一个稳定读取者，但父 Run、并行子 Agent、ask_question、Client Tool、
// tool_confirm 可能同时等待不同 toolUseId 的回包。Hub 统一调用 readClient，再按 waiter 的 match
// 条件分发消息，避免多个 goroutine 竞争读取同一连接导致消息被错误消费。
type clientMessageHub struct {
	readClient ClientMessageReader
	mu         sync.Mutex
	waiters    map[*clientMsgWaiter]struct{}
	// pending 是未被任何等待者认领的可寻址消息缓冲（有界，丢最旧）。
	pending []params.ReactWSMessage
	started bool
	// deadErr 非 nil 表示读通道已终结（断连/关闭）：pump 已退出不再消费 readClient，
	// 之后注册的等待者必须立即收到该错误——否则并行委派下「断连时还在模型调用、
	// 稍后才进入等待」的子 run 会永远阻塞，父 run 的 wg.Wait 跟随挂死。
	deadErr error
}

const clientHubPendingCap = 16

// clientMsgWaiter 是一次阻塞等待的注册项：ch 缓冲 1，投递必然成功（等待者要么在收，要么已注销）。
type clientMsgWaiter struct {
	ch    chan hubEvent
	match func(params.ReactWSMessage) bool
}

type hubEvent struct {
	msg params.ReactWSMessage
	err error
}

func newClientMessageHub(readClient ClientMessageReader) *clientMessageHub {
	return &clientMessageHub{
		readClient: readClient,
		waiters:    make(map[*clientMsgWaiter]struct{}),
	}
}

// wait 阻塞等待一条匹配的前端上行消息。match 需要同时约束消息类型与 toolUseId 归属。
// wait 注册一个一次性 waiter，并等待第一条满足 match 的上行消息。
// 注册前先扫描 pending 缓冲，解决“前端回包先到、等待者稍后才注册”的竞态；返回后 waiter 自动注销。
func (h *clientMessageHub) wait(match func(params.ReactWSMessage) bool) (params.ReactWSMessage, error) {
	return h.waitTimeout(match, 0)
}

// waitTimeout 是带超时的 wait：timeout<=0 表示不限（历史语义）。
// 超时后注销 waiter 并返回 ErrInteractionTimeout；若消息在超时判定瞬间已投递，
// 仍以消息优先（避免竞态丢消息）。注销后迟到的匹配消息进入 pending 缓冲，可被后续等待者认领。
func (h *clientMessageHub) waitTimeout(match func(params.ReactWSMessage) bool, timeout time.Duration) (params.ReactWSMessage, error) {
	if h == nil || h.readClient == nil {
		return params.ReactWSMessage{}, errClientReaderRequired
	}
	w := &clientMsgWaiter{ch: make(chan hubEvent, 1), match: match}
	h.mu.Lock()
	// 读通道已终结：不再注册等待，立即向调用方返回断连错误（迟到等待者的收敛路径）。
	if h.deadErr != nil {
		h.mu.Unlock()
		return params.ReactWSMessage{}, h.deadErr
	}
	// 注册前先认领缓冲：答复先于等待者注册到达时（事件发射与等待注册之间的窗口），在此补投。
	for i, msg := range h.pending {
		if w.match(msg) {
			h.pending = append(h.pending[:i], h.pending[i+1:]...)
			h.mu.Unlock()
			return msg, nil
		}
	}
	h.waiters[w] = struct{}{}
	needStart := !h.started
	h.started = true
	h.mu.Unlock()
	if needStart {
		go h.pump()
	}
	if timeout <= 0 {
		event := <-w.ch
		return event.msg, event.err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case event := <-w.ch:
		return event.msg, event.err
	case <-timer.C:
		// 消息可能在超时判定瞬间已投递：先抢一次消息，抢不到才按超时收敛。
		select {
		case event := <-w.ch:
			return event.msg, event.err
		default:
		}
		h.mu.Lock()
		delete(h.waiters, w)
		h.mu.Unlock()
		return params.ReactWSMessage{}, ErrInteractionTimeout
	}
}

// pump 是唯一的 readClient 消费循环；断连/关闭时向全部等待者广播错误后退出。
// pump 是 Hub 中唯一允许调用 readClient 的 goroutine。
// 它只负责读取和路由，不解释业务 payload；消息语义由真正等待该消息的 Tool/HITL 代码处理。
func (h *clientMessageHub) pump() {
	for {
		msg, err := h.readClient()
		if err != nil {
			h.broadcastError(err)
			return
		}
		h.dispatch(msg)
	}
}

func (h *clientMessageHub) broadcastError(err error) {
	h.mu.Lock()
	waiters := make([]*clientMsgWaiter, 0, len(h.waiters))
	for w := range h.waiters {
		waiters = append(waiters, w)
	}
	h.pending = nil // 连接已断，待领缓冲不再有意义
	h.deadErr = err // 标记读通道终结：之后注册的等待者立即收到该错误
	h.mu.Unlock()
	for _, w := range waiters {
		w.ch <- hubEvent{err: err}
	}
}

// dispatch 按分发规则把一条上行消息投递给等待者（投递不持锁，ch 缓冲保证不阻塞）。
func (h *clientMessageHub) dispatch(msg params.ReactWSMessage) {
	h.mu.Lock()
	waiters := make([]*clientMsgWaiter, 0, len(h.waiters))
	for w := range h.waiters {
		waiters = append(waiters, w)
	}
	h.mu.Unlock()
	if len(waiters) == 0 {
		if isClientAddressableMessage(msg) {
			h.bufferPending(msg)
		}
		return
	}

	// cancel 广播：每个等待者各自按现有语义返回取消错误。
	if msg.Type == EventCancel {
		for _, w := range waiters {
			w.ch <- hubEvent{msg: msg}
		}
		return
	}
	for _, w := range waiters {
		if w.match(msg) {
			w.ch <- hubEvent{msg: msg}
			return
		}
	}
	// 可寻址消息未匹配任何等待者：可能属于尚未注册的等待者（事件先于等待注册到达的窗口），
	// 进待领缓冲，等待者注册时认领；缓冲有界丢最旧，异常洪峰不会无限积压。
	if isClientAddressableMessage(msg) {
		h.bufferPending(msg)
		return
	}
	// 非可寻址消息：单等待者保持历史严格语义（unexpected message 交给它报错）；
	// 多等待者视为乱序消息，忽略并记日志，不打断其他等待者。
	if len(waiters) == 1 {
		waiters[0].ch <- hubEvent{msg: msg}
		return
	}
	zlog.Infof(nil, "[React.ClientHub] 忽略未被任何等待者认领的上行消息: type=%s", msg.Type)
}

// bufferPending 把未认领的可寻址消息放入待领缓冲（有界，丢最旧）。
// bufferPending 暂存暂时没有 waiter 认领、但未来可能被工具等待逻辑消费的消息。
// 容量有上限，避免错误客户端持续发送无法匹配的消息导致内存无界增长。
func (h *clientMessageHub) bufferPending(msg params.ReactWSMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pending) >= clientHubPendingCap {
		h.pending = h.pending[1:]
	}
	h.pending = append(h.pending, msg)
}

// isClientAddressableMessage 判定消息是否按 toolUseId 寻址（可缓冲待领）。
func isClientAddressableMessage(msg params.ReactWSMessage) bool {
	return msg.Type == EventToolUseAnswer || msg.Type == EventClientToolUseEnd
}

// toolUseAnswerID 解析 tool_use_answer 信封里的 toolUseId（匹配键）。
func toolUseAnswerID(msg params.ReactWSMessage) string {
	if msg.Type != EventToolUseAnswer {
		return ""
	}
	var envelope struct {
		ToolUseID string `json:"toolUseId"`
	}
	if err := json.Unmarshal(msg.Payload, &envelope); err != nil {
		return ""
	}
	return envelope.ToolUseID
}

// waitClientMessage 是交互等待函数的统一入口：经 hub 注册匹配规则并阻塞等待。
// 配置 react.loop.interaction_timeout_sec > 0 时启用交互等待超时：超时返回
// ErrInteractionTimeout，由各交互工具以错误工具结果回灌模型继续循环（不终止 run）。
func (s *reactEngineState) waitClientMessage(match func(params.ReactWSMessage) bool) (params.ReactWSMessage, error) {
	if seconds := conf.GetReactRuntimeConfig().Loop.InteractionTimeoutSec; seconds > 0 {
		return s.clientHub.waitTimeout(match, time.Duration(seconds)*time.Second)
	}
	return s.clientHub.wait(match)
}

// clientToolUseEndIDs 解析 client_tool_use_end 消息里包含的全部 toolUseId（匹配键集合）。
func clientToolUseEndIDs(msg params.ReactWSMessage) map[string]bool {
	if msg.Type != EventClientToolUseEnd {
		return nil
	}
	var payload struct {
		ToolOutputs []struct {
			ToolUseID string `json:"toolUseId"`
		} `json:"toolOutputs"`
	}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil
	}
	ids := make(map[string]bool, len(payload.ToolOutputs))
	for _, output := range payload.ToolOutputs {
		ids[output.ToolUseID] = true
	}
	return ids
}
