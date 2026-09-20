package conf

import (
	"slices"
	"strings"

	"react-base-service/golib/base"
	"react-base-service/golib/env"
	"react-base-service/golib/redis"
	"react-base-service/golib/server/http"
	"react-base-service/golib/zlog"
)

var (
	BasicConf TBasic
)

type TBasic struct {
	Pprof  base.PprofConfig
	Log    zlog.LogConfig
	Server http.ServerConfig
}

// ModelVersionLimit 模型版本级能力限制
type ModelVersionLimit struct {
	MaxCompletionTokens int `yaml:"max_completion_tokens"`
}

const (
	defaultReactMaxSteps                    = 8
	defaultReactStreamIdleTimeoutSec        = 120
	defaultReactCompactTokenTrigger         = 170000
	defaultReactCompactTokenTarget          = 90000
	defaultReactCompactTimeoutSec           = 20
	defaultReactCompactSummaryLimit         = 6000
	defaultReactCompactFallbackPreviewLimit = 800
	defaultReactToolResultInlineLimitBytes  = 64 * 1024
	defaultReactToolResultPreviewLimit      = 4 * 1024
	defaultReactToolResultDBTTLSec          = 24 * 60 * 60
	defaultReactToolResultDBMaxBytes        = 16 * 1024 * 1024
	defaultReactToolResultReadLimit         = 8192
	defaultReactToolResultMaxReadLimit      = 32768
	defaultReactMemoryResidentMaxItems      = 16
	defaultReactMemoryResidentBudgetChars   = 2000
	defaultReactMemoryIndexMaxItems         = 64
	defaultReactMemoryDetachedMaxItems      = 500
	defaultReactMemoryReflectionCooldownMin = 60
	defaultReactMemoryReflectionMaxWrites   = 20
	defaultReactMemoryReflectionTranscript  = 16000
	defaultReactSubAgentMaxParallel         = 1
	defaultReactSubAgentMaxSteps            = 8
	defaultReactSubAgentMaxDepth            = 2
	defaultReactGraphMemoryTimeoutMs        = 15000
	defaultReactGraphMemoryInjectMaxFacts   = 8
	defaultReactGraphMemoryInjectMaxChars   = 1200
	defaultReactGraphMemoryInjectTimeoutMs  = 1500
	defaultReactWorkspaceRootDir            = "./data/workspaces"
	defaultReactWorkspaceMirrorDir          = "./data/repo-cache"
	defaultReactWorkspaceGitTimeoutSec      = 180
)

// ReactRuntimeConfig ReAct 运行时配置，只承载线上需要按模型和成本调整的策略参数。
type ReactRuntimeConfig struct {
	// MaxSteps 是请求未显式指定 maxSteps 时的默认最大推理轮次。
	MaxSteps int `yaml:"max_steps"`
	// StreamIdleTimeoutSec 是单次模型流式调用的空闲超时：连续该秒数内未收到任何 chunk 即判定卡死并报错。
	StreamIdleTimeoutSec int `yaml:"stream_idle_timeout_sec"`
	// ContextCompact 控制历史上下文自动压缩的触发水位、目标水位和摘要长度。
	ContextCompact ReactContextCompactConfig `yaml:"context_compact"`
	// ToolResult 控制工具结果回填、落库和 resultRef 分片读取策略。
	ToolResult ReactToolResultConfig `yaml:"tool_result"`
	// Models 控制 ReAct 前端可选模型、默认模型以及组内自动互备策略。
	Models ReactModelsConfig `yaml:"models"`
	// PlaygroundWhitelist 是 playground 页面访问白名单；为空时不限制，所有登录用户均可访问。
	PlaygroundWhitelist []string `yaml:"playground_whitelist"`
	// AllowPlan 控制 create_plan（计划确认）能力；未配置时默认开启。
	AllowPlan *bool `yaml:"allow_plan"`
	// Memory 控制长期记忆（跨会话记忆）能力；未配置时默认关闭。
	Memory ReactMemoryConfig `yaml:"memory"`
	// SubAgent 控制子 Agent 委派能力（delegate_agent）；未配置时默认关闭，
	// 行为与历史版本一致（不注册 delegate_agent、不解析 agent 资源）。
	SubAgent ReactSubAgentConfig `yaml:"subagent"`
	// GraphMemory 控制时序事实图谱记忆（Graphiti，Layer2 长期记忆）；未配置时默认关闭。
	// 依赖外部 Graphiti REST 服务，设计见 docs/知识库与长期记忆集成改造方案.md §4。
	GraphMemory ReactGraphMemoryConfig `yaml:"graph_memory"`
	// Workspace 控制服务端代码工作区（P2-1）：服务代码解析 → bare mirror 缓存 →
	// 每 run git worktree 隔离，并动态挂载只读 repo MCP 工具。
	Workspace ReactWorkspaceConfig `yaml:"workspace"`
	// Bundle 控制 Agent Bundle 插件包安装（P3）：agents/skills/mcp.json 打包展开写入
	// 注册表，同名覆盖可回滚卸载；来源限白名单前缀（内部 git / 本地路径）。
	Bundle ReactBundleConfig `yaml:"bundle"`
}

