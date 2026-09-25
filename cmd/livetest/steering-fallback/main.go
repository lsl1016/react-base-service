// livetest/steering-fallback：run 取消时未消费 guide 降级排队实测客户端。
//
// 前置：同 steering-guide。
//   - queue 开启（S2）：预期收到 steer_delivery_changed，账本行落 queued（不丢输入），
//     且取消后无自动续跑（失败暂停语义）；
//   - queue 关闭（S1 行为）：预期收到 steer_discarded（reason=turn_cancelled），账本行作废。
//
// 流程：发起长输出 run，首条正文增量到达时发送引导并立即取消
// （让 cancel 落在 guide 消费边界之前，保持 admitted 未消费状态）。
//
// 用法：go run ./cmd/livetest/steering-fallback
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
		"userPrompt":   "请从 1 逐个数到 60，每个数字占一行，不要输出其他内容，不要调用任何工具。",
		"modelKey":     "deepseek",
		"modelVersion": "deepseek-flash",
	})
	_ = websocket.JSON.Send(conn, wsMsg{Type: "run", Payload: runPayload})

	var sessionID, runID string
	steered, cancelled := false, false
	fallbackSeen := false

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
			case "content_delta":
				if !steered {
					steered = true
					steer, _ := json.Marshal(map[string]any{
						"callerKey":  "demo-app",
						"userPrompt": "取消前发送的引导消息：数完后请说明你数到了多少。",
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "run", SessionID: sessionID, RunID: runID, Payload: steer})
					fmt.Println("ACTION steer_sent")
					// 立即取消：让 cancel 落在 guide 消费边界之前（admitted 未消费状态）。
					_ = websocket.JSON.Send(conn, wsMsg{Type: "cancel", SessionID: sessionID, RunID: runID})
					cancelled = true
					fmt.Println("ACTION cancel_sent")
				}
			case "steer_guided":
				fmt.Println("EVENT", m.Type, string(m.Payload))
			case "steer_delivery_changed", "steer_drained", "steer_discarded":
				if m.Type == "steer_delivery_changed" {
					fallbackSeen = true
				}
				fmt.Println("EVENT", m.Type, string(m.Payload))
			case "cancelled":
				fmt.Println("EVENT cancelled runId=", m.RunID)
				// 取消后不自动续跑：等待 2 秒确认没有新 run 事件。
				time.Sleep(2 * time.Second)
				fmt.Println("RESULT sessionId=", sessionID, "runId=", runID, "steered=", steered, "cancelled=", cancelled, "fallback=", fallbackSeen)
				return
			case "done":
				fmt.Println("EVENT done(意外，说明取消前 guide 已被消费或取消未生效)")
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(120 * time.Second):
		fmt.Println("RESULT timeout")
	}
}
