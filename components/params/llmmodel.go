package params

// WhitelistResp 白名单响应
type WhitelistResp struct {
	Users []string `json:"users"`
}

// ========== 模型管理接口 DTO ==========

// ConnCheckReq 连通性检测请求
type ConnCheckReq struct {
	ModelKey     string `json:"modelKey" binding:"required"`
	ModelVersion string `json:"modelVersion" binding:"required"`
	ApiKey       string `json:"apiKey" binding:"required"`
	// ApiURL 是可选的自定义接入面（模型配置面板填写）；空=走 api.yaml 全局端点。
	ApiURL string `json:"apiUrl,omitempty"`
}

// CreateUserModelReq 创建模型请求
type CreateUserModelReq struct {
	ModelName         string   `json:"modelName" binding:"required"`
	ModelKey          string   `json:"modelKey" binding:"required"`
	ModelVersion      string   `json:"modelVersion" binding:"required"`
	// ApiKey 与 ConnectionID 二选一：引用连接时可不填 key（binding 的 required 由 service 兜底校验）。
	ApiKey            string   `json:"apiKey"`
	BizScenes         []string `json:"bizScenes" binding:"required,min=1"`
	IsPlatformDefault int      `json:"isPlatformDefault"`
	// ConnectionID 引用的 LLM 连接；非 0 时凭证/端点/协议以连接为准。
	ConnectionID uint `json:"connectionId,omitempty"`
	// ApiURL 自定义接入面（base url）；空=走 api.yaml 全局端点。
	ApiURL string `json:"apiUrl,omitempty"`
	// ContextTokens 上下文容量（token）；0=回退模型目录。
	ContextTokens int `json:"contextTokens,omitempty"`
	// MaxOutputTokens 单次最大输出 token；0=回退端点/目录/内置默认。
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	// 能力开关：1=支持。决定 thinking 参数是否随请求下发与前端选项透出。
	SupportThinking int `json:"supportThinking,omitempty"`
	SupportTools    int `json:"supportTools,omitempty"`
	SupportVision   int `json:"supportVision,omitempty"`
}

// UpdateUserModelReq 编辑模型请求
type UpdateUserModelReq struct {
	ID                uint     `json:"id" binding:"required"`
	ModelName         string   `json:"modelName"`
	ModelKey          string   `json:"modelKey"`
	ModelVersion      string   `json:"modelVersion"`
	ApiKey            string   `json:"apiKey"`
	BizScenes         []string `json:"bizScenes" binding:"omitempty,min=1"`
	IsPlatformDefault int      `json:"isPlatformDefault"`
	// ConnectionID 引用的 LLM 连接；0=自包含模式。编辑时始终以请求值为准（可清空回到自带 key）。
	ConnectionID *uint `json:"connectionId,omitempty"`
	// ApiURL 自定义接入面（base url）；空=走 api.yaml 全局端点。
	ApiURL string `json:"apiUrl,omitempty"`
	// ContextTokens 上下文容量（token）；0=回退模型目录。
	ContextTokens int `json:"contextTokens,omitempty"`
	// MaxOutputTokens 单次最大输出 token；0=回退端点/目录/内置默认。
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	// 能力开关：1=支持。
	SupportThinking int `json:"supportThinking,omitempty"`
	SupportTools    int `json:"supportTools,omitempty"`
	SupportVision   int `json:"supportVision,omitempty"`
}

// DeleteUserModelReq 删除模型请求
type DeleteUserModelReq struct {
	ID uint `json:"id" binding:"required"`
}

// GetUserModelDetailReq 模型详情请求
type GetUserModelDetailReq struct {
	ID uint `json:"id" binding:"required"`
}

// ListUserModelsReq 模型列表请求
type ListUserModelsReq struct {
	BizScene  string `json:"bizScene"`
	ModelName string `json:"modelName"`
}