// ReactBundleConfig Agent Bundle 插件包安装配置。
type ReactBundleConfig struct {
	// Enabled 控制总开关；未配置默认 false（/react/bundle/* 返回未开启）。
	Enabled *bool `yaml:"enabled"`
	// CacheDir 是 bundle 源仓库的 bare mirror 缓存目录。
	CacheDir string `yaml:"cache_dir"`
	// GitTimeoutSec 是单次 git 子操作（clone/fetch/worktree/rev-parse）的超时秒数。
	GitTimeoutSec int `yaml:"git_timeout_sec"`
	// AllowedSourcePrefixes 是安装来源白名单前缀：git URL 与本地路径都必须命中其一，
	// 空列表 = 拒绝一切来源（安全默认）。
	AllowedSourcePrefixes []string `yaml:"allowed_source_prefixes"`
}

// BundleEnabled 解析 bundle.enabled：未配置默认 false。
func (c ReactBundleConfig) BundleEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return false
}

// ReactWorkspaceConfig 服务端代码工作区配置。
type ReactWorkspaceConfig struct {
	// Enabled 控制总开关；未配置默认 false（不注册 load_runtime_code，行为与历史一致）。
	Enabled *bool `yaml:"enabled"`
	// RootDir 是每 run worktree 的分配根目录。
	RootDir string `yaml:"root_dir"`
	// MirrorDir 是 bare mirror 缓存目录（共享对象库，全服务单副本）。
	MirrorDir string `yaml:"mirror_dir"`
	// GitTimeoutSec 是单次 git 子操作（clone/fetch/worktree）的超时秒数。
	GitTimeoutSec int `yaml:"git_timeout_sec"`
	// Resolvers 是静态服务解析表：service 名 → 仓库与各环境的 ref。
	// P2 首版不接公司镜像中心，按此白名单解析；真实链路后续实现 RuntimeSourceResolver 接入。
	Resolvers []ReactWorkspaceResolverConf `yaml:"resolvers"`
}

// ReactWorkspaceResolverConf 描述一个服务的代码来源。
type ReactWorkspaceResolverConf struct {
	// Service 是服务标识（load_runtime_code 入参），仅允许字母数字下划线中划线。
	Service string `yaml:"service"`
	// RepoURL 是 git 仓库地址（https/ssh/file 均可）。
	RepoURL string `yaml:"repo_url"`
	// Refs 是环境 → 分支/commit/tag 的映射；未命中的环境回退 DefaultRef。
	Refs map[string]string `yaml:"refs"`
	// DefaultRef 是缺省 ref（空时用 HEAD 即默认分支）。
	DefaultRef string `yaml:"default_ref"`
}

// WorkspaceEnabled 解析 workspace.enabled：未配置默认 false。
func (c ReactWorkspaceConfig) WorkspaceEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return false
}

