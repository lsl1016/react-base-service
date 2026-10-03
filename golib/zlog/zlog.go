// Package zlog 提供上下文日志的最小实现（stdout + 可选文件落盘与轮转），
package zlog

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"gopkg.in/natefinch/lumberjack.v2"
)

// LogConfig 日志配置。
type LogConfig struct {
	Level  string `yaml:"level"`
	Stdout bool   `yaml:"stdout"`
	// Dir 文件输出目录；为空时不落盘（仅 stdout）。
	Dir string `yaml:"dir"`
	// Filename 文件名前缀，生成 <Dir>/<Filename>.log；默认 app。
	Filename string `yaml:"filename"`
	// MaxSizeMB 单文件大小上限（MB），超过即切割；未配置（<=0）默认 100。
	MaxSizeMB int `yaml:"max_size_mb"`
	// MaxAgeDays 切割文件保留天数；0 表示不按时间清理（lumberjack 语义）。
	MaxAgeDays int `yaml:"max_age_days"`
	// MaxBackups 保留的切割文件数量上限；0 表示不限制数量（lumberjack 语义）。
	MaxBackups int `yaml:"max_backups"`
}

// Field 结构化日志字段。
type Field struct {
	Key    string
	String string
}

// String 构造字符串字段。
func String(key, value string) Field {
	return Field{Key: key, String: value}
}

var (
	mu       sync.Mutex
	level    = "info"
	logger   = log.New(os.Stdout, "", log.LstdFlags)
	closer   io.Closer
	hookFns  []func([]Field) []Field
	noLogKey = "zlog_no_log"
)

// InitLog 初始化日志：按配置组装 stdout / 文件（lumberjack 轮转）输出。
func InitLog(cfg LogConfig) {
	mu.Lock()
	defer mu.Unlock()
	if cfg.Level != "" {
		level = strings.ToLower(cfg.Level)
	}

	writers := make([]io.Writer, 0, 2)
	if cfg.Stdout || strings.TrimSpace(cfg.Dir) == "" {
		writers = append(writers, os.Stdout)
	}
	if dir := strings.TrimSpace(cfg.Dir); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			name := strings.TrimSpace(cfg.Filename)
			if name == "" {
				name = "app"
			}
			maxSize := cfg.MaxSizeMB
			if maxSize <= 0 {
				maxSize = 100
			}
			rotator := &lumberjack.Logger{
				Filename:   filepath.Join(dir, name+".log"),
				MaxSize:    maxSize,
				MaxAge:     cfg.MaxAgeDays,
				MaxBackups: cfg.MaxBackups,
				Compress:   true,
			}
			writers = append(writers, rotator)
			closer = rotator
		}
	}

	var out io.Writer
	switch len(writers) {
	case 0:
		out = os.Stdout
	case 1:
		out = writers[0]
	default:
		out = io.MultiWriter(writers...)
	}
	logger.SetOutput(out)
}

// CloseLogger 关闭日志（触发轮转器落盘收尾）。
func CloseLogger() {
	mu.Lock()
	defer mu.Unlock()
	if closer != nil {
		_ = closer.Close()
		closer = nil
	}
}

// RegisterHookField 注册字段改写钩子（本地实现为空操作存储）。
func RegisterHookField(fn func([]Field) []Field) {
	mu.Lock()
	defer mu.Unlock()
	hookFns = append(hookFns, fn)
}

// SetNoLogFlag 标记当前请求不输出访问日志。
func SetNoLogFlag(ctx context.Context) {
	if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil {
		ginCtx.Set(noLogKey, true)
	}
}

// GetLogID 读取请求日志 ID。
func GetLogID(ctx context.Context) string {
	if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil {
		if v := ginCtx.GetString("logID"); v != "" {
			return v
		}
	}
	if ctx != nil {
		if v, ok := ctx.Value("logID").(string); ok {
			return v
		}
	}
	return ""
}

// GetRequestID 读取请求 ID。
func GetRequestID(ctx context.Context) string {
	if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil {
		if v := ginCtx.GetString("requestId"); v != "" {
			return v
		}
	}
	if ctx != nil {
		if v, ok := ctx.Value("requestId").(string); ok {
			return v
		}
	}
	return ""
}

func logf(ctx context.Context, lvl, format string, v ...interface{}) {
	msg := sprintf(format, v...)
	logID := GetLogID(ctx)
	if logID == "" {
		logger.Printf("[%s] %s", strings.ToUpper(lvl), msg)
		return
	}
	logger.Printf("[%s] [%s] %s", strings.ToUpper(lvl), logID, msg)
}

func levelEnabled(lvl string) bool {
	order := map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
	cur, ok := order[level]
	if !ok {
		cur = 1
	}
	return order[lvl] >= cur
}

func Debugf(ctx context.Context, format string, v ...interface{}) {
	if levelEnabled("debug") {
		logf(ctx, "debug", format, v...)
	}
}

func Infof(ctx context.Context, format string, v ...interface{}) {
	if levelEnabled("info") {
		logf(ctx, "info", format, v...)
	}
}

func Warnf(ctx context.Context, format string, v ...interface{}) {
	if levelEnabled("warn") {
		logf(ctx, "warn", format, v...)
	}
}

func Errorf(ctx context.Context, format string, v ...interface{}) {
	logf(ctx, "error", format, v...)
}
