package react

import (
	"context"

	"react-base-service/conf"
	model "react-base-service/models/llm"
	agentService "react-base-service/service/agent"
	memoryService "react-base-service/service/memory"
	toolService "react-base-service/service/tool"

	"github.com/gin-gonic/gin"
)

// serverToolRuntime 是 Agent Runtime 依赖的服务端 Tool 执行边界。
// 接口定义在 react 侧，具体实现由 service/tool 提供，避免 ReAct 核心感知 HTTP/MCP 传输细节。
type serverToolRuntime interface {
	Execute(ctx context.Context, req toolService.ExecuteRequest) (toolService.ExecuteResult, error)
}

// agentRuntime 是 Agent Runtime 依赖的子 Agent 定义解析边界。
// 子 Run 生命周期仍由 react 负责，resolver 只处理可见性和实时定义解析。
type agentRuntime interface {
	FindVisible(ctx *gin.Context, callerKey string, routeValues []string) ([]model.Agent, error)
	Resolve(ctx *gin.Context, callerKey string, routeValues []string, agentKey string) (*model.Agent, error)
	Policy(agent model.Agent) agentService.RuntimePolicy
}

// memoryRuntime 是 ReAct 对长期记忆的最小依赖面。
// 作用域、查询、渲染、写核心都由 service/memory 负责。
type memoryRuntime interface {
	ResolveScope(callerKey, userName string, allowUserScope bool) memoryService.RuntimeScope
	BuildContext(ctx *gin.Context, callerKey, userName string, cfg conf.ReactMemoryConfig) (string, error)
	List(ctx *gin.Context, scope memoryService.RuntimeScope, options memoryService.RuntimeListOptions) ([]memoryService.RuntimeListItem, error)
	Read(ctx *gin.Context, scope memoryService.RuntimeScope, itemIDs []uint64) ([]memoryService.RuntimeReadItem, error)
	ApplyMutation(ctx *gin.Context, input memoryService.MutationInput) (map[string]interface{}, error)
}

// runtimeServices 集中保存 ReAct Engine 的外部能力依赖。
// Engine 只依赖这些能力边界，不直接绑定 HTTP/MCP、Agent 表路由或 Memory 持久化实现。
type runtimeServices struct {
	serverTools serverToolRuntime
	agents      agentRuntime
	memory      memoryRuntime
}

func defaultRuntimeServices() runtimeServices {
	return runtimeServices{
		serverTools: toolService.DefaultRuntime(),
		agents:      agentService.DefaultRuntime(),
		memory:      memoryService.DefaultRuntime(),
	}
}

// 以下 fallback 兼容测试里手工构造的最小 reactEngineState/runtimeRequest。
func (s runtimeServices) serverToolExecutor() serverToolRuntime {
	if s.serverTools != nil {
		return s.serverTools
	}
	return toolService.DefaultRuntime()
}

func (s runtimeServices) agentResolver() agentRuntime {
	if s.agents != nil {
		return s.agents
	}
	return agentService.DefaultRuntime()
}

func (s runtimeServices) memoryExecutor() memoryRuntime {
	if s.memory != nil {
		return s.memory
	}
	return memoryService.DefaultRuntime()
}