// ReactSubAgentConfig 子 Agent 委派配置：主 Agent 经 delegate_agent 把子任务派给
// 注册表中的专家子 Agent，引擎按 agent 定义装配隔离子 run 执行。
type ReactSubAgentConfig struct {
	// Enabled 控制子 Agent 委派总开关；未配置时默认 false。
	Enabled *bool `yaml:"enabled"`
	// MaxParallel 是同一轮多个 delegate_agent 调用的并行上限；默认 1（串行，与历史行为一致）。
	MaxParallel int `yaml:"max_parallel"`
	// DefaultMaxSteps 是 agent 未配置 max_steps 时子 run 的步数上限。
	DefaultMaxSteps int `yaml:"default_max_steps"`
	// MaxDepth 是委派嵌套深度上限（子 Agent 再委派），防递归失控；外层 run 深度为 0。
	MaxDepth int `yaml:"max_depth"`
}

// SubAgentEnabled 解析 subagent.enabled：未配置时默认 false。
func (c ReactSubAgentConfig) SubAgentEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return false
}

// AllowPlanEnabled 解析 allow_plan 配置：未配置时默认 true。
func (c ReactRuntimeConfig) AllowPlanEnabled() bool {
	if c.AllowPlan != nil {
		return *c.AllowPlan
	}
	return true
}

// ReactMemoryConfig 长期记忆配置：常驻层注入预算、目录上限、按需层软上限与作用域开关。
type ReactMemoryConfig struct {
	// Enabled 控制长期记忆总开关；未配置时默认 false（不注入、不注册工具），
	// 显式开启前需先执行 tblLlmMemoryItem/tblLlmMemoryRevision 建表。
	Enabled *bool `yaml:"enabled"`
	// ResidentMaxItems 是单个记忆空间常驻层的条数上限。
	ResidentMaxItems int `yaml:"resident_max_items"`
	// ResidentBudgetChars 是常驻层注入 system 前缀的字符预算，超出按更新时间截断。
	ResidentBudgetChars int `yaml:"resident_budget_chars"`
	// IndexMaxItems 是按需层目录索引注入的最大条数。
	IndexMaxItems int `yaml:"index_max_items"`
	// DetachedMaxItems 是单个记忆空间按需层 active 条目软上限，超出拒绝新增。
	DetachedMaxItems int `yaml:"detached_max_items"`
	// AllowUserScope 控制是否启用 caller_user 维度记忆；未配置时默认 true。
	// false 时全部记忆收敛到 caller 维度（同 caller 用户共享）。
	AllowUserScope *bool `yaml:"allow_user_scope"`
	// Reflection 控制压缩事件后的自动整理（后台受限子 run）。
	Reflection ReactMemoryReflectionConfig `yaml:"reflection"`
}

// ReactMemoryReflectionConfig 控制 reflection：压缩（compact_end）后异步派生受限子 run，
// 审阅被压缩的近期对话并整理记忆；写入走统一写核心（source=reflection）。
type ReactMemoryReflectionConfig struct {
	// Enabled 控制是否启用自动整理；未配置时默认 false。
	Enabled *bool `yaml:"enabled"`
	// CooldownMinutes 是同一 session 两次 reflection 的最小间隔（进程内冷却表）。
	CooldownMinutes int `yaml:"cooldown_minutes"`
	// MaxWritesPerRun 是单次 reflection 允许的记忆写操作上限（create/update/delete 合计）。
	MaxWritesPerRun int `yaml:"max_writes_per_run"`
	// TranscriptCharLimit 是注入给 reflection 的被压缩原文转录的字符上限（取最近部分）。
	TranscriptCharLimit int `yaml:"transcript_char_limit"`
}

// ReflectionEnabled 解析 reflection.enabled：未配置时默认 false。
func (c ReactMemoryReflectionConfig) ReflectionEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return false
}

// MemoryEnabled 解析 memory.enabled：未配置时默认 false。
func (c ReactMemoryConfig) MemoryEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return false
}

