package hub

import (
	"encoding/json"
	"errors"
	core "react-base-service/service/react/internal/core"
	"sync"
	"testing"
	"time"

	"react-base-service/components/params"
)

// newHubTestReader 构造可注入消息/错误的 readClient 模拟。
func newHubTestReader() (core.ClientMessageReader, chan<- params.ReactWSMessage, chan<- error) {
	msgCh := make(chan params.ReactWSMessage, 8)
	errCh := make(chan error, 1)
	reader := func() (params.ReactWSMessage, error) {
		select {
		case msg := <-msgCh:
			return msg, nil
		case err := <-errCh:
			return params.ReactWSMessage{}, err
		}
	}
	return reader, msgCh, errCh
}

func answerMsg(toolUseID, content string) params.ReactWSMessage {
	payload, _ := json.Marshal(map[string]any{
		"toolUseId": toolUseID,
		"content":   content,
	})
	return params.ReactWSMessage{Type: core.EventToolUseAnswer, Payload: payload}
}

// TestClientHubRoutesByToolUseId 验证并行 HITL 路由：两个等待者按各自 toolUseId 认领消息。
func TestClientHubRoutesByToolUseId(t *testing.T) {
	reader, msgCh, _ := newHubTestReader()
	hub := NewClientMessageHub(reader)

	answerA := toolUseAnswerIDMatcher("call_a")
	answerB := toolUseAnswerIDMatcher("call_b")

	var wg sync.WaitGroup
	results := make(map[string]string, 2)
	var mu sync.Mutex
	for key, matcher := range map[string]func(params.ReactWSMessage) bool{"a": answerA, "b": answerB} {
		wg.Add(1)
		go func(key string, matcher func(params.ReactWSMessage) bool) {
			defer wg.Done()
			msg, err := hub.Wait(matcher)
			if err != nil {
				mu.Lock()
				results[key] = "err:" + err.Error()
				mu.Unlock()
				return
			}
			var envelope struct {
				ToolUseID string `json:"toolUseId"`
			}
			_ = json.Unmarshal(msg.Payload, &envelope)
			mu.Lock()
			results[key] = envelope.ToolUseID
			mu.Unlock()
		}(key, matcher)
	}

	// 等两个等待者注册完成后再注入消息（顺序与等待者注册顺序相反，验证无"先到先得"误配）。
	time.Sleep(50 * time.Millisecond)
	msgCh <- answerMsg("call_b", `{"answers":[]}`)
	msgCh <- answerMsg("call_a", `{"answers":[]}`)
	wg.Wait()

	if results["a"] != "call_a" || results["b"] != "call_b" {
		t.Fatalf("消息路由错配: %+v", results)
	}
}

func toolUseAnswerIDMatcher(toolUseID string) func(params.ReactWSMessage) bool {
	return func(m params.ReactWSMessage) bool {
		return ToolUseAnswerID(m) == toolUseID
	}
}

// TestClientHubCancelBroadcast 验证 cancel 消息广播给全部等待者（级联取消语义）。
func TestClientHubCancelBroadcast(t *testing.T) {
	reader, msgCh, _ := newHubTestReader()
	hub := NewClientMessageHub(reader)

	var wg sync.WaitGroup
	cancelled := make(chan string, 2)
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			msg, err := hub.Wait(func(m params.ReactWSMessage) bool { return false }) // 不匹配任何消息
			if err != nil {
				cancelled <- key + ":err"
				return
			}
			if msg.Type == core.EventCancel {
				cancelled <- key + ":cancel"
			}
		}(key)
	}
	time.Sleep(50 * time.Millisecond)
	msgCh <- params.ReactWSMessage{Type: core.EventCancel}
	wg.Wait()

	if got := <-cancelled; got != "a:cancel" && got != "b:cancel" {
		t.Fatalf("等待者应收到 cancel: %s", got)
	}
	if got := <-cancelled; got != "a:cancel" && got != "b:cancel" {
		t.Fatalf("等待者应收到 cancel: %s", got)
	}
}

// TestClientHubSingleWaiterStrict 验证单等待者对「非可寻址」消息保持历史严格语义：
// 不匹配的消息仍投给唯一等待者（由上层按 unexpected message 报错）。
// 可寻址消息（tool_use_answer/client_tool_use_end）未匹配时进待领缓冲，见 PendingRace 测试。
func TestClientHubSingleWaiterStrict(t *testing.T) {
	reader, msgCh, _ := newHubTestReader()
	hub := NewClientMessageHub(reader)

	type result struct {
		msg params.ReactWSMessage
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		msg, err := hub.Wait(func(m params.ReactWSMessage) bool {
			return m.Type == core.EventToolUseAnswer && ToolUseAnswerID(m) == "call_target"
		})
		resultCh <- result{msg, err}
	}()
	time.Sleep(50 * time.Millisecond)
	// 非可寻址类型：单等待者直接收到（严格语义由上层校验报错）。
	msgCh <- params.ReactWSMessage{Type: "heartbeat_like_unexpected"}

	got := <-resultCh
	if got.err != nil {
		t.Fatalf("单等待者不应报错: %v", got.err)
	}
	if got.msg.Type != "heartbeat_like_unexpected" {
		t.Fatalf("单等待者应收到该非可寻址消息: %+v", got.msg)
	}
}

