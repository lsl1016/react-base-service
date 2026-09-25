// livetest/queue-manage：S3 队列管理全链路实测客户端。
//
// 前置：服务运行于 127.0.0.1:8180；llm.react.steering.{enabled,queue}=true（queue_auto_drain 默认 true）；
// caller=demo-app；deepseek/deepseek-flash 可用。
//
// 流程：run 调用 ask_question 进入等待态 → 连续 steer 三条消息（均落队列）→
// HTTP 队列管理：list(3) → update(改写 B) → reorder(倒序 C,B,A) → list(校验顺序) → delete(删 A) →
// 作答 ask_question → run1 收敛 → 自动续跑按重排后顺序消费 C、B（steer_drained 顺序 + 最终答复
// 以【C 内容】【B 内容】开头）→ done。
//
// 用法：go run ./cmd/livetest/queue-manage
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/websocket"
)

const (
	baseURL   = "http://127.0.0.1:8180/react-base-service"
	wsURL     = "ws://127.0.0.1:8180/react-base-service/react/ws"
	userName  = "steer-tester"
	callerKey = "demo-app"
)

type wsMsg struct {
	Type      string          `json:"type"`
	RunID     string          `json:"runId,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	AgentPath string          `json:"agentPath,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// queueAPI 调用 /react/queue/* 管理接口。
func queueAPI(path string, body map[string]any) map[string]any {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/react/"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Name", userName)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("HTTP_FAIL", path, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	fmt.Println("HTTP", path, string(data))
	return out
}

func main() {
	cfg, err := websocket.NewConfig(wsURL, "http://127.0.0.1")
	if err != nil {
		fmt.Println("CONFIG_FAIL", err)
		os.Exit(1)
	}
	cfg.Header = http.Header{"X-User-Name": []string{userName}}
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		fmt.Println("DIAL_FAIL", err)
		os.Exit(1)
	}
	defer conn.Close()

	runPayload, _ := json.Marshal(map[string]any{
		"callerKey":    callerKey,
		"userPrompt":   "请调用 ask_question 工具问我一个问题（一个问题即可），然后等待我的回答再继续。",
		"modelKey":     "deepseek",
		"modelVersion": "deepseek-flash",
	})
	_ = websocket.JSON.Send(conn, wsMsg{Type: "run", Payload: runPayload})

	var sessionID, runID, askToolUseID string
	queuedCount := 0
	managed := false
	drainedIDs := []string{}
	doneCount := 0

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
			case "steer_queued", "steer_guided", "steer_rejected", "steer_drained", "steer_discarded":
				fmt.Println("EVENT", m.Type, string(m.Payload))
				if m.Type == "steer_queued" {
					queuedCount++
					if queuedCount == 3 && !managed {
						managed = true
						// 三条排队就绪：走 HTTP 管理链路（list → update → reorder → list → delete）。
						time.Sleep(500 * time.Millisecond)
						runQueueManagement(sessionID)
						// 管理完成：作答 ask_question，run1 收敛后按重排顺序自动续跑。
						answerPayload, _ := json.Marshal(map[string]any{
							"toolUseId": askToolUseID,
							"content":   map[string]any{"skipped": true},
						})
						_ = websocket.JSON.Send(conn, wsMsg{Type: "tool_use_answer", SessionID: sessionID, RunID: runID, Payload: answerPayload})
						fmt.Println("ACTION answer_sent")
					}
				}
				if m.Type == "steer_drained" {
					var p struct {
						PendingInputID string `json:"pendingInputId"`
					}
					_ = json.Unmarshal(m.Payload, &p)
					drainedIDs = append(drainedIDs, p.PendingInputID)
				}
			case "tool_use_start":
				var p struct {
					ToolUseID string `json:"toolUseId"`
					ToolName  string `json:"toolName"`
				}
				_ = json.Unmarshal(m.Payload, &p)
				if p.ToolName == "ask_question" {
					time.Sleep(2 * time.Second)
					askToolUseID = p.ToolUseID
					// 进入等待态后连续排队三条（等待态 + queue 开启 → 全部落队列）。
					for i, text := range []string{"【原始A】请以【A原始】开头回应", "【原始B】请以【B改写】开头回应", "【原始C】请以【C重排】开头回应"} {
						steer, _ := json.Marshal(map[string]any{
							"callerKey":  callerKey,
							"userPrompt": text,
						})
						_ = websocket.JSON.Send(conn, wsMsg{Type: "run", SessionID: sessionID, RunID: runID, Payload: steer})
						time.Sleep(300 * time.Millisecond)
						_ = i
					}
					fmt.Println("ACTION steer_x3_sent")
				}
			case "done":
				doneCount++
				fmt.Println("EVENT done", "runId=", m.RunID, "count=", doneCount)
				if doneCount >= 3 {
					fmt.Println("RESULT sessionId=", sessionID, "queued=", queuedCount, "managed=", managed, "drained=", drainedIDs)
					return
				}
			case "cancelled", "error", "timeout":
				fmt.Println("EVENT", m.Type, string(m.Payload))
				fmt.Println("RESULT abnormal sessionId=", sessionID)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(300 * time.Second):
		fmt.Println("RESULT timeout sessionId=", sessionID, "queued=", queuedCount, "drained=", drainedIDs)
	}
}

// runQueueManagement 依次执行 list → update(B) → reorder(C,B) → list → delete(C)。
func runQueueManagement(sessionID string) {
	common := func() map[string]any {
		return map[string]any{"sessionId": sessionID, "callerKey": callerKey, "routeValues": []string{}}
	}

	list1 := queueAPI("queue/list", common())
	items := extractItems(list1)
	if len(items) != 3 {
		fmt.Println("ASSERT_FAIL expect 3 queued items, got", len(items))
		os.Exit(1)
	}

	// 按 seq 定位三条：A(第一条) B(第二条) C(第三条)。
	a, b, c := items[0], items[1], items[2]

	queueAPI("queue/update", map[string]any{
		"sessionId": sessionID, "callerKey": callerKey, "routeValues": []string{},
		"id": b["id"], "content": "【B改写】请以【B改写】开头回应",
	})
	queueAPI("queue/reorder", map[string]any{
		"sessionId": sessionID, "callerKey": callerKey, "routeValues": []string{},
		"idList": []any{c["id"], b["id"], a["id"]},
	})
	list2 := queueAPI("queue/list", common())
	items2 := extractItems(list2)
	if len(items2) == 3 && items2[0]["id"] == c["id"] && items2[2]["id"] == a["id"] {
		fmt.Println("ASSERT_OK reorder applied:", items2[0]["pendingInputId"], items2[1]["pendingInputId"], items2[2]["pendingInputId"])
	} else {
		fmt.Println("ASSERT_FAIL reorder not reflected:", items2)
	}
	queueAPI("queue/delete", map[string]any{
		"sessionId": sessionID, "callerKey": callerKey, "routeValues": []string{},
		"id": a["id"],
	})
	queueAPI("queue/list", common())
	fmt.Println("ACTION queue_managed (deleted A; remaining head should be C)")
}

func extractItems(resp map[string]any) []map[string]any {
	out := []map[string]any{}
	data, _ := resp["data"].(map[string]any)
	items, _ := data["items"].([]any)
	for _, item := range items {
		if typed, ok := item.(map[string]any); ok {
			out = append(out, typed)
		}
	}
	return out
}
