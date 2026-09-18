package agent

import (
	"strings"

	"react-base-service/components/route"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// Runtime 是 Agent 运行期解析器；管理 CRUD 与运行期发现通过同一 service 包隔离于 ReAct 核心。
type Runtime struct{}

var defaultRuntime = &Runtime{}

func DefaultRuntime() *Runtime {
	return defaultRuntime
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

// FindVisibleForRuntime/ResolveForRuntime 保留函数入口，兼容现有调用与测试。
func FindVisibleForRuntime(ctx *gin.Context, callerKey string, routeValues []string) ([]model.Agent, error) {
	return defaultRuntime.FindVisible(ctx, callerKey, routeValues)
}

func ResolveForRuntime(ctx *gin.Context, callerKey string, routeValues []string, agentKey string) (*model.Agent, error) {
	return defaultRuntime.Resolve(ctx, callerKey, routeValues, agentKey)
}
