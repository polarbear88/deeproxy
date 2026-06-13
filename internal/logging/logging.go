// Package logging 提供基于标准库 log/slog 的日志器初始化。
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// parseLevel 把级别字符串解析为 slog.Level，未知回退 info。
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ParseLevel 是 parseLevel 的导出版本，供上层（api 后台改日志级别）解析字符串级别。
func ParseLevel(level string) slog.Level { return parseLevel(level) }

// New 根据级别字符串创建一个写到 stderr 的文本格式 slog.Logger。
// 支持 debug/info/warn/error，未知级别回退为 info。
func New(level string) *slog.Logger {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(h)
}

// NewWithHandlers 创建一个同时写到 stderr 文本 Handler 与若干额外 Handler 的 Logger。
//
// 用途（v2 装配）：把 syslog 内存环形缓冲的 Handler 与控制台输出并联，
// 使日志既打到 stderr 又进入后台可实时推送的内存缓冲（系统日志页 SSE）。
// 各 Handler 独立判级与输出，互不影响（DRY：复用 fanout）。
func NewWithHandlers(level string, extra ...slog.Handler) *slog.Logger {
	stderrH := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(level)})
	handlers := append([]slog.Handler{stderrH}, extra...)
	return slog.New(&fanout{handlers: handlers})
}

// NewWithLevelVar 与 NewWithHandlers 类似，但所有 Handler 共用一个【可动态调整】的
// *slog.LevelVar 作为级别。取消配置文件后日志级别迁入 system_setting，后台改级别时只需对
// 该 LevelVar 调 Set（原子、并发安全），无需重启即热生效——这是 log_level 动态化的关键。
//
// levelVar 由调用方（cmd）创建并持有；本函数只把它作为各 Handler 的 Level 接入。
func NewWithLevelVar(levelVar *slog.LevelVar, extra ...slog.Handler) *slog.Logger {
	stderrH := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: levelVar})
	handlers := append([]slog.Handler{stderrH}, extra...)
	return slog.New(&fanout{handlers: handlers})
}

// fanout 是把一条日志记录分发给多个 Handler 的组合 Handler。
type fanout struct {
	handlers []slog.Handler
}

// Enabled 只要任一子 Handler 启用该级别即返回 true（让记录有机会被处理）。
func (f *fanout) Enabled(ctx context.Context, lv slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, lv) {
			return true
		}
	}
	return false
}

// Handle 把记录分发给所有【启用该级别】的子 Handler。
func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// WithAttrs 对每个子 Handler 派生带属性的副本。
func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanout{handlers: next}
}

// WithGroup 对每个子 Handler 派生带分组的副本。
func (f *fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &fanout{handlers: next}
}
