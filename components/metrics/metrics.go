// Package metrics 集中注册 Prometheus 指标，业务代码仅调用打点函数。
// 暴露端点：GET /metrics（主端口根路径，见 router/metrics.go）。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// WSConnections 当前 WebSocket 连接数。
	WSConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "react_ws_connections",
		Help: "Current react websocket connections.",
	})

	// RunsActive 当前活跃 run 数。
	RunsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "react_runs_active",
		Help: "Current active react runs.",
	})

	// RunsTotal run 终态计数（status: finished/error/cancelled/expired）。
	RunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_runs_total",
		Help: "Total react runs by final status.",
	}, []string{"status"})

	// ReactDelegationsTotal 子 Agent 委派计数（agent_key × status：
	// success/error/cancelled/no_response/agent_not_found/depth_limited），观测委派命中率与误配。
	ReactDelegationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_delegations_total",
		Help: "Total sub-agent delegations by agent_key and outcome.",
	}, []string{"agent_key", "status"})

	// SessionsCreatedTotal 新建会话计数。
	SessionsCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "react_sessions_created_total",
		Help: "Total react sessions created.",
	})

	// ModelRoundDuration 模型单轮耗时分布（model: 平台/版本）。
	ModelRoundDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "react_model_round_duration_seconds",
		Help:    "React model round duration in seconds.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120},
	}, []string{"model"})

	// ModelFailoversTotal 模型互备切换次数。
	ModelFailoversTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "react_model_failovers_total",
		Help: "Total react model failover switches.",
	})

	// ModelRetriesTotal 模型调用同模型重试次数（reason: http_429/stream_idle_timeout/unknown 等）。
	ModelRetriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_model_retries_total",
		Help: "Total react model same-model retries by failure reason.",
	}, []string{"reason"})

	// SoftLandingsTotal 软着陆收尾触发次数（reason: step_budget/token_budget/time_budget/context_limit）。
	SoftLandingsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_soft_landings_total",
		Help: "Total react soft-landing activations by reason.",
	}, []string{"reason"})

	// AnomalyWarningsTotal 重复工具调用异常提醒注入次数。
	AnomalyWarningsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "react_anomaly_warnings_total",
		Help: "Total repeated-tool-call anomaly warnings injected into model context.",
	})

	// ToolCallsTotal 工具调用计数（tool, status: success/error/cancelled）。
	ToolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_tool_calls_total",
		Help: "Total react tool calls by tool and status.",
	}, []string{"tool", "status"})

	// ToolDuration 工具执行耗时分布。
	ToolDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "react_tool_duration_seconds",
		Help:    "React tool execution duration in seconds.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"tool"})

	// PythonExecTimeoutsTotal python_exec 沙箱超时次数。
	PythonExecTimeoutsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "react_python_exec_timeouts_total",
		Help: "Total python_exec sandbox timeouts.",
	})

	// HTTPRequestsTotal HTTP 请求计数（method, path, code）。
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by method, path and status code.",
	}, []string{"method", "path", "code"})

	// HTTPRequestDuration HTTP 请求耗时分布。
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"method", "path"})

	// MemoryWritesTotal 长期记忆写操作计数（action: create/update/delete/rollback, source: model/admin/reflection, status: success/error）。
	MemoryWritesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_memory_writes_total",
		Help: "Total memory mutations by action, source and status.",
	}, []string{"action", "source", "status"})

	// MemoryItems active 记忆条目数水位（owner_type × layer，写后重算）。
	MemoryItems = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "react_memory_items",
		Help: "Active memory items by owner type and layer.",
	}, []string{"owner_type", "layer"})

	// MemoryResidentChars 常驻层正文字符量水位（owner_type，写后重算；预算水位观测）。
	MemoryResidentChars = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "react_memory_resident_chars",
		Help: "Resident-layer memory content chars by owner type.",
	}, []string{"owner_type"})

	// MemoryReflectionTotal reflection 自动整理计数（status: triggered/cooldown_skipped/success/error）。
	MemoryReflectionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_memory_reflection_total",
		Help: "Total memory reflection runs by status.",
	}, []string{"status"})

	// MemoryItemsByType active 记忆条目数水位（owner_type × memory_type，V2 类型化观测）。
	MemoryItemsByType = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "react_memory_items_by_type",
		Help: "Active memory items by owner type and memory type.",
	}, []string{"owner_type", "memory_type"})

	// MemoryExtractorTotal 记忆自动沉淀（extractor）计数（status: triggered/cooldown_skipped/empty/success/error）。
	MemoryExtractorTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_memory_extractor_total",
		Help: "Total memory extractor runs by status.",
	}, []string{"status"})

	// GraphMemorySearchTotal 时序图谱检索计数（status: ok/error；工具调用与注入块共用）。
	GraphMemorySearchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_graph_memory_search_total",
		Help: "Total graph memory searches by status.",
	}, []string{"status"})

	// GraphMemoryInjectFacts 注入块渲染的事实条数水位（含 0=未命中，观测注入命中情况）。
	GraphMemoryInjectFacts = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "react_graph_memory_inject_facts",
		Help: "Facts rendered into the graph memory injection block.",
	}, []string{"status"})

	// GraphMemoryEpisodeTotal episode 写入计数（status: ok/error；含模型显式写入）。
	GraphMemoryEpisodeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "react_graph_memory_episode_total",
		Help: "Total graph memory episodes written by status.",
	}, []string{"status"})
)
