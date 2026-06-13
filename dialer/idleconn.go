package dialer

import (
	"net"
	"time"
)

// idleConn 包装一个 net.Conn，为其增加“空闲超时”能力。
//
// 背景：go-socks5 的内部中继（io.Copy）不会给连接设置任何读写截止时间，
// 因此一条双向都不再有数据的“半开连接”（如对端崩溃无 RST、NAT 静默丢弃）
// 会让中继 goroutine 与文件描述符长期挂起、泄漏。
//
// 解决：每次成功 Read/Write 后，把读截止时间向后滚动 idle 时长。
// 一旦在 idle 时长内两个方向都无任何数据，Read 会因超时返回错误，
// 触发库关闭整条连接，从而回收资源。
type idleConn struct {
	net.Conn
	idle time.Duration
}

// WrapIdle 用空闲超时包装给定连接。idle <= 0 时不包装，原样返回。
func WrapIdle(c net.Conn, idle time.Duration) net.Conn {
	if idle <= 0 {
		return c
	}
	ic := &idleConn{Conn: c, idle: idle}
	// 设置初始截止时间，避免建连后立即空闲却无超时。
	_ = ic.touch()
	return ic
}

// touch 把读截止时间向后滚动 idle 时长。
func (c *idleConn) touch() error {
	return c.Conn.SetReadDeadline(time.Now().Add(c.idle))
}

// Read 读取数据；读到数据后滚动空闲计时。
func (c *idleConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		_ = c.touch()
	}
	return n, err
}

// Write 写出数据；写出后同样滚动读截止时间，
// 因为一次成功的写也说明连接仍然活跃，不应被判定为空闲。
func (c *idleConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		_ = c.touch()
	}
	return n, err
}

// halfCloser 抽象支持半关闭写方向的连接（*net.TCPConn 实现了它）。
type halfCloser interface {
	CloseWrite() error
}

// CloseWrite 把半关闭转发给底层连接，使包装后的连接仍可参与“写方向先收尾”的
// 双向中继。底层不支持时返回 nil（退化为不半关闭，不影响正确性）。
func (c *idleConn) CloseWrite() error {
	if hc, ok := c.Conn.(halfCloser); ok {
		return hc.CloseWrite()
	}
	return nil
}
