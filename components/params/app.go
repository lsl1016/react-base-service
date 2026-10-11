package params

// CopyAppTarget 描述复制配置时要创建的目标 App。
type CopyAppTarget struct {
	AppKey   string `json:"appKey" binding:"required,max=32"`
	Name        string `json:"name" binding:"required,max=128"`
	Description string `json:"description"`
	Platform    string `json:"platform" binding:"required,max=32"`
}

// CopyAppConfigReq App 配置复制请求。
type CopyAppConfigReq struct {
	SourceAppKey string           `json:"sourceAppKey" binding:"required,max=32"`
	TargetApp    CopyAppTarget `json:"targetApp" binding:"required"`
}

// AppResourceStats App 聚合资源操作数量。
type AppResourceStats struct {
	Apps          int64 `json:"apps,omitempty"`
	Skills           int64 `json:"skills"`
	SystemPrompts    int64 `json:"systemPrompts"`
	Tools            int64 `json:"tools"`
	ToolUserPolicies int64 `json:"toolUserPolicies"`
	ApiKeys          int64 `json:"apiKeys"`
}

// CopyAppConfigResp App 配置复制结果。
type CopyAppConfigResp struct {
	TargetAppKey string              `json:"targetAppKey"`
	Copied          AppResourceStats `json:"copied"`
}

// DeleteAppReq 单个 App 及其聚合资源删除请求。
type DeleteAppReq struct {
	AppKey string `json:"appKey" binding:"required,max=32"`
}

// DeleteAppResp 单个 App 及其聚合资源删除结果。
type DeleteAppResp struct {
	AppKey string              `json:"appKey"`
	Deleted   AppResourceStats `json:"deleted"`
}