// MemoryAllowUserScope 解析 memory.allow_user_scope：未配置时默认 true。
func (c ReactMemoryConfig) MemoryAllowUserScope() bool {
	if c.AllowUserScope != nil {
		return *c.AllowUserScope
	}
	return true
}

// ReactGraphMemoryConfig 时序事实图谱记忆（Graphiti）配置。GroupID 由服务端按 run
// 作用域强制注入，绝不暴露给模型入参（防跨组越权读写）。
type ReactGraphMemoryConfig struct {
	// Enabled 是总开关；未配置时默认 false（不注册工具、不注入）。
	Enabled *bool `yaml:"enabled"`
	// Endpoint 是 Graphiti REST 服务地址（graphiti/server/graph_service）。
	Endpoint string `yaml:"endpoint"`
	// TimeoutMs 是工具调用（检索/写入）的单次请求超时。
	TimeoutMs int `yaml:"timeout_ms"`
	// GroupScope 是图谱分区作用域：caller（同 caller 共享一张图）或 caller_user（按用户分区）；默认 caller_user。
	GroupScope string `yaml:"group_scope"`
	// Inject 控制运行启动时的相关事实注入块（<graph_memory>）。
	Inject ReactGraphMemoryInjectConfig `yaml:"inject"`
	// Write 控制模型侧写入工具（graph_memory_write）。
	Write ReactGraphMemoryWriteConfig `yaml:"write"`
}

// ReactGraphMemoryInjectConfig 控制 <graph_memory> 注入：按本次用户输入检索相关事实，
// 有则注入、空/失败/超时静默跳过（注入是增强不是依赖）。
type ReactGraphMemoryInjectConfig struct {
	// Enabled 控制是否注入；未配置时默认 true（总开关开启时生效）。
	Enabled *bool `yaml:"enabled"`
	// MaxFacts 是单次注入的事实条数上限。
	MaxFacts int `yaml:"max_facts"`
	// MaxChars 是注入块字符预算，超出截断。
	MaxChars int `yaml:"max_chars"`
	// TimeoutMs 是注入检索的超时预算；超时放弃注入，不阻塞 run 启动主链路。
	TimeoutMs int `yaml:"timeout_ms"`
}

// ReactGraphMemoryWriteConfig 控制模型侧写入（W1 显式沉淀工具）。
// 会话收敛自动回灌（W2，on_run_finish）按改造方案 P3 后续落地。
type ReactGraphMemoryWriteConfig struct {
	// Enabled 控制是否注册 graph_memory_write 工具；未配置时默认 false（灰度）。
	Enabled *bool `yaml:"enabled"`
}

// InjectEnabled 解析 inject.enabled：未配置时默认 true。
func (c ReactGraphMemoryInjectConfig) InjectEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

// WriteEnabled 解析 write.enabled：未配置时默认 false。
func (c ReactGraphMemoryWriteConfig) WriteEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return false
}

// GraphMemoryEnabled 解析 graph_memory.enabled：未配置时默认 false。
func (c ReactGraphMemoryConfig) GraphMemoryEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return false
}

// GraphMemoryUserScope 解析 group_scope 是否为 caller_user 维度；未配置或非法值默认 true。
func (c ReactGraphMemoryConfig) GraphMemoryUserScope() bool {
	return c.GroupScope != "caller"
}

// ReactModelConfig 描述一个可由前端选择、也可参与自动互备的模型。
type ReactModelConfig struct {
	ModelKey     string `yaml:"model_key" json:"modelKey"`
	ModelVersion string `yaml:"model_version" json:"modelVersion"`
	DisplayName  string `yaml:"display_name" json:"displayName"`
}

// ReactModelsConfig 控制 ReAct 模型选择列表和自动互备。
type ReactModelsConfig struct {
	Default        ReactModelConfig   `yaml:"default" json:"defaultModel"`
	Available      []ReactModelConfig `yaml:"available" json:"models"`
	MutualFailover *bool              `yaml:"mutual_failover" json:"mutualFailover"`
}

