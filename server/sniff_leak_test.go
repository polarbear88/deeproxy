package server

import (
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	"deeproxy/store"
)

// sniff_leak_test.go：CRITICAL #1 回归测试。
//
// 漏洞背景：嗅探首包读取的超时此前形同虚设——peekFirstPacket 对 *bufio.Reader 做
// SetReadDeadline 类型断言恒为 false，sniffTimeout 完全不生效。默认开启嗅探时，
// 客户端连上后【不发首包】（TCP 半开 / 端口扫描 / 慢速攻击）会让首包 Read 永久阻塞，
// ServeConn 不返回 → conn 不关闭 → 每条恶意连接永久泄漏一个 goroutine + 一个 fd，
// 少量并发即可耗尽资源（DoS）。
//
// 本测试模拟「客户端连上、完成 SOCKS5 握手进入嗅探路径、但绝不发送首包」，断言：
// 在 sniffTimeout 到期后，被测 server 因首包读超时而走默认动作并最终收尾连接，
// 服务端 goroutine 数能回落到基线附近（不随连接数线性增长 = 不泄漏）。

// dialHalfOpenSniff 建立一条连接并完成到「嗅探 success 已回」为止的 SOCKS5 握手，
// 然后【故意不发送任何应用层首包】，模拟半开 / 慢速攻击客户端。
// 返回该连接，调用方负责最终关闭（清理）。
func dialHalfOpenSniff(t *testing.T, proxyAddr, username string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 deeproxy 失败: %v", err)
	}
	// 握手阶段给读写设个上限，避免测试端自己卡死；进入嗅探后清除。
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// 方法协商：仅提供用户名/密码认证。
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("写方法协商失败: %v", err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(conn, rep); err != nil {
		t.Fatalf("读方法回复失败: %v", err)
	}

	// 用户名/密码认证（用户名携带动态上游尾段）。
	authMsg := []byte{0x01, byte(len(username))}
	authMsg = append(authMsg, username...)
	authMsg = append(authMsg, byte(len("secret")))
	authMsg = append(authMsg, "secret"...)
	if _, err := conn.Write(authMsg); err != nil {
		t.Fatalf("写认证失败: %v", err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
		t.Fatalf("读认证回复失败: %v", err)
	}

	// CONNECT 到一个 IP（未命中 ip-cidr → 进入嗅探路径，server 会先回 success 等首包）。
	msg := []byte{0x05, 0x01, 0x00, atypIPv4}
	msg = append(msg, net.ParseIP("203.0.113.7").To4()...)
	msg = append(msg, byte(443>>8), byte(443&0xff))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("写 CONNECT 请求失败: %v", err)
	}

	// 读 success 回复头（嗅探路径先回 0x00 才会等首包）。
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("读回复头失败: %v", err)
	}
	if head[1] != 0x00 {
		t.Fatalf("嗅探路径应先回 success(0x00)，实际 0x%02x", head[1])
	}
	// IPv4 绑定地址 4 + 端口 2。
	if _, err := io.ReadFull(conn, make([]byte, 4+2)); err != nil {
		t.Fatalf("读绑定地址失败: %v", err)
	}

	// 清除测试端 deadline：从此【绝不再写任何字节】，模拟攻击者保持连接但不发首包。
	_ = conn.SetDeadline(time.Time{})
	return conn
}

// TestSniff_FirstPacketTimeout_NoLeak 复现并验证 CRITICAL #1 修复：
// 大量「连上不发首包」的连接不会让服务端 goroutine 永久泄漏。
func TestSniff_FirstPacketTimeout_NoLeak(t *testing.T) {
	// 用 reject 默认动作：嗅探超时回退默认动作=reject 时，server 直接关闭连接，
	// 路径最短，便于稳定断言「超时即收尾」。（修复前此处会永久阻塞在首包 Read。）
	env, _, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:never.match", Action: "forward"}},
		"reject")

	// sniffTimeout = 300ms（见 harness cfg.SniffTimeoutMs）。
	const conns = 30

	// 基线 goroutine 数：先稳定一下运行时再采样。
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()

	// 并发打开 N 条半开嗅探连接（成功进入「等首包」状态后绝不发首包）。
	clients := make([]net.Conn, 0, conns)
	for i := 0; i < conns; i++ {
		c := dialHalfOpenSniff(t, env.addr, user)
		clients = append(clients, c)
	}
	t.Cleanup(func() {
		for _, c := range clients {
			_ = c.Close()
		}
	})

	// 等待远超 sniffTimeout：修复后，每条连接的首包 Read 都会在 ~300ms 内超时，
	// server 走默认动作(reject)并关闭连接，对应的 ServeConn goroutine 退出。
	// 修复前：所有 goroutine 永久阻塞在 Read，下面的回落断言必然失败。
	deadline := time.Now().Add(5 * time.Second)
	var now int
	for time.Now().Before(deadline) {
		runtime.GC()
		now = runtime.NumGoroutine()
		// 允许少量浮动（运行时/测试自身 goroutine）；只要没有随连接数线性堆积即视为不泄漏。
		if now <= base+5 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if now > base+5 {
		t.Fatalf("首包超时后 goroutine 未回落，疑似泄漏：基线=%d 当前=%d（开了 %d 条半开连接）",
			base, now, conns)
	}
}
