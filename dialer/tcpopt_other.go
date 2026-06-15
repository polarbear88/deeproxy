//go:build !linux && !darwin && !windows

// tcpopt_other.go —— 其余冷门平台（如 freebsd 等无对应 socket 选项的 GOOS）的空实现降级。
//
// 为什么是空实现：Linux/Windows/darwin 已各自有原生实现（tcpopt_linux.go: TCP_USER_TIMEOUT、
// tcpopt_windows.go: TCP_MAXRTMS、tcpopt_darwin.go: TCP_RXT_CONNDROPTIME）。本文件只兜底其余
// 没有等价「重传/未确认数据存活上限」socket 选项的平台——这些平台优雅降级，不设该选项，仅靠既有
// keepalive 75s（见 dialer.go KeepAliveConfig）检出死连接，保证「单一静态二进制 + 全平台 go build
// 全过」的硬约束不被破坏（详见 tcpopt_linux.go 的 build-tag 说明）。
//
// 仅 import syscall 是为了 syscall.RawConn 这一参数类型；syscall.RawConn 是各 GOOS stdlib 各自
// 定义的可移植接口类型，函数签名与其它平台版本严格一致，故三接入点无需感知平台差异。
package dialer

import "syscall"

// ControlTCPUserTimeout 在无对应 socket 选项的平台为 no-op，直接返回 nil（不修改 socket）。
// 签名与 tcpopt_linux.go / _windows.go / _darwin.go 版本完全一致，使三接入点的 Control 字段赋值跨平台统一。
func ControlTCPUserTimeout(network, address string, c syscall.RawConn) error {
	return nil
}
