package server

import (
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"deeproxy/store"
)

// sniff_test.go：v2 嗅探集成测试（AC-8：IP 未命中 ip-cidr → SNI/HTTP Host 还原域名再选路）。
// 复用 harness_test.go 的脚手架；用 Type A 组（尾段 base64 动态上游）驱动嗅探后的 forward。

// startBigSender 启动 TCP 服务器：连入即写 n 字节后关闭（不读），复现“响应大 + 半关闭”场景。
func startBigSender(t *testing.T, n int) (addr string, payload []byte) {
	t.Helper()
	payload = make([]byte, n)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = c.Write(payload) }(c)
		}
	}()
	return l.Addr().String(), payload
}

// makeClientHello 用 crypto/tls 生成带指定 SNI 的真实 ClientHello 字节流。
func makeClientHello(t *testing.T, sni string) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	go func() {
		_ = tls.Client(c1, &tls.Config{ServerName: sni, InsecureSkipVerify: true}).Handshake()
	}()
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := c2.Read(buf)
	if err != nil {
		t.Fatalf("生成 ClientHello 失败: %v", err)
	}
	return buf[:n]
}

// sniffEnv 启动一个开启嗅探的 Type A deeproxy（默认 SniffEnabled=true），上游指向 echo，
// 返回环境、假上游与可直接使用的“含动态上游尾段”的用户名。
func sniffEnv(t *testing.T, rules []store.Rule, def string) (env *deeproxyEnv, fu *fakeUpstream, username string) {
	echo := startEcho(t)
	fu = &fakeUpstream{realTarget: echo}
	upAddr := fu.start(t, nil)
	env = startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     rules,
		defAction: def,
		user:      "alice", pwd: "secret",
	})
	username = "alice-ga-" + encBase64("uu", "pp", upAddr)
	return env, fu, username
}

// TestRelay_NoTruncation 回归：客户端发完即半关闭后，目标大响应必须完整中继不截断（direct 路径）。
func TestRelay_NoTruncation(t *testing.T) {
	addr, payload := startBigSender(t, 1<<20) // 1 MiB
	host, port := splitHostPort(t, addr)

	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "ip-cidr:127.0.0.0/8", Action: "direct"}},
		defAction: "reject",
		user:      "alice", pwd: "secret",
	})

	rep, conn := socks5Connect(t, env.addr, "alice-ga", "secret", 0x01, atypIPv4, host, port)
	if rep != 0x00 {
		t.Fatalf("期望 success(0x00)，实际 0x%02x", rep)
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("响应被截断：收到 %d 字节，期望 %d", len(got), len(payload))
	}
}

// TestSniff_SNI_Forward 覆盖 AC-8：IP 目标 + TLS ClientHello(SNI=fwd.test)，
// 域名规则 forward → 经上游中继；首包被回放并经 echo 原样返回。
func TestSniff_SNI_Forward(t *testing.T) {
	env, fu, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:fwd.test", Action: "forward"}},
		"reject")

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "1.2.3.4", 443)
	if rep != 0x00 {
		t.Fatalf("嗅探路径应先回 success(0x00)，实际 0x%02x", rep)
	}
	hello := makeClientHello(t, "fwd.test")
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(hello); err != nil {
		t.Fatalf("发送 ClientHello 失败: %v", err)
	}
	got := make([]byte, len(hello))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败(转发链路不通?): %v", err)
	}
	if string(got) != string(hello) {
		t.Fatal("回显内容与发送首包不一致，回放/中继有误")
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1（嗅探后走上游）", fu.count.Load())
	}
}

// TestSniff_SNI_Reject 覆盖 AC-8：SNI=block.test 命中 reject → 连接被关闭（success 已回，无法回 0x02）。
func TestSniff_SNI_Reject(t *testing.T) {
	env, _, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:block.test", Action: "reject"}},
		"forward")

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "1.2.3.4", 443)
	if rep != 0x00 {
		t.Fatalf("嗅探路径应先回 success(0x00)，实际 0x%02x", rep)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(makeClientHello(t, "block.test")); err != nil {
		return // 写入即失败也算连接被拒
	}
	n, err := conn.Read(make([]byte, 16))
	if err == nil && n > 0 {
		t.Fatalf("嗅探 reject 后连接应被关闭，却读到 %d 字节", n)
	}
}

// TestSniff_HTTPHost_Forward 覆盖 AC-8：IP 目标 + 明文 HTTP(Host: h.test) → 命中 forward。
func TestSniff_HTTPHost_Forward(t *testing.T) {
	env, fu, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:h.test", Action: "forward"}},
		"reject")

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "5.6.7.8", 80)
	if rep != 0x00 {
		t.Fatalf("期望 success(0x00)，实际 0x%02x", rep)
	}
	req := []byte("GET / HTTP/1.1\r\nHost: h.test\r\nConnection: close\r\n\r\n")
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("发送 HTTP 请求失败: %v", err)
	}
	got := make([]byte, len(req))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败: %v", err)
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1", fu.count.Load())
	}
}

// TestSniff_Fallback 覆盖 AC-8：首包既非 TLS 也非 HTTP → 走默认动作(forward)并正常中继。
func TestSniff_Fallback(t *testing.T) {
	env, fu, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:never.test", Action: "reject"}},
		"forward")

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "9.9.9.9", 1234)
	if rep != 0x00 {
		t.Fatalf("期望 success(0x00)，实际 0x%02x", rep)
	}
	payload := []byte{0x00, 0x01, 0x02, 0x03, 0x04}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("发送负载失败: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败(默认动作中继不通?): %v", err)
	}
	if string(got) != string(payload) {
		t.Fatal("回显内容不一致")
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1（默认 forward）", fu.count.Load())
	}
}

