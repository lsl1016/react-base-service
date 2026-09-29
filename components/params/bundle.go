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

// BundleBrowseReq Bundle 来源目录浏览请求（安装表单的目录选择器）。
type BundleBrowseReq struct {
	// Path 为空 = 返回白名单根目录清单；非空 = 白名单内已存在的目录，返回其一层子目录。
	Path string `json:"path"`
}

// BundleDirItem 可选目录项。
type BundleDirItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// BundleBrowseResp Bundle 来源目录浏览结果。
type BundleBrowseResp struct {
	// Current 是当前所在目录（空 = 白名单根列表这一伪层级）。
	Current string `json:"current"`
	// Parent 是上一级目录（根列表层为空；等于根目录时为空，由前端隐藏返回按钮）。
	Parent string `json:"parent"`
	Dirs   []BundleDirItem `json:"dirs"`
}
