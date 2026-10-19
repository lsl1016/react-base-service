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

// Command 自定义斜杠命令（tblLlmCommand）：按 app+route 作用域解析，
// 用户输入 /name args 时由命令分发层展开模板后替换 UserPrompt 入模。
// 表结构与 CRUD 口径照 tblLlmSystemPrompt 样板。
type Command struct {
	ID            uint                  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	CommandKey    string                `json:"commandKey" gorm:"column:command_key;not null"`
	Name          string                `json:"name" gorm:"column:name;not null"`
	Description   string                `json:"description" gorm:"column:description;not null;default:''"`
	ArgumentHint  string                `json:"argumentHint" gorm:"column:argument_hint;not null;default:''"`
	Content       string                `json:"content" gorm:"column:content;type:mediumtext;not null"`
	AppKey        string                `json:"appKey" gorm:"column:app_key;not null"`
	RouteValues   string                `json:"routeValues" gorm:"column:route_values;not null;default:'[]'"`
	Status        int                   `json:"status" gorm:"column:status;not null;default:1"`
	CreatedBy     string                `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	UpdatedBy     string                `json:"updatedBy" gorm:"column:updated_by;not null;default:''"`
	CreatedAt     time.Time             `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt     time.Time             `json:"updatedAt" gorm:"column:updated_at"`
	DeletedAt     soft_delete.DeletedAt `json:"deletedAt" gorm:"column:deleted_at;not null;default:0"`
}

func (*Command) TableName() string { return "tblLlmCommand" }

func CreateCommand(ctx *gin.Context, c *Command) error {
	if err := helpers.MysqlClientLLM.Model(&Command{}).WithContext(ctx).Create(c).Error; err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetCommandByKey(ctx *gin.Context, commandKey string) (*Command, error) {
	var c Command
	err := helpers.MysqlClientLLM.Model(&Command{}).WithContext(ctx).
		Where("command_key = ?", commandKey).First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &c, nil
}

// FindCommandsByAppAndRoutes 按能力作用域 + 路由前缀解析启用的自定义命令
//（与 FindSkillsByAppAndRoutes 同口径：route_values JSON 串精确值 IN 匹配）。
func FindCommandsByAppAndRoutes(ctx *gin.Context, scopeKeys []string, routePrefixes []string) ([]Command, error) {
	var commands []Command
	err := helpers.MysqlClientLLM.Model(&Command{}).WithContext(ctx).
		Where("app_key IN ? AND status = 1 AND route_values IN ?", scopeKeys, routePrefixes).
		Order("created_at ASC").
		Find(&commands).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return commands, nil
}

// ListAllCommands 管理控制台全量列表（AppKey 为空时全部 app）。
func ListAllCommands(ctx *gin.Context, appKey string) ([]Command, error) {
	query := helpers.MysqlClientLLM.Model(&Command{}).WithContext(ctx)
	if appKey != "" {
		query = query.Where("app_key = ?", appKey)
	}
	var commands []Command
	if err := query.Order("app_key ASC, created_at DESC").Find(&commands).Error; err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return commands, nil
}

// UpdateCommandByKey 按业务键局部更新（map 零值不更新语义由调用方保证）。
func UpdateCommandByKey(ctx *gin.Context, commandKey string, updates map[string]any) error {
	if err := helpers.MysqlClientLLM.Model(&Command{}).WithContext(ctx).
		Where("command_key = ?", commandKey).Updates(updates).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}

// DeleteCommandByKey 软删除。
func DeleteCommandByKey(ctx *gin.Context, commandKey string) error {
	if err := helpers.MysqlClientLLM.Model(&Command{}).WithContext(ctx).
		Where("command_key = ?", commandKey).Delete(&Command{}).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}
