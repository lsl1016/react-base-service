package react

import (
	"strings"

	"react-base-service/components"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	memoryService "react-base-service/service/memory"

	"github.com/gin-gonic/gin"
)

// 长期记忆管理面接口（P2：审计与管理面）——查询、人工修订与回滚。
//
// 写操作统一走 service/memory.ApplyMutation / RollbackRevision（与引擎 memory_write 工具同一条
// 校验、幂等收敛、乐观锁、敏感信息拦截与修订流水），source=admin、createdBy=操作人，无旁门写入。
// 查询直接读模型层（tblLlmMemoryItem / tblLlmMemoryRevision）。
// 所有端点在 memory.enabled=false 时直接拒绝（表未建时避免裸 SQL 报错）。

type memoryListRequest struct {
	OwnerType  string `json:"ownerType"`
	OwnerKey   string `json:"ownerKey"`
	Layer      string `json:"layer"`
	MemoryType string `json:"memoryType"`
	Tag        string `json:"tag"`
	Keyword    string `json:"keyword"`
	// IncludeDeleted 包含软删条目（审计视图）。
	IncludeDeleted bool `json:"includeDeleted"`
	Limit          int  `json:"limit"`
	Offset         int  `json:"offset"`
}

type memoryCreateRequest struct {
	OwnerType   string  `json:"ownerType"`
	OwnerKey    string  `json:"ownerKey"`
	Layer       string  `json:"layer"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Description string  `json:"retrievalHint"`
	MemoryType  string  `json:"memoryType"`
	Confidence  float64 `json:"confidence"`
	Importance  int     `json:"importance"`
	Tags        string  `json:"tags"`
	Reason      string  `json:"reason"`
}

type memoryUpdateRequest struct {
	ItemID      uint64  `json:"itemId"`
	Version     int     `json:"version"`
	Layer       string  `json:"layer"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Description string  `json:"retrievalHint"`
	MemoryType  string  `json:"memoryType"`
	Confidence  float64 `json:"confidence"`
	Importance  int     `json:"importance"`
	Tags        string  `json:"tags"`
	Reason      string  `json:"reason"`
}

type memoryDeleteRequest struct {
	ItemID  uint64 `json:"itemId"`
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}

type memoryRevisionsRequest struct {
	ItemID uint64 `json:"itemId"`
}

type memoryRollbackRequest struct {
	RevisionID uint64 `json:"revisionId"`
	Reason     string `json:"reason"`
}

