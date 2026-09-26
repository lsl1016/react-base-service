package llmmodel

import "react-base-service/components/params"

// 模型配置参数 schema（声明式下发）：前端模型配置面板据此渲染数值控件的
// min/max/step/default，避免前后端各自硬编码范围。校验常量与 schema 同源。

const (
	// maxModelContextTokensLimit 上下文容量上限（token）。0 = 回退模型目录/全局压缩阈值。
	maxModelContextTokensLimit = 2_000_000
	// maxModelOutputTokensLimit 单次最大输出上限（token）。
	maxModelOutputTokensLimit = 200_000
	// minModelContextTokens 上下文容量输入下限（非 0 值的有效下限）。
	minModelContextTokens = 4096
	// minModelMaxOutputTokens 最大输出输入下限（非 0 值的有效下限）。
	minModelMaxOutputTokens = 1024
)

// MaxModelContextTokensLimit / MaxModelOutputTokensLimit 供外部（校验/展示）读取上限。
const (
	MaxModelContextTokensLimit = maxModelContextTokensLimit
	MaxModelOutputTokensLimit  = maxModelOutputTokensLimit
)

func intPtr(v int) *int { return &v }

// BuildModelConfigParams 组装模型/上下文/思考相关参数 schema。
func BuildModelConfigParams() []params.ConfigParamSchema {
	return []params.ConfigParamSchema{
		{
			Key: "model.contextTokens", Label: "上下文容量", Group: "model", Type: "int",
			Min: intPtr(minModelContextTokens), Max: intPtr(maxModelContextTokensLimit), Step: intPtr(4096),
			Unit: "tokens", Description: "该模型的上下文窗口容量；0=回退模型目录配置，同时作为压缩触发阈值的推导基准",
		},
		{
			Key: "model.maxOutputTokens", Label: "最大输出", Group: "model", Type: "int",
			Min: intPtr(minModelMaxOutputTokens), Max: intPtr(maxModelOutputTokensLimit), Step: intPtr(1024),
			Unit: "tokens", Description: "单次模型调用的最大输出 token；0=回退端点/目录/内置默认",
		},
		{
			Key: "reasoning.budgetTokens", Label: "思考预算", Group: "reasoning", Type: "int",
			Min: intPtr(1024), Max: intPtr(131072), Step: intPtr(1024),
			Unit: "tokens", Description: "自定义思考模式的 thinking 预算（Anthropic 系直接生效，OpenAI 系折算为 effort 档位）",
		},
		{
			Key: "compact.tokenTrigger", Label: "压缩触发阈值", Group: "context", Type: "int",
			Min: intPtr(8192), Max: intPtr(maxModelContextTokensLimit), Step: intPtr(4096),
			Unit: "tokens", Description: "上下文高水位；按模型目录推导有效窗口时该值作为兜底",
		},
		{
			Key: "compact.tokenTarget", Label: "压缩目标水位", Group: "context", Type: "int",
			Min: intPtr(4096), Max: intPtr(maxModelContextTokensLimit), Step: intPtr(4096),
			Unit: "tokens", Description: "压缩后希望降到的目标 token 数，避免下一轮反复压缩",
		},
		{
			Key: "compact.summaryLimit", Label: "压缩摘要上限", Group: "context", Type: "int",
			Min: intPtr(1000), Max: intPtr(64000), Step: intPtr(1000),
			Unit: "chars", Description: "压缩摘要的最大字符数",
		},
		{
			Key: "compact.outputReserveTokens", Label: "输出预留", Group: "context", Type: "int",
			Min: intPtr(1024), Max: intPtr(200000), Step: intPtr(1024),
			Unit: "tokens", Description: "从模型窗口为输出预留的 token 数；有效窗口 = 上下文容量 - 输出预留 - 缓冲",
		},
		{
			Key: "compact.bufferTokens", Label: "安全缓冲", Group: "context", Type: "int",
			Min: intPtr(0), Max: intPtr(100000), Step: intPtr(1024),
			Unit: "tokens", Description: "压缩触发阈值相对有效窗口的安全缓冲",
		},
		{
			Key: "compact.microcompactKeepRecent", Label: "微压缩保留组数", Group: "context", Type: "int",
			Min: intPtr(1), Max: intPtr(50), Step: intPtr(1),
			Description: "微压缩时保留不清理的最近工具结果消息组数",
		},
	}
}
