// Package netcap 承载 Runtime 内置网络能力：web_fetch 网页抓取与 web_search 联网检索。
//
// 依赖方向：netcap 只依赖 core 与外部领域包（api/llm、conf、golib/zlog），
// 不依赖 react 门面。执行函数以 ctx+runID 显式传参替代引擎状态方法，
// 由门面在 meta tool 分发处薄委托调用。
package netcap