// memoryAdminItemView 是管理面列表/写入返回的条目视图（含正文与归属，供管理面板审计）。
type memoryAdminItemView struct {
	ItemID      uint    `json:"itemId"`
	OwnerType   string  `json:"ownerType"`
	OwnerKey    string  `json:"ownerKey"`
	Layer       string  `json:"layer"`
	MemoryType  string  `json:"memoryType"`
	Confidence  float64 `json:"confidence"`
	Importance  int     `json:"importance"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Description string  `json:"description"`
	Tags        string  `json:"tags"`
	Source      string  `json:"source"`
	State       string  `json:"state"`
	Version     int     `json:"version"`
	LastReason  string  `json:"lastReason"`
	CreatedBy   string  `json:"createdBy"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

type memoryRevisionView struct {
	RevisionID uint   `json:"revisionId"`
	ItemID     uint   `json:"itemId"`
	Action     string `json:"action"`
	BeforeJSON string `json:"beforeJSON"`
	AfterJSON  string `json:"afterJSON"`
	Reason     string `json:"reason"`
	Source     string `json:"source"`
	CreatedBy  string `json:"createdBy"`
	CreatedAt  string `json:"createdAt"`
}

// requireMemoryEnabled 是管理面统一前置：memory 未启用时直接参数错误返回，避免表未建时裸查报错。
func requireMemoryEnabled(ctx *gin.Context) bool {
	if conf.CustomConf.LLM.React.Memory.MemoryEnabled() {
		return true
	}
	components.RenderJsonFail(ctx, components.ParamInvalidf("长期记忆未启用（llm.react.memory.enabled），请先开启并执行建表"))
	return false
}

// validateMemoryOwner 校验 owner 维度取值；ownerKey 由调用方显式给出（caller_key 或 caller_key|userName）。
func validateMemoryOwner(ownerType, ownerKey string) (model.MemoryOwner, error) {
	ownerType = strings.TrimSpace(ownerType)
	ownerKey = strings.TrimSpace(ownerKey)
	switch ownerType {
	case model.MemoryOwnerTypeCaller:
		if ownerKey == "" {
			return model.MemoryOwner{}, components.ParamInvalidf("ownerKey 不能为空（caller 维度传 callerKey）")
		}
		return model.BuildCallerMemoryOwner(ownerKey), nil
	case model.MemoryOwnerTypeCallerUser:
		if ownerKey == "" || !strings.Contains(ownerKey, "|") {
			return model.MemoryOwner{}, components.ParamInvalidf("caller_user 维度 ownerKey 格式为 callerKey|userName")
		}
		parts := strings.SplitN(ownerKey, "|", 2)
		return model.BuildCallerUserMemoryOwner(parts[0], parts[1]), nil
	default:
		return model.MemoryOwner{}, components.ParamInvalidf("ownerType 仅支持 caller / caller_user")
	}
}

func memoryAdminItemToView(item model.MemoryItem) memoryAdminItemView {
	return memoryAdminItemView{
		ItemID:      item.ID,
		OwnerType:   item.OwnerType,
		OwnerKey:    item.OwnerKey,
		Layer:       item.Layer,
		MemoryType:  item.MemoryType,
		Confidence:  item.Confidence,
		Importance:  item.Importance,
		Title:       item.Title,
		Content:     item.Content,
		Description: item.Description,
		Tags:        item.Tags,
		Source:      item.Source,
		State:       item.State,
		Version:     item.Version,
		LastReason:  item.LastReason,
		CreatedBy:   item.CreatedBy,
		CreatedAt:   item.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:   item.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

func memoryRevisionToView(revision model.MemoryRevision) memoryRevisionView {
	return memoryRevisionView{
		RevisionID: revision.ID,
		ItemID:     revision.ItemID,
		Action:     revision.Action,
		BeforeJSON: revision.BeforeJSON,
		AfterJSON:  revision.AfterJSON,
		Reason:     revision.Reason,
		Source:     revision.Source,
		CreatedBy:  revision.CreatedBy,
		CreatedAt:  revision.CreatedAt.Format("2006-01-02 15:04:05"),
	}
}

// memoryMutationResultView 把写核心返回的 map 附加条目当前视图，管理面板一次拿全。
func renderMemoryMutationSucc(ctx *gin.Context, result map[string]interface{}, itemID uint64) {
	view := map[string]interface{}{"result": result}
	if itemID > 0 {
		if item, err := model.GetMemoryItemByIDAnyState(ctx, uint(itemID)); err == nil && item != nil {
			view["item"] = memoryAdminItemToView(*item)
		}
	}
	components.RenderJsonSucc(ctx, view)
}

// ListMemories 分页查询记忆条目（管理面板列表页）。
//
//	@Summary      长期记忆列表
//	@Description  按 owner/layer/tag/keyword 过滤分页查询记忆条目；includeDeleted=true 时含软删（审计视图）
//	@Tags         React
//	@Accept       json
//	@Produce      json
//	@Router       /react/memory/list [post]
func ListMemories(ctx *gin.Context) {
	if !requireMemoryEnabled(ctx) {
		return
	}
	var req memoryListRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	layer := strings.TrimSpace(req.Layer)
	if layer != "" && layer != model.MemoryLayerResident && layer != model.MemoryLayerDetached {
		components.RenderJsonFail(ctx, components.ParamInvalidf("layer 仅支持 resident/detached"))
		return
	}
	memoryType := strings.TrimSpace(req.MemoryType)
	if memoryType != "" && !model.IsValidMemoryType(memoryType) {
		components.RenderJsonFail(ctx, components.ParamInvalidf("memoryType 仅支持 preference/fact/event/procedure"))
		return
	}
	items, total, err := model.FindMemoryItemsByFilter(ctx, model.MemoryItemFilter{
		OwnerType:      strings.TrimSpace(req.OwnerType),
		OwnerKey:       strings.TrimSpace(req.OwnerKey),
		Layer:          layer,
		MemoryType:     memoryType,
		Tag:            strings.TrimSpace(req.Tag),
		Keyword:        strings.TrimSpace(req.Keyword),
		IncludeDeleted: req.IncludeDeleted,
		Limit:          req.Limit,
		Offset:         req.Offset,
	})
	if err != nil {
		zlog.Errorf(ctx, "[Memory] 查询记忆列表失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	views := make([]memoryAdminItemView, 0, len(items))
	for _, item := range items {
		views = append(views, memoryAdminItemToView(item))
	}
	components.RenderJsonSucc(ctx, gin.H{"items": views, "total": total})
}

// CreateMemory 新增记忆（管理面播种 caller 级公共记忆的主要入口）。
//
//	@Summary      长期记忆新增
//	@Description  在指定记忆空间新增条目；与模型写入共用幂等收敛/容量守门/敏感拦截/修订流水（source=admin）
//	@Tags         React
//	@Accept       json
//	@Produce      json
//	@Router       /react/memory/create [post]
func CreateMemory(ctx *gin.Context) {
	if !requireMemoryEnabled(ctx) {
		return
	}
	var req memoryCreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	owner, err := validateMemoryOwner(req.OwnerType, req.OwnerKey)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	result, err := memoryService.ApplyMutation(ctx, memoryService.MutationInput{
		Action:      "create",
		Layer:       req.Layer,
		Title:       req.Title,
		Content:     req.Content,
		Description: req.Description,
		MemoryType:  strings.TrimSpace(req.MemoryType),
		Confidence:  req.Confidence,
		Importance:  req.Importance,
		Tags:        req.Tags,
		Reason:      req.Reason,
		Owner:       owner,
		Source:      model.MemorySourceAdmin,
		CreatedBy:   helpers.GetUserName(ctx),
	})
	if err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("%s", err.Error()))
		return
	}
	itemID, _ := result["itemId"].(uint)
	renderMemoryMutationSucc(ctx, result, uint64(itemID))
}

// UpdateMemory 人工修订记忆条目。
//
//	@Summary      长期记忆更新
//	@Description  按 itemId 修订条目（version>0 时乐观锁校验）；修订流水 action=update、source=admin
//	@Tags         React
//	@Accept       json
//	@Produce      json
//	@Router       /react/memory/update [post]
func UpdateMemory(ctx *gin.Context) {
	if !requireMemoryEnabled(ctx) {
		return
	}
	var req memoryUpdateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if req.ItemID == 0 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("itemId 不能为空"))
		return
	}
	result, err := memoryService.ApplyMutation(ctx, memoryService.MutationInput{
		Action:      "update",
		ItemID:      uint(req.ItemID),
		Version:     req.Version,
		Layer:       req.Layer,
		Title:       req.Title,
		Content:     req.Content,
		Description: req.Description,
		MemoryType:  strings.TrimSpace(req.MemoryType),
		Confidence:  req.Confidence,
		Importance:  req.Importance,
		Tags:        req.Tags,
		Reason:      req.Reason,
		Source:      model.MemorySourceAdmin,
		CreatedBy:   helpers.GetUserName(ctx),
	})
	if err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("%s", err.Error()))
		return
	}
	renderMemoryMutationSucc(ctx, result, req.ItemID)
}