// ReactContextCompactConfig ReAct 历史上下文压缩配置。
type ReactContextCompactConfig struct {
	// Enabled 控制是否启用自动上下文压缩；未配置时默认启用。
	Enabled *bool `yaml:"enabled"`
	// TokenTrigger 是上下文 token 数高水位，超过后触发压缩（主判断）。
	// 同时作为 ReAct 入口前置 token 检查的上限：不可压缩部分（system + tools + 当前 user input）
	// 超过该值直接拒绝（因为压缩救不了不可压缩部分）。
	// 压缩判断优先用上一轮模型调用真实 input_tokens，冷启动用估算 token。
	TokenTrigger int `yaml:"token_trigger"`
	// TokenTarget 是压缩后希望降到的目标 token 数，用于避免下一轮反复压缩。
	TokenTarget int `yaml:"token_target"`
	// TimeoutSec 是调用模型生成压缩摘要的超时时间，超时后回退本地摘要。
	TimeoutSec int `yaml:"timeout_sec"`
	// SummaryLimit 是压缩摘要最大字符数。
	SummaryLimit int `yaml:"summary_limit"`
	// FallbackMessagePreviewLimit 是本地压缩回退时每条历史消息的预览字符数。
	FallbackMessagePreviewLimit int `yaml:"fallback_message_preview_limit"`
}

// ReactToolResultConfig ReAct 工具结果上下文回填配置。
type ReactToolResultConfig struct {
	// InlineLimitBytes 是工具结果可直接回填模型上下文的最大字节数。
	InlineLimitBytes int `yaml:"inline_limit_bytes"`
	// PreviewLimit 是大工具结果回填给模型和前端事件的预览字符数。
	PreviewLimit int `yaml:"preview_limit"`
	// DBTTLSec 是 resultRef 对应完整工具结果的保留秒数。
	DBTTLSec int `yaml:"db_ttl_sec"`
	// DBMaxBytes 是单条完整工具结果允许落库的最大字节数。
	DBMaxBytes int `yaml:"db_max_bytes"`
	// ReadLimit 是 read_tool_result 默认读取字符数。
	ReadLimit int `yaml:"read_limit"`
	// MaxReadLimit 是 read_tool_result 单次允许读取的最大字符数。
	MaxReadLimit int `yaml:"max_read_limit"`
}

// LLMConfig 大模型业务配置（模型目录、ReAct 运行时等）
type LLMConfig struct {
	Models             map[string]ModelCatalog      `yaml:"models"`
	ModelVersionLimits map[string]ModelVersionLimit `yaml:"model_version_limits"`
	React              ReactRuntimeConfig           `yaml:"react"`
	ModelWhitelist     []string                     `yaml:"model_whitelist"`
}

// ModelCatalog 模型目录（每类模型下可包含多个版本）
type ModelCatalog struct {
	Versions         []string `yaml:"versions"`
	DefaultVersion   string   `yaml:"default_version"`
	MaxContextTokens int      `yaml:"max_context_tokens"`
	MaxOutputTokens  int      `yaml:"max_output_tokens"`
}

// TCustom 对应 custom.yaml（LLM 业务配置）
type TCustom struct {
	LLM       LLMConfig       `yaml:"llm"`
	MCP       MCPConfig       `yaml:"mcp"`
	MCPServer MCPServerConfig `yaml:"mcp_server"`
	AsyncTask AsyncTaskConfig `yaml:"async_task"`
}

