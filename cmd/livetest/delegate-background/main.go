// livetest/delegate-background：A1 后台委派 + Q1 运行时通知邮箱全链路实测客户端。
//
// 前置：服务运行于 127.0.0.1:8180；caller=demo-app；存在可用子 Agent（echo-agent）；
// deepseek/deepseek-flash 模型可用（conf/mount/custom.yaml 与服务端一致）。
//
// 场景 absorb（默认）：
//  run 指示模型 background=true 委派 echo-agent 一个快速任务，再调用一次 get_tool 后收尾。
//  预期：tool_use_end(delegate_agent 后台启动) → 子 run 完成 → notice_drained（合并通知
//  注入下一模型轮）→ 最终答复转达后台结果 → done。
//
// 场景 cancel：
//  run 指示模型 background=true 委派一个慢任务，客户端收到启动回执后立即 cancel。
//  预期：父 run cancelled；子 run 被级联取消（成本可控优先的有意分歧）；完成通知因父 run
//  已终态不落账本（服务端日志 [React.Notify] 父 run 已终态）。
//
// 用法：go run ./cmd/livetest/delegate-background [absorb|cancel]
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
	scenario := "absorb"
	if len(os.Args) > 1 {
		scenario = os.Args[1]
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

	prompt := "请使用 delegate_agent 工具（background=true）把任务『直接回复四个字：任务完成』委派给 agent echo-agent。" +
		"工具返回后，调用 get_tool 查看 todo_write 的 schema，再调用 todo_write 写入一条『等待后台委派结果』，然后结束回复。"
	childTask := "直接回复四个字：任务完成"
	if scenario == "cancel" {
		prompt = "请使用 delegate_agent 工具（background=true）把任务『先输出从 1 到 30 的每个数字，每个数字单独一行，然后回复：慢任务完成』委派给 agent echo-agent。工具返回后直接结束回复。"
		childTask = "先输出从 1 到 30 的每个数字，每个数字单独一行，然后回复：慢任务完成"
	}
	_ = childTask

	runPayload, _ := json.Marshal(map[string]any{
		"callerKey":    "demo-app",
		"userPrompt":   prompt,
		"modelKey":     "deepseek",
		"modelVersion": "deepseek-flash",
	})
	_ = websocket.JSON.Send(conn, wsMsg{Type: "run", Payload: runPayload})

	var sessionID, runID string
	launched := false
	noticeDrained := 0
	cancelSent := false
	childRunIDs := map[string]bool{}

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
			lane := "main"
			if m.AgentPath != "" {
				lane = m.AgentPath
				childRunIDs[m.RunID] = true
			}
			switch m.Type {
			case "tool_use_start", "tool_use_end":
				var p struct {
					ToolName string `json:"toolName"`
					ToolUseID string `json:"toolUseId"`
					Content  string `json:"content"`
				}
				_ = json.Unmarshal(m.Payload, &p)
				summary := p.Content
				if len(summary) > 90 {
					summary = summary[:90] + "..."
				}
				summary = strings.ReplaceAll(summary, "\n", " ")
				fmt.Println("EVENT", m.Type, "lane=", lane, "tool=", p.ToolName, "runId=", m.RunID, "content=", summary)
				if m.Type == "tool_use_end" && lane == "main" && !launched && strings.Contains(p.Content, "后台委派已启动") {
					launched = true
					fmt.Println("EVENT background_launched runId=", m.RunID)
					if scenario == "cancel" {
						// 启动回执一到立即取消：父 run 大概率正在下一模型轮/工具轮中，取消窗口最稳。
						cancelPayload, _ := json.Marshal(map[string]any{})
						_ = websocket.JSON.Send(conn, wsMsg{Type: "cancel", SessionID: sessionID, RunID: runID, Payload: cancelPayload})
						cancelSent = true
						fmt.Println("ACTION cancel_sent")
					}
				}
			case "notice_drained":
				noticeDrained++
				fmt.Println("EVENT notice_drained lane=", lane, "payload=", string(m.Payload))
			case "done":
				fmt.Println("EVENT done runId=", m.RunID, "lane=", lane)
				if lane == "main" {
					fmt.Println("RESULT sessionId=", sessionID, "scenario=", scenario, "launched=", launched, "noticeDrained=", noticeDrained, "cancelSent=", cancelSent, "childRuns=", len(childRunIDs))
					return
				}
			case "cancelled", "error", "timeout":
				fmt.Println("EVENT", m.Type, "lane=", lane, "runId=", m.RunID, "payload=", string(m.Payload))
				if lane == "main" && scenario == "cancel" {
					// 主 run 已取消：不立即关连接（会把后台子 run 断连成 error 而不是 cancelled），
					// 留出时间让级联取消的子 run 收敛，再退出做 DB 断言。
					fmt.Println("EVENT waiting_child_settle...")
					time.Sleep(10 * time.Second)
					fmt.Println("RESULT sessionId=", sessionID, "scenario=", scenario, "launched=", launched, "noticeDrained=", noticeDrained, "cancelSent=", cancelSent, "childRuns=", len(childRunIDs))
					return
				}
				if lane == "main" {
					fmt.Println("RESULT sessionId=", sessionID, "scenario=", scenario, "launched=", launched, "noticeDrained=", noticeDrained, "cancelSent=", cancelSent, "childRuns=", len(childRunIDs))
					return
				}
			default:
				// 流式增量与心跳不打印，保持输出可读。
				switch m.Type {
				case "heartbeat", "content_start", "content_delta", "content_end",
					"thought_start", "thought_delta", "thought_end", "todo_update":
				default:
					fmt.Println("EVENT", m.Type, "lane=", lane, "runId=", m.RunID, "payload=", truncate(string(m.Payload), 120))
				}
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(240 * time.Second):
		fmt.Println("RESULT timeout sessionId=", sessionID, "scenario=", scenario, "launched=", launched, "noticeDrained=", noticeDrained, "cancelSent=", cancelSent, "childRuns=", len(childRunIDs))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
