//go:build windows

// tcpopt_windows.go —— Windows 平台的「死连接清理上限」socket 选项实装。
//
// 为什么单独成文件并用 build-tag：Windows 没有 Linux 的 TCP_USER_TIMEOUT，但有语义相近的
// TCP_MAXRT（最大重传时间）——TCP_MAXRTMS 以毫秒为单位限定「连接自首次重传起，持续重传多久后
// 被内核放弃」。属「重传/未确认数据存活上限」语义，对「≤90s 回收彻底 ACK 停滞的死连接」足够接近。
// windows.TCP_MAXRTMS 仅定义在 x/sys 的 windows 包，靠 build-tag 与 tcpopt_linux.go/_darwin.go 同构裁剪。
//
// 为什么用 SetsockoptInt 而非 WSAIoctl：TCP_MAXRT/TCP_MAXRTMS 是普通的 setsockopt(IPPROTO_TCP, ...)
// 选项，不需要 WSAIoctl——后者仅 SIO_KEEPALIVE_VALS 那类结构体式 keepalive 调参才需要，本处不涉及。
// 单位为【毫秒】，与 Linux 一致，故直接用 dialer.go 的毫秒常量 tcpUserTimeoutMs（90000）。
package dialer

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// ControlTCPUserTimeout 是 net.Dialer.Control / net.ListenConfig.Control 的回调，
// 在底层 socket fd 上设置 TCP_MAXRTMS = tcpUserTimeoutMs 毫秒（Windows 等价于 Linux 的 USER_TIMEOUT 90s）。
// 签名与其它平台版本严格一致，使三接入点的 Control 字段赋值跨平台统一（DRY）。
func ControlTCPUserTimeout(network, address string, c syscall.RawConn) error {
	// 外层 ctrlErr 抛 c.Control 自身错误，内层 setErr 捕获 setsockopt 错误，避免被吞掉（同 Linux 版）。
	var setErr error
	ctrlErr := c.Control(func(fd uintptr) {
		// Windows 句柄类型为 windows.Handle，需从 RawConn.Control 给出的 uintptr fd 转换。
		setErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_TCP, windows.TCP_MAXRTMS, tcpUserTimeoutMs)
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return setErr
}
