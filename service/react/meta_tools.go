package react

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/params"
	"react-base-service/components/route"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

const (
	metaToolListTools      = "list_tools"
	metaToolGetTool        = "get_tool"
	metaToolExecuteTool    = "execute_tool"
	metaToolListSkills     = "list_skills"
	metaToolGetSkill       = "get_skill"
	metaToolReadToolResult = "read_tool_result"
	metaToolInspectData    = "inspect_data"
	metaToolPythonExec     = "python_exec"
	metaToolTodoWrite      = "todo_write"
	metaToolAskQuestion    = "ask_question"
	metaToolDisplayFiles   = "displayFiles"
	// metaToolResolveAsyncTask 标记异步任务已完结，是 pending 提醒的唯一清除入口（TTL 过期兜底）。
	metaToolResolveAsyncTask = "resolve_async_task"
	// metaToolGetAsyncTask 回读异步任务完整记录，用于提醒内容被截断时取全量提交参数/响应。
	metaToolGetAsyncTask = "get_async_task"
	// metaToolReadAttachment 读取上传附件文本内容到上下文（理解类任务）。
	metaToolReadAttachment = "read_attachment"
	// metaToolInspectAttachment 探查 csv 附件的表结构（写分析代码前的准备）。
	metaToolInspectAttachment = "inspect_attachment"
	// metaToolCreatePlan 提交分步执行计划，等待用户在前端确认后执行（第一期：确认交互闭环）。
	metaToolCreatePlan = "create_plan"
	// metaToolLoadRuntimeCode 加载服务线上代码到本 run 专属工作区并挂载只读检索工具（P2-1）。
	metaToolLoadRuntimeCode = "load_runtime_code"
	// metaToolDelegateAgent 把子任务委派给注册表中的专家子 Agent（隔离子 run 执行，结果回填父循环）。
	// 描述按 caller 可见 agent 清单动态渲染，主 LLM 由此"发现"子代理（OH TaskToolSet 模式）。
	metaToolDelegateAgent = "delegate_agent"
	// metaToolSendMessage 向已委派的子代理发送补充消息（A2 父→子通道，见 send_message.go；
	// 复用 S1 guide 注入机制，对齐 ZCode messageSink）。
	metaToolSendMessage = "send_message"
	// 长期记忆三工具：memory.enabled 开启时注册（见 memory.go）。
	metaToolMemoryList  = "memory_list"
	metaToolMemoryRead  = "memory_read"
	metaToolMemoryWrite = "memory_write"
	// 时序事实图谱记忆工具：graph_memory.enabled 开启时注册（见 graph_memory.go）。
	metaToolGraphMemorySearch = "graph_memory_search"
	metaToolGraphMemoryWrite  = "graph_memory_write"
)

// isInternalMetaTool 判断工具名是否属于 Runtime 内置 Meta Tool，内置工具不走外部工具注册表。
func isInternalMetaTool(name string) bool {
	switch name {
	case metaToolListTools, metaToolGetTool, metaToolExecuteTool, metaToolListSkills, metaToolGetSkill, metaToolReadToolResult, metaToolInspectData, metaToolPythonExec, metaToolTodoWrite, metaToolAskQuestion, metaToolDisplayFiles, metaToolResolveAsyncTask, metaToolGetAsyncTask, metaToolReadAttachment, metaToolInspectAttachment, metaToolCreatePlan, metaToolDelegateAgent, metaToolSendMessage, metaToolLoadRuntimeCode, metaToolMemoryList, metaToolMemoryRead, metaToolMemoryWrite, metaToolGraphMemorySearch, metaToolGraphMemoryWrite:
		return true
	default:
		return false
	}
}

