package components

import (
	"fmt"

	"react-base-service/golib/base"
)

// ParamInvalidf 返回带自定义提示语的参数错误（错误码同 ErrorParamInvalid）。
// 不要用 ErrorParamInvalid.Sprintf 传自定义消息：Sprintf 以 ErrMsg 为格式化模板，
// 而 ErrorParamInvalid 的模板不含格式化动词，会产生 %!(EXTRA ...) 乱码。
func ParamInvalidf(format string, v ...interface{}) base.Error {
	return base.Error{ErrNo: ErrorParamInvalid.ErrNo, ErrMsg: fmt.Sprintf(format, v...)}
}

// 4000000-4999999 参数检查错误
var ErrorParamInvalid = base.Error{
	ErrNo:  4000,
	ErrMsg: "param invalid",
}

// 5000000-5999999 内部逻辑错误
var ErrorSystemError = base.Error{
	ErrNo:  5000,
	ErrMsg: "system internal error",
}

var ErrorDbInsert = base.Error{
	ErrNo:  3100000,
	ErrMsg: "db insert error: %s",
}
var ErrorDbUpdate = base.Error{
	ErrNo:  3100001,
	ErrMsg: "db update error: %s",
}
var ErrorDbSelect = base.Error{
	ErrNo:  3100002,
	ErrMsg: "db get error: %s",
}

var ErrorRedisGet = base.Error{
	ErrNo:  3200000,
	ErrMsg: "redis get error: %s",
}
var ErrorRedisSet = base.Error{
	ErrNo:  3200001,
	ErrMsg: "redis set error: %s",
}

var ErrorUserNameMismatch = base.Error{
	ErrNo:  110003,
	ErrMsg: "请求用户名与登录用户不一致",
}

// LLM 相关错误 6000000-6099999
var ErrorSessionNotFound = base.Error{
	ErrNo:  6000001,
	ErrMsg: "会话不存在",
}
var ErrorSessionExpired = base.Error{
	ErrNo:  6000002,
	ErrMsg: "会话已过期，请重新发起对话",
}
var ErrorLLMRequest = base.Error{
	ErrNo:  6000010,
	ErrMsg: "大模型请求失败: %s",
}
var ErrorLLMStream = base.Error{
	ErrNo:  6000011,
	ErrMsg: "大模型流式响应异常: %s",
}
var ErrorLLMRequestTooLarge = base.Error{
	ErrNo:  6000012,
	ErrMsg: "请求体超出处理上限",
}
var ErrorPlatformNotSupported = base.Error{
	ErrNo:  6000020,
	ErrMsg: "不支持的平台类型: %s",
}
var ErrorModelNotSupported = base.Error{
	ErrNo:  6000022,
	ErrMsg: "不支持的模型: %s",
}
var ErrorLLMApiKeyNotConfigured = base.Error{
	ErrNo:  6000023,
	ErrMsg: "未配置平台对应的大模型api_key: %s",
}

// ReAct Runtime 相关错误 60001xx
var ErrorReactRunActive = base.Error{
	ErrNo:  6000100,
	ErrMsg: "当前会话已有运行中的 ReAct run: %s",
}
var ErrorReactRunNotFound = base.Error{
	ErrNo:  6000101,
	ErrMsg: "ReAct run 不存在: %s",
}
var ErrorReactRunFailed = base.Error{
	ErrNo:  6000102,
	ErrMsg: "ReAct run 执行失败: %s",
}
var ErrorReactToolResultTooLarge = base.Error{
	ErrNo:  6000103,
	ErrMsg: "ReAct tool result 超出单条存储上限: %s",
}
var ErrorReactSessionNotFound = base.Error{
	ErrNo:  6000104,
	ErrMsg: "ReAct session 不存在: %s",
}
var ErrorReactInputTooLong = base.Error{
	ErrNo:  6000105,
	ErrMsg: "输入内容过长，请精简或拆分提问: %s",
}
var ErrorSubAgentBudgetExceeded = base.Error{
	ErrNo:  6000107,
	ErrMsg: "子 Agent 预算超限（累计 %d tokens > 上限 %d，含委派孙代理），已终止: %s",
}
var ErrorBundleImportInvalid = base.Error{
	ErrNo:  6000108,
	ErrMsg: "Bundle 安装失败: %s",
}
var ErrorBundleNotFound = base.Error{
	ErrNo:  6000109,
	ErrMsg: "Bundle 不存在: %s",
}
var ErrorReactPlaygroundForbidden = base.Error{
	ErrNo:  6000106,
	ErrMsg: "无权访问 ReAct playground，请联系管理员开通权限",
}

