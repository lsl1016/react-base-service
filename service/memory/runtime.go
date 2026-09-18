package memory

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"react-base-service/conf"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// Runtime 提供 Agent Runtime 需要的记忆能力边界；持久化细节留在 service/memory 内部。
type Runtime struct{}

var defaultRuntime = &Runtime{}

func DefaultRuntime() *Runtime {
	return defaultRuntime
}

// RuntimeScope 描述一次 Agent run 可见的记忆空间与写入目标。
type RuntimeScope struct {
	Owners      []model.MemoryOwner
	WriteOwner  model.MemoryOwner
	AllowedKeys map[string]struct{}
}

// ResolveRuntimeScope 解析 caller/caller_user 记忆作用域。
// caller 是共享底座；启用 user scope 时 caller_user 是高优先级覆盖层与默认写入目标。
func ResolveRuntimeScope(callerKey, userName string, allowUserScope bool) RuntimeScope {
	scope := RuntimeScope{
		Owners:      []model.MemoryOwner{model.BuildCallerMemoryOwner(callerKey)},
		AllowedKeys: make(map[string]struct{}, 2),
	}
	scope.WriteOwner = scope.Owners[0]
	scope.AllowedKeys[OwnerScopeKey(scope.Owners[0].OwnerType, scope.Owners[0].OwnerKey)] = struct{}{}
	if allowUserScope {
		userOwner := model.BuildCallerUserMemoryOwner(callerKey, userName)
		scope.Owners = append(scope.Owners, userOwner)
		scope.WriteOwner = userOwner
		scope.AllowedKeys[OwnerScopeKey(userOwner.OwnerType, userOwner.OwnerKey)] = struct{}{}
	}
	return scope
}

