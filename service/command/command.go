// Package command 是斜杠命令的解析与目录服务（docs/plan/20261013_斜杠命令支持设计方案.md）。
//
// 依赖边界：只依赖 models/llm、components/{route,params}、conf 与 gin，
// 不 import service/react、service/plan、service/workflow（与 plan→react 分流同理由：
// 依赖方向必须单向，执行胶水留在 controller 层）。
//
// 命令三分型（对齐 ZCode 的分发模型）：
//   - 控制型：命令分发层就地处理，不产生模型 turn（P0 仅错误路径，机制为 P1 /help、/new 预留）；
//   - 标记型：附加指令（RunDirective）进入正常 run 管线（/compact 强制压缩）；
//   - 展开型：模板展开后替换 UserPrompt 进入正常 run（/workflow、自定义命令）。
package command

import (
	"strings"

	"github.com/gin-gonic/gin"

	"react-base-service/components/route"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// Kind 命令语义三分型。
type Kind int

const (
	KindControl    Kind = iota // 控制型：分发层就地处理，不产生模型 turn
	KindFlaggedRun             // 标记型：附加指令进入正常 run 管线
	KindExpansion              // 展开型：模板展开后替换 UserPrompt
)

// Source 取值。
const (
	SourceBuiltin = "builtin"
	SourceCustom  = "custom"
)

// CatalogGroup 前端浮层分组名。
const (
	GroupBuiltin = "命令"
	GroupCustom  = "自定义命令"
)

// Definition 是一条命令的完整定义。
type Definition struct {
	Name         string
	Aliases      []string
	Kind         Kind
	Description  string
	ArgumentHint string
	Group        string
	Source       string
	// Expand 把参数展开为入模正文（内置展开型命令的代码内模板）；自定义命令走 ExpandTemplate。
	Expand func(args string) string
	// CustomContent 非空表示自定义命令，展开时以其为模板。
	CustomContent string
	// RequireArgs 标记参数必填（空参数时命令分发层直接回执拒绝，不展开）。
	RequireArgs bool
}

// Resolution 是一次命令解析的结果。
type Resolution struct {
	// Name 是用户输入的原名（别名归一前，回执展示用）。
	Name string
	// Args 是首 token 之后的参数原文（已 TrimSpace）。
	Args string
	// Definition 命中时非 nil；nil 且 UnknownReserved=true 表示命中保留名但未实现/未启用。
	Definition *Definition
	// UnknownReserved 表示首 token 是保留名但当前不可用（未实现或能力开关未开）。
	UnknownReserved bool
}

// builtinCommands 内置命令表（唯一权威定义处；保留名集合与之同源）。
var builtinCommands = []Definition{
	{
		Name:         "compact",
		Aliases:      []string{"compress"},
		Kind:         KindFlaggedRun,
		Description:  "手动压缩当前会话上下文，可选附加摘要指令影响压缩侧重",
		ArgumentHint: "[附加摘要指令]",
		Group:        GroupBuiltin,
		Source:       SourceBuiltin,
	},
	{
		Name:         "workflow",
		Kind:         KindExpansion,
		Description:  "把任务描述创建为无人值守 workflow（定时/手动触发的完整自动执行任务）",
		ArgumentHint: "<任务描述>",
		Group:        GroupBuiltin,
		Source:       SourceBuiltin,
		Expand:       expandWorkflow,
		RequireArgs:  true,
	},
}

// reservedNames 保留名集合：内置命令名与别名 + P1 预留命令名。
// 自定义命令不允许使用（创建时校验）；用户输入命中保留名但不可用时给明确回执，
// 而不是静默当普通 prompt（避免「/plan 打错成无反馈」）。
var reservedNames = buildReservedNames()

func buildReservedNames() map[string]bool {
	names := map[string]bool{}
	for _, def := range builtinCommands {
		names[def.Name] = true
		for _, alias := range def.Aliases {
			names[alias] = true
		}
	}
	// P1 预留：executionMode 映射与低成本控制型命令。
	for _, name := range []string{"plan", "react", "help", "new"} {
		names[name] = true
	}
	return names
}

// IsReservedName 判断名字是否保留（自定义命令创建校验用）。
func IsReservedName(name string) bool {
	return reservedNames[strings.ToLower(strings.TrimSpace(name))]
}

// IsKnownCommandName 判断名字是否为当前可用的内置命令或保留名（纯词法、无 DB 访问）；
// steering 准入兜底用：已知命令不允许 guide 注入/排队（会丢失命令语义）。
func IsKnownCommandName(name string) bool {
	return LookupBuiltin(name) != nil || IsReservedName(name)
}

// LookupBuiltin 按名字/别名查内置命令（含 workflow.enabled 门控：关闭时 /workflow 按普通 prompt 上送）。
func LookupBuiltin(name string) *Definition {
	for i := range builtinCommands {
		def := &builtinCommands[i]
		if def.Name == name {
			return visibleBuiltin(def)
		}
		for _, alias := range def.Aliases {
			if alias == name {
				return visibleBuiltin(def)
			}
		}
	}
	return nil
}

func visibleBuiltin(def *Definition) *Definition {
	if def.Name == "workflow" && !conf.CustomConf.LLM.Workflow.Enabled {
		return nil
	}
	return def
}

// Resolve 解析 userPrompt 首 token：
//   - slash_command.enabled 未开 → 一律返回 nil（直通，零行为变化）；
//   - 命中内置命令 → Definition（含别名归一）；
//   - 命中保留名但不可用 → {UnknownReserved: true}；
//   - 命中 app+route 作用域内的自定义命令 → Definition（Source=custom）；
//   - 其余（含任意 / 开头但非命令的普通文本，如「/etc/hosts 路径」）→ nil。
func Resolve(ctx *gin.Context, appKey string, routeValues []string, userPrompt string) *Resolution {
	if !conf.GetReactRuntimeConfigForApp(appKey).SlashCommand.SlashCommandEnabled() {
		return nil
	}
	name, args, ok := SplitCommand(userPrompt)
	if !ok {
		return nil
	}
	res := &Resolution{Name: name, Args: args}
	if def := LookupBuiltin(name); def != nil {
		res.Definition = def
		return res
	}
	if IsReservedName(name) {
		res.UnknownReserved = true
		return res
	}
	if def := resolveCustom(ctx, appKey, routeValues, name); def != nil {
		res.Definition = def
		return res
	}
	// 未知 / 开头短词：无法与「消息里引用 / 路径」区分，按普通 prompt 直通。
	return nil
}

// resolveCustom 在 app+route 作用域内按名字精确匹配自定义命令。
func resolveCustom(ctx *gin.Context, appKey string, routeValues []string, name string) *Definition {
	app, err := model.GetActiveAppByKey(ctx, appKey)
	if err != nil || app == nil {
		return nil
	}
	commands, err := model.FindCommandsByAppAndRoutes(ctx, model.AppCapabilityScopeKeys(app), route.BuildRoutePrefixes(routeValues))
	if err != nil {
		return nil
	}
	for _, c := range commands {
		if c.Name != name {
			continue
		}
		return &Definition{
			Name:          c.Name,
			Kind:          KindExpansion,
			Description:   c.Description,
			ArgumentHint:  c.ArgumentHint,
			Group:         GroupCustom,
			Source:        SourceCustom,
			CustomContent: c.Content,
		}
	}
	return nil
}
