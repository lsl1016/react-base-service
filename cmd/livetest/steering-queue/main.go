// livetest/steering-queue：Steering S2 排队 + run 结束自动续跑全链路实测客户端。
//
// 前置：同 steering-guide，且 llm.react.steering.queue: true（queue_auto_drain 默认 true）。
// 流程：run 调用 ask_question 进入等待态后发送两条引导消息（预期均回执 steer_queued 且
// queueLength 递增——等待态不被打断、输入不丢），作答后 run1 收敛；
// 预期自动续跑：steer_drained(A) → 自动开启 run2 → 收敛 → steer_drained(B) → 自动开启 run3 →
// 收敛 done，全程同一 WS 连接、FIFO 顺序。
//
// 用法：go run ./cmd/livetest/steering-queue
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/websocket"
)

type wsMsg struct {
	Type      string          `json:"type"`
	RunID     string          `json:"runId,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func main() {
	cfg, err := websocket.NewConfig("ws://127.0.0.1:8180/react-base-service/react/ws", "http://127.0.0.1")
	if err != nil {
		fmt.Println("CONFIG_FAIL", err)
		os.Exit(1)
	}
	cfg.Header = http.Header{"X-User-Name": []string{"steer-tester"}}
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		fmt.Println("DIAL_FAIL", err)
		os.Exit(1)
	}
	defer conn.Close()

	runPayload, _ := json.Marshal(map[string]any{
		"callerKey":    "demo-app",
		"userPrompt":   "请调用 ask_question 工具问我一个问题（一个问题即可），然后等待我的回答再继续。",
		"modelKey":     "deepseek",
		"modelVersion": "deepseek-flash",
	})
	_ = websocket.JSON.Send(conn, wsMsg{Type: "run", Payload: runPayload})

	var sessionID, runID, askToolUseID string
	queuedA, queuedB := false, false
	answered := false
	runCount := 1
	drainedCount := 0

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var m wsMsg
			if err := websocket.JSON.Receive(conn, &m); err != nil {
				fmt.Println("READ_END", err)
				return
			}
			if m.SessionID != "" && sessionID == "" {
				sessionID = m.SessionID
			}
			if m.RunID != "" {
				if runID == "" {
					runID = m.RunID
				} else if m.RunID != runID {
					runID = m.RunID
					runCount++
					fmt.Println("EVENT new_run_started runId=", runID, "runCount=", runCount)
				}
			}
			switch m.Type {
			case "done":
				// 链中每个 run 都有自己的 done；队列排空后的最后一个 done 才是终点。
				if drainedCount >= 2 {
					fmt.Println("EVENT done(final)", "runId=", m.RunID)
					fmt.Println("RESULT sessionId=", sessionID, "runs=", runCount, "queued=", queuedA, queuedB, "drained=", drainedCount)
					return
				}
				fmt.Println("EVENT done(chain continues)", "runId=", m.RunID)
			case "error", "cancelled", "timeout":
				fmt.Println("EVENT", m.Type, string(m.Payload), "runId=", m.RunID)
				fmt.Println("RESULT abnormal sessionId=", sessionID, "runs=", runCount, "queued=", queuedA, queuedB, "drained=", drainedCount)
				return
			case "steer_queued", "steer_guided", "steer_rejected":
				fmt.Println("EVENT", m.Type, string(m.Payload), "runId=", m.RunID)
				if m.Type == "steer_queued" && !queuedA {
					queuedA = true
					// 排队第二条，验证 FIFO。
					steerB, _ := json.Marshal(map[string]any{
						"callerKey":  "demo-app",
						"userPrompt": "排队消息B：请在最终回答中以【队列B】开头，用一句话回应。",
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "run", SessionID: sessionID, RunID: runID, Payload: steerB})
					queuedB = true
					fmt.Println("ACTION steerB_sent")
				} else if m.Type == "steer_queued" && queuedA && queuedB && !answered {
					// 两条都已入队：立即作答 ask_question，run 1 收敛后应自动续跑 A、B。
					answered = true
					answerPayload, _ := json.Marshal(map[string]any{
						"toolUseId": askToolUseID,
						"content":   map[string]any{"skipped": true},
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "tool_use_answer", SessionID: sessionID, RunID: runID, Payload: answerPayload})
					fmt.Println("ACTION answer_sent runId=", m.RunID)
				}
			case "steer_drained", "steer_delivery_changed", "steer_discarded":
				if m.Type == "steer_drained" {
					drainedCount++
				}
				fmt.Println("EVENT", m.Type, string(m.Payload))
			case "tool_use_start":
				var p struct {
					ToolUseID string `json:"toolUseId"`
					ToolName  string `json:"toolName"`
				}
				_ = json.Unmarshal(m.Payload, &p)
				if p.ToolName == "ask_question" && !queuedA {
					// 等 run 行状态落为 waiting_client_message 后再发（避免与状态写入竞态）。
					time.Sleep(2 * time.Second)
					askToolUseID = p.ToolUseID
					steerA, _ := json.Marshal(map[string]any{
						"callerKey":  "demo-app",
						"userPrompt": "排队消息A：请在最终回答中以【队列A】开头，用一句话回应。",
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "run", SessionID: sessionID, RunID: runID, Payload: steerA})
					fmt.Println("ACTION steerA_sent_while_waiting")
				}
			case "tool_use_end":
				// 观察点：ask_question 的 tool_use_end 在作答后才触发，这里只记录。
				fmt.Println("EVENT tool_use_end runId=", m.RunID)
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(300 * time.Second):
		fmt.Println("RESULT timeout sessionId=", sessionID, "runs=", runCount, "queued=", queuedA, queuedB, "drained=", drainedCount)
	}
}
