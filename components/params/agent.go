package params

// CreateAgentReq 子 Agent 创建请求
type CreateAgentReq struct {
	AgentKey        string   `json:"agentKey" binding:"required"`
	Name            string   `json:"name" binding:"required"`
	Description     string   `json:"description" binding:"required"`
	CallerKey       string   `json:"callerKey" binding:"required"`
	RouteValues     []string `json:"routeValues"`
	SystemPrompt    string   `json:"systemPrompt" binding:"required"`
	ModelKey        string   `json:"modelKey"`
	ModelVersion    string   `json:"modelVersion"`
	Tools           []string `json:"tools"`
	Skills          []string `json:"skills"`
	MaxSteps        int      `json:"maxSteps"`
	MaxTokensPerRun int      `json:"maxTokensPerRun"`
	ReadOnly        *int     `json:"readOnly"`
	PermissionMode  string   `json:"permissionMode"`
	Status          *int     `json:"status" binding:"required"`
}

// UpdateAgentReq 子 Agent 更新请求
type UpdateAgentReq struct {
	AgentID         string   `json:"agentId" binding:"required"`
	AgentKey        string   `json:"agentKey" binding:"required"`
	Name            string   `json:"name" binding:"required"`
	Description     string   `json:"description" binding:"required"`
	RouteValues     []string `json:"routeValues"`
	SystemPrompt    *string  `json:"systemPrompt"`
	ModelKey        *string  `json:"modelKey"`
	ModelVersion    *string  `json:"modelVersion"`
	Tools           []string `json:"tools"`
	Skills          []string `json:"skills"`
	MaxSteps        *int     `json:"maxSteps"`
	MaxTokensPerRun *int     `json:"maxTokensPerRun"`
	ReadOnly        *int     `json:"readOnly"`
	PermissionMode  *string  `json:"permissionMode"`
	Status          *int     `json:"status" binding:"required"`
}

// DeleteAgentReq 子 Agent 删除请求
type DeleteAgentReq struct {
	AgentID string `json:"agentId" binding:"required"`
}

// ListAgentsReq 子 Agent 列表请求；callerKey 为空表示跨 caller 列出全部（管理控制台「全部」视图）。
type ListAgentsReq struct {
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues" binding:"required"`
}

// AgentDetailReq 子 Agent 详情请求
type AgentDetailReq struct {
	AgentID string `json:"agentId" binding:"required"`
}

// ImportAgentReq 子 Agent Markdown 定义导入请求：
// markdown 为「frontmatter + 正文」格式，frontmatter 字段与 CreateAgentReq 同名（yaml 风格），
// 正文即 system_prompt；frontmatter 中的 callerKey/routeValues 缺省时取请求体字段。
type ImportAgentReq struct {
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
	Status      *int     `json:"status"`
	Markdown    string   `json:"markdown" binding:"required"`
}

// AgentResp 子 Agent 响应
type AgentResp struct {
	AgentID         string   `json:"agentId"`
	AgentKey        string   `json:"agentKey"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	CallerKey       string   `json:"callerKey"`
	RouteValues     []string `json:"routeValues"`
	SystemPrompt    string   `json:"systemPrompt"`
	ModelKey        string   `json:"modelKey"`
	ModelVersion    string   `json:"modelVersion"`
	Tools           []string `json:"tools"`
	Skills          []string `json:"skills"`
	MaxSteps        int      `json:"maxSteps"`
	MaxTokensPerRun int      `json:"maxTokensPerRun"`
	ReadOnly        int      `json:"readOnly"`
	PermissionMode  string   `json:"permissionMode"`
	Status          int      `json:"status"`
	// Source 是定义来源：builtin=代码内置 profile（不可编辑/删除，仅可 fork）；
	// 空 = DB 注册行（custom）。
	Source          string   `json:"source,omitempty"`
	CreatedBy       string   `json:"createdBy"`
	UpdatedBy       string   `json:"updatedBy"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}