// DeleteMemory 软删记忆条目。
//
//	@Summary      长期记忆删除
//	@Description  按 itemId 软删条目（version>0 时乐观锁校验）；修订流水 action=delete、source=admin
//	@Tags         React
//	@Accept       json
//	@Produce      json
//	@Router       /react/memory/delete [post]
func DeleteMemory(ctx *gin.Context) {
	if !requireMemoryEnabled(ctx) {
		return
	}
	var req memoryDeleteRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if req.ItemID == 0 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("itemId 不能为空"))
		return
	}
	result, err := memoryService.ApplyMutation(ctx, memoryService.MutationInput{
		Action:    "delete",
		ItemID:    uint(req.ItemID),
		Version:   req.Version,
		Reason:    req.Reason,
		Source:    model.MemorySourceAdmin,
		CreatedBy: helpers.GetUserName(ctx),
	})
	if err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("%s", err.Error()))
		return
	}
	renderMemoryMutationSucc(ctx, result, req.ItemID)
}

// ListMemoryRevisions 查询条目修订历史（正序，含前后快照）。
//
//	@Summary      长期记忆修订历史
//	@Description  按 itemId 返回该条目的全部修订流水（create/update/delete/rollback 前后快照与原因）
//	@Tags         React
//	@Accept       json
//	@Produce      json
//	@Router       /react/memory/revisions [post]
func ListMemoryRevisions(ctx *gin.Context) {
	if !requireMemoryEnabled(ctx) {
		return
	}
	var req memoryRevisionsRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if req.ItemID == 0 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("itemId 不能为空"))
		return
	}
	revisions, err := model.FindMemoryRevisionsByItemID(ctx, uint(req.ItemID))
	if err != nil {
		zlog.Errorf(ctx, "[Memory] 查询修订历史失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	views := make([]memoryRevisionView, 0, len(revisions))
	for _, revision := range revisions {
		views = append(views, memoryRevisionToView(revision))
	}
	components.RenderJsonSucc(ctx, views)
}

// RollbackMemory 回滚条目到指定修订的 before 快照。
//
//	@Summary      长期记忆回滚
//	@Description  按 revisionId 把条目恢复到该修订的前置状态，以 rollback 修订反向提交；create 修订不可回滚
//	@Tags         React
//	@Accept       json
//	@Produce      json
//	@Router       /react/memory/rollback [post]
func RollbackMemory(ctx *gin.Context) {
	if !requireMemoryEnabled(ctx) {
		return
	}
	var req memoryRollbackRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("请求体解析失败: %s", err.Error()))
		return
	}
	if req.RevisionID == 0 {
		components.RenderJsonFail(ctx, components.ParamInvalidf("revisionId 不能为空"))
		return
	}
	result, err := memoryService.RollbackRevision(ctx, uint(req.RevisionID), req.Reason, helpers.GetUserName(ctx))
	if err != nil {
		components.RenderJsonFail(ctx, components.ParamInvalidf("%s", err.Error()))
		return
	}
	itemID, _ := result["itemId"].(uint)
	renderMemoryMutationSucc(ctx, result, uint64(itemID))
}