// MergeRuntimeItems 合并多记忆空间条目：同 itemKey 时 caller_user 恒覆盖 caller，
// 最终结果按更新时间倒序输出。
func MergeRuntimeItems(items []model.MemoryItem) []model.MemoryItem {
	slices.SortStableFunc(items, func(a, b model.MemoryItem) int {
		return cmp.Compare(runtimeOwnerPriority(a.OwnerType), runtimeOwnerPriority(b.OwnerType))
	})
	merged := make([]model.MemoryItem, 0, len(items))
	indexByKey := make(map[string]int, len(items))
	for _, item := range items {
		key := item.ItemKey
		if pos, ok := indexByKey[key]; ok {
			merged[pos] = item
			continue
		}
		indexByKey[key] = len(merged)
		merged = append(merged, item)
	}
	slices.SortStableFunc(merged, func(a, b model.MemoryItem) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return merged
}

func runtimeOwnerPriority(ownerType string) int {
	if ownerType == model.MemoryOwnerTypeCallerUser {
		return 1
	}
	return 0
}

// RenderRuntimeContext 渲染注入 system prompt 的 <memory> 块。
func RenderRuntimeContext(items []model.MemoryItem, cfg conf.ReactMemoryConfig) string {
	var resident, detached []model.MemoryItem
	for _, item := range items {
		if item.Layer == model.MemoryLayerResident {
			resident = append(resident, item)
		} else {
			detached = append(detached, item)
		}
	}
	if len(resident) == 0 && len(detached) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<memory>\n")
	sb.WriteString("## 长期记忆（自动维护）\n\n")

	if len(resident) > 0 {
		sb.WriteString("### 常驻\n")
		used := 0
		truncated := 0
		for _, item := range resident {
			line := fmt.Sprintf("- [%s] %s\n", item.Title, strings.TrimSpace(item.Content))
			if used+len([]rune(line)) > cfg.ResidentBudgetChars {
				truncated++
				continue
			}
			sb.WriteString(line)
			used += len([]rune(line))
		}
		if truncated > 0 {
			sb.WriteString(fmt.Sprintf("（另有 %d 条常驻记忆超出字符预算未注入，可用 memory_list 查看）\n", truncated))
		}
		sb.WriteString("\n")
	}

	if len(detached) > 0 {
		sb.WriteString("### 记忆目录（需要时用 memory_read 按 itemId 读取全文）\n")
		listed := detached
		overflow := 0
		if len(listed) > cfg.IndexMaxItems {
			overflow = len(listed) - cfg.IndexMaxItems
			listed = listed[:cfg.IndexMaxItems]
		}
		for _, item := range listed {
			description := strings.TrimSpace(item.Description)
			if description == "" {
				description = strings.TrimSpace(item.Content)
			}
			sb.WriteString(fmt.Sprintf("- #%d [%s] %s\n", item.ID, item.Title, description))
		}
		if overflow > 0 {
			sb.WriteString(fmt.Sprintf("（另有 %d 条记忆未列出，可用 memory_list 检索）\n", overflow))
		}
	}

	sb.WriteString("\n记忆使用纪律：以上内容自动维护、可能过时；与用户当前表述冲突时以用户为准，并用 memory_write 修正。\n")
	sb.WriteString("</memory>")
	return sb.String()
}

// BuildRuntimeContext 在 run 初始化阶段完成作用域解析、查询、覆盖合并与渲染。
func BuildRuntimeContext(ctx *gin.Context, callerKey, userName string, cfg conf.ReactMemoryConfig) (string, error) {
	scope := ResolveRuntimeScope(callerKey, userName, cfg.MemoryAllowUserScope())
	items, err := model.FindActiveMemoryItemsByOwners(ctx, scope.Owners)
	if err != nil {
		return "", err
	}
	return RenderRuntimeContext(MergeRuntimeItems(items), cfg), nil
}


const (
	RuntimeReadMaxItems    = 10
	RuntimeListDefaultSize = 20
	RuntimeListMaxSize     = 50
)

// RuntimeListOptions 是 memory_list 的领域查询条件。
type RuntimeListOptions struct {
	Layer   string
	Tag     string
	Keyword string
	Limit   int
}

// RuntimeListItem 是 memory_list 返回给 Runtime 的稳定视图。
type RuntimeListItem struct {
	ItemID      uint   `json:"itemId"`
	Layer       string `json:"layer"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Tags        string `json:"tags"`
	Source      string `json:"source"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

// ListRuntimeItems 在当前作用域内完成查询、覆盖合并与过滤。
func ListRuntimeItems(ctx *gin.Context, scope RuntimeScope, options RuntimeListOptions) ([]RuntimeListItem, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = RuntimeListDefaultSize
	}
	if limit > RuntimeListMaxSize {
		limit = RuntimeListMaxSize
	}
	layer := strings.TrimSpace(options.Layer)
	if layer == "" {
		layer = model.MemoryLayerDetached
	}
	tag := strings.TrimSpace(options.Tag)
	keyword := strings.ToLower(strings.TrimSpace(options.Keyword))

	items, err := model.FindActiveMemoryItemsByOwners(ctx, scope.Owners)
	if err != nil {
		return nil, err
	}
	views := make([]RuntimeListItem, 0, limit)
	for _, item := range MergeRuntimeItems(items) {
		if layer != "all" && item.Layer != layer {
			continue
		}
		if tag != "" && !RuntimeItemHasTag(item.Tags, tag) {
			continue
		}
		if keyword != "" && !RuntimeItemMatchesKeyword(item, keyword) {
			continue
		}
		views = append(views, RuntimeListItem{
			ItemID:      item.ID,
			Layer:       item.Layer,
			Title:       item.Title,
			Description: item.Description,
			Tags:        item.Tags,
			Source:      item.Source,
			Version:     item.Version,
			UpdatedAt:   item.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
		if len(views) >= limit {
			break
		}
	}
	return views, nil
}

func RuntimeItemHasTag(tags, tag string) bool {
	for _, part := range strings.Split(tags, ",") {
		if strings.TrimSpace(part) == tag {
			return true
		}
	}
	return false
}

func RuntimeItemMatchesKeyword(item model.MemoryItem, keyword string) bool {
	haystack := strings.ToLower(item.Title + "\n" + item.Description + "\n" + item.Tags)
	return strings.Contains(haystack, strings.ToLower(strings.TrimSpace(keyword)))
}

// RuntimeReadItem 是 memory_read 返回的稳定视图。
type RuntimeReadItem struct {
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

// ReadRuntimeItems 在当前作用域内按 ID 读取正文；任一条越权时整单拒绝。
func ReadRuntimeItems(ctx *gin.Context, scope RuntimeScope, itemIDs []uint64) ([]RuntimeReadItem, error) {
	if len(itemIDs) == 0 {
		return nil, fmt.Errorf("itemIds 不能为空")
	}
	if len(itemIDs) > RuntimeReadMaxItems {
		return nil, fmt.Errorf("一次最多读取 %d 条记忆", RuntimeReadMaxItems)
	}
	views := make([]RuntimeReadItem, 0, len(itemIDs))
	for _, rawID := range itemIDs {
		if rawID == 0 {
			continue
		}
		item, err := model.GetActiveMemoryItemByID(ctx, uint(rawID))
		if err != nil {
			return nil, err
		}
		if item == nil {
			return nil, fmt.Errorf("记忆 #%d 不存在或已删除", rawID)
		}
		if _, ok := scope.AllowedKeys[OwnerScopeKey(item.OwnerType, item.OwnerKey)]; !ok {
			return nil, fmt.Errorf("记忆 #%d 不在当前作用域内，无权读取", rawID)
		}
		views = append(views, RuntimeReadItem{
			ItemID:     item.ID,
			Layer:      item.Layer,
			Title:      item.Title,
			Content:    item.Content,
			Tags:       item.Tags,
			Source:     item.Source,
			Version:    item.Version,
			LastReason: item.LastReason,
			UpdatedAt:  item.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	if len(views) == 0 {
		return nil, fmt.Errorf("itemIds 不能为空")
	}
	return views, nil
}


// BuildContext/List/Read/ApplyMutation 提供给 Agent Runtime 的可注入方法集合。
func (r *Runtime) ResolveScope(callerKey, userName string, allowUserScope bool) RuntimeScope {
	return ResolveRuntimeScope(callerKey, userName, allowUserScope)
}

func (r *Runtime) BuildContext(ctx *gin.Context, callerKey, userName string, cfg conf.ReactMemoryConfig) (string, error) {
	return BuildRuntimeContext(ctx, callerKey, userName, cfg)
}

func (r *Runtime) List(ctx *gin.Context, scope RuntimeScope, options RuntimeListOptions) ([]RuntimeListItem, error) {
	return ListRuntimeItems(ctx, scope, options)
}

func (r *Runtime) Read(ctx *gin.Context, scope RuntimeScope, itemIDs []uint64) ([]RuntimeReadItem, error) {
	return ReadRuntimeItems(ctx, scope, itemIDs)
}

func (r *Runtime) ApplyMutation(ctx *gin.Context, input MutationInput) (map[string]interface{}, error) {
	return ApplyMutation(ctx, input)
}