// MCPServerConfig 声明 MCP 服务端网关（对外把 tblLlmTool 的 http 工具暴露成 MCP 协议）。
// 与 MCP（客户端配置）相互独立：enabled=false 时主服务不挂 /mcp 端点。
type MCPServerConfig struct {
	Enabled bool `yaml:"enabled"`
	// Name/Version 是 initialize 握手返回的 server 实现标识（客户端展示用）。
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	// Instructions 覆盖随 initialize 下发的模型使用规范；空则用内置默认文案。
	Instructions string `yaml:"instructions"`
	// ForwardCookies 把调用方请求里的指定 Cookie 透传给工具上游（上游依赖登录态的场景）。
	ForwardCookies []string `yaml:"forward_cookies"`
	// AdminTokens 是管理台（/react/mcp-admin 页面）的静态令牌白名单；
	// 空列表 = 接受任意非空令牌（与 playground 管理面同级的内网联调默认）。
	AdminTokens []string `yaml:"admin_tokens"`
	// Audit 控制调用审计异步落库（tblLlmMcpCallLog）。
	Audit MCPServerAuditConfig `yaml:"audit"`
}

// MCPServerAuditConfig 审计落库配置。
type MCPServerAuditConfig struct {
	// WorkerNum 落库并发数（默认 10）；队列容量固定 4096，写满丢弃不阻塞业务。
	WorkerNum int `yaml:"worker_num"`
}

// MCPConfig 声明 MCP 客户端：servers 的 kind 必须命中代码内适配器白名单。
type MCPConfig struct {
	// CallerKey 是 MCP 工具同步进注册表时挂载的调用方；为空则不启用 MCP。
	CallerKey string          `yaml:"caller_key"`
	Servers   []MCPServerConf `yaml:"servers"`
	// AllowPrivateEndpoint 允许连接环回/私网 MCP 端点（默认拒绝，SSRF 防护）。
	// 仅本机开发/演示环境开启；生产环境必须保持 false。
	AllowPrivateEndpoint bool `yaml:"allow_private_endpoint"`
}

// MCPServerConf 描述一个 MCP 服务器：kind=repo 选自 stdio 适配器白名单（env 注入子进程）；
// kind=http 走 Streamable HTTP（endpoint 必填，经 SSRF 校验，拒绝环回/私网/保留地址）。
type MCPServerConf struct {
	Name      string            `yaml:"name"`
	Kind      string            `yaml:"kind"`
	Env       map[string]string `yaml:"env"`
	Endpoint  string            `yaml:"endpoint"`
	TimeoutMs int               `yaml:"timeout_ms"`
	Headers   map[string]string `yaml:"headers"`
}

// AsyncTaskConfig 控制异步任务状态同步框架。
// 基座服务默认无内置 Provider；接入方按 SchedulerProvider 接口自行注册。
type AsyncTaskConfig struct {
	Enabled bool `yaml:"enabled"`
}

// ResourceConf 对应 resource.yaml
type ResourceConf struct {
	Mysql map[string]base.MysqlConf
	Redis map[string]redis.RedisConf
	Cos   COSConfig `yaml:"cos"`
}

// COSConfig COS 配置
type COSConfig struct {
	SecretID   string `yaml:"secretID"`
	SecretKey  string `yaml:"secretKey"`
	Bucket     string `yaml:"bucket"`
	AppID      string `yaml:"appID"`
	Region     string `yaml:"region"`
	Endpoint   string `yaml:"endpoint"`
	Timeout    int    `yaml:"timeout"`
	PathPrefix string `yaml:"pathPrefix"`
	LocalDir   string `yaml:"localDir"`
}

// LLMApiConfig LLM API 连接配置（api_key、各模型的接口地址等）
type LLMApiConfig struct {
	ApiKeys   map[string]string         `yaml:"api_keys"`
	Endpoints map[string]EndpointConfig `yaml:"endpoints"`
}

// EndpointConfig 单个模型的 API 端点配置
type EndpointConfig struct {
	ApiUrl    string `yaml:"api_url"`
	MaxTokens int    `yaml:"max_tokens"`
}

// TApi 对应 api.yaml
type TApi struct {
	LLM        LLMApiConfig   `yaml:"llm"`
	PythonExec base.ApiClient `yaml:"python_exec"`
}

func InitConf() {
	env.LoadConf("config.yaml", env.SubConfMount, &BasicConf)
	env.LoadConf("custom.yaml", env.SubConfMount, &CustomConf)
	env.LoadConf("api.yaml", env.SubConfMount, &API)
	env.LoadConf("resource.yaml", env.SubConfMount, &RConf)
}

