//go:build darwin

// tcpopt_darwin.go —— macOS 平台的「死连接清理上限」socket 选项实装。
//
// 为什么单独成文件并用 build-tag：macOS 没有 Linux 的 TCP_USER_TIMEOUT，但有语义相近的
// TCP_RXT_CONNDROPTIME——它限定「连接在持续重传（即有未确认在途数据）多久后被内核放弃」。
// 二者都属「重传/未确认数据存活上限」语义，对「≤90s 回收彻底 ACK 停滞的死连接」目标足够接近。
// 把它单独成文件靠 build-tag 裁剪，与 tcpopt_linux.go 同构：unix.TCP_RXT_CONNDROPTIME 仅定义在
// x/sys 的 zerrors_darwin_*.go 中，写进跨平台共用文件会令 linux/windows 交叉编译因符号缺失而失败。
//
// 关键单位差异：TCP_RXT_CONNDROPTIME 的值是【秒】，而 Linux TCP_USER_TIMEOUT 是【毫秒】。
// 故此处用 dialer.go 的派生常量 tcpUserTimeoutSec（= tcpUserTimeoutMs/1000 = 90），不可误用毫秒常量。
package dialer

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// ControlTCPUserTimeout 是 net.Dialer.Control / net.ListenConfig.Control 的回调，
// 在底层 socket fd 上设置 TCP_RXT_CONNDROPTIME = tcpUserTimeoutSec 秒（macOS 等价于 Linux 的
// USER_TIMEOUT 90s）。签名与其它平台版本严格一致，使三接入点的 Control 字段赋值跨平台统一（DRY）。
func ControlTCPUserTimeout(network, address string, c syscall.RawConn) error {
	// 外层 ctrlErr 抛 c.Control 自身错误，内层 setErr 捕获 setsockopt 错误，避免被吞掉（同 Linux 版）。
	var setErr error
	ctrlErr := c.Control(func(fd uintptr) {
		// TCP_RXT_CONNDROPTIME 单位为秒：连接持续重传超过该秒数仍无 ACK 进展则内核放弃（丢弃连接）。
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_RXT_CONNDROPTIME, tcpUserTimeoutSec)
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return setErr
}
