// Package mem 承载 Runtime 记忆 adapter 的纯函数部分：作用域解析、上下文渲染与工具声明。
// 执行方法（memory_list/read/write）留在 react 门面（依赖引擎状态）。
package mem

import (
	core "react-base-service/service/react/internal/core"

	llm "react-base-service/api/llm"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	memoryService "react-base-service/service/memory"

	"github.com/gin-gonic/gin"
)

const (
	memoryReadMaxItems    = 10
	memoryListDefaultSize = 20
	memoryListMaxSize     = 50
)

// MemoryScopeResolved 是 service/react 保留的兼容视图。
// 真正的 owner scope、合并、读写校验和持久化均已下沉到 service/memory；ReAct 层只负责把 Memory
// 暴露成模型 Tool，并把当前 Caller/User/Run 信息转换为 Memory Runtime 的调用参数。
type MemoryScopeResolved struct {
	owners      []model.MemoryOwner
	writeOwner  model.MemoryOwner
	allowedKeys map[string]struct{}
}

func ResolveMemoryScope(callerKey, userName string, allowUserScope bool) MemoryScopeResolved {
	scope := memoryService.ResolveRuntimeScope(callerKey, userName, allowUserScope)
	return MemoryScopeResolved{
		owners:      scope.Owners,
		writeOwner:  scope.WriteOwner,
		allowedKeys: scope.AllowedKeys,
	}
}

func MemoryOwnerKey(owner model.MemoryOwner) string {
	return memoryService.OwnerScopeKey(owner.OwnerType, owner.OwnerKey)
}

func MergeMemoryItems(items []model.MemoryItem) []model.MemoryItem {
	return memoryService.MergeRuntimeItems(items)
}

func RenderMemoryContext(items []model.MemoryItem, cfg conf.ReactMemoryConfig) string {
	return memoryService.RenderRuntimeContext(items, cfg)
}

// BuildMemoryContextForRun 保留 React Runtime 入口；查询/合并/渲染已下沉到 service/memory。
func BuildMemoryContextForRun(ctx *gin.Context, callerKey, userName string) (string, error) {
	return memoryService.BuildRuntimeContext(ctx, callerKey, userName, conf.GetReactRuntimeConfig().Memory)
}

// MemoryToolDefinitions 声明模型可见的长期记忆工具。
// Memory 与普通 Business Tool 不同：它属于 Runtime 内置能力，因此不走 get_tool/execute_tool 两阶段协议；
// 是否暴露由 ExecutionProfile.AllowMemory 控制，读写安全边界由 service/memory 再次校验。
func MemoryToolDefinitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		core.ObjectTool(core.MetaToolMemoryList,
			"列出当前作用域的长期记忆索引（默认按需层全部）。返回条目的 itemId/title/description/tags/layer/memoryType，不含正文；需要正文时用 memory_read。",
			map[string]interface{}{
				"layer": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"resident", "detached", "all"},
					"description": "层级过滤，默认 detached。",
				},
				"memoryType": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"preference", "fact", "event", "procedure"},
					"description": "记忆类型过滤，可选。",
				},
				"tag":     core.StringSchema("按标签精确过滤，可选。"),
				"keyword": core.StringSchema("标题/描述关键词过滤，可选。"),
				"limit":   core.NumberSchema("返回条数上限，默认 20，最大 50。"),
			}),
		core.ObjectTool(core.MetaToolMemoryRead,
			"按 itemId 读取记忆全文（正文、版本、来源与最近修订原因）。一次最多 10 条。",
			map[string]interface{}{
				"itemIds": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "number"},
					"description": "memory_list 或记忆目录返回的 itemId 数组。",
				},
			}),
		MemoryWriteToolDefinition(),
	}
}

func MemoryWriteToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        core.MetaToolMemoryWrite,
		Description: "写入/更新/删除长期记忆。只记稳定事实与明确偏好（用户称呼、业务口径、长期约定），不记一次性任务上下文，不记录凭证、证件号、密钥等敏感信息（含敏感形态的内容会被直接拒绝）；宁可少写不写错。用户明确要求忘记时执行 delete。每次操作必须给 reason。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": core.StringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"create", "update", "delete"},
					"description": "操作类型。",
				},
				"itemId":  core.NumberSchema("条目 ID，update/delete 必填。"),
				"version": core.NumberSchema("update 时读取到的版本号（乐观锁）；不传则直接覆盖。"),
				"layer": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"resident", "detached"},
					"description": "层级。create 默认 detached；update 不传保持原层级。常驻层注入每一轮对话，写入需加倍审慎。",
				},
				"title":         core.StringSchema("短标题（≤32字），create/update 必填。"),
				"content":       core.StringSchema("记忆正文，一到三句原子事实（≤500字），create/update 必填。"),
				"retrievalHint": core.StringSchema("检索提示：什么场景需要想起这条记忆（≤512字），create/update 必填。"),
				"memoryType": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"preference", "fact", "event", "procedure"},
					"description": "记忆类型，默认 fact。preference 仅用于用户明确表达过的稳定偏好（称呼、语言、输出格式偏好）；event 用于带时间的关键决定/经历（正文中注明「截至 YYYY-MM-DD」）；procedure 用于沉淀的可复用工作方法。拿不准就用 fact。update 不传保持原类型。",
				},
				"tags":   core.StringSchema("逗号分隔标签，可选。"),
				"reason": core.StringSchema("必填：为什么写入/修改/删除。"),
			},
			"required":             []string{"description", "action", "reason"},
			"additionalProperties": false,
		},
	}
}

type memoryListInput struct {
	Layer      string `json:"layer"`
	MemoryType string `json:"memoryType"`
	Tag        string `json:"tag"`
	Keyword    string `json:"keyword"`
	Limit      int    `json:"limit"`
}

type memoryListItemView struct {
	ItemID      uint   `json:"itemId"`
	Layer       string `json:"layer"`
	MemoryType  string `json:"memoryType"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Tags        string `json:"tags"`
	Source      string `json:"source"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

func MemoryItemHasTag(tags, tag string) bool {
	return memoryService.RuntimeItemHasTag(tags, tag)
}

func MemoryItemMatchesKeyword(item model.MemoryItem, keyword string) bool {
	return memoryService.RuntimeItemMatchesKeyword(item, keyword)
}

type memoryReadInput struct {
	ItemIDs []uint64 `json:"itemIds"`
}

type memoryReadItemView struct {
	ItemID     uint   `json:"itemId"`
	Layer      string `json:"layer"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Tags       string `json:"tags"`
	Source     string `json:"source"`
	Version    int    `json:"version"`
	LastReason string `json:"lastReason"`
	UpdatedAt  string `json:"updatedAt"`
}

type memoryWriteInput struct {
	Action      string `json:"action"`
	ItemID      uint64 `json:"itemId"`
	Version     int    `json:"version"`
	Layer       string `json:"layer"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Description string `json:"retrievalHint"`
	MemoryType  string `json:"memoryType"`
	Tags        string `json:"tags"`
	Reason      string `json:"reason"`
}

func MemorySourceForRun(isReflection bool) string {
	if isReflection {
		return model.MemorySourceReflection
	}
	return model.MemorySourceModel
}