// Caller 相关错误 6010xxx
var ErrorCallerNotFound = base.Error{
	ErrNo:  6010001,
	ErrMsg: "调用方不存在: %s",
}
var ErrorCallerRegFailed = base.Error{
	ErrNo:  6010002,
	ErrMsg: "调用方注册失败: %s",
}
var ErrorCallerDuplicate = base.Error{
	ErrNo:  6010003,
	ErrMsg: "调用方已存在: %s",
}

// Skill 相关错误 6020xxx
var ErrorSkillNotFound = base.Error{
	ErrNo:  6020001,
	ErrMsg: "技能不存在: %s",
}
var ErrorSkillCreateFailed = base.Error{
	ErrNo:  6020002,
	ErrMsg: "技能创建失败: %s",
}
var ErrorSkillMatchFailed = base.Error{
	ErrNo:  6020003,
	ErrMsg: "技能匹配失败: %s",
}
var ErrorSkillExecFailed = base.Error{
	ErrNo:  6020004,
	ErrMsg: "技能执行失败: %s",
}
var ErrorSkillDefaultExists = base.Error{
	ErrNo:  6020005,
	ErrMsg: "该路由下已存在兜底技能，不允许重复添加: callerKey=%s, routeValues=%s",
}
var ErrorSkillImportInvalid = base.Error{
	ErrNo:  6020006,
	ErrMsg: "Skill 定义文件非法: %s",
}

// Tool 相关错误 6030xxx
var ErrorToolNotFound = base.Error{
	ErrNo:  6030001,
	ErrMsg: "工具不存在: %s",
}
var ErrorToolRegisterFailed = base.Error{
	ErrNo:  6030002,
	ErrMsg: "工具注册失败: %s",
}
var ErrorToolExecFailed = base.Error{
	ErrNo:  6030003,
	ErrMsg: "工具执行失败: %s",
}
var ErrorToolExecTimeout = base.Error{
	ErrNo:  6030004,
	ErrMsg: "工具执行超时: %s",
}
var ErrorToolDuplicate = base.Error{
	ErrNo:  6030005,
	ErrMsg: "同一调用方下工具名称已存在: callerKey=%s, name=%s",
}
var ErrorToolUserPolicyNotFound = base.Error{
	ErrNo:  6030006,
	ErrMsg: "工具用户白名单策略不存在: %s",
}
var ErrorToolUserPolicyDuplicate = base.Error{
	ErrNo:  6030007,
	ErrMsg: "工具用户白名单策略已存在: %s",
}
var ErrorToolUserPolicyRequiresDisabled = base.Error{
	ErrNo:  6030008,
	ErrMsg: "工具需先关闭后再配置用户白名单: %s",
}
var ErrorToolUserPolicyInvalid = base.Error{
	ErrNo:  6030009,
	ErrMsg: "工具用户白名单策略配置非法: %s",
}

// Token 相关错误 6040xxx
var ErrorTokenExceeded = base.Error{
	ErrNo:  6040001,
	ErrMsg: "Token数量超出限制: %s",
}
var ErrorModelNotFound = base.Error{
	ErrNo:  6040002,
	ErrMsg: "模型配置不存在: %s",
}

