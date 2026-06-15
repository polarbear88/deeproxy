//go:build linux

// tcpopt_linux.go —— Linux 平台的 TCP_USER_TIMEOUT socket 选项实装。
//
// 为什么单独成文件并用 build-tag：unix.TCP_USER_TIMEOUT 这个常量仅定义在 x/sys 的
// zerrors_linux.go 中，darwin / windows 的 x/sys 包根本没有该符号。若把它写进一个
// 跨平台共用的文件，darwin/windows 交叉编译会因「符号缺失」直接编译失败——这是编译期
// 符号解析问题，运行时 if runtime.GOOS 之类的判断救不了（A2 伪选项）。故必须靠 build-tag
// 在编译期裁剪：Linux 编译进本文件的实装，其它平台编译进各自的 tcpopt_*.go
// （windows: TCP_MAXRTMS、darwin: TCP_RXT_CONNDROPTIME、其余: tcpopt_other.go 空实现降级到
// keepalive 75s），同时保持「单一静态二进制、全平台 go build 全过」的硬约束。
package dialer

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// ControlTCPUserTimeout 是 net.Dialer.Control / net.ListenConfig.Control 的回调，
// 在底层 socket fd 上设置 TCP_USER_TIMEOUT = tcpUserTimeoutMs（见 dialer.go 常量）。
//
// 签名 (network, address string, c syscall.RawConn) error 同时满足 Dialer.Control
// 与 ListenConfig.Control 两种字段类型，故三接入点（listener / DialDirect / DialUpstream.fwd）
// 可共用本函数（DRY，见 dialer.go 四层超时真值表注释）。
//
// 为什么作用在“真实 TCP socket”上：Control 回调拿到的 fd 是内核真实 TCP 连接的文件描述符，
// 三接入点分别命中 客户端→本机监听器 / 本机→直连目标 / 本机→上游 SOCKS5 这三跳真实 TCP，
// 不触及 forward 隧道内被中继的 SOCKS 载荷流——正是预期范围。
func ControlTCPUserTimeout(network, address string, c syscall.RawConn) error {
	// 用 c.Control 在 fd 上同步执行 setsockopt；外层 error 用来抛出 c.Control 自身的错误，
	// 内层 setErr 捕获 setsockopt 的错误，避免被 c.Control 的返回值吞掉。
	var setErr error
	ctrlErr := c.Control(func(fd uintptr) {
		// TCP_USER_TIMEOUT：当连接上“有未确认在途数据”时，限定其最长存活；
		// 与 keepalive 语义互补——keepalive 仅在连接空闲且无未确认数据时探测。
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, tcpUserTimeoutMs)
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return setErr
}
