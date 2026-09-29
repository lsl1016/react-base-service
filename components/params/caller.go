package params

// CopyCallerTarget 描述复制配置时要创建的目标 Caller。
type CopyCallerTarget struct {
	CallerKey   string `json:"callerKey" binding:"required,max=32"`
	Name        string `json:"name" binding:"required,max=128"`
	Description string `json:"description"`
	Platform    string `json:"platform" binding:"required,max=32"`
}

// CopyCallerConfigReq Caller 配置复制请求。
type CopyCallerConfigReq struct {
	SourceCallerKey string           `json:"sourceCallerKey" binding:"required,max=32"`
	TargetCaller    CopyCallerTarget `json:"targetCaller" binding:"required"`
}

// CallerResourceStats Caller 聚合资源操作数量。
type CallerResourceStats struct {
	Callers          int64 `json:"callers,omitempty"`
	Skills           int64 `json:"skills"`
	SystemPrompts    int64 `json:"systemPrompts"`
	Tools            int64 `json:"tools"`
	ToolUserPolicies int64 `json:"toolUserPolicies"`
	ApiKeys          int64 `json:"apiKeys"`
}

// CopyCallerConfigResp Caller 配置复制结果。
type CopyCallerConfigResp struct {
	TargetCallerKey string              `json:"targetCallerKey"`
	Copied          CallerResourceStats `json:"copied"`
}

// DeleteCallerReq 单个 Caller 及其聚合资源删除请求。
type DeleteCallerReq struct {
	CallerKey string `json:"callerKey" binding:"required,max=32"`
}

// DeleteCallerResp 单个 Caller 及其聚合资源删除结果。
type DeleteCallerResp struct {
	CallerKey string              `json:"callerKey"`
	Deleted   CallerResourceStats `json:"deleted"`
}