// internalMetaToolDefinitions 定义 Runtime 内置工具（稳定 Meta Tool 集合）。
// allow_plan=false 时 create_plan 不进工具列表（模型不可见）。
func internalMetaToolDefinitions() []llm.ToolDefinition {
	definitions := []llm.ToolDefinition{
		// list_tools 已软下线：Business Tool 轻量索引在 run 初始化阶段注入 system 前缀，完整 parameters 仍通过 get_tool 按需加载。
		objectTool(metaToolGetTool, "按 toolId 或 name 加载一个 Business Tool，并返回其 parameters/outputSchema；加载后必须通过 execute_tool 调用，不要直接调用返回的业务工具名", map[string]interface{}{"toolId": stringSchema("工具ID，可选"), "name": stringSchema("工具名称，可选")}),
		executeToolDefinition(),
		// list_skills 已软下线：Skill 摘要在 run 初始化阶段注入 system 前缀，完整说明仍通过 get_skill 按需加载。
		objectTool(metaToolGetSkill, "按 skillId 或 name 加载一个 Skill 的完整说明", map[string]interface{}{"skillId": stringSchema("技能ID，可选"), "name": stringSchema("技能名称，可选")}),
		objectTool(metaToolReadToolResult, "读取 resultRef 指向的工具大结果或 StepResultRef 指向的正式步骤结果。该工具只读取授权范围内的原始内容；理解 JSON 结构时优先使用 inspect_data。", map[string]interface{}{"resultRef": stringSchema("工具结果 resultRef，或 StepResultRef.step_result_id"), "offset": numberSchema("读取起始位置，可选"), "limit": numberSchema("单次读取长度，可选")}),
		inspectDataToolDefinition(),
		pythonExecToolDefinition(),
		todoWriteToolDefinition(),
		askQuestionToolDefinition(),
		displayFilesToolDefinition(),
		resolveAsyncTaskToolDefinition(),
		getAsyncTaskToolDefinition(),
		readAttachmentToolDefinition(),
		inspectAttachmentToolDefinition(),
	}
	if conf.CustomConf.LLM.React.AllowPlanEnabled() {
		definitions = append(definitions, createPlanToolDefinition())
	}
	// workspace.enabled=true 时注册 load_runtime_code（P2-1 代码工作区入口）。
	if conf.CustomConf.LLM.React.Workspace.WorkspaceEnabled() {
		definitions = append(definitions, loadRuntimeCodeDefinition())
	}
	// memory.enabled=true 时注册长期记忆三工具（list/read/write），关闭时模型不可见。
	if conf.CustomConf.LLM.React.Memory.MemoryEnabled() {
		definitions = append(definitions, memoryToolDefinitions()...)
	}
	// graph_memory.enabled=true 时注册时序图谱记忆工具（search + 可选 write），关闭时模型不可见。
	if conf.CustomConf.LLM.React.GraphMemory.GraphMemoryEnabled() {
		definitions = append(definitions, graphMemoryToolDefinitions()...)
	}
	return definitions
}

// runtimeToolDefinitions 计算一个 run 的完整工具声明集（含 delegate_agent 动态渲染），
// 供引擎每轮装配与 run/delegate 入口 token 检查两处共用，避免口径漂移。
func runtimeToolDefinitions(req *runtimeRequest, profile ExecutionProfile) []llm.ToolDefinition {
	definitions := internalMetaToolDefinitionsForType(req.payload.Type)
	if profile.AllowSubagent && req.delegationAllowed() {
		definitions = append(definitions, delegateAgentToolDefinition(req.agents))
		definitions = append(definitions, sendMessageToolDefinition())
	}
	return definitions
}

func executeToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolExecuteTool,
		Description: "执行已通过 get_tool 加载的 Business Tool；description 用一句话说明本次工具调用目的，仅用于前端展示；input/arguments 必须符合 get_tool 返回的 parameters。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。**必须使用名词短语，禁止以动词开头**。错误：加载xxx模块 / 查看当前可用的业务工具；正确：xxx模块 / 当前可用的业务工具。错误：获取报表数据，正确：报表数据。错误：更新图表配置，正确：图表配置更新。错误：查看可用工具列表，正确：可用工具列表。"),
				"toolId":      stringSchema("工具ID，可选；推荐使用。"),
				"name":        stringSchema("工具展示名，可选。"),
				"callName":    stringSchema("get_tool 返回的 callName，可选。"),
				"input": map[string]interface{}{
					"type":        "object",
					"description": "业务工具入参对象。",
				},
				"arguments": map[string]interface{}{
					"type":        "object",
					"description": "业务工具入参对象；与 input 二选一。",
				},
			},
			"required": []string{"description"},
		},
	}
}

func pythonExecToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolPythonExec,
		Description: "执行 Python 代码。inputs 是可选的命名输入集合；没有外部输入时可以不传或传空对象，Python 侧会收到空输入容器。如需传入数据，inputs 的 key 是 Python 中使用的 alias，value 描述输入来源。支持六种输入来源：tool_result、raw_json、text、expr、attachment、http。type=tool_result 时，只需提供 ref=resultRef；后端会读取该前序工具结果并按 JSON 解析后放入对应 alias。type=raw_json 时，value 直接作为 JSON 值放入对应 alias。type=text 或 type=expr 时，value 作为字符串放入对应 alias。type=attachment 时，只需提供 fileId（来自 attachments 清单）；后端会根据 fileId 解析出该文件的 COS 路径，沙箱据此把文件内容读出来、并作为文本字符串（统一 UTF-8）注入到对应 alias。所以脚本里 inputs['alias'] 已经是读好的文件内容（不是文件路径），你不用也无法再去读文件：读 CSV 直接用 df = pd.read_csv(io.StringIO(inputs['alias']))，严禁把 inputs['alias'] 当路径传给 pd.read_csv/open。type=http 时，提供 url；沙箱拉取该 URL 的文本数据、同样作为内容字符串注入 alias，需要时用 io.StringIO/io.BytesIO 包装。凡是对上传文件做统计、聚合、过滤、连表、计算等数据处理，都用 attachment 输入源引用文件，不要先用 read_attachment 把大文件读进上下文。Python 脚本从 stdin 读取输入容器（import json, sys; inputs = json.loads(sys.stdin.read())），并按 alias 访问对应输入。inspect_data / inspect_attachment 不是必选前置步骤；仅当结构不明确时先探查（前序 JSON 工具结果用 inspect_data，上传附件用 inspect_attachment 预览头部）。生成的 Python 代码必须遵守 python 参数中列出的安全约束：只能使用白名单库，必须从 stdin 读取输入，最终只向 stdout 输出单个 JSON 对象，禁止文件操作、命令执行、网络访问、环境变量读取和动态执行。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次 Python 执行目的，用于前端展示。"),
				"python":      stringSchema("要执行的 Python 代码。必须使用 import json, sys; inputs = json.loads(sys.stdin.read()) 读取输入容器；stdin JSON 的顶层 key 来自 python_exec.inputs 的 alias。没有外部输入时 inputs 为空对象。最终结果只能向 stdout 输出单个 JSON 对象，使用 print(json.dumps(result, ensure_ascii=False)) 输出；调试信息只写 stderr。需要产出文件（图片、CSV、Excel、PDF 等任意类型）时，在这个 stdout JSON 对象里放一个 artifacts 数组，每个元素为 {\"type\":\"image/png\",\"name\":\"chart.png\",\"content\":\"<文件内容>\"}。content 的规则：二进制文件（png/jpg/pdf/xlsx 等）必须放 base64（或 data URI），文本文件（csv/txt/json/svg 等）可直接放原始文本；type 用完整 MIME（如 image/png、text/csv、application/pdf），name 带正确扩展名。后端会把 content 解码后上传到对象存储，base64/文件内容不会回填给你——你只会看到产物的类型/名称/大小/artifactId 描述符，所以不要依赖 artifacts 内容做后续推理，也不要把文件内容写进其它字段。需要向用户展示这些文件时，调用 displayFiles 工具，把描述符里的 artifactId 原样放入 artifactIds 参数；禁止在正文中输出文件链接或自行构造任何 uri。单份文件大小上限 10MB，超限的产物不会保存（描述符会标记 dropped 并说明原因），收到 dropped 反馈时请缩小后重新生成。用 matplotlib 生图时须先 matplotlib.use('Agg')；图中含中文时必须设置 plt.rcParams['font.sans-serif']=['WenQuanYi Zen Hei'] 和 plt.rcParams['axes.unicode_minus']=False（该中文字体已在沙箱预装，直接设置即可，禁止用 os/glob 探测字体文件），再 savefig 到 io.BytesIO 后 base64 编码放入 artifacts.content。安全约束（执行前会做静态安全扫描，违反将被拒绝执行）：只能使用白名单库 sys、json、pandas、numpy、math、statistics、datetime、matplotlib、io、base64；禁止 import os、subprocess、socket、requests、shutil 等危险库；禁止 eval、exec、compile、__import__ 等动态执行；禁止 open 及任何文件读写、删除、覆盖和目录操作；禁止 system、popen 等命令执行与子进程调用；禁止读取环境变量（os.environ、getenv）与 globals()、locals()；禁止任何网络访问。"),
				"inputs": map[string]interface{}{
					"type":        "object",
					"description": "可选的命名输入集合。key 是 Python 脚本里的数据别名 alias；value 是输入来源对象。不需要外部输入时可不传或传空对象。支持六种来源类型：tool_result、raw_json、text、expr、attachment、http。type=tool_result 时只需提供 ref，后端会读取该前序工具结果并按 JSON 解析后放入对应 alias；type=raw_json 时 value 直接作为 JSON 值；type=text/expr 时 value 作为字符串；type=attachment 时只需提供 fileId，引用上传附件交给沙箱分析；type=http 时提供 url，沙箱自动拉取。",
					"additionalProperties": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"type": map[string]interface{}{
								"type":        "string",
								"enum":        []string{"tool_result", "raw_json", "text", "expr", "attachment", "http"},
								"description": "来源类型。tool_result 由后端读取 ref 指向的前序工具结果并按 JSON 解析；raw_json 直接使用 JSON 值；text/expr 直接使用字符串值；attachment 由后端根据 fileId 解析出该文件的 COS 路径，以 COS 引用交给沙箱读取；http 由沙箱自动拉取 url 指向的数据。",
							},
							"ref":    stringSchema("前序工具执行结果的 resultRef；仅当 type=tool_result 时必填。"),
							"fileId": stringSchema("上传附件的 fileId（来自 attachments 清单）；仅当 type=attachment 时必填。"),
							"url":    stringSchema("要拉取的数据 URL（http/https）；仅当 type=http 时必填。"),
							"value": map[string]interface{}{
								"description": "内联值；当 type=raw_json/text/expr 时必填。raw_json 直接传 JSON 值，text/expr 直接传字符串值。",
							},
						},
						"required":             []string{"type"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"description", "python"},
			"additionalProperties": false,
		},
	}
}

func inspectDataToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolInspectData,
		Description: "探测前序工具结果的数据结构。inspect_data 不是 python_exec 的必选前置步骤，只在前序工具结果被截断、压缩，或 JSON 结构、字段路径、样例不明确时再调用；如果结果未截断且结构简单，可以直接进入 python_exec。第一版仅支持 JSON，且数据只来自前序工具结果：传 source.type=tool_result、source.ref=resultRef。返回结构路径、类型、样例和 Python 读取说明；如果 python_exec 依赖多个工具结果，应分别对相关 resultRef 调用 inspect_data，再把这些原始 resultRef 通过 python_exec.inputs 的不同 alias 传入。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次数据探测目的，用于前端展示。"),
				"source": map[string]interface{}{
					"type":        "object",
					"description": "数据来源。第一版仅支持 tool_result，通过 ref 读取前序工具 resultRef。",
					"properties": map[string]interface{}{
						"type": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"tool_result"},
							"description": "来源类型：固定为 tool_result。",
						},
						"ref": stringSchema("前序工具结果引用 resultRef。"),
					},
					"required":             []string{"type", "ref"},
					"additionalProperties": false,
				},
				"formatHint":  stringSchema("数据格式提示。第一版仅支持 json，可不传。"),
				"sampleLimit": numberSchema("每个数组节点最多保留的样例数量，默认 5。"),
			},
			"required":             []string{"description", "source"},
			"additionalProperties": false,
		},
	}
}

func todoWriteToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolTodoWrite,
		Description: "更新当前 Agent 线程的 todo 状态。用户任务需要拆分为可执行、可验证的子任务后写入 todo；任务推进、完成、跳过或无法继续时必须及时更新状态。只要当前存在 pending 或 in_progress todo，在输出最终答复前必须先调用本工具，将已完成项标记为 completed，将未完成、跳过或不再需要的项标记为 cancelled。merge=false 用于创建或覆盖当前任务列表，merge=true 用于按 id 增量更新当前任务列表。任何时刻最多只能有一个 in_progress todo。每当开始一个新的 todo 时，必须先在 assistant 正文中使用 Markdown 一级标题格式 `# <中文序号>、<完整 content>` 单独输出当前步骤标题；中文序号按照当前完整 todo 列表顺序生成，从“一”开始，依次使用“一、二、三……十、十一……”，例如 `# 一、检查现有实现`。中文序号只用于标题展示，不得写入或修改 todo 的 content；标题前后不得添加其他正文。并在同一响应中调用本工具将该 todo 更新为 in_progress；该响应不得同时调用其他实施工具，待状态更新成功后的下一轮再执行该步骤。每个 todo 只在首次进入 in_progress 时输出一次步骤标题，同一步骤后续调用工具时不要重复输出。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
				"merge": map[string]interface{}{
					"type":        "boolean",
					"description": "是否增量合并。true 时按 id 合并并保留未传字段；false 时整体覆盖当前 todo 列表。",
				},
				"todos": map[string]interface{}{
					"type":        "array",
					"description": "todo 项数组。新增或整体替换时需要 content；增量更新已有项时可只传 id 和 status。",
					"minItems":    1,
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":      stringSchema("稳定 todo id，例如 inspect、implement、verify。"),
							"content": stringSchema("todo 展示内容，应描述一个具体、可执行、可验证的子任务。"),
							"status": map[string]interface{}{
								"type":        "string",
								"enum":        []string{reactTodoStatusPending, reactTodoStatusInProgress, reactTodoStatusCompleted, reactTodoStatusCancelled},
								"description": "todo 状态。",
							},
						},
						"required":             []string{"id"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"description", "merge", "todos"},
			"additionalProperties": false,
		},
	}
}

// objectTool 快速构造 object 参数类型的工具定义，供内置工具声明复用。
func objectTool(name, description string, properties map[string]interface{}) llm.ToolDefinition {
	mergedProperties := map[string]interface{}{
		"description": stringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
	}
	for key, value := range properties {
		mergedProperties[key] = value
	}
	return llm.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": mergedProperties,
			"required":   []string{"description"},
		},
	}
}

// stringSchema 构造字符串字段 schema，保持内置工具参数声明写法简洁。
func stringSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description}
}

// numberSchema 构造数字字段 schema，用于 offset、limit 等分页参数声明。
func numberSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "number", "description": description}
}

type toolDescriptionInput struct {
	Description string `json:"description"`
}

func extractToolDescription(input json.RawMessage) string {
	var req toolDescriptionInput
	_ = json.Unmarshal(input, &req)
	return strings.TrimSpace(req.Description)
}

func stripToolDescriptionInput(input json.RawMessage) json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return input
	}
	delete(raw, "description")
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return input
	}
	return data
}

