package server

import (
	"context"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	"deeproxy/dialer"
)

// 本文件实现 SOCKS5 服务的【生命周期与健壮性加固】，与核心转发逻辑（server.go）分离：
//   - recoverPool：每连接 goroutine 的 panic 兜底（C1，防单连接 panic 崩溃整进程）；
//   - deadlineListener：accept 即给新连接设握手读截止时间（C2，防 slowloris 半开连接泄漏）。
//
// 二者均为旁路加固，不进入字节中继热路径：recoverPool 仅在 goroutine 入口包一层 defer；
// 握手截止时间在 connectHandle 进入后即被清除（见 server.go），中继期间不受影响。

// handshakeTimeout 是 SOCKS5 握手阶段（方法协商 + 认证 + CONNECT 请求解析）的读超时。
//
// 为什么必须有（C2/CRITICAL）：go-socks5 的 ServeConn 在读 greeting / authenticate /
// ParseRequest 全程【不设任何读截止时间】。客户端完成 TCP 握手后若迟迟不发 SOCKS5
// 首字节（或逐字节慢喂），ServeConn 的 Read 会永久阻塞、goroutine + fd 永不回收；
// 默认 0.0.0.0 监听下少量并发即可耗尽 fd（典型 slowloris DoS）。
// 故在 Accept 时即给连接设一个握手期读截止；一旦进入 connectHandle（说明请求已解析完毕、
// 握手成功），立即清除该截止时间，使后续中继不被它误伤。
const handshakeTimeout = 10 * time.Second

// recoverPool 实现 socks5.GPool：为每条连接的处理 goroutine 包一层 panic recover。
//
// 为什么必须有（C1/CRITICAL）：go-socks5 的 Serve 用裸 `go ServeConn(conn)` 起每连接 goroutine，
// 无任何 recover。中继处理的是【不可信客户端输入】，连接路径上任意一处 panic（nil 解引用、
// 切片越界、未来代码改动引入的缺陷）都会沿调用栈逸出、终止【整个进程】——杀死所有其它在途
// 连接与管理后台。gin.Recovery 只保护 HTTP handler，保不住 SOCKS goroutine。
// 通过 WithGPool 注入本池后，库的 goFunc 会用 Submit 调度，从而每连接独立 recover：
// 单连接 panic 仅记日志并关闭该连接，绝不波及进程。
type recoverPool struct {
	logger *slog.Logger
}

// Submit 在带 panic recover 的新 goroutine 中执行 f，并返回 nil（表示已自行调度）。
//
// 返回 nil 后 go-socks5 不会再 `go f()`，避免重复执行；若返回非 nil 则库会回退到裸 go，
// 故这里恒返回 nil 以确保 recover 始终生效。
func (p recoverPool) Submit(f func()) error {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 单连接处理 panic：记错误 + 调用栈，goroutine 正常收尾（conn 由 ServeConn 的
				// defer conn.Close() 关闭），进程继续服务其它连接。
				p.logger.Error("SOCKS5 连接处理发生 panic，已恢复（不影响其它连接）",
					"panic", r, "stack", string(debug.Stack()))
			}
		}()
		f()
	}()
	return nil
}

// deadlineListener 包装 net.Listener，在每次 Accept 返回新连接时为其设一个握手期读截止时间。
//
// 这样 go-socks5 在握手阶段的任何阻塞读都受 timeout 约束；连接进入 connectHandle（握手成功）
// 后由 clearHandshakeDeadline 清除该截止，使后续双向中继不被握手超时误伤。
type deadlineListener struct {
	net.Listener
	timeout time.Duration
}

// Accept 接受新连接并立即设握手读截止时间（timeout<=0 时不设，退化为原生行为）。
func (l deadlineListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.timeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(l.timeout))
	}
	return c, nil
}

// Listen 在 network/addr 上创建一个【带握手读超时】的监听器（C2）。
//
// cmd 装配阶段用它替代裸 net.Listen，再把返回的 listener 交给 socks5.Server.Serve；
// 关闭该 listener 即可让 Serve 的 Accept 返回错误从而停止接受新连接（H1 优雅关闭的抓手）。
func Listen(network, addr string) (net.Listener, error) {
	// 用 ListenConfig 替代裸 net.Listen，给每个 accept 的客户端 TCP 连接启用 keepalive 检活。
	//
	// 为什么必须有（#5 泄漏主修复）：客户端断电时不发 FIN/RST，relay 期间 io.Copy(target, clientR)
	// 在死客户端读上永久阻塞，goroutine + 上游连接 + 注册表条目永不回收。OS keepalive 探测到
	// 死连接后令 Read 返错 → io.Copy 返回 → closeBoth + Deregister 清理上游与注册表。
	//
	// KeepAlive:-1 显式禁用裸 duration 路径，完全交由 KeepAliveConfig（dialer 包单一事实源）决定；
	// 周期 30/15/3 = 75s 落在 AC「死连接 ≤90s 清理」窗内（裸 KeepAlive:30s 走默认 15s×9=165s 会超窗）。
	// context.Background()：监听器生命周期 = 进程级，装配期一次性创建，无需取消语义。
	//
	// Control: dialer.ControlTCPUserTimeout —— 给每个 accept 的客户端 TCP 连接加死连接清理上限 90s
	// （各平台原生实现，详见 dialer.go 的平台真值表注释 / tcpopt_*.go）。
	// 为何这一层不可少：keepalive 仅在连接【空闲且无未确认数据】时探测；而当服务端正向客户端回发数据、
	// 发送缓冲里有未确认数据时，内核会抑制 keepalive 探测（不发），此时死客户端无法被 keepalive 检出。
	// USER_TIMEOUT 正是覆盖这一盲区：连接【有未确认在途数据】90s 仍无 ACK 进展即重置回收。三接入点
	// （listener / DialDirect / DialUpstream.fwd）共用 dialer.ControlTCPUserTimeout（DRY，权衡见 dialer.go 真值表注释）。
	lc := net.ListenConfig{
		KeepAlive:       -1,
		KeepAliveConfig: dialer.KeepAliveConfig,
		Control:         dialer.ControlTCPUserTimeout,
	}
	l, err := lc.Listen(context.Background(), network, addr)
	if err != nil {
		return nil, err
	}
	return deadlineListener{Listener: l, timeout: handshakeTimeout}, nil
}

// clearHandshakeDeadline 清除连接上的握手读截止时间（设为零值=永不超时）。
//
// 在 connectHandle 进入后调用：此刻 SOCKS5 请求已解析完毕、握手成功，握手超时使命完成；
// 必须清除，否则它会在后续中继/嗅探读取时误触发超时（中继的空闲超时另由 idleConn 负责）。
// 入参是 go-socks5 透传的底层连接（以 io.Writer 形态）；非 net.Conn 时静默跳过（防御性）。
func clearHandshakeDeadline(w any) {
	if c, ok := w.(net.Conn); ok {
		_ = c.SetReadDeadline(time.Time{})
	}
}