// ApiKey 相关错误 6050xxx
var ErrorApiKeyNotFound = base.Error{
	ErrNo:  6050001,
	ErrMsg: "API Key不存在: %s",
}
var ErrorApiKeyDuplicate = base.Error{
	ErrNo:  6050002,
	ErrMsg: "该路由下已存在API Key: callerKey=%s, routeValues=%s",
}

// 澄清相关错误 6070xxx
var ErrorClarifyInvalid = base.Error{
	ErrNo:  6070001,
	ErrMsg: "澄清已失效，请重新提问",
}
var ErrorClarifyConflict = base.Error{
	ErrNo:  6070002,
	ErrMsg: "澄清提交冲突，该卡片已被处理",
}

// SystemPrompt 相关错误 6060xxx
var ErrorSystemPromptNotFound = base.Error{
	ErrNo:  6060001,
	ErrMsg: "系统提示词不存在: %s",
}
var ErrorSystemPromptDuplicate = base.Error{
	ErrNo:  6060002,
	ErrMsg: "该路由下已存在系统提示词: callerKey=%s, routeValues=%s",
}

// 用户模型相关错误 6080xxx
var ErrorUserModelNotFound = base.Error{
	ErrNo:  6080001,
	ErrMsg: "用户模型不存在: %s",
}
var ErrorUserModelDuplicate = base.Error{
	ErrNo:  6080002,
	ErrMsg: "模型名称已存在: %s",
}
var ErrorUserModelConnectivityFailed = base.Error{
	ErrNo:  6080003,
	ErrMsg: "模型连通性检测失败: %s",
}
var ErrorUserModelCategoryNotSupported = base.Error{
	ErrNo:  6080004,
	ErrMsg: "不支持的模型分类: %s",
}
var ErrorUserModelOwnerMismatch = base.Error{
	ErrNo:  6080005,
	ErrMsg: "无权操作他人模型",
}
var ErrorUserModelNoPermission = base.Error{
	ErrNo:  6080006,
	ErrMsg: "无权限执行此操作，仅白名单用户可用",
}
var ErrorUserModelInvalidBizScenes = base.Error{
	ErrNo:  6080007,
	ErrMsg: "应用场景不能为空",
}
var ErrorCreditsInsufficient = base.Error{
	ErrNo:  6080008,
	ErrMsg: "积分不足，模型当前不可用",
}
var ErrorModelNotPlatformDefault = base.Error{
	ErrNo:  6080009,
	ErrMsg: "仅支持平台默认模型",
}

// 这两个错误在统一响应层映射为 HTTP 403，便于工具调用方和 Agent 识别权限失败。

// Python 分析相关错误 6070xxx
var ErrorPythonAnalysisFailed = base.Error{
	ErrNo:  6070004,
	ErrMsg: "Python分析失败: %s",
}
var ErrorPythonScriptNotFound = base.Error{
	ErrNo:  6070005,
	ErrMsg: "Python脚本不存在: %s",
}
var ErrorPythonScriptUnsafe = base.Error{
	ErrNo:  6070003,
	ErrMsg: "Python脚本校验失败: %s",
}

// 子 Agent 相关错误 6090xxx
var ErrorAgentNotFound = base.Error{
	ErrNo:  6090001,
	ErrMsg: "子Agent不存在: %s",
}
var ErrorAgentDuplicate = base.Error{
	ErrNo:  6090002,
	ErrMsg: "同一调用方下 agent_key 已存在: callerKey=%s, agentKey=%s",
}
var ErrorAgentKeyInvalid = base.Error{
	ErrNo:  6090003,
	ErrMsg: "agent_key 仅允许字母、数字、下划线和中划线: %s",
}
var ErrorAgentImportInvalid = base.Error{
	ErrNo:  6090004,
	ErrMsg: "Agent 定义文件非法: %s",
}

// 运行时设置相关错误 6100xxx（管理面板「运行时配置」在线覆盖 yaml 策略）
var ErrorRuntimeSettingInvalid = base.Error{
	ErrNo:  6100001,
	ErrMsg: "运行时设置非法: %s",
}
