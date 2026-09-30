package e2e

import (
	"os"
	"strings"
	"testing"

	"react-base-service/conf"
	model "react-base-service/models/llm"
	"react-base-service/service/workspace"
)

// P2-1 代码工作区 e2e（REACT_DELEGATE_E2E=1，真实 git mirror/worktree + repo MCP + glm-4.6）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/react/internal/e2e/ -run TestWorkspace -v -timeout 600s
//
// 验证：load_runtime_code 建 worktree、ws_* 工具挂载与同步、code-agent 检索线上代码、
// run 终态后工作区与工具副本全部释放。

const e2eCodeAgentMD = `---
agent_key: code-agent
name: 代码检索代理
description: |
  适用问题：在线上代码中定位函数/报错逻辑、查看实现与变更
  不适用：与代码无关的查询
max_steps: 8
tools: []
---
你是代码检索代理。收到任务后：
1. 先调用 load_runtime_code 加载目标服务代码（service 通常为 react-base-service，env 可省略）；
2. 用 get_tool 加载 ws_react-base-service_search_code（或 ws_react-base-service_get_repo_map / ws_react-base-service_read_file）等前缀工具，再通过 execute_tool 执行检索；
3. 用中文汇报：目标符号所在文件、核心行为一句话。
只做检索与阅读，不要修改任何东西。`

func TestWorkspaceLoadAndSearchE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e")
	}
	setupComplexE2E(t)
	if !conf.CustomConf.LLM.React.Workspace.WorkspaceEnabled() {
		t.Fatal("llm.react.workspace.enabled 必须为 true（conf/mount/custom.yaml）")
	}

	// repo-mcp stdio 适配器按相对路径 bin/repo-mcp.exe 拉起：测试进程切到仓库根再还原。
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	ctx := newHeadlessGinContext("e2e-workspace")
	importE2EAgent(t, ctx, e2eCodeAgentMD)

	result, _ := runE2EConversation(t, ctx,
		"请调用 delegate_agent 把任务「加载 react-base-service 服务代码，定位 delegateAgentPath 函数，报告它所在的文件与作用」委派给子代理 code-agent（agent_key: code-agent），拿到结论后转述。",
		10, nil, nil)

	subRuns := findSubRuns(t, ctx, result.SessionID, result.RunID)
	if len(subRuns) != 1 || subRuns[0].AgentPath != "main/code-agent" {
		t.Fatalf("子 run 不符: %v", subRunPaths(subRuns))
	}
	codeRun := subRuns[0]
	if codeRun.State != model.ReactRunStateFinished {
		t.Fatalf("code-agent 子 run 应为 finished: %s", codeRun.State)
	}

	// 子 run 内真实发生过检索：工具结果里有 ws_ 前缀工具的执行痕迹。
	messages, _ := model.GetReactMessagesByRunID(ctx, codeRun.RunID)
	searched := false
	for _, message := range messages {
		if message.MessageType == model.ReactMessageTypeToolResult && strings.Contains(message.ContentJSON, "ws_react-base-service") {
			searched = true
		}
	}
	if !searched {
		t.Fatal("子 run 应执行过 ws_react-base-service_ 前缀的检索工具")
	}

	// 最终回复给出定位结论（函数语义：拼子 Agent 事件路径 main/<agent>）。
	subFinal := runFinalContent(t, ctx, codeRun.RunID)
	if !strings.Contains(subFinal, "delegate") || !strings.Contains(subFinal, "main/") {
		t.Fatalf("code-agent 回复应包含函数定位结论: %s", subFinal)
	}

	// run 终态后：工作区目录清空、工具副本移除、MCP 客户端已停。
	allocRoot := conf.GetReactRuntimeConfig().Workspace.RootDir
	if entries, err := os.ReadDir(allocRoot); err == nil && len(entries) > 0 {
		t.Fatalf("run 结束后工作区根目录应为空: %v", entries)
	}
	if tools, err := model.ListToolsByType(ctx, "mcp"); err == nil {
		for _, tool := range tools {
			if strings.HasPrefix(tool.Name, "ws_react-base-service") {
				t.Fatalf("run 结束后 ws_ 工具副本应被清理: %s", tool.Name)
			}
		}
	}
	if workspace.Default() == nil {
		t.Fatal("workspace manager 不可用")
	}
	t.Logf("工作区 e2e 通过: code-agent 回复=%s", truncateForLog(subFinal, 200))
}

func truncateForLog(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
