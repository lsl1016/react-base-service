package model

import (
	"encoding/json"
	"errors"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

type Tool struct {
	ID          uint   `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ToolID      string `json:"toolId" gorm:"column:tool_id;not null"`
	Name        string `json:"name" gorm:"column:name;not null"`
	Description string `json:"description" gorm:"column:description"`
	ToolType    string `json:"toolType" gorm:"column:tool_type;not null"`
	CallerKey   string `json:"callerKey" gorm:"column:caller_key;not null"`
	RouteValues string `json:"routeValues" gorm:"column:route_values"`
	Config      string `json:"config" gorm:"column:config"`
	// PermissionMode 是工具级危险操作确认模式：auto=自动执行（默认）、confirm=每次人工确认、
	// confirm_risky=入参/工具名命中风险正则才确认（P2-3）。
	PermissionMode string                `json:"permissionMode" gorm:"column:permission_mode;not null;default:'auto'"`
	Status         int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy      string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy      string                `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt      time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt      time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt      soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (t *Tool) TableName() string {
	return "tblLlmTool"
}

func CreateTool(ctx *gin.Context, tool *Tool) error {
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).Create(tool).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

// ExistToolByCallerAndName 检查同一 caller 下是否存在同名 tool
func ExistToolByCallerAndName(ctx *gin.Context, callerKey, name string) (bool, error) {
	var count int64
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("caller_key = ? AND name = ?", callerKey, name).
		Count(&count).Error
	if err != nil {
		return false, components.ErrorDbSelect.Wrap(err)
	}
	return count > 0, nil
}

func GetToolByToolID(ctx *gin.Context, toolID string) (*Tool, error) {
	var tool Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("tool_id = ?", toolID).First(&tool).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &tool, nil
}

// GetToolByToolIDUnscoped 含软删除记录查询（MCP 连接重新启用时恢复同步用：
// 软删除行仍占用 uk_tool_id 唯一键，只能更新恢复，不能重复插入）。
func GetToolByToolIDUnscoped(ctx *gin.Context, toolID string) (*Tool, error) {
	var tool Tool
	err := helpers.MysqlClientLLM.Unscoped().Model(&Tool{}).WithContext(ctx).
		Where("tool_id = ?", toolID).First(&tool).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &tool, nil
}

func UpdateToolByToolID(ctx *gin.Context, toolID string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("tool_id = ?", toolID).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

// UpdateToolByToolIDUnscoped 含软删除行的更新（恢复软删除的 MCP 注册工具用）。
func UpdateToolByToolIDUnscoped(ctx *gin.Context, toolID string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Unscoped().Model(&Tool{}).WithContext(ctx).
		Where("tool_id = ?", toolID).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func SoftDeleteToolByToolID(ctx *gin.Context, toolID string) error {
	tx := helpers.MysqlClientLLM.WithContext(ctx).Where("tool_id = ?", toolID).Delete(&Tool{})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListToolsByCaller(ctx *gin.Context, callerKey string) ([]Tool, error) {
	var tools []Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("caller_key = ?", callerKey).
		Order("created_at DESC").
		Find(&tools).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return tools, nil
}

// ListToolsByCallerAndExactRoute 按 callerKey + routeValues 精确匹配查询 tool 列表
func ListToolsByCallerAndExactRoute(ctx *gin.Context, callerKey string, routeValues string) ([]Tool, error) {
	var tools []Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("caller_key = ? AND route_values = ?", callerKey, routeValues).
		Order("created_at DESC").
		Find(&tools).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return tools, nil
}

// ListToolsByType 按工具类型查询全部工具（含停用；MCP 注册表清理/统计用）。
func ListToolsByType(ctx *gin.Context, toolType string) ([]Tool, error) {
	var tools []Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("tool_type = ?", toolType).
		Find(&tools).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return tools, nil
}

// ListMCPServerTools 返回指定 MCP 服务器同步进注册表的工具（按 config.mcpServer 匹配）。
func ListMCPServerTools(ctx *gin.Context, serverName string) ([]Tool, error) {
	tools, err := ListToolsByType(ctx, "mcp")
	if err != nil {
		return nil, err
	}
	matched := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		var cfg struct {
			MCPServer string `json:"mcpServer"`
		}
		if err := json.Unmarshal([]byte(tool.Config), &cfg); err != nil || cfg.MCPServer != serverName {
			continue
		}
		matched = append(matched, tool)
	}
	return matched, nil
}

// OfflineMCPServerTools 把一个 MCP 服务器同步进注册表的工具标记停用（status=0，不软删：
// 重新连接成功后下次同步会自动恢复启用，行保留供管理面审计）。
//   - callerKey 为空 = 全部 caller 名下的副本（连接检测失败，整台服务器失联下线）；
//     非空 = 仅该 caller 名下（同步时清理该作用域内服务器端已下架的工具）。
//   - activeNames 非空时只停用名单外的工具（工具名 <server>_<tool>，刷新同步用）；
//     为空 = 匹配到的全部停用。
//   - 已停用行与软删除行不受影响。返回本次停用的行数。
func OfflineMCPServerTools(ctx *gin.Context, serverName, callerKey string, activeNames []string) (int64, error) {
	tools, err := ListToolsByType(ctx, "mcp")
	if err != nil {
		return 0, err
	}
	active := make(map[string]bool, len(activeNames))
	for _, name := range activeNames {
		active[name] = true
	}
	offlined := int64(0)
	for _, tool := range tools {
		if tool.Status == 0 {
			continue
		}
		if callerKey != "" && tool.CallerKey != callerKey {
			continue
		}
		var cfg struct {
			MCPServer string `json:"mcpServer"`
		}
		if err := json.Unmarshal([]byte(tool.Config), &cfg); err != nil || cfg.MCPServer != serverName {
			continue
		}
		if active[tool.Name] {
			continue
		}
		if err := UpdateToolByToolID(ctx, tool.ToolID, map[string]interface{}{
			"status":     0,
			"updated_by": "mcp-sync",
		}); err != nil {
			return offlined, err
		}
		offlined++
	}
	return offlined, nil
}

// ListGatewayHTTPTools 返回 MCP 网关的工具基础集合：全部启用的 http 工具行
//（跨 caller，caller_key 仅作归属标记，不参与 MCP 可见性；route_values 同样不参与）。
// 工具对哪个应用可见由 tblLlmMcpAppTool 白名单决定。同名工具可能存在于多个 caller
// 名下，排序按 name/id 固定，由调用方（网关注册表）按名去重。
func ListGatewayHTTPTools(ctx *gin.Context) ([]Tool, error) {
	var tools []Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("status = 1 AND tool_type = 'http'").
		Order("name ASC, id ASC").Find(&tools).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return tools, nil
}

// GetHTTPToolsByIDs 按自增主键批量查询 http 工具行（mcp-server 管理台兼容层用；
// 数字 ID 是该管理台编辑/授权的操作键）。
func GetHTTPToolsByIDs(ctx *gin.Context, ids []uint) ([]Tool, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var tools []Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("id IN ? AND tool_type = 'http'", ids).Find(&tools).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return tools, nil
}

// FindToolsByCallerAndRoutes 按 callerKey + 路由前缀匹配查询 tool；
// 同时并入「默认作用域」（caller_key=default）下命中的工具，对全部 caller 生效。
func FindToolsByCallerAndRoutes(ctx *gin.Context, callerKey string, routePrefixes []string) ([]Tool, error) {
	var tools []Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Where("caller_key IN ? AND status = 1 AND route_values IN ?", CallerScopeKeys(callerKey), routePrefixes).
		Find(&tools).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return tools, nil
}

// ListAllTools 列出全部 caller 的工具（管理控制台「全部」视图用）。
func ListAllTools(ctx *gin.Context) ([]Tool, error) {
	var tools []Tool
	err := helpers.MysqlClientLLM.Model(&Tool{}).WithContext(ctx).
		Order("caller_key ASC, created_at DESC").
		Find(&tools).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return tools, nil
}
