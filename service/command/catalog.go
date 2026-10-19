package command

import (
	"github.com/gin-gonic/gin"

	"react-base-service/components/params"
	"react-base-service/components/route"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// ListCatalog 聚合 app 可见命令目录（前端 / 菜单数据源，单一权威源）：
// 内置表（workflow 受 llm.workflow.enabled 联动剔除）+ DB 自定义命令（app+route 作用域）。
// slash_command.enabled 未开时返回空（前端不显示命令分组）。
func ListCatalog(ctx *gin.Context, appKey string, routeValues []string) ([]params.CommandCatalogItem, error) {
	if !conf.GetReactRuntimeConfigForApp(appKey).SlashCommand.SlashCommandEnabled() {
		return []params.CommandCatalogItem{}, nil
	}
	items := make([]params.CommandCatalogItem, 0, 8)
	for i := range builtinCommands {
		def := &builtinCommands[i]
		if visibleBuiltin(def) == nil {
			continue
		}
		items = append(items, params.CommandCatalogItem{
			Name:         def.Name,
			Description:  def.Description,
			ArgumentHint: def.ArgumentHint,
			Source:       SourceBuiltin,
			Group:        def.Group,
		})
	}
	app, err := model.GetActiveAppByKey(ctx, appKey)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return items, nil
	}
	commands, err := model.FindCommandsByAppAndRoutes(ctx, model.AppCapabilityScopeKeys(app), route.BuildRoutePrefixes(routeValues))
	if err != nil {
		return nil, err
	}
	for _, c := range commands {
		items = append(items, params.CommandCatalogItem{
			Name:         c.Name,
			Description:  c.Description,
			ArgumentHint: c.ArgumentHint,
			Source:       SourceCustom,
			Group:        GroupCustom,
		})
	}
	return items, nil
}
