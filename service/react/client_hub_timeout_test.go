package react

// 交互等待超时（waitTimeout）单元测试。

import (
	"errors"
	"testing"
	"time"

	"react-base-service/components/params"
)

// TestClientHubWaitTimeoutReturnsAndDeregisters 验证超时返回 ErrInteractionTimeout，
// 且 waiter 被注销：迟到消息进入 pending 缓冲而不是投递给已超时的等待者。
func TestClientHubWaitTimeoutReturnsAndDeregisters(t *testing.T) {
	reader, msgCh, _ := newHubTestReader()
	hub := newClientMessageHub(reader)

	_, err := hub.waitTimeout(func(m params.ReactWSMessage) bool {
		return m.Type == EventToolUseAnswer
	}, 30*time.Millisecond)
	if !IsErrInteractionTimeout(err) {
		t.Fatalf("expected ErrInteractionTimeout, got %v", err)
	}

	// 超时后消息到达：无等待者时进入 pending（answer 可寻址）
	msg := answerMsg("late_call", "too late")
	msgCh <- msg
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		buffered := len(hub.pending)
		hub.mu.Unlock()
		if buffered == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	hub.mu.Lock()
	buffered := len(hub.pending)
	hub.mu.Unlock()
	if buffered != 1 {
		t.Fatalf("late addressable message should be buffered for future waiters")
	}

	// 后续等待者能从缓冲认领该消息
	got, err := hub.waitTimeout(func(m params.ReactWSMessage) bool {
		return m.Type == EventToolUseAnswer && toolUseAnswerID(m) == "late_call"
	}, time.Second)
	if err != nil || toolUseAnswerID(got) != "late_call" {
		t.Fatalf("later waiter should reclaim pending message, got %v/%v", got, err)
	}
}

// TestClientHubWaitTimeoutMessageAtDeadlineWins 验证竞态处理：消息在超时判定瞬间已投递时，
// 以消息优先而不是返回超时。
func TestClientHubWaitTimeoutMessageAtDeadlineWins(t *testing.T) {
	reader, msgCh, _ := newHubTestReader()
	hub := newClientMessageHub(reader)

	go func() {
		time.Sleep(20 * time.Millisecond)
		msgCh <- answerMsg("race_call", "in time")
	}()
	msg, err := hub.waitTimeout(func(m params.ReactWSMessage) bool {
		return m.Type == EventToolUseAnswer && toolUseAnswerID(m) == "race_call"
	}, 50*time.Millisecond)
	if err != nil || toolUseAnswerID(msg) != "race_call" {
		t.Fatalf("message delivered near deadline should win over timeout, got %v/%v", msg, err)
	}
}

// TestClientHubWaitZeroTimeoutBlocks 历史语义：timeout<=0 时无限等待（由 cancel 打断）。
func TestClientHubWaitZeroTimeoutBlocks(t *testing.T) {
	reader, _, errCh := newHubTestReader()
	hub := newClientMessageHub(reader)

	done := make(chan error, 1)
	go func() {
		_, err := hub.waitTimeout(func(m params.ReactWSMessage) bool { return true }, 0)
		done <- err
	}()
	// 短暂等待确认未返回（无限等待语义）
	select {
	case err := <-done:
		t.Fatalf("zero timeout must block, got early return %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	errCh <- errors.New("closed")
	if err := <-done; err == nil {
		t.Fatalf("reader close should unblock waiter with error")
	}
}
