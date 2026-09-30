package params

// BundleInstallReq Bundle 安装请求（P3 插件包）。
type BundleInstallReq struct {
	// Source 是安装来源：内部 git URL 或本地路径，必须命中 llm.react.bundle.allowed_source_prefixes 白名单。
	Source string `json:"source" binding:"required"`
	// Ref 是 git 来源的分支/tag/commit（空=HEAD）；本地路径来源忽略。
	Ref string `json:"ref"`
	// RepoPath 是包内相对子目录（monorepo 场景），禁止绝对路径与 ..。
	RepoPath string `json:"repoPath"`
	// CallerKey 是包内 agent/skill 未在 frontmatter 声明 caller_key 时的兜底归属。
	CallerKey string `json:"callerKey" binding:"required"`
	// RouteValues 是 agent/skill 的兜底路由。
	RouteValues []string `json:"routeValues"`
}

// BundleUninstallReq Bundle 卸载请求（回滚：新建资源软删、覆盖资源按安装前快照恢复）。
type BundleUninstallReq struct {
	Name string `json:"name" binding:"required"`
}

// BundleListItemResp 已安装 Bundle 列表项。
type BundleListItemResp struct {
	BundleID       string `json:"bundleId"`
	Name           string `json:"name"`
	Version        string `json:"version"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	ResolvedRef    string `json:"resolvedRef"`
	AgentCount     int    `json:"agentCount"`
	SkillCount     int    `json:"skillCount"`
	McpServerCount int    `json:"mcpServerCount"`
	InstalledBy    string `json:"installedBy"`
	InstalledAt    string `json:"installedAt"`
}

// BundleResp Bundle 安装结果（复用列表项形态）。
type BundleResp = BundleListItemResp

// BundleUploadResp Bundle 目录上传暂存结果（path 直接作为 install source 使用）。
type BundleUploadResp struct {
	// Path 是服务端暂存目录（bundle 根，已预检可解析），填入 install 的 source。
	Path string `json:"path"`
	// Name 是所选目录名（前端回显用）。
	Name string `json:"name"`
}

