package agent

import (
	"encoding/json"
	"strings"

	"react-base-service/components/route"
	model "react-base-service/models/llm"

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
}

// EffectiveMaxSteps 返回子 Run 最终步数上限；Agent 未配置时使用 Runtime 默认值。
func (p RuntimePolicy) EffectiveMaxSteps(defaultMaxSteps int) int {
	if p.MaxSteps > 0 {
		return p.MaxSteps
	}
	return defaultMaxSteps
}

// FindVisible 返回指定 caller/路由下当前可见的 Agent 定义。
func (r *Runtime) FindVisible(ctx *gin.Context, callerKey string, routeValues []string) ([]model.Agent, error) {
	return model.FindAgentsByCallerAndRoutes(ctx, callerKey, route.BuildRoutePrefixes(routeValues))
}

// Resolve 实时解析一个可见 Agent；nil,nil 表示当前作用域下不存在该 agentKey。
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
// 白名单为空保持原快照，表示继承 caller 全部可见工具；非空按 name/toolId 匹配。
func (p RuntimePolicy) FilterToolIndexSnapshot(snapshotJSON string) string {
	if len(p.ToolRefs) == 0 {
		return snapshotJSON
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
		}
	}
	data, _ := json.Marshal(filtered)
	return string(data)
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
