package model

import (
	"errors"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

const (
	// AgentPermissionModeInherit 继承父 run 权限（默认；P2 危险操作确认生效前的唯一行为）。
	AgentPermissionModeInherit = "inherit"
	// AgentPermissionModeAuto / Confirm / ConfirmRisky 预留给 P2 工具级危险操作确认。
	AgentPermissionModeAuto         = "auto"
	AgentPermissionModeConfirm      = "confirm"
	AgentPermissionModeConfirmRisky = "confirm_risky"
)

// Agent 是注册类资源「子 Agent」：主 Agent 通过 delegate_agent 工具把子任务委派给它，
// 引擎按定义装配隔离子 run（独立历史/步数上限/工具与 Skill 白名单）执行。
type Agent struct {
	ID             uint                  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	AgentID        string                `json:"agentId" gorm:"column:agent_id;not null"`
	AgentKey       string                `json:"agentKey" gorm:"column:agent_key;not null"`
	Name           string                `json:"name" gorm:"column:name;not null"`
	Description    string                `json:"description" gorm:"column:description;not null"`
	CallerKey      string                `json:"callerKey" gorm:"column:caller_key;not null"`
	RouteValues    string                `json:"routeValues" gorm:"column:route_values;not null;default:'[]'"`
	SystemPrompt   string                `json:"systemPrompt" gorm:"column:system_prompt;type:mediumtext;not null"`
	ModelKey       string                `json:"modelKey" gorm:"column:model_key"`
	ModelVersion   string                `json:"modelVersion" gorm:"column:model_version"`
	ToolsJSON      string                `json:"toolsJson" gorm:"column:tools_json;not null;default:'[]'"`
	SkillsJSON     string                `json:"skillsJson" gorm:"column:skills_json;not null;default:'[]'"`
	MaxSteps       int                   `json:"maxSteps" gorm:"column:max_steps;not null;default:8"`
	// MaxTokensPerRun 是子 run 递归 token 预算上限（P3：0=不限），
	// 口径 = 本 run 输入+输出+委派孙代理的 delegated_*（OH max_budget_per_run 的 token 版）。
	MaxTokensPerRun int `json:"maxTokensPerRun" gorm:"column:max_tokens_per_run;not null;default:0"`
	// ReadOnly 声明只读执行域（WP2 内置只读子代理同款语义）：子 run 内一切非 readOnly
	// 业务工具（含 client 工具）被硬拦截。单调收紧——嵌套委派只能继承不能放宽。
	ReadOnly        int                   `json:"readOnly" gorm:"column:read_only;not null;default:0"`
	PermissionMode  string                `json:"permissionMode" gorm:"column:permission_mode;not null;default:'inherit'"`
	Status         int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy      string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy      string                `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt      time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt      time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt      soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (a *Agent) TableName() string {
	return "tblLlmAgent"
}

func CreateAgent(ctx *gin.Context, agent *Agent) error {
	err := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).Create(agent).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetAgentByAgentID(ctx *gin.Context, agentID string) (*Agent, error) {
	var agent Agent
	err := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).
		Where("agent_id = ?", agentID).First(&agent).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &agent, nil
}

func GetAgentByAgentIDUnscoped(ctx *gin.Context, agentID string) (*Agent, error) {
	var agent Agent
	err := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).
		Unscoped().
		Where("agent_id = ?", agentID).First(&agent).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &agent, nil
}

// GetAgentByCallerAndAgentKey 按 caller + agent_key 精确定位（不含默认作用域合并）。
func GetAgentByCallerAndAgentKey(ctx *gin.Context, callerKey, agentKey string) (*Agent, error) {
	var agent Agent
	err := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).
		Where("caller_key = ? AND agent_key = ?", callerKey, agentKey).First(&agent).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &agent, nil
}

// GetAgentByCallerAndAgentKeyUnscoped 含软删行按 caller + agent_key 定位：
// uk_caller_agent 不含 deleted_at，重建同 key agent 时需查软删行做复活更新。
func GetAgentByCallerAndAgentKeyUnscoped(ctx *gin.Context, callerKey, agentKey string) (*Agent, error) {
	var agent Agent
	err := helpers.MysqlClientLLM.Unscoped().Model(&Agent{}).WithContext(ctx).
		Where("caller_key = ? AND agent_key = ?", callerKey, agentKey).First(&agent).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &agent, nil
}

func UpdateAgentByAgentIDUnscoped(ctx *gin.Context, agentID string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Unscoped().Model(&Agent{}).WithContext(ctx).
		Where("agent_id = ?", agentID).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func UpdateAgentByAgentID(ctx *gin.Context, agentID string, updates map[string]interface{}) error {
	tx := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).
		Where("agent_id = ?", agentID).
		Updates(updates)
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func SoftDeleteAgentByAgentID(ctx *gin.Context, agentID string) error {
	tx := helpers.MysqlClientLLM.WithContext(ctx).Where("agent_id = ?", agentID).Delete(&Agent{})
	if tx.Error != nil {
		return components.ErrorDbUpdate.Wrap(tx.Error)
	}
	return nil
}

func ListAllAgents(ctx *gin.Context) ([]Agent, error) {
	var agents []Agent
	err := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).
		Order("caller_key ASC, agent_key ASC").
		Find(&agents).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return agents, nil
}

// ListAgentsByCallerAndRoutes 按 callerKey + 路由前缀匹配查询 agent 列表（管理列表语义，不过滤 status）。
func ListAgentsByCallerAndRoutes(ctx *gin.Context, callerKey string, routePrefixes []string) ([]Agent, error) {
	var agents []Agent
	db := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).
		Where("caller_key = ?", callerKey)
	if len(routePrefixes) > 0 {
		db = db.Where("route_values IN ?", routePrefixes)
	}
	err := db.Order("agent_key ASC").Find(&agents).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return agents, nil
}

// FindAgentsByCallerAndRoutes 按 callerKey + 路由前缀匹配查询启用的 agent；
// 同时并入「默认作用域」（caller_key=default）下命中的 agent，对全部 caller 生效。
func FindAgentsByCallerAndRoutes(ctx *gin.Context, callerKey string, routePrefixes []string) ([]Agent, error) {
	var agents []Agent
	err := helpers.MysqlClientLLM.Model(&Agent{}).WithContext(ctx).
		Where("caller_key IN ? AND status = 1 AND route_values IN ?", CallerScopeKeys(callerKey), routePrefixes).
		Order("agent_key ASC").
		Find(&agents).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return agents, nil
}
