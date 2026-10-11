package params

// UpdateSubAgentSettingReq subagent 委派策略更新请求。
// 字段级覆盖语义：指针为 nil = 该字段不修改（保留现状），非 nil = 覆盖；
// 想清除覆盖回落 yaml 时传对应字段的 "clear" 约定由 service 层处理（enabled 等布尔字段
// 无法用 nil/false 区分「未传」与「关闭」，故布尔关闭=显式传 false，数字清空见 ClearFields）。
type UpdateSubAgentSettingReq struct {
	// AppKey 应用维度：空=写全局行；非空=写该 app 的专属覆盖行（优先于全局）。
	AppKey string `json:"appKey"`
	Enabled         *bool `json:"enabled"`
	MaxParallel     *int  `json:"maxParallel"`
	DefaultMaxSteps *int  `json:"defaultMaxSteps"`
	MaxDepth        *int  `json:"maxDepth"`
	// ClearFields 是要清除覆盖、回落 yaml/默认值的字段名列表（enabled/maxParallel/defaultMaxSteps/maxDepth）。
	ClearFields []string `json:"clearFields"`
}

// SubAgentSettingFieldResp 单个设置字段的生效视图：value 是当前生效值，
// source 标识取值来源（override=DB 覆盖 / yaml=custom.yaml / default=内置默认），
// baseline 是清除该字段覆盖后将生效的回落值（面板据此预览「关掉覆盖再保存」的结果）。
type SubAgentSettingFieldResp struct {
	Value    interface{} `json:"value"`
	Source   string      `json:"source"`
	Baseline interface{} `json:"baseline,omitempty"`
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
	// AppKey 应用维度：空=写全局行；非空=写该 app 的专属覆盖行（优先于全局）。
	AppKey string `json:"appKey"`
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

// UpdateMemorySettingReq 长期记忆策略更新请求（/setting/memory/update）。
// 字段级覆盖语义与 subagent 一致：指针为 nil = 不修改；清除覆盖走 ClearFields。
type UpdateMemorySettingReq struct {
	// AppKey 应用维度：空=写全局行；非空=写该 app 的专属覆盖行（优先于全局）。
	AppKey string `json:"appKey"`
	Enabled             *bool `json:"enabled"`
	ResidentMaxItems    *int  `json:"residentMaxItems"`
	ResidentBudgetChars *int  `json:"residentBudgetChars"`
	IndexMaxItems       *int  `json:"indexMaxItems"`
	DetachedMaxItems    *int  `json:"detachedMaxItems"`
	AllowUserScope      *bool `json:"allowUserScope"`
	// ClearFields 是要清除覆盖、回落 yaml/默认值的字段名列表
	//（enabled/residentMaxItems/residentBudgetChars/indexMaxItems/detachedMaxItems/allowUserScope）。
	ClearFields []string `json:"clearFields"`
}

// MemorySettingResp 长期记忆策略响应：effective 是合并后的生效值（与引擎实际消费口径一致），
// override 是 DB 当前覆盖字段（null=未覆盖），另附审计信息供面板展示。
type MemorySettingResp struct {
	Effective struct {
		Enabled             bool `json:"enabled"`
		ResidentMaxItems    int  `json:"residentMaxItems"`
		ResidentBudgetChars int  `json:"residentBudgetChars"`
		IndexMaxItems       int  `json:"indexMaxItems"`
		DetachedMaxItems    int  `json:"detachedMaxItems"`
		AllowUserScope      bool `json:"allowUserScope"`
	} `json:"effective"`
	Sources map[string]SubAgentSettingFieldResp `json:"sources"`
	Override *struct {
		Enabled             *bool `json:"enabled"`
		ResidentMaxItems    *int  `json:"residentMaxItems"`
		ResidentBudgetChars *int  `json:"residentBudgetChars"`
		IndexMaxItems       *int  `json:"indexMaxItems"`
		DetachedMaxItems    *int  `json:"detachedMaxItems"`
		AllowUserScope      *bool `json:"allowUserScope"`
	} `json:"override"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
}

// UpdateWebSettingReq 联网能力总开关更新请求（/setting/web/update）。
// 一枚开关同时作用于 web_fetch 与 web_search；字段级覆盖语义与 subagent 一致：
// 布尔显式传 false 即关闭，清除覆盖回落 yaml 走 ClearFields。
type UpdateWebSettingReq struct {
	// AppKey 应用维度：空=写全局行；非空=写该 app 的专属覆盖行（优先于全局）。
	AppKey string `json:"appKey"`
	Enabled *bool `json:"enabled"`
	// ClearFields 是要清除覆盖、回落 yaml/默认值的字段名列表（enabled）。
	ClearFields []string `json:"clearFields"`
}

// WebSettingResp 联网能力响应：effective 是合并后的生效值（与引擎实际消费口径一致，
// enabled 为 web_fetch / web_search 共用的开关值）；Configured 标识 web_search 连接参数
//（kind=searxng + base_url）是否完整——开关开启但 Configured=false 时 web_search 不会注册
//（半配置状态，web_fetch 不受影响），面板据此展示警示。
type WebSettingResp struct {
	Effective struct {
		Enabled    bool `json:"enabled"`
		Configured bool `json:"configured"`
	} `json:"effective"`
	Sources map[string]SubAgentSettingFieldResp `json:"sources"`
	Override *struct {
		Enabled *bool `json:"enabled"`
	} `json:"override"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
}

// UpdateWorkspaceSettingReq 检索代码和工作区开关更新请求（全局设置，无 app 维度——
// 内置工具不按 app 隔离）。
type UpdateWorkspaceSettingReq struct {
	// Enabled 是否启用检索代码和工作区；布尔字段显式传 false 即关闭，清除走 clearFields。
	Enabled *bool `json:"enabled"`
	// ClearFields 清除覆盖回落 yaml/默认值的字段名（当前仅 enabled）。
	ClearFields []string `json:"clearFields"`
}

// WorkspaceSettingFieldResp 单字段生效视图。
type WorkspaceSettingFieldResp struct {
	Value  bool   `json:"value"`
	Source string `json:"source"`
}

// WorkspaceSettingResp 检索代码和工作区开关生效视图。
type WorkspaceSettingResp struct {
	Effective struct {
		Enabled bool `json:"enabled"`
		// ResolversConfigured 是基础设施可用性（解析表是否配置了服务）：
		// 开关已开但此处为 false 时 load_runtime_code 仍不会注册。
		ResolversConfigured bool `json:"resolversConfigured"`
	} `json:"effective"`
	Sources map[string]WorkspaceSettingFieldResp `json:"sources"`
	Override *struct {
		Enabled *bool `json:"enabled"`
	} `json:"override,omitempty"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
}
