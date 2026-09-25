// livetest/steering-guide：Steering S1 guide 注入全链路实测客户端。
//
// 前置：依赖容器已就绪（docker compose up -d mysql redis sandbox）、服务已启动
// （go run main.go，监听 :8180）、llm.react.steering.enabled: true、demo-app caller 可用。
// 流程：发起长输出 run（数数），首条正文增量到达（模型流进行中）时发送第二条 run 消息
// 触发 guide 准入，预期依次收到 steer_guided → steer_drained → done；
// 消息表顺序应为 原始user → assistant → guide user → 新 assistant（guide 不插入 tool_use/tool_result 之间）。
//
// 用法：go run ./cmd/livetest/steering-guide [sessionId] [steerText] [cancelAfterGuided]
//   - sessionId 为空则新建会话；cancelAfterGuided=cancel 时收到 guided 回执后立即取消
//     （用于观察取消路径的结算/降级）。
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
	// 用法: go run ./cmd/livetest/steering-guide [sessionId] [steerText] [cancelAfterGuided]
	sessionArg := ""
	steerText := "数完后请再补充一句：你刚才一共数到了多少？让这句话以【引导收到】开头。"
	cancelAfterGuided := false
	if len(os.Args) > 1 {
		sessionArg = os.Args[1]
	}
	if len(os.Args) > 2 {
		steerText = os.Args[2]
	}
	if len(os.Args) > 3 && os.Args[3] == "cancel" {
		cancelAfterGuided = true
	}
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
		"userPrompt":   "请从 1 逐个数到 100，每个数字占一行，除数字外不要输出任何其他内容，不要调用任何工具。",
		"modelKey":     "deepseek",
		"modelVersion": "deepseek-flash",
	})
	_ = websocket.JSON.Send(conn, wsMsg{Type: "run", SessionID: sessionArg, Payload: runPayload})

	var sessionID, runID string
	steered := false
	drained := false
	guidedReceipt := ""
	firstDeltaSeen := false
	contentAfterSteer := 0

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
				if steered {
					contentAfterSteer++
				}
				if !firstDeltaSeen {
					firstDeltaSeen = true
					steerPayload, _ := json.Marshal(map[string]any{
						"callerKey":  "demo-app",
						"userPrompt": steerText,
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "run", SessionID: sessionID, RunID: runID, Payload: steerPayload})
					steered = true
					fmt.Println("ACTION steer_sent sessionId=", sessionID, "runId=", runID)
				}
			case "steer_guided", "steer_queued", "steer_rejected":
				if m.Type == "steer_guided" || m.Type == "steer_queued" || m.Type == "steer_rejected" {
					guidedReceipt = m.Type
					if m.Type == "steer_guided" && cancelAfterGuided {
						_ = websocket.JSON.Send(conn, wsMsg{Type: "cancel", SessionID: sessionID, RunID: runID})
						fmt.Println("ACTION cancel_sent")
					}
				}
				if m.Type == "steer_drained" {
					drained = true
				}
				fmt.Println("EVENT", m.Type, string(m.Payload))
			case "steer_drained", "steer_delivery_changed", "steer_discarded":
				if m.Type == "steer_drained" {
					drained = true
				}
				fmt.Println("EVENT", m.Type, string(m.Payload))
			case "done":
				fmt.Println("EVENT done", string(m.Payload))
				fmt.Println("RESULT", "steered=", steered, "drained=", drained, "receipt=", guidedReceipt, "contentAfterSteer=", contentAfterSteer, "sessionId=", sessionID, "runId=", runID)
				return
			case "error", "cancelled", "timeout":
				fmt.Println("EVENT", m.Type, string(m.Payload))
				fmt.Println("RESULT abnormal", "steered=", steered, "drained=", drained, "receipt=", guidedReceipt, "sessionId=", sessionID, "runId=", runID)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(180 * time.Second):
		fmt.Println("RESULT timeout", "steered=", steered, "drained=", drained, "receipt=", guidedReceipt, "sessionId=", sessionID, "runId=", runID)
	}
}