// executeInternalTool 执行内置 Meta Tool，并复用普通工具结果的摘要、持久化和事件协议。
func (s *reactEngineState) executeInternalTool(call llm.ToolCall, step int) (llm.ToolResultContent, error) {
	if call.Name == metaToolExecuteTool {
		return s.executeLoadedBusinessTool(call, step)
	}
	if call.Name == metaToolAskQuestion {
		return s.executeAskQuestion(call, step)
	}
	// displayFiles 需在发射 tool_use_start 前完成解析并替换 input（前端只消费 start 里的 input），走专属路径。
	if call.Name == metaToolDisplayFiles {
		return s.executeDisplayFilesTool(call, step)
	}
	description := extractToolDescription(call.Input)
	toolInput := stripToolDescriptionInput(call.Input)
	_ = s.emitter.EmitStep(step, EventToolUseStart, params.ReactToolUseStartPayload{ToolUseID: call.ID, ToolName: call.Name, ToolInput: toolInput, Description: description, ExecutedBy: executedByInternal, Status: toolExecutionStatusRunning})
	start := time.Now()
	call.Input = toolInput
	content, meta, isError, err := s.executeInternalToolContent(call, step)
	if err != nil {
		if _, _, interrupted := classifyToolInterruption(s.runCtx, err); interrupted {
			result := s.closeInterruptedToolUse(call, step, executedByInternal, start, err)
			return result, err
		}
		content = err.Error()
		isError = true
		meta = nil
	}
	normalized := normalizeToolResult(call.ID, content, isError, executedByInternal)
	if normalized.ResultRef != "" {
		if err := storeResultRef(s.ctx, s.sessionID, s.runID, call.ID, normalized.ResultRef, content); err != nil {
			return llm.ToolResultContent{}, err
		}
	}
	_ = s.emitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: normalized.Content, ResultRef: normalized.ResultRef, Truncated: normalized.Truncated, OmittedChars: normalized.OmittedChars, IsError: normalized.IsError, ExecutedBy: executedByInternal, Status: normalized.Status, DurationMs: time.Since(start).Milliseconds(), Meta: meta})
	return llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: normalized.IsError, Meta: meta}, nil
}

// executeInternalToolContent 按工具名分发到具体 Meta Tool 处理函数。
// 第二个返回值 meta 是工具产物的 UI 旁路（python_exec / displayFiles 的产物 uri），仅前端展示与回放，不进模型上下文；
// 其余工具无产物，统一用 noToolMeta 包成 meta=nil。
func (s *reactEngineState) executeInternalToolContent(call llm.ToolCall, step int) (string, json.RawMessage, bool, error) {
	switch call.Name {
	case metaToolGetTool:
		return noToolMeta(s.getTool(call.Input))
	case metaToolListSkills:
		return noToolMeta(s.listSkills())
	case metaToolGetSkill:
		return noToolMeta(s.getSkill(call.Input))
	case metaToolReadToolResult:
		return noToolMeta(s.readToolResult(call.Input))
	case metaToolInspectData:
		return noToolMeta(s.inspectData(call.Input))
	case metaToolPythonExec:
		return s.executePythonExec(call.ID, call.Input)
	case metaToolTodoWrite:
		return noToolMeta(s.updateTodos(call, step))
	case metaToolCreatePlan:
		return noToolMeta(executeCreatePlan(s.sessionID, s.runID, call.Input))
	case metaToolDelegateAgent:
		return noToolMeta(s.executeDelegateAgent(call, step))
	case metaToolSendMessage:
		return noToolMeta(s.executeSendMessage(call, step))
	case metaToolLoadRuntimeCode:
		return noToolMeta(s.executeLoadRuntimeCode(call.Input))
	case metaToolResolveAsyncTask:
		return noToolMeta(s.resolveAsyncTask(call.Input))
	case metaToolGetAsyncTask:
		return noToolMeta(s.getAsyncTask(call.Input))
	case metaToolReadAttachment:
		return noToolMeta(s.readAttachment(call.Input))
	case metaToolInspectAttachment:
		return noToolMeta(s.inspectAttachment(call.Input))
	case metaToolMemoryList:
		return noToolMeta(s.executeMemoryList(call.Input))
	case metaToolMemoryRead:
		return noToolMeta(s.executeMemoryRead(call.Input))
	case metaToolMemoryWrite:
		return noToolMeta(s.executeMemoryWrite(call.Input))
	case metaToolGraphMemorySearch:
		return noToolMeta(s.executeGraphMemorySearch(call.Input))
	case metaToolGraphMemoryWrite:
		return noToolMeta(s.executeGraphMemoryWrite(call.Input))
	default:
		return "", nil, true, fmt.Errorf("unknown internal meta tool: %s", call.Name)
	}
}

