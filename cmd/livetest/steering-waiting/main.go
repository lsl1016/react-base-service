// livetest/steering-waiting：HITL 等待态 Steering 保护实测客户端。
//
// 前置：同 steering-guide（llm.react.steering.enabled: true）。
// 流程：run 调用 ask_question 进入 waiting_client_message 后发送引导消息，
// 预期收到 steer_rejected {"reason":"run_not_steerable"}（等待态绝不被引导打断），
// 随后以 tool_use_answer（skipped）作答让 run 正常收敛。
// 注意 tool_use_start 到 run 行落 waiting 状态有毫秒级窗口，脚本内置 2s 延迟避开竞态。
//
// 用法：go run ./cmd/livetest/steering-waiting
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
	steered := false
	answered := false
	rejectedReceipt := ""

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
			if m.RunID != "" && runID == "" {
				runID = m.RunID
			}
			switch m.Type {
			case "tool_use_start":
				var p struct {
					ToolUseID string `json:"toolUseId"`
					ToolName  string `json:"toolName"`
				}
				_ = json.Unmarshal(m.Payload, &p)
				if p.ToolName == "ask_question" && !steered {
					// 等 run 行状态落为 waiting_client_message 后再发（避免与状态写入竞态）。
					time.Sleep(2 * time.Second)
					askToolUseID = p.ToolUseID
					steerPayload, _ := json.Marshal(map[string]any{
						"callerKey":  "demo-app",
						"userPrompt": "等待期间发送的引导消息（应被拒绝）",
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "run", SessionID: sessionID, RunID: runID, Payload: steerPayload})
					steered = true
					fmt.Println("ACTION steer_sent_while_waiting", sessionID, runID)
				}
			case "steer_guided", "steer_queued", "steer_rejected":
				rejectedReceipt = m.Type
				fmt.Println("EVENT", m.Type, string(m.Payload))
				// 收到回执后作答 ask_question 让 run 正常收敛。
				if !answered {
					answered = true
					answerPayload, _ := json.Marshal(map[string]any{
						"toolUseId": askToolUseID,
						"content":   map[string]any{"skipped": true},
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "tool_use_answer", SessionID: sessionID, RunID: runID, Payload: answerPayload})
					fmt.Println("ACTION answer_sent")
				}
			case "steer_drained", "steer_delivery_changed", "steer_discarded":
				fmt.Println("EVENT", m.Type, string(m.Payload))
			case "done":
				fmt.Println("EVENT done", string(m.Payload))
				fmt.Println("RESULT", "steered=", steered, "receipt=", rejectedReceipt, "sessionId=", sessionID, "runId=", runID)
				return
			case "error", "cancelled", "timeout":
				fmt.Println("EVENT", m.Type, string(m.Payload))
				fmt.Println("RESULT abnormal", "steered=", steered, "receipt=", rejectedReceipt)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(180 * time.Second):
		fmt.Println("RESULT timeout", "steered=", steered, "receipt=", rejectedReceipt)
	}
}
