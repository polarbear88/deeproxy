package server

import (
	"errors"
	"io"
	"net"
	"syscall"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

// 内部哨兵错误。
var (
	// errNoDecision 表示 ConnectHandle 未能从 context 取到 Allow 阶段的判定结果。
	errNoDecision = errors.New("缺少规则判定结果")
	// errSniffReject 表示嗅探到域名后命中 reject 规则；此时 success 已回复，只能关闭连接。
	errSniffReject = errors.New("嗅探后命中拒绝规则")
)

// closeWriter 抽象支持半关闭写方向的连接。
type closeWriter interface{ CloseWrite() error }

// sendSuccess 回复 SOCKS5 成功。
// 注意：go-socks5 的 SendReply 对成功码要求传入一个有效的 *net.TCPAddr，
// 否则会把成功码改写成 RepAddrTypeNotSupported(0x08)。CONNECT 客户端不关心
// BND.ADDR，这里用零地址即可。
func sendSuccess(w io.Writer) error {
	return socks5.SendReply(w, statute.RepSuccess, &net.TCPAddr{IP: net.IPv4zero, Port: 0})
}

// replyCodeFor 把拨号错误映射为合适的 SOCKS5 回复码。
// 用 errors.Is 比对 syscall 错误码而非匹配错误字符串，以保证跨平台（含 Windows
// 本地化错误消息）稳定——net 包会把各平台底层错误包装成可被 errors.Is 命中的 errno。
// 上游经 SOCKS 协议报告的目标级失败不是 syscall 错误，落到默认 RepHostUnreachable。
func replyCodeFor(err error) byte {
	switch {
	case err == nil:
		return statute.RepSuccess
	case errors.Is(err, syscall.ECONNREFUSED):
		return statute.RepConnectionRefused
	case errors.Is(err, syscall.ENETUNREACH):
		return statute.RepNetworkUnreachable
	default:
		return statute.RepHostUnreachable
	}
}

// relay 在客户端与目标之间做双向数据中继，并在任一方向结束时尽力半关闭对端写方向，
// 使另一方向能正常收尾（half-close）。
//
// 语义与 go-socks5 原生中继一致：**等待两个方向都干净结束**才返回，
// 仅当某一方向出错时才提前返回。早期版本“只等一个方向”会在一方 EOF 后立即关闭整条
// 连接，截断另一方向尚未发完的数据（典型如“请求小、响应大”的 HTTP/gRPC）。
// 病态的双向静默连接由上游 conn 的 idleConn 读超时兜底，不会永久挂起。
//
// 参数 clientW/clientR 是客户端连接的写端与读端（go-socks5 在 ConnectHandle 中
// 分别以 writer 和 request.Reader 暴露）；target 是已建立的目标连接。
func relay(clientW io.Writer, clientR io.Reader, target net.Conn) error {
	errc := make(chan error, 2)

	// 客户端 → 目标
	go func() {
		_, err := io.Copy(target, clientR)
		if cw, ok := target.(closeWriter); ok {
			_ = cw.CloseWrite() // 通知目标：客户端方向已结束
		}
		errc <- err
	}()

	// 目标 → 客户端
	go func() {
		_, err := io.Copy(clientW, target)
		if cw, ok := clientW.(closeWriter); ok {
			_ = cw.CloseWrite() // 通知客户端：响应已结束
		}
		errc <- err
	}()

	// 等待两个方向都结束；任一方向出错则提前返回该错误。
	for range 2 {
		if e := <-errc; e != nil {
			return e
		}
	}
	return nil
}

// copyResult 是单个中继方向的结果（字节数 + 错误）。
type copyResult struct {
	n   int64
	err error
}

// relayCounted 与 relay 行为完全一致（双向中继 + half-close），但额外返回
// 各方向的字节数，供连接结束后【一次性】埋点到 stats（零侵入字节中继热路径）。
//
// 性能说明（一号硬约束）：字节计数来自 io.Copy 自身的返回值 n，热循环仍是纯
// io.Copy，无额外 per-byte 工作、无锁；统计只在 relay 返回后由调用方调用一次
// stats.AddUp/AddDown（atomic），完全不进入字节中继循环。
//
// 返回 up=客户端→目标字节数（上行）、down=目标→客户端字节数（下行）、err=首个出错方向。
func relayCounted(clientW io.Writer, clientR io.Reader, target net.Conn) (up, down int64, err error) {
	upc := make(chan copyResult, 1)
	downc := make(chan copyResult, 1)

	// 客户端 → 目标（上行）。
	go func() {
		n, e := io.Copy(target, clientR)
		if cw, ok := target.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		upc <- copyResult{n: n, err: e}
	}()

	// 目标 → 客户端（下行）。
	go func() {
		n, e := io.Copy(clientW, target)
		if cw, ok := clientW.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		downc <- copyResult{n: n, err: e}
	}()

	// 等待两个方向都结束，累计字节数；保留首个非 nil 错误。
	ur := <-upc
	dr := <-downc
	up, down = ur.n, dr.n
	if ur.err != nil {
		return up, down, ur.err
	}
	return up, down, dr.err
}
