package agent

import (
	"encoding/json"
	"strings"

	"react-base-service/components/route"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"

	"github.com/gin-gonic/gin"
)

// Runtime 是 Agent 运行期解析器；管理 CRUD 与运行期发现/策略解析通过同一 service 包隔离于 ReAct 核心。
type Runtime struct{}

var defaultRuntime = &Runtime{}

func DefaultRuntime() *Runtime {
	return defaultRuntime
}

// RuntimePolicy 是 Agent 定义在运行期的能力策略视图。
// 注意保持现有兼容语义：tools 为空=继承 caller 全部可见工具；skills 为空=不注入任何 Skill。
type RuntimePolicy struct {
	ToolRefs        []string
	SkillRefs       []string
	PermissionMode  string
	MaxSteps        int
	MaxTokensPerRun int
	// ReadOnly 是只读执行域声明（WP2）：子 run 内非 readOnly 业务工具被硬拦截。
	// 单调收紧由调用方保证（父 run 开关 || 本定义声明），Policy 本身只透传定义值。
	ReadOnly bool
}

// EffectiveMaxSteps 返回子 Run 最终步数上限；Agent 未配置时使用 Runtime 默认值。
func (p RuntimePolicy) EffectiveMaxSteps(defaultMaxSteps int) int {
	if p.MaxSteps > 0 {
		return p.MaxSteps
	}
	return defaultMaxSteps
}

// FindVisible 返回指定 caller/路由下当前可见的 Agent 定义（DB 行 + 未被同名遮蔽的内置 profile）。
// DB 行在前、内置在后；同 agent_key 的 DB 行整体覆盖内置（同名即 fork）。
func (r *Runtime) FindVisible(ctx *gin.Context, callerKey string, routeValues []string) ([]model.Agent, error) {
	dbAgents, err := model.FindAgentsByCallerAndRoutes(ctx, callerKey, route.BuildRoutePrefixes(routeValues))
	if err != nil {
		return nil, err
	}
	return mergeBuiltinAgents(dbAgents), nil
}

// Resolve 实时解析一个可见 Agent（DB > 内置同名覆盖）；nil,nil 表示当前作用域下不存在该 agentKey。
func (r *Runtime) Resolve(ctx *gin.Context, callerKey string, routeValues []string, agentKey string) (*model.Agent, error) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return nil, nil
	}
	agents, err := r.FindVisible(ctx, callerKey, routeValues)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].AgentKey == agentKey {
			agent := agents[i]
			return &agent, nil
		}
	}
	return nil, nil
}

// Policy 把持久化 Agent 定义转换为稳定的运行期策略。
// JSON 解析失败按空列表处理，沿用存量容错行为；permissionMode 统一归一为 inherit/auto/confirm/confirm_risky。
func (r *Runtime) Policy(agent model.Agent) RuntimePolicy {
	return RuntimePolicy{
		ToolRefs:        parseRuntimeReferences(agent.ToolsJSON),
		SkillRefs:       parseRuntimeReferences(agent.SkillsJSON),
		PermissionMode:  normalizePermissionMode(agent.PermissionMode),
		MaxSteps:        agent.MaxSteps,
		MaxTokensPerRun: agent.MaxTokensPerRun,
		ReadOnly:        agent.ReadOnly == 1,
	}
}

type runtimeToolIndexItem struct {
	ToolID string `json:"toolId"`
	Name   string `json:"name"`
}

type runtimeSkillIndexItem struct {
	SkillID string `json:"skillId"`
	Name    string `json:"name"`
}

// FilterToolIndexSnapshot 按 Agent 工具白名单过滤 run 工具索引。
// 白名单为空保持原快照，表示继承 caller 全部可见工具；非空按 name/toolId 匹配，
// 并支持两个约定 token（builtin.go）：
//   - @none：显式不继承任何业务工具（返回空索引）；
//   - @readonly：只保留声明了 readOnly 的业务工具（只读子代理用；tools 提供全量工具定义）。
//
// tools 为 nil 时 @readonly 退化为「token 被忽略、按其余显式名单匹配」——调用方应始终传入全量可见工具。
func (p RuntimePolicy) FilterToolIndexSnapshot(snapshotJSON string, tools []model.Tool) string {
	if len(p.ToolRefs) == 0 {
		return snapshotJSON
	}
	if runtimeReferenceSet(p.ToolRefs)[AgentToolRefNone] {
		return "[]"
	}
	readonlyMode := runtimeReferenceSet(p.ToolRefs)[AgentToolRefReadOnly]
	var readonlyToolIDs map[string]bool
	if readonlyMode {
		readonlyToolIDs = make(map[string]bool, len(tools))
		for _, tool := range tools {
			if toolConfigReadOnly(tool) {
				readonlyToolIDs[tool.ToolID] = true
			}
		}
	}
	var items []runtimeToolIndexItem
	if err := json.Unmarshal([]byte(snapshotJSON), &items); err != nil {
		return "[]"
	}
	allowed := runtimeReferenceSet(p.ToolRefs)
	filtered := make([]runtimeToolIndexItem, 0, len(items))
	for _, item := range items {
		if allowed[item.Name] || allowed[item.ToolID] {
			filtered = append(filtered, item)
			continue
		}
		if readonlyMode && readonlyToolIDs[item.ToolID] {
			filtered = append(filtered, item)
		}
	}
	data, _ := json.Marshal(filtered)
	return string(data)
}

// toolConfigReadOnly 解析业务工具 config 的 readOnly 声明；解析失败按可写处理（保守）。
func toolConfigReadOnly(tool model.Tool) bool {
	cfg, err := toolService.ParseToolConfig(tool.Config)
	if err != nil {
		return false
	}
	return cfg.ReadOnly
}

// FilterSkillIndexSnapshot 按 Agent Skill 白名单过滤 run Skill 索引。
// 白名单为空不注入任何 Skill，保持现有专家子 Agent 隔离语义；非空按 name/skillId 匹配。
func (p RuntimePolicy) FilterSkillIndexSnapshot(snapshotJSON string) string {
	if len(p.SkillRefs) == 0 {
		return "[]"
	}
	var items []runtimeSkillIndexItem
	if err := json.Unmarshal([]byte(snapshotJSON), &items); err != nil {
		return "[]"
	}
	allowed := runtimeReferenceSet(p.SkillRefs)
	filtered := make([]runtimeSkillIndexItem, 0, len(items))
	for _, item := range items {
		if allowed[item.Name] || allowed[item.SkillID] {
			filtered = append(filtered, item)
		}
	}
	data, _ := json.Marshal(filtered)
	return string(data)
}

func parseRuntimeReferences(raw string) []string {
	var values []string
	_ = json.Unmarshal([]byte(raw), &values)
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func runtimeReferenceSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	return set
}

// FindVisibleForRuntime/ResolveForRuntime 保留函数入口，兼容现有调用与测试。
func FindVisibleForRuntime(ctx *gin.Context, callerKey string, routeValues []string) ([]model.Agent, error) {
	return defaultRuntime.FindVisible(ctx, callerKey, routeValues)
}

func ResolveForRuntime(ctx *gin.Context, callerKey string, routeValues []string, agentKey string) (*model.Agent, error) {
	return defaultRuntime.Resolve(ctx, callerKey, routeValues, agentKey)
}
