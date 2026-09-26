package params

// UpdateSubAgentSettingReq subagent 委派策略更新请求。
// 字段级覆盖语义：指针为 nil = 该字段不修改（保留现状），非 nil = 覆盖；
// 想清除覆盖回落 yaml 时传对应字段的 "clear" 约定由 service 层处理（enabled 等布尔字段
// 无法用 nil/false 区分「未传」与「关闭」，故布尔关闭=显式传 false，数字清空见 ClearFields）。
type UpdateSubAgentSettingReq struct {
	Enabled         *bool `json:"enabled"`
	MaxParallel     *int  `json:"maxParallel"`
	DefaultMaxSteps *int  `json:"defaultMaxSteps"`
	MaxDepth        *int  `json:"maxDepth"`
	// ClearFields 是要清除覆盖、回落 yaml/默认值的字段名列表（enabled/maxParallel/defaultMaxSteps/maxDepth）。
	ClearFields []string `json:"clearFields"`
}

// SubAgentSettingFieldResp 单个设置字段的生效视图：value 是当前生效值，
// source 标识取值来源（override=DB 覆盖 / yaml=custom.yaml / default=内置默认）。
type SubAgentSettingFieldResp struct {
	Value  interface{} `json:"value"`
	Source string      `json:"source"`
}

// SubAgentSettingResp subagent 委派策略响应：effective 是合并后的生效值（与引擎实际消费口径一致），
// override 是 DB 当前覆盖字段（null=未覆盖），另附审计信息供面板展示。
type SubAgentSettingResp struct {
	Effective struct {
		Enabled         bool `json:"enabled"`
		MaxParallel     int  `json:"maxParallel"`
		DefaultMaxSteps int  `json:"defaultMaxSteps"`
		MaxDepth        int  `json:"maxDepth"`
	} `json:"effective"`
	Sources map[string]SubAgentSettingFieldResp `json:"sources"`
	// Override 是 DB 覆盖原文（指针语义，null=未覆盖）；面板据此区分「当前值从哪来」。
	Override *struct {
		Enabled         *bool `json:"enabled"`
		MaxParallel     *int  `json:"maxParallel"`
		DefaultMaxSteps *int  `json:"defaultMaxSteps"`
		MaxDepth        *int  `json:"maxDepth"`
	} `json:"override"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
}

// UpdateContextCompactSettingReq 上下文压缩策略更新请求（/setting/context/update）。
// 字段级覆盖语义与 subagent 一致：指针为 nil = 不修改；清除覆盖走 ClearFields。
type UpdateContextCompactSettingReq struct {
	Enabled                *bool `json:"enabled"`
	TokenTrigger           *int  `json:"tokenTrigger"`
	TokenTarget            *int  `json:"tokenTarget"`
	SummaryLimit           *int  `json:"summaryLimit"`
	OutputReserveTokens    *int  `json:"outputReserveTokens"`
	BufferTokens           *int  `json:"bufferTokens"`
	MicrocompactEnabled    *bool `json:"microcompactEnabled"`
	MicrocompactKeepRecent *int  `json:"microcompactKeepRecent"`
	// ClearFields 是要清除覆盖、回落 yaml/默认值的字段名列表。
	ClearFields []string `json:"clearFields"`
}

// ContextCompactSettingResp 上下文压缩策略响应：effective 是合并后的生效值
//（与引擎实际消费口径一致），override 是 DB 当前覆盖字段（null=未覆盖）。
type ContextCompactSettingResp struct {
	Effective struct {
		Enabled                bool `json:"enabled"`
		TokenTrigger           int  `json:"tokenTrigger"`
		TokenTarget            int  `json:"tokenTarget"`
		SummaryLimit           int  `json:"summaryLimit"`
		OutputReserveTokens    int  `json:"outputReserveTokens"`
		BufferTokens           int  `json:"bufferTokens"`
		MicrocompactEnabled    bool `json:"microcompactEnabled"`
		MicrocompactKeepRecent int  `json:"microcompactKeepRecent"`
	} `json:"effective"`
	// Sources 逐字段标识取值来源（override=DB 覆盖 / yaml=custom.yaml / default=内置默认）。
	Sources map[string]SubAgentSettingFieldResp `json:"sources"`
	Override *struct {
		Enabled                *bool `json:"enabled"`
		TokenTrigger           *int  `json:"tokenTrigger"`
		TokenTarget            *int  `json:"tokenTarget"`
		SummaryLimit           *int  `json:"summaryLimit"`
		OutputReserveTokens    *int  `json:"outputReserveTokens"`
		BufferTokens           *int  `json:"bufferTokens"`
		MicrocompactEnabled    *bool `json:"microcompactEnabled"`
		MicrocompactKeepRecent *int  `json:"microcompactKeepRecent"`
	} `json:"override"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
}