// TestClientHubPendingBufferRace 验证「答复先于等待者注册到达」的竞态：
// 可寻址消息未匹配任何等待者时进入待领缓冲，等待者注册时先认领，不错投给既有等待者。
func TestClientHubPendingBufferRace(t *testing.T) {
	reader, msgCh, _ := newHubTestReader()
	hub := NewClientMessageHub(reader)

	// 等待者 A 先注册（启动 pump）。
	gotA := make(chan string, 1)
	go func() {
		msg, err := hub.Wait(toolUseAnswerIDMatcher("call_a"))
		if err != nil {
			gotA <- "err"
			return
		}
		gotA <- ToolUseAnswerID(msg)
	}()
	time.Sleep(50 * time.Millisecond)

	// B 的答复先到达（B 的等待者尚未注册）：必须进缓冲而不是错投给 A。
	msgCh <- answerMsg("call_b", `{}`)
	time.Sleep(100 * time.Millisecond)
	select {
	case v := <-gotA:
		t.Fatalf("B 的答复不得错投给 A: %s", v)
	default:
	}

	// B 的等待者注册后从缓冲认领自己的答复。
	gotB := make(chan string, 1)
	go func() {
		msg, err := hub.Wait(toolUseAnswerIDMatcher("call_b"))
		if err != nil {
			gotB <- "err"
			return
		}
		gotB <- ToolUseAnswerID(msg)
	}()
	select {
	case id := <-gotB:
		if id != "call_b" {
			t.Fatalf("B 应认领自己的答复: %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待者注册后应从待领缓冲认领答复")
	}

	// A 的答复随后到达，正常投递。
	msgCh <- answerMsg("call_a", `{}`)
	select {
	case id := <-gotA:
		if id != "call_a" {
			t.Fatalf("A 应收到自己的答复: %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("A 的答复未投递")
	}
}

// TestClientHubErrorBroadcast 验证底层 readClient 错误广播给全部等待者。
func TestClientHubErrorBroadcast(t *testing.T) {
	reader, _, errCh := newHubTestReader()
	hub := NewClientMessageHub(reader)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := hub.Wait(func(m params.ReactWSMessage) bool { return true })
			errs <- err
		}()
	}
	time.Sleep(50 * time.Millisecond)
	errCh <- errors.New("connection closed")
	wg.Wait()

	for i := 0; i < 2; i++ {
		if err := <-errs; err == nil || err.Error() != "connection closed" {
			t.Fatalf("等待者应收到广播错误: %v", err)
		}
	}
}

// TestClientHubLateWaiterAfterDisconnect 防回归（并行断连级联 e2e 抓出的引擎缺陷）：
// pump 在断连后永久退出，若「断连时还在模型调用、稍后才进入等待」的子 run 此刻注册
// 等待者，必须立即收到断连错误立即收敛——修复前会永远阻塞在 w.ch，父 run 的
// wg.Wait 跟随挂死（run 永不终态）。
func TestClientHubLateWaiterAfterDisconnect(t *testing.T) {
	reader, _, errCh := newHubTestReader()
	hub := NewClientMessageHub(reader)

	// 第一个等待者先注册（pump 随之启动），然后读通道断连。
	firstDone := make(chan error, 1)
	go func() {
		_, err := hub.Wait(func(m params.ReactWSMessage) bool { return true })
		firstDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	errCh <- errors.New("connection closed")
	if err := <-firstDone; err == nil || err.Error() != "connection closed" {
		t.Fatalf("在册等待者应收到断连错误: %v", err)
	}

	// 迟到等待者：pump 已退出，注册即返回断连错误而非永久阻塞。
	done := make(chan error, 1)
	go func() {
		_, err := hub.Wait(func(m params.ReactWSMessage) bool { return true })
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != "connection closed" {
			t.Fatalf("迟到等待者应立即收到断连错误: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("迟到等待者永久阻塞（断连后注册的等待者未收敛）")
	}
}

// TestClientHubMultiWaiterUnmatchedIgnored 验证多等待者时未被认领的消息被忽略
// 而不是打断其他等待者（乱序/过期消息隔离）。
func TestClientHubMultiWaiterUnmatchedIgnored(t *testing.T) {
	reader, msgCh, _ := newHubTestReader()
	hub := NewClientMessageHub(reader)

	got := make(chan string, 1)
	go func() {
		msg, err := hub.Wait(toolUseAnswerIDMatcher("call_mine"))
		if err != nil {
			got <- "err"
			return
		}
		got <- ToolUseAnswerID(msg)
	}()
	// 第二个等待者确保 hub 处于多等待者形态。
	go func() {
		_, _ = hub.Wait(toolUseAnswerIDMatcher("call_other"))
	}()
	time.Sleep(50 * time.Millisecond)
	msgCh <- answerMsg("call_unrelated", `{}`) // 无任何等待者认领
	time.Sleep(50 * time.Millisecond)
	msgCh <- answerMsg("call_mine", `{}`)

	select {
	case id := <-got:
		if id != "call_mine" {
			t.Fatalf("等待者应收到自己的消息: %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("无关消息不应吞掉后续匹配消息的投递")
	}
}