var CustomConf TCustom
var API TApi
var RConf ResourceConf

func GetModelCatalog(modelKey string) *ModelCatalog {
	catalog, ok := CustomConf.LLM.Models[modelKey]
	if !ok {
		return nil
	}
	return &catalog
}

func GetModelVersionLimit(version string) *ModelVersionLimit {
	normalizedVersion := strings.ToLower(strings.TrimSpace(version))
	if normalizedVersion == "" {
		return nil
	}
	limit, ok := CustomConf.LLM.ModelVersionLimits[normalizedVersion]
	if !ok {
		return nil
	}
	return &limit
}

// IsReactPlaygroundWhitelisted 判断登录用户是否可访问 ReAct playground 能力；白名单为空时不限制。
func IsReactPlaygroundWhitelisted(userName string) bool {
	whitelist := CustomConf.LLM.React.PlaygroundWhitelist
	return len(whitelist) == 0 || slices.Contains(whitelist, strings.TrimSpace(userName))
}

func GetReactRuntimeConfig() ReactRuntimeConfig {
	cfg := CustomConf.LLM.React
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = defaultReactMaxSteps
	}
	if cfg.StreamIdleTimeoutSec <= 0 {
		cfg.StreamIdleTimeoutSec = defaultReactStreamIdleTimeoutSec
	}

	compact := cfg.ContextCompact
	if compact.TokenTrigger <= 0 {
		compact.TokenTrigger = defaultReactCompactTokenTrigger
	}
	if compact.TokenTarget <= 0 {
		compact.TokenTarget = defaultReactCompactTokenTarget
	}
	if compact.TokenTarget >= compact.TokenTrigger {
		compact.TokenTarget = compact.TokenTrigger * 60 / 100
	}
	if compact.TimeoutSec <= 0 {
		compact.TimeoutSec = defaultReactCompactTimeoutSec
	}
	if compact.SummaryLimit <= 0 {
		compact.SummaryLimit = defaultReactCompactSummaryLimit
	}
	if compact.FallbackMessagePreviewLimit <= 0 {
		compact.FallbackMessagePreviewLimit = defaultReactCompactFallbackPreviewLimit
	}
	cfg.ContextCompact = compact

	toolResult := cfg.ToolResult
	if toolResult.InlineLimitBytes <= 0 {
		toolResult.InlineLimitBytes = defaultReactToolResultInlineLimitBytes
	}
	if toolResult.PreviewLimit <= 0 {
		toolResult.PreviewLimit = defaultReactToolResultPreviewLimit
	}
	if toolResult.DBTTLSec <= 0 {
		toolResult.DBTTLSec = defaultReactToolResultDBTTLSec
	}
	if toolResult.DBMaxBytes <= 0 {
		toolResult.DBMaxBytes = defaultReactToolResultDBMaxBytes
	}
	if toolResult.ReadLimit <= 0 {
		toolResult.ReadLimit = defaultReactToolResultReadLimit
	}
	if toolResult.MaxReadLimit <= 0 {
		toolResult.MaxReadLimit = defaultReactToolResultMaxReadLimit
	}
	if toolResult.MaxReadLimit < toolResult.ReadLimit {
		toolResult.MaxReadLimit = toolResult.ReadLimit
	}
	cfg.ToolResult = toolResult

	memory := cfg.Memory
	if memory.ResidentMaxItems <= 0 {
		memory.ResidentMaxItems = defaultReactMemoryResidentMaxItems
	}
	if memory.ResidentBudgetChars <= 0 {
		memory.ResidentBudgetChars = defaultReactMemoryResidentBudgetChars
	}
	if memory.IndexMaxItems <= 0 {
		memory.IndexMaxItems = defaultReactMemoryIndexMaxItems
	}
	if memory.DetachedMaxItems <= 0 {
		memory.DetachedMaxItems = defaultReactMemoryDetachedMaxItems
	}
	reflection := memory.Reflection
	if reflection.CooldownMinutes <= 0 {
		reflection.CooldownMinutes = defaultReactMemoryReflectionCooldownMin
	}
	if reflection.MaxWritesPerRun <= 0 {
		reflection.MaxWritesPerRun = defaultReactMemoryReflectionMaxWrites
	}
	if reflection.TranscriptCharLimit <= 0 {
		reflection.TranscriptCharLimit = defaultReactMemoryReflectionTranscript
	}
	memory.Reflection = reflection
	cfg.Memory = memory

	// subagent 策略统一经 EffectiveSubAgentConfig 合并管理面板「运行时配置」的 DB 覆盖
	//（覆盖 > yaml > 内置默认），与 /setting/subagent 面板响应口径一致。
	cfg.SubAgent = EffectiveSubAgentConfig(GetRuntimeSettingOverride().SubAgent)

	workspace := cfg.Workspace
	if strings.TrimSpace(workspace.RootDir) == "" {
		workspace.RootDir = defaultReactWorkspaceRootDir
	}
	if strings.TrimSpace(workspace.MirrorDir) == "" {
		workspace.MirrorDir = defaultReactWorkspaceMirrorDir
	}
	if workspace.GitTimeoutSec <= 0 {
		workspace.GitTimeoutSec = defaultReactWorkspaceGitTimeoutSec
	}
	resolvers := make([]ReactWorkspaceResolverConf, 0, len(workspace.Resolvers))
	for _, item := range workspace.Resolvers {
		item.Service = strings.TrimSpace(item.Service)
		item.RepoURL = strings.TrimSpace(item.RepoURL)
		if item.Service == "" || item.RepoURL == "" {
			continue
		}
		resolvers = append(resolvers, item)
	}
	workspace.Resolvers = resolvers
	cfg.Workspace = workspace

	graphMemory := cfg.GraphMemory
	if graphMemory.TimeoutMs <= 0 {
		graphMemory.TimeoutMs = defaultReactGraphMemoryTimeoutMs
	}
	inject := graphMemory.Inject
	if inject.MaxFacts <= 0 {
		inject.MaxFacts = defaultReactGraphMemoryInjectMaxFacts
	}
	if inject.MaxChars <= 0 {
		inject.MaxChars = defaultReactGraphMemoryInjectMaxChars
	}
	if inject.TimeoutMs <= 0 {
		inject.TimeoutMs = defaultReactGraphMemoryInjectTimeoutMs
	}
	graphMemory.Inject = inject
	graphMemory.Endpoint = strings.TrimRight(strings.TrimSpace(graphMemory.Endpoint), "/")
	cfg.GraphMemory = graphMemory

	models := cfg.Models
	available := make([]ReactModelConfig, 0, len(models.Available))
	seenModels := make(map[string]struct{}, len(models.Available))
	for _, item := range models.Available {
		item.ModelKey = strings.TrimSpace(item.ModelKey)
		item.ModelVersion = strings.TrimSpace(item.ModelVersion)
		item.DisplayName = strings.TrimSpace(item.DisplayName)
		if item.ModelKey == "" || item.ModelVersion == "" {
			continue
		}
		identity := item.ModelKey + "\x00" + item.ModelVersion
		if _, exists := seenModels[identity]; exists {
			continue
		}
		seenModels[identity] = struct{}{}
		if item.DisplayName == "" {
			item.DisplayName = item.ModelVersion
		}
		available = append(available, item)
	}
	models.Available = available
	models.Default.ModelKey = strings.TrimSpace(models.Default.ModelKey)
	models.Default.ModelVersion = strings.TrimSpace(models.Default.ModelVersion)
	models.Default.DisplayName = strings.TrimSpace(models.Default.DisplayName)
	if (models.Default.ModelKey == "" || models.Default.ModelVersion == "") && len(available) > 0 {
		models.Default = available[0]
	}
	cfg.Models = models
	return cfg
}