// UserModelItem 模型列表响应项（API Key 脱敏）
type UserModelItem struct {
	ID                uint     `json:"id"`
	ModelHash         string   `json:"modelHash"`
	UserName          string   `json:"userName"`
	ModelName         string   `json:"modelName"`
	ModelKey          string   `json:"modelKey"`
	ModelVersion      string   `json:"modelVersion"`
	ApiKey            string   `json:"apiKey"`
	BizScenes         []string `json:"bizScenes"`
	// ConnectionID 非 0 表示引用连接（key/端点以连接为准，apiKey 字段为空）。
	ConnectionID      uint     `json:"connectionId,omitempty"`
	ApiURL            string   `json:"apiUrl,omitempty"`
	ContextTokens     int      `json:"contextTokens,omitempty"`
	MaxOutputTokens   int      `json:"maxOutputTokens,omitempty"`
	SupportThinking   int      `json:"supportThinking,omitempty"`
	SupportTools      int      `json:"supportTools,omitempty"`
	SupportVision     int      `json:"supportVision,omitempty"`
	IsPlatformDefault int      `json:"isPlatformDefault"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
	BaseCredits       *int     `json:"baseCredits,omitempty"`
	BonusCredits      *int     `json:"bonusCredits,omitempty"`
}

// ========== 连接管理接口 DTO ==========

// CreateConnectionReq 创建连接请求
type CreateConnectionReq struct {
	Name        string   `json:"name" binding:"required"`
	Protocol    string   `json:"protocol" binding:"required"` // openai | anthropic
	BaseURL     string   `json:"baseUrl" binding:"required"`
	ApiKey      string   `json:"apiKey" binding:"required"`
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
}

// UpdateConnectionReq 编辑连接请求（key 留空 = 保持原值）
type UpdateConnectionReq struct {
	ID          uint     `json:"id" binding:"required"`
	Name        string   `json:"name"`
	Protocol    string   `json:"protocol"`
	BaseURL     string   `json:"baseUrl"`
	ApiKey      string   `json:"apiKey"`
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
	Status      *int     `json:"status"`
}

// DeleteConnectionReq 删除连接请求
type DeleteConnectionReq struct {
	ID uint `json:"id" binding:"required"`
}

// ConnectionItem 连接响应项（key 脱敏）
type ConnectionItem struct {
	ID          uint     `json:"id"`
	Name        string   `json:"name"`
	Protocol    string   `json:"protocol"`
	BaseURL     string   `json:"baseUrl"`
	ApiKey      string   `json:"apiKey"` // 脱敏展示；编辑时留空=保持原值
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
	Status      int      `json:"status"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	// ModelCount 引用该连接的用户模型数
	ModelCount int64 `json:"modelCount"`
}

// ConnectionCheckReq 连接级连通性检测请求
type ConnectionCheckReq struct {
	ID uint `json:"id" binding:"required"`
}

// ConnectionModelsReq 拉取连接可用模型列表请求（服务端代理 GET {base}/models）
type ConnectionModelsReq struct {
	ID uint `json:"id" binding:"required"`
}

// ConnectionModelsResp 模型发现响应（模型 ID + 能力目录自动填充的能力位）
type ConnectionModelsResp struct {
	Models []ConnectionModelItem `json:"models"`
}

// ConnectionModelItem 单个可用模型：能力位来自本地能力目录（LookupModelCapabilities），
// 未命中目录时三项全 0（保守），批量启用后可在「模型与参数」tab 手动修正。
type ConnectionModelItem struct {
	ID string `json:"id"`
	// 能力位：1=支持（目录命中自动填充）；0=未声明或目录未命中。
	SupportTools    int `json:"supportTools"`
	SupportThinking int `json:"supportThinking"`
	SupportVision   int `json:"supportVision"`
	// ContextTokens/MaxOutputTokens 目录声明的容量；0=未知（回退模型目录/默认）。
	ContextTokens   int `json:"contextTokens,omitempty"`
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

// ========== 积分管理接口 DTO ==========

// AdjustCreditsReq 积分调整请求
type AdjustCreditsReq struct {
	UserName  string `json:"userName" binding:"required"`
	ModelHash string `json:"modelHash" binding:"required"`
	Delta     int    `json:"delta" binding:"required"`
}

// ModelsResp 可用模型列表响应
type ModelsResp struct {
	Models []ModelInfo `json:"models"`
	// Vendors 是用户模型可选择的厂商枚举（新枚举，/model/create 校验口径同源）；
	// 与 Models（模型目录 key）分开：目录 key 用于客户端路由，Vendors 用于模型注册。
	Vendors []string `json:"vendors,omitempty"`
}

// ModelInfo 模型信息
type ModelInfo struct {
	Key            string   `json:"key"`
	Versions       []string `json:"versions"`
	DefaultVersion string   `json:"defaultVersion"`
}
