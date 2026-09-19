package mcpgateway

import (
	"runtime/debug"
	"sync"
	"time"
	"unicode/utf8"

	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

const (
	// auditQueueSize 审计队列容量，写满后丢弃并告警，绝不阻塞业务返回
	auditQueueSize = 4096
	// auditBatchSize 单批最大落库条数
	auditBatchSize = 100
	// auditFlushInterval 批量落库的最长等待时间
	auditFlushInterval = 1 * time.Second
	// defaultAuditWorkerNum 未配置时使用的有限并发数
	defaultAuditWorkerNum = 10
	// maxArgumentsLen 入参快照上限，超出部分截断
	maxArgumentsLen = 4096
	// maxResponseTextLen 审计只保留响应摘要，避免大结构化结果超过 MySQL TEXT。
	maxResponseTextLen = 32 * 1024
)

// auditWriter 异步审计：单协程组批 + 信号量限流的落库协程组（自 mcp-server 移植）。
//
// 工具调用本身要走上游 HTTP，耗时通常在数百毫秒，
// 审计写库不应再串行叠加延迟，且写库失败绝不能影响业务返回。
type auditWriter struct {
	queue   chan *model.McpCallLog
	stop    chan struct{}
	done    chan struct{}
	workers chan struct{} // 落库并发信号量

	batches sync.WaitGroup
}

var auditWriterState struct {
	sync.RWMutex
	writer *auditWriter
}

// StartAuditWriter 启动审计组批协程和落库协程组。
func StartAuditWriter() error {
	auditWriterState.Lock()
	defer auditWriterState.Unlock()
	if auditWriterState.writer != nil {
		return nil
	}

	workerNum := conf.CustomConf.MCPServer.Audit.WorkerNum
	if workerNum <= 0 {
		workerNum = defaultAuditWorkerNum
	}

	writer := &auditWriter{
		queue:   make(chan *model.McpCallLog, auditQueueSize),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		workers: make(chan struct{}, workerNum),
	}
	auditWriterState.writer = writer
	go writer.consume()
	return nil
}

// CloseAuditWriter 停止接收新记录，排空内存队列并等待在途批次完成。
func CloseAuditWriter() {
	auditWriterState.Lock()
	writer := auditWriterState.writer
	auditWriterState.writer = nil
	auditWriterState.Unlock()
	if writer == nil {
		return
	}

	close(writer.stop)
	<-writer.done
	writer.batches.Wait()
}

func (w *auditWriter) consume() {
	defer close(w.done)
	for {
		stopped := false
		func() {
			defer func() {
				if value := recover(); value != nil {
					zlog.Errorf(nil, "[MCPGW] audit consumer panic, restarting: %v\n%s", value, debug.Stack())
				}
			}()
			consumeAuditQueue(w.queue, w.stop, auditFlushInterval, auditBatchSize, w.submitBatch)
			stopped = true
		}()
		if stopped {
			return
		}
	}
}

func consumeAuditQueue(queue <-chan *model.McpCallLog, stop <-chan struct{}, flushInterval time.Duration,
	batchSize int, submit func([]*model.McpCallLog)) {
	buffer := make([]*model.McpCallLog, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	flush := func() {
		if len(buffer) == 0 {
			return
		}
		batch := make([]*model.McpCallLog, len(buffer))
		copy(batch, buffer)
		submit(batch)
		buffer = buffer[:0]
	}
	appendRecord := func(record *model.McpCallLog) {
		if record == nil {
			return
		}
		buffer = append(buffer, record)
		if len(buffer) >= batchSize {
			flush()
		}
	}

	for {
		select {
		case record := <-queue:
			appendRecord(record)
		case <-ticker.C:
			flush()
		case <-stop:
			for {
				select {
				case record := <-queue:
					appendRecord(record)
				default:
					flush()
					return
				}
			}
		}
	}
}

// submitBatch 以信号量限制并发地落库；信号量占满时同步落库，避免丢弃记录。
func (w *auditWriter) submitBatch(records []*model.McpCallLog) {
	w.batches.Add(1)
	select {
	case w.workers <- struct{}{}:
		go func() {
			defer w.batches.Done()
			defer func() { <-w.workers }()
			flushAudit(records)
		}()
	default:
		// 并发已满：同步写库，宁可慢也不丢审计
		defer w.batches.Done()
		flushAudit(records)
	}
}

func flushAudit(records []*model.McpCallLog) {
	if len(records) == 0 {
		return
	}
	// 落库协程没有对应的请求上下文，使用独立且不再修改的 Context，
	// 避免与 gin 请求生命周期的 Context 复用产生竞态。
	ctx := &gin.Context{}
	if err := model.BatchInsertMcpCallLogs(ctx, records); err != nil {
		zlog.Errorf(ctx, "[MCPGW] 审计批量落库失败: count=%d err=%v", len(records), err)
	}
}

// AuditEntry 是一次 MCP 调用的审计快照。
type AuditEntry struct {
	RequestID    string
	AppKey       string
	UserName     string
	McpMethod    string
	ToolName     string
	Arguments    string
	ResponseText string
	ResultCode   int
	ErrorMsg     string
	CostMs       int
	ClientIP     string
	ClientInfo   string
}

// SubmitAudit 投递一条审计记录，队列满时丢弃，不阻塞调用方。
func SubmitAudit(ctx *gin.Context, entry AuditEntry) {
	record := &model.McpCallLog{
		RequestID:    entry.RequestID,
		AppKey:       entry.AppKey,
		UserName:     entry.UserName,
		McpMethod:    entry.McpMethod,
		ToolName:     entry.ToolName,
		Arguments:    truncateAuditText(entry.Arguments, maxArgumentsLen),
		ResponseText: truncateAuditText(entry.ResponseText, maxResponseTextLen),
		ResultCode:   entry.ResultCode,
		ErrorMsg:     truncateRunes(entry.ErrorMsg, 250),
		CostMs:       entry.CostMs,
		ClientIP:     entry.ClientIP,
		ClientInfo:   entry.ClientInfo,
		CreatedAt:    time.Now(),
	}

	auditWriterState.RLock()
	writer := auditWriterState.writer
	if writer == nil {
		auditWriterState.RUnlock()
		return
	}
	select {
	case writer.queue <- record:
	default:
		zlog.Warnf(ctx, "[MCPGW] audit queue full, drop record, tool: %s", entry.ToolName)
	}
	auditWriterState.RUnlock()
}

// recordToolCall 记录一次工具调用的上游结果。
func recordToolCall(ctx *gin.Context, binding *ToolBinding, upstream *UpstreamResult) {
	var resultCode int
	var errMsg string
	if upstream != nil && !upstream.Success {
		resultCode = upstream.Code
		errMsg = upstream.Message
	}

	recordToolBinding(ctx, binding)
	ctx.Set(auditResultCodeKey, resultCode)
	ctx.Set(auditErrorMsgKey, errMsg)
}

// recordToolBinding 在调用上游前记录工具身份，确保参数校验或网络失败也可审计。
func recordToolBinding(ctx *gin.Context, binding *ToolBinding) {
	ctx.Set(auditToolNameKey, binding.Name)
	ctx.Set(auditBindingKey, binding)
}

const (
	auditToolNameKey   = "mcp_audit_tool_name"
	auditBindingKey    = "mcp_audit_binding"
	auditArgumentsKey  = "mcp_audit_arguments"
	auditResultCodeKey = "mcp_audit_result_code"
	auditErrorMsgKey   = "mcp_audit_error_msg"
)

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func truncateAuditText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