// noToolMeta 把无产物工具的 (content, isError, err) 适配为带 meta 的四元返回，meta 恒为 nil。
func noToolMeta(content string, isError bool, err error) (string, json.RawMessage, bool, error) {
	return content, nil, isError, err
}

type reactSkillIndexItem struct {
	SkillID            string `json:"skillId"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	TriggerCondition   string `json:"triggerCondition"`
	ForbiddenCondition string `json:"forbiddenCondition,omitempty"`
	IsDefault          int    `json:"isDefault"`
	RouteValues        string `json:"routeValues"`
}

func buildSkillIndexSnapshotJSON(skills []model.Skill) string {
	items := make([]reactSkillIndexItem, 0, len(skills))
	for _, skill := range skills {
		items = append(items, reactSkillIndexItem{
			SkillID:            skill.SkillID,
			Name:               skill.Name,
			Description:        skill.Description,
			TriggerCondition:   skill.TriggerCondition,
			ForbiddenCondition: skill.ForbiddenCondition,
			IsDefault:          skill.IsDefault,
			RouteValues:        skill.RouteValues,
		})
	}
	data, _ := json.Marshal(items)
	return string(data)
}

func renderSkillIndexSummary(snapshotJSON string) string {
	snapshotJSON = strings.TrimSpace(snapshotJSON)
	if snapshotJSON == "" || snapshotJSON == "null" || snapshotJSON == "[]" {
		return ""
	}
	var items []reactSkillIndexItem
	if err := json.Unmarshal([]byte(snapshotJSON), &items); err != nil || len(items) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## 当前 run 可用 Skill 摘要索引\n")
	sb.WriteString("以下摘要仅用于判断是否需要加载 Skill；如果某个 Skill 适合用户任务，必须调用 get_skill 获取完整说明后再执行，不要仅凭摘要执行。\n\n")
	for _, item := range items {
		tag := ""
		if item.IsDefault == 1 {
			tag = " [兜底]"
		}
		sb.WriteString(fmt.Sprintf("- skillId: %s\n  name: %s%s\n  description: %s\n  triggerCondition: %s\n", item.SkillID, item.Name, tag, item.Description, item.TriggerCondition))
		if strings.TrimSpace(item.ForbiddenCondition) != "" {
			sb.WriteString(fmt.Sprintf("  forbiddenCondition: %s\n", item.ForbiddenCondition))
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// listSkills 返回轻量 Skill 索引，作为软下线期间的兼容入口；模型默认不再看到该工具。
func (s *reactEngineState) listSkills() (string, bool, error) {
	skills, err := model.FindSkillsByCallerAndRoutes(s.ctx, s.req.payload.CallerKey, route.BuildRoutePrefixes(s.req.payload.RouteValues))
	if err != nil {
		return "", true, err
	}
	data := buildSkillIndexSnapshotJSON(skills)
	_ = model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"skills_index_snapshot_json": data})
	return data, false, nil
}

// getSkill 按需加载 Skill 完整定义，并记录当前 run 已加载过的 Skill。
func (s *reactEngineState) getSkill(input json.RawMessage) (string, bool, error) {
	var req struct {
		SkillID string `json:"skillId"`
		Name    string `json:"name"`
	}
	_ = json.Unmarshal(input, &req)
	skills, err := model.FindSkillsByCallerAndRoutes(s.ctx, s.req.payload.CallerKey, route.BuildRoutePrefixes(s.req.payload.RouteValues))
	if err != nil {
		return "", true, err
	}
	for _, skill := range skills {
		if (req.SkillID != "" && skill.SkillID == req.SkillID) || (req.Name != "" && skill.Name == req.Name) {
			s.loadedSkillID[skill.SkillID] = true
			_ = s.persistLoadedSkills()
			data, _ := json.Marshal(skill)
			return string(data), false, nil
		}
	}
	return "", true, fmt.Errorf("skill not found")
}

// persistLoadedSkills 持久化当前 run 已加载 Skill 列表，避免只在内存中保留模型上下文扩展记录。
func (s *reactEngineState) persistLoadedSkills() error {
	ids := make([]string, 0, len(s.loadedSkillID))
	for id := range s.loadedSkillID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	idsJSON, _ := json.Marshal(ids)
	return model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"loaded_skill_ids": string(idsJSON)})
}

// readToolResult 读取普通 Tool resultRef 或 Plan StepResultRef 的局部分片。
// 两种引用统一做 Session 范围授权，避免模型拿到其它会话的引用后跨会话读取结果。
func (s *reactEngineState) readToolResult(input json.RawMessage) (string, bool, error) {
	var req struct {
		ResultRef string `json:"resultRef"`
		Offset    int    `json:"offset"`
		Limit     int    `json:"limit"`
	}
	_ = json.Unmarshal(input, &req)
	req.ResultRef = strings.TrimSpace(req.ResultRef)
	if req.ResultRef == "" {
		return "", true, fmt.Errorf("resultRef is required")
	}

	content, ok, err := readResultRef(s.ctx, s.sessionID, s.runID, req.ResultRef)
	if err != nil {
		return "", true, err
	}
	if !ok {
		content, ok, err = s.readPlanStepResultRef(req.ResultRef)
		if err != nil {
			return "", true, err
		}
	}
	if !ok {
		return "", true, fmt.Errorf("resultRef not found or expired")
	}
	part, hasMore, nextOffset := sliceResultContent(content, req.Offset, req.Limit)
	payload := map[string]interface{}{
		"resultRef":  req.ResultRef,
		"offset":     req.Offset,
		"limit":      req.Limit,
		"content":    part,
		"hasMore":    hasMore,
		"nextOffset": nextOffset,
	}
	data, _ := json.Marshal(payload)
	return string(data), false, nil
}

// readPlanStepResultRef 读取 Plan Step 的不可变结果。
// V1 以 session_id 作为授权边界：同一 Plan 的后续 Scoped ReactRun 与 outer Run 共用 Session，
// 因而可以按需读取前序 StepResult；其它 Session 即使知道 step_result_id 也无法读取。
// 边界约定（D9）：react 服务层不 import service/plan；此处经共享的 models 层读取
// plan_result_ 前缀引用是 plan → react 单向依赖下唯一的既定例外，禁止在此扩展
// 对 Plan 状态机的任何写操作或其它 Plan 表访问。
func (s *reactEngineState) readPlanStepResultRef(resultRef string) (string, bool, error) {
	if !strings.HasPrefix(strings.TrimSpace(resultRef), "plan_result_") {
		return "", false, nil
	}
	db := model.GetLLMDB().WithContext(s.ctx)
	var result model.PlanStepResult
	query := db.Where("step_result_id = ?", resultRef).Limit(1).Find(&result)
	if query.Error != nil {
		return "", false, query.Error
	}
	if query.RowsAffected == 0 {
		return "", false, nil
	}

	var execution model.PlanExecution
	query = db.Where("plan_execution_id = ? AND session_id = ?", result.PlanExecutionID, s.sessionID).
		Limit(1).Find(&execution)
	if query.Error != nil {
		return "", false, query.Error
	}
	if query.RowsAffected == 0 {
		return "", false, nil
	}

	var resultValue any
	if strings.TrimSpace(result.ResultJSON) != "" && json.Valid([]byte(result.ResultJSON)) {
		_ = json.Unmarshal([]byte(result.ResultJSON), &resultValue)
	} else {
		resultValue = result.ResultJSON
	}
	payload := map[string]interface{}{
		"stepResultId":    result.StepResultID,
		"planExecutionId": result.PlanExecutionID,
		"stepId":          result.StepID,
		"stepAttemptId":   result.StepAttemptID,
		"status":          result.Status,
		"summary":         result.Summary,
		"result":          resultValue,
	}
	data, _ := json.Marshal(payload)
	return string(data), true, nil
}
