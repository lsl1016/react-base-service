package react

// load_runtime_code 是 ReAct Runtime 进入代码 Workspace 的薄适配入口。
//
// Workspace 生命周期与引用计数绑定（(service, commit) 粒度共享）：load_runtime_code 只负责
// 请求 workspace.Manager 接入共享工作区并返回动态挂载的代码检索 Tool；实际 Git/worktree/MCP
// 管理由 service/workspace 负责。这样 ReAct Engine 不直接理解文件系统、Git 或仓库协议，
// 只把 Workspace 暴露为一组普通 Business Tool。
//
// load_runtime_code（P2-1 服务端代码 Workspace 的业务入口，方案 §4.2.1）：
//
// 模型（主 Agent 或 code-agent）调用后，workspace.Manager 按静态解析表把目标服务代码
// 接入 (service, commit) 共享 worktree（同一 commit 多 run/caller 复用一份），并动态挂载
// 只读 repo MCP——工具名前缀 ws_<service>_<commit短哈希>_*（list_files / read_file /
// search_code / find_symbol / get_file_symbols / find_references / get_repo_map，其中符号类
// 工具为 go/ast 声明感知实现），完整工具名以本工具返回的 tools 字段为准；
// 后续按普通业务工具两段式使用（get_tool 加载 → execute_tool 执行），引擎零特殊分支。
// run 终态时统一 ReleaseRun 减引用（run() 与 delegate 收尾路径都挂了幂等清理），
// caller 归零清工具副本、总归零才回收 worktree 与 MCP。

import (
	"encoding/json"

	llm "react-base-service/api/llm"
	"react-base-service/service/workspace"
)

func loadRuntimeCodeDefinition() llm.ToolDefinition {
	return objectTool(metaToolLoadRuntimeCode,
		"把目标服务的线上代码加载到只读工作区，并挂载代码检索工具（前缀 ws_<service>_<commit短哈希>_：文件列表/读取/文本检索/Go 声明级符号查找/单文件符号表/引用候选/仓库地图；完整工具名以返回的 tools 字段为准，不要自行拼接）。同一服务同一 commit 全局共享一份快照，多会话并发加载会直接复用。加载完成后用 get_tool 加载具体检索工具、execute_tool 执行。适合定位报错代码、核对线上逻辑、查看近期变更。",
		map[string]interface{}{
			"service": stringSchema("目标服务名（须在 workspace.resolvers 白名单内，如 react-base-service）"),
			"env":     stringSchema("环境标识（如 prod/test，可选；未配置用默认 ref）"),
		})
}

// executeLoadRuntimeCode 为当前 Run 接入/复用 (service, commit) 共享代码工作区。
// 返回的 ws_<service>_<commit8>_* 工具仍必须走 get_tool -> execute_tool 两阶段协议，因此
// Workspace 不需要在 ReAct 主循环中增加新的特殊执行分支；Run 终态由 run()/delegate 收尾
// 统一 ReleaseRun 减引用。
func (s *reactEngineState) executeLoadRuntimeCode(input json.RawMessage) (string, bool, error) {
	var req struct {
		Service string `json:"service"`
		Env     string `json:"env"`
	}
	_ = json.Unmarshal(input, &req)
	if req.Service == "" {
		return "load_runtime_code requires service", true, nil
	}
	alloc, err := workspace.Default().Load(s.ctx, s.runID, s.req.payload.CallerKey, req.Service, req.Env)
	if err != nil {
		return err.Error(), true, nil
	}
	// 工具名带 commit 段，必须以 MCPServerName 为前缀拼接（服务端名/客户端名一致）。
	prefix := alloc.MCPServerName + "_"
	result, _ := json.Marshal(map[string]string{
		"service": alloc.Service,
		"env":     alloc.Env,
		"commit":  alloc.Commit,
		"ref":     alloc.Ref,
		"tools":   prefix + "list_files / " + prefix + "read_file / " + prefix + "search_code / " + prefix + "search_pattern / " + prefix + "find_symbol / " + prefix + "get_file_symbols / " + prefix + "find_references / " + prefix + "get_repo_map",
		"note":    "代码已锁定到该 commit 的只读工作区（同一 commit 多会话共享同一快照）；先 get_tool 加载上述工具再 execute_tool 调用；run 结束自动释放",
	})
	return string(result), false, nil
}
