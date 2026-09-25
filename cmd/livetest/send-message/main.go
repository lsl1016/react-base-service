// livetest/send-message：A2 SendMessage（父→子代理消息）+ 完成通知回灌实测客户端。
//
// 前置：同 queue-manage；subagent.enabled=true；存在 echo-agent（demo-app）。
//
// 流程：run 指示模型 background=true 委派 echo-agent 一个"从 1 数到 30 再收尾"的慢任务，
// 随后立即用 send_message 工具按 delegate 返回的 runId 补发一条消息；子代理在计数中的
// 模型步边界读到补充消息（其 lane 上出现 steer_drained），最终回复包含补充内容；
// 子 run 完成后通知回灌主 run（notice_drained）。
//
// 用法：go run ./cmd/livetest/send-message
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

type wsMsg struct {
	Type      string          `json:"type"`
	RunID     string          `json:"runId,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	AgentPath string          `json:"agentPath,omitempty"`
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

	prompt := "请按顺序完成四步：" +
		"第一步，用 delegate_agent 工具（background=true）把任务『先调用 ask_question 问用户“任务方向是否确认？”，等待回答后，复述用户的回答并回复一行：任务执行完毕』委派给 agent echo-agent，记下返回的 runId；" +
		"第二步，立即用 send_message 工具（run_id 用第一步返回的 runId）发送消息『请在最终回复的最后一行加上【补充要求已收到】』；" +
		"第三步，调用 get_tool 查看 todo_write 的 schema；" +
		"第四步，调用 todo_write 写入一条『等待后台子任务』，然后结束回复。"

	runPayload, _ := json.Marshal(map[string]any{
		"callerKey":    "demo-app",
		"userPrompt":   prompt,
		"modelKey":     "deepseek",
		"modelVersion": "deepseek-flash",
	})
	_ = websocket.JSON.Send(conn, wsMsg{Type: "run", Payload: runPayload})

	var sessionID string
	launched, messageSent, childDrained, noticeDrained := false, false, 0, 0
	answeredChild := false
	var childAskToolUseID string

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
			lane := "main"
			if m.AgentPath != "" {
				lane = m.AgentPath
			}
			switch m.Type {
			case "tool_use_start", "tool_use_end":
				var p struct {
					ToolName  string `json:"toolName"`
					ToolUseID string `json:"toolUseId"`
					Content   string `json:"content"`
				}
				_ = json.Unmarshal(m.Payload, &p)
				summary := strings.ReplaceAll(p.Content, "\n", " ")
				if len(summary) > 80 {
					summary = summary[:80] + "..."
				}
				fmt.Println("EVENT", m.Type, "lane=", lane, "runId=", m.RunID, "tool=", p.ToolName, "content=", summary)
				if m.Type == "tool_use_end" && lane == "main" && strings.Contains(p.Content, "后台委派已启动") {
					launched = true
				}
				if m.Type == "tool_use_end" && lane == "main" && strings.Contains(p.Content, "消息已作为引导注入") {
					messageSent = true
				}
				if m.Type == "tool_use_start" && lane != "main" && p.ToolName == "ask_question" && !answeredChild {
					// 子代理的 ask_question（服务端内置工具形态）：等补充消息落账后代答。
					childAskToolUseID = p.ToolUseID
					fmt.Println("EVENT child_ask_question lane=", lane, "toolUseId=", p.ToolUseID)
				}
			case "steer_drained":
				if lane != "main" {
					childDrained++
				}
				fmt.Println("EVENT steer_drained lane=", lane, "payload=", string(m.Payload))
			case "notice_drained":
				noticeDrained++
				fmt.Println("EVENT notice_drained lane=", lane, "payload=", string(m.Payload))
			case "done":
				fmt.Println("EVENT done lane=", lane, "runId=", m.RunID)
				if lane == "main" && noticeDrained > 0 {
					fmt.Println("RESULT sessionId=", sessionID, "launched=", launched, "messageSent=", messageSent, "childDrained=", childDrained, "noticeDrained=", noticeDrained)
					return
				}
			case "cancelled", "error", "timeout":
				fmt.Println("EVENT", m.Type, "lane=", lane, "runId=", m.RunID, "payload=", string(m.Payload))
				if lane == "main" {
					fmt.Println("RESULT abnormal sessionId=", sessionID, "launched=", launched, "messageSent=", messageSent, "childDrained=", childDrained, "noticeDrained=", noticeDrained)
					return
				}
			}
			// 子代理问题已登记、补充消息已发送：延迟片刻后代答，让子代理恢复后立即消费补充消息。
			if !answeredChild && childAskToolUseID != "" && messageSent {
				answeredChild = true
				go func() {
					time.Sleep(1500 * time.Millisecond)
					answerPayload, _ := json.Marshal(map[string]any{
						"toolUseId": childAskToolUseID,
						"content":   map[string]any{"skipped": true},
					})
					_ = websocket.JSON.Send(conn, wsMsg{Type: "tool_use_answer", SessionID: sessionID, Payload: answerPayload})
					fmt.Println("ACTION child_answer_sent toolUseId=", childAskToolUseID)
				}()
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(300 * time.Second):
		fmt.Println("RESULT timeout sessionId=", sessionID, "launched=", launched, "messageSent=", messageSent, "childDrained=", childDrained, "noticeDrained=", noticeDrained)
	}
}
