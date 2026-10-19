package params

// ---------------------------------------------------------------------------
// 斜杠命令（docs/plan/20261013_斜杠命令支持设计方案.md）
// ---------------------------------------------------------------------------

// ReactCommandDirective 是 run payload 上的命令执行指令（标记型命令由命令分发层写入）。
// 当前仅 /compact 使用：run 启动后第 0 步强制执行全量压缩，随后直接收尾（不进入模型对话轮）。
type ReactCommandDirective struct {
	// ForceCompact 标记本次 run 是 /compact 触发的手动压缩 run。
	ForceCompact bool `json:"forceCompact,omitempty"`
	// CompactInstructions 是 /compact <附加指令> 携带的附加摘要指令（可选）。
	CompactInstructions string `json:"compactInstructions,omitempty"`
}

// ReactCommandReceiptPayload 是命令回执事件（command_receipt）载荷：
// 命令分发层/引擎的就地处理结论（拒绝、未知命令、压缩结果提示等）以本事件即时下发，
// 不产生模型 turn，也不落库回放（P2 无 run 消息机制再统一解决）。
type ReactCommandReceiptPayload struct {
	// Command 是命令名（别名归一前用户输入的原名）。
	Command string `json:"command"`
	// Result 是处理结论：ok / rejected / error。
	Result string `json:"result"`
	// Reason 是 rejected 时的原因词汇（run_active / unknown_command / command_disabled）。
	Reason string `json:"reason,omitempty"`
	// Message 是给用户的提示文案。
	Message string `json:"message,omitempty"`
}

// 命令回执 Result 取值。
const (
	ReactCommandReceiptOk       = "ok"
	ReactCommandReceiptRejected = "rejected"
	ReactCommandReceiptError    = "error"
)

// 命令回执 rejected 的 Reason 取值。
const (
	// ReactCommandRejectRunActive run 活跃期命令一律拒绝（不排队：排队晋升会丢失命令语义）。
	ReactCommandRejectRunActive = "run_active"
	// ReactCommandRejectUnknown 命中保留名但当前版本未实现该命令。
	ReactCommandRejectUnknown = "unknown_command"
	// ReactCommandRejectDisabled 命令所属能力开关未开启（如 workflow.enabled 关闭）。
	ReactCommandRejectDisabled = "command_disabled"
)

// CommandCatalogReq 查询 app 可见命令目录（前端 / 菜单数据源）。
type CommandCatalogReq struct {
	AppKey      string   `json:"appKey" binding:"required"`
	RouteValues []string `json:"routeValues"`
}

// CommandCatalogItem 是命令目录里的单条命令。
type CommandCatalogItem struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	// Source 取值 builtin | custom。
	Source string `json:"source"`
	// Group 是前端浮层分组名。
	Group string `json:"group"`
}

type CommandCatalogResp struct {
	Commands []CommandCatalogItem `json:"commands"`
}

// CommandCreateReq 创建自定义命令。
type CommandCreateReq struct {
	AppKey       string   `json:"appKey" binding:"required"`
	RouteValues  []string `json:"routeValues"`
	Name         string   `json:"name" binding:"required"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argumentHint"`
	// Content 是提示词模板正文（markdown），支持 $ARGUMENTS / $1 $2 占位符。
	Content string `json:"content" binding:"required"`
}

// CommandUpdateReq 更新自定义命令（CommandKey 定位；零值字段不更新，Status 指针区分未传）。
type CommandUpdateReq struct {
	CommandKey   string   `json:"commandKey" binding:"required"`
	Name         string   `json:"name"`
	Description  *string  `json:"description"`
	ArgumentHint *string  `json:"argumentHint"`
	Content      string   `json:"content"`
	Status       *int     `json:"status"`
}

// CommandDeleteReq 删除自定义命令（软删）。
type CommandDeleteReq struct {
	CommandKey string `json:"commandKey" binding:"required"`
}

// CommandListReq 管理控制台命令列表；AppKey 为空时返回全部（与 skill 管理口径一致）。
type CommandListReq struct {
	AppKey string `json:"appKey"`
}

// CommandDetailReq 查询单条自定义命令。
type CommandDetailReq struct {
	CommandKey string `json:"commandKey" binding:"required"`
}

// CommandResp 是自定义命令的对外视图。
type CommandResp struct {
	CommandKey   string   `json:"commandKey"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argumentHint"`
	AppKey       string   `json:"appKey"`
	RouteValues  []string `json:"routeValues"`
	Content      string   `json:"content"`
	Status       int      `json:"status"`
	CreatedBy    string   `json:"createdBy"`
	UpdatedBy    string   `json:"updatedBy"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}
