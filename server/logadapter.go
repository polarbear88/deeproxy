package server

import (
	"errors"
	"fmt"
	"log/slog"
)

// errNoDecision 表示拨号阶段未能从 context 取到 Allow 阶段的判定结果。
// 正常流程一定先经过 connectRule.Allow，故出现该错误意味着内部流程异常。
var errNoDecision = errors.New("缺少规则判定结果")

// socksLogger 把 go-socks5 的 Logger 接口（仅 Errorf）桥接到 slog。
// 库内部的错误日志会以 warn 级别经由我们的结构化日志器输出。
type socksLogger struct {
	logger *slog.Logger
}

// newSocksLogger 构造一个 slog 适配的库日志器。
func newSocksLogger(logger *slog.Logger) *socksLogger {
	return &socksLogger{logger: logger}
}

// Errorf 实现 socks5.Logger 接口。
func (s *socksLogger) Errorf(format string, args ...any) {
	s.logger.Warn("socks5: " + fmt.Sprintf(format, args...))
}
