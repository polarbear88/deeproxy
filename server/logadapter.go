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
//
// 注意：go-socks5 对每个连接级错误都会调用 Errorf，但其中绝大多数是良性事件
// （客户端断开、EOF、非 SOCKS5 握手、认证失败等），并非服务端真正的故障。
// 若按 warn/error 输出会在正常使用时刷屏，因此统一降到 debug 级——
// 平时安静，需要排查时把 log_level 调成 debug 即可看到。
type socksLogger struct {
	logger *slog.Logger
}

// newSocksLogger 构造一个 slog 适配的库日志器。
func newSocksLogger(logger *slog.Logger) *socksLogger {
	return &socksLogger{logger: logger}
}

// Errorf 实现 socks5.Logger 接口，将库内部消息以 debug 级别输出。
func (s *socksLogger) Errorf(format string, args ...any) {
	s.logger.Debug("socks5: " + fmt.Sprintf(format, args...))
}
