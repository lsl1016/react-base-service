// Package core 承载 ReAct Runtime 的无状态基础件：内置 Meta Tool 名称常量、
// ExecutionProfile 能力档案、JSON Schema 校验、token 估算与 Skill 关键词触发匹配。
//
// core 是 react 子包依赖图的底座：只允许依赖外部领域包（api/llm、models、conf、service/token），
// 不允许反向依赖 service/react 门面或其他能力域子包。引擎门面通过别名桥接
// （service/react/core_bridge.go）保持包内既有私有符号名不变。
package core
