package mcpgateway

import (
	"strings"

	"react-base-service/conf"
)

// defaultInstructions 通用规则，适用于全部工具，不绑定任何具体平台
//（自 mcp-server 移植；可经 custom.yaml 的 mcp_server.instructions 覆盖整段文案）。
const defaultInstructions = `通用规则：
- 当用户要查询数据或执行操作、且可用工具覆盖该场景时，优先使用工具，不要凭记忆编造答案。
- 不要向用户暴露底层 HTTP method/path，除非用户明确要求看底层接口细节。
- 缺少必填参数时，不要猜测，先一次性向用户追问补齐。
- 如果 list/search 类工具与 detail/operation 类工具配套，先检索定位对象，再复用前一步结果里的关键标识（ID、token 等）继续调用，不要重复向用户索取。
- 如果前一个工具结果里已经返回了可复用的标识，后续调用直接复用。
- 如果查询结果有多个候选，不要自动替用户选一个；先返回候选摘要，让用户确认。
- 查看、搜索、分析、解释类需求优先使用只读工具；创建、修改、删除、发布、部署、重启、改配置等需求视为写操作。
- 写操作在用户意图不明确时必须先确认；不要编造缺失的参数值。
- 工具返回错误时，阅读错误文案并改正参数后重试；多次失败则如实告知用户，不要无限重试。`

// buildInstructions 拼装完整的 server instructions；配置存在时优先使用配置文案。
func buildInstructions() string {
	if custom := strings.TrimSpace(conf.CustomConf.MCPServer.Instructions); custom != "" {
		return custom
	}
	return defaultInstructions
}
