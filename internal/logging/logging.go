// Package logging 提供基于标准库 log/slog 的日志器初始化。
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New 根据级别字符串创建一个写到 stderr 的文本格式 slog.Logger。
// 支持 debug/info/warn/error，未知级别回退为 info。
func New(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	return slog.New(h)
}