// TestSniff_IPCIDRPrecedence 覆盖 AC-8：IP 命中 ip-cidr 规则时不嗅探，直接按该规则(direct)。
func TestSniff_IPCIDRPrecedence(t *testing.T) {
	echo := startEcho(t)
	fu := &fakeUpstream{realTarget: echo}
	upAddr := fu.start(t, nil)
	host, port := splitHostPort(t, echo)

	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "ip-cidr:127.0.0.0/8", Action: "direct"}},
		defAction: "reject",
		user:      "alice", pwd: "secret",
	})
	user := "alice-ga-" + encBase64("uu", "pp", upAddr)

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, host, port)
	if rep != 0x00 {
		t.Fatalf("ip-cidr direct 期望 success(0x00)，实际 0x%02x", rep)
	}
	msg := []byte("hello-direct")
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败: %v", err)
	}
	if fu.count.Load() != 0 {
		t.Fatalf("ip-cidr direct 不应经过上游，计数=%d", fu.count.Load())
	}
}

// writeInChunks 把 data 切成 parts 段，段间留 gap 间隔分别写出，
// 模拟「应用层消息被拆成多个 TCP 段」的真实网络行为（每次 Write→对端一次 Read）。
// gap 之和需远小于 sniffTimeout(测试环境 300ms)，否则会触发嗅探超时。
func writeInChunks(t *testing.T, conn net.Conn, data []byte, parts int, gap time.Duration) {
	t.Helper()
	if parts < 1 {
		parts = 1
	}
	step := (len(data) + parts - 1) / parts // 向上取整，保证覆盖全部字节
	for i := 0; i < len(data); i += step {
		end := i + step
		if end > len(data) {
			end = len(data)
		}
		if _, err := conn.Write(data[i:end]); err != nil {
			t.Fatalf("分段写第 [%d:%d] 失败: %v", i, end, err)
		}
		if end < len(data) {
			time.Sleep(gap) // 制造段边界：让对端先 Read 到前半段，再收到后半段
		}
	}
}

// TestSniff_SNI_Forward_SplitTwoSegments 回归 Critical 缺陷：
// TLS ClientHello 被拆成 2 个 TCP 段到达时，循环读必须把记录读全再嗅探 SNI，
// 否则旧实现只读首段会因记录不完整而嗅探失败、回退默认动作。
func TestSniff_SNI_Forward_SplitTwoSegments(t *testing.T) {
	env, fu, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:fwd.test", Action: "forward"}},
		"reject") // 默认 reject：只有「嗅探成功 → 命中 forward」才会走上游，否则连接被拒

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "1.2.3.4", 443)
	if rep != 0x00 {
		t.Fatalf("嗅探路径应先回 success(0x00)，实际 0x%02x", rep)
	}
	hello := makeClientHello(t, "fwd.test")
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	writeInChunks(t, conn, hello, 2, 20*time.Millisecond) // 拆 2 段

	got := make([]byte, len(hello))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败(拆段后嗅探失败→默认 reject 导致链路不通?): %v", err)
	}
	if string(got) != string(hello) {
		t.Fatal("回显与发送首包不一致，回放/中继有误")
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1（拆段 ClientHello 应仍嗅出 fwd.test 并 forward）", fu.count.Load())
	}
}

// TestSniff_SNI_Forward_ManySegments 进一步压测循环读的累积/顺序：
// ClientHello 拆成 5 个小段到达，断言仍能嗅出 SNI 且首包按序完整回放。
func TestSniff_SNI_Forward_ManySegments(t *testing.T) {
	env, fu, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:fwd.test", Action: "forward"}},
		"reject")

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "1.2.3.4", 443)
	if rep != 0x00 {
		t.Fatalf("嗅探路径应先回 success(0x00)，实际 0x%02x", rep)
	}
	hello := makeClientHello(t, "fwd.test")
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	writeInChunks(t, conn, hello, 5, 15*time.Millisecond) // 拆 5 段，间隔合计 ~60ms < 300ms 预算

	got := make([]byte, len(hello))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败: %v", err)
	}
	if string(got) != string(hello) {
		t.Fatal("多段回显与发送首包不一致，累积/顺序有误")
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1（5 段 ClientHello 应仍嗅出域名并 forward）", fu.count.Load())
	}
}

// TestSniff_HTTPHost_Forward_Split 回归碎片化 HTTP：
// 请求被拆成「方法名未发全(GE)」+ 剩余两段，循环读必须靠 couldBecomeHTTP 继续等而非误判非 HTTP，
// 直到请求头 CRLFCRLF 收全才嗅出 Host 并命中 forward。
func TestSniff_HTTPHost_Forward_Split(t *testing.T) {
	env, fu, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:h.test", Action: "forward"}},
		"reject")

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "5.6.7.8", 80)
	if rep != 0x00 {
		t.Fatalf("期望 success(0x00)，实际 0x%02x", rep)
	}
	req := []byte("GET / HTTP/1.1\r\nHost: h.test\r\nConnection: close\r\n\r\n")
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	// 故意让首段只含 "GE"（方法名未发全），其余分两段；验证不会被误判为非 HTTP 提前收手。
	if _, err := conn.Write(req[:2]); err != nil {
		t.Fatalf("写首段失败: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	writeInChunks(t, conn, req[2:], 2, 20*time.Millisecond)

	got := make([]byte, len(req))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回显失败(碎片 HTTP 嗅探失败→默认 reject?): %v", err)
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1（碎片 HTTP 应仍嗅出 h.test 并 forward）", fu.count.Load())
	}
}
