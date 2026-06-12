package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	socks5 "github.com/things-go/go-socks5"

	"deeproxy/config"
	"deeproxy/internal/logging"
	"deeproxy/rule"
)

// ---------- 测试基础设施 ----------

// fakeUpstream 是一个供测试使用的“假上游 SOCKS5 代理”。
// 它记录被请求的目标域名与调用次数，并把所有 CONNECT 一律拨到 realTarget，
// 从而既能验证转发链路（AC5a/AC6），又能验证 deeproxy 未在本地解析域名（AC10a）。
type fakeUpstream struct {
	count      atomic.Int64 // 经过本上游的转发次数
	lastFQDN   atomic.Value // 最近一次收到的目标 FQDN（string）
	realTarget string       // 真实回连目标（httptest 地址）
}

// start 启动假上游，返回监听地址与清理函数。requireAuth 非空时启用用户名/密码校验。
func (f *fakeUpstream) start(t *testing.T, requireAuth map[string]string) string {
	t.Helper()
	opts := []socks5.Option{
		// 假上游也跳过本地 DNS，保留 FQDN 以便断言 deeproxy 透传了域名。
		socks5.WithResolver(nopResolver{}),
		socks5.WithDialAndRequest(func(ctx context.Context, _, _ string, req *socks5.Request) (net.Conn, error) {
			f.count.Add(1)
			if req.DestAddr != nil {
				f.lastFQDN.Store(req.DestAddr.FQDN)
			}
			// 一律拨到真实测试目标（忽略请求里的占位地址）。
			return net.Dial("tcp", f.realTarget)
		}),
	}
	if requireAuth != nil {
		opts = append(opts, socks5.WithCredential(socks5.StaticCredentials(requireAuth)))
	}
	srv := socks5.NewServer(opts...)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("假上游监听失败: %v", err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

// startDeeproxy 用给定规则启动被测 deeproxy 服务，返回其监听地址。
func startDeeproxy(t *testing.T, rules []config.RuleSpec, def rule.Action) string {
	t.Helper()
	cfg := &config.Config{
		Listen:         "127.0.0.1:0",
		DefaultAction:  string(def),
		LogLevel:       "error", // 测试时压低日志噪音
		IdleTimeoutSec: 300,
		Rules:          rules,
	}
	engine, err := rule.NewEngine(rules, def)
	if err != nil {
		t.Fatalf("构建规则引擎失败: %v", err)
	}
	srv := New(cfg, engine, logging.New("error"))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("deeproxy 监听失败: %v", err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

// encUser 生成携带上游信息的 base64 用户名。
func encUser(upUser, upPwd, upAddr string) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s@%s", upUser, upPwd, upAddr))
}

// ---------- 原始 SOCKS5 客户端（用于精确断言回复码） ----------

const (
	atypIPv4   = 0x01
	atypDomain = 0x03
)

// socks5Connect 用原始字节实现一次 SOCKS5 用户名/密码认证 + 指定命令的请求，
// 返回服务端回复中的 REP 码与已建立的连接（成功时可继续收发数据）。
// cmd: 0x01=CONNECT 0x02=BIND 0x03=ASSOCIATE。
func socks5Connect(t *testing.T, proxyAddr, username string, cmd, atyp byte, host string, port int) (byte, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 deeproxy 失败: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// 1) 方法协商：仅提供用户名/密码认证(0x02)。
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("写方法协商失败: %v", err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(conn, rep); err != nil {
		t.Fatalf("读方法回复失败: %v", err)
	}
	if rep[0] != 0x05 || rep[1] != 0x02 {
		t.Fatalf("方法协商未选用户名/密码认证: %v", rep)
	}

	// 2) 用户名/密码认证子协商（RFC 1929）。密码用占位 "x"。
	pwd := "x"
	authMsg := []byte{0x01, byte(len(username))}
	authMsg = append(authMsg, username...)
	authMsg = append(authMsg, byte(len(pwd)))
	authMsg = append(authMsg, pwd...)
	if _, err := conn.Write(authMsg); err != nil {
		t.Fatalf("写认证失败: %v", err)
	}
	authRep := make([]byte, 2)
	if _, err := io.ReadFull(conn, authRep); err != nil {
		t.Fatalf("读认证回复失败: %v", err)
	}
	if authRep[1] != 0x00 {
		// 认证失败：返回一个特殊标记码 0xFF，调用方据此判断。
		_ = conn.Close()
		return 0xFF, nil
	}

	// 3) 发送请求（CONNECT/BIND/ASSOCIATE）。
	msg := []byte{0x05, cmd, 0x00, atyp}
	switch atyp {
	case atypIPv4:
		ip := net.ParseIP(host).To4()
		msg = append(msg, ip...)
	case atypDomain:
		msg = append(msg, byte(len(host)))
		msg = append(msg, host...)
	}
	msg = append(msg, byte(port>>8), byte(port&0xff))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("写请求失败: %v", err)
	}

	// 4) 读回复头：VER REP RSV ATYP ...
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("读回复头失败: %v", err)
	}
	// 读掉 BND.ADDR + BND.PORT，使连接进入可收发数据状态。
	switch head[3] {
	case atypIPv4:
		io.ReadFull(conn, make([]byte, 4+2))
	case atypDomain:
		l := make([]byte, 1)
		io.ReadFull(conn, l)
		io.ReadFull(conn, make([]byte, int(l[0])+2))
	default: // IPv6
		io.ReadFull(conn, make([]byte, 16+2))
	}
	// 清除收发阶段的截止时间（成功路径后续要做 HTTP）。
	_ = conn.SetDeadline(time.Time{})
	return head[1], conn // head[1] 即 REP 码
}

// httpGetOver 在已建立的 SOCKS5 连接上发起一次 HTTP GET，返回响应体。
func httpGetOver(t *testing.T, conn net.Conn, host string) string {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("写 HTTP 请求失败: %v", err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("读 HTTP 响应失败: %v", err)
	}
	return string(data)
}

// ---------- 测试用例 ----------

// newTarget 启动一个返回固定 body 的 httptest 目标服务器。
func newTarget(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "HELLO-DEEPROXY")
	}))
	t.Cleanup(ts.Close)
	return ts, ts.Listener.Addr().String()
}

// TestForwardWithUpstreamAuth 覆盖 AC5a + AC10a：
// 规则 forward 时经上游成功取到响应；上游计数+1；上游收到的是域名（证明 deeproxy 未本地解析）。
func TestForwardWithUpstreamAuth(t *testing.T) {
	_, targetAddr := newTarget(t)
	fu := &fakeUpstream{realTarget: targetAddr}
	upAddr := fu.start(t, map[string]string{"uu": "pp"})

	proxyAddr := startDeeproxy(t,
		[]config.RuleSpec{{Match: "domain-suffix:forward.test", Action: "forward"}},
		rule.ActionReject) // 默认 reject，确保命中的是 forward 规则

	user := encUser("uu", "pp", upAddr)
	rep, conn := socks5Connect(t, proxyAddr, user, 0x01, atypDomain, "x.forward.test", 1234)
	if rep != 0x00 {
		t.Fatalf("forward 期望 REP=0x00，实际 0x%02x", rep)
	}
	body := httpGetOver(t, conn, "x.forward.test")
	if !contains(body, "HELLO-DEEPROXY") {
		t.Fatalf("forward 响应体不含目标内容: %q", body)
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数 = %d, 期望 1", fu.count.Load())
	}
	// AC10a：上游收到的是域名 FQDN，说明 deeproxy 透传了域名、未本地解析。
	if got, _ := fu.lastFQDN.Load().(string); got != "x.forward.test" {
		t.Fatalf("上游收到的 FQDN = %q, 期望 x.forward.test（证明无本地 DNS）", got)
	}
}

// TestForwardBadUpstreamAuth 覆盖 AC5a 反例：上游凭据错误导致 forward 失败。
func TestForwardBadUpstreamAuth(t *testing.T) {
	_, targetAddr := newTarget(t)
	fu := &fakeUpstream{realTarget: targetAddr}
	upAddr := fu.start(t, map[string]string{"uu": "pp"})

	proxyAddr := startDeeproxy(t,
		[]config.RuleSpec{{Match: "domain-suffix:forward.test", Action: "forward"}},
		rule.ActionReject)

	user := encUser("uu", "WRONG", upAddr) // 错误的上游密码
	rep, conn := socks5Connect(t, proxyAddr, user, 0x01, atypDomain, "x.forward.test", 1234)
	if conn != nil {
		_ = conn.Close()
	}
	if rep == 0x00 {
		t.Fatalf("上游认证错误时 forward 不应成功（REP=0x00）")
	}
}

// TestDirect 覆盖 AC6：规则 direct 时本机直连目标，上游计数不变。
func TestDirect(t *testing.T) {
	_, targetAddr := newTarget(t)
	fu := &fakeUpstream{realTarget: targetAddr}
	upAddr := fu.start(t, nil)

	host, portStr, _ := net.SplitHostPort(targetAddr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	proxyAddr := startDeeproxy(t,
		[]config.RuleSpec{{Match: "ip-cidr:127.0.0.0/8", Action: "direct"}},
		rule.ActionReject)

	user := encUser("uu", "pp", upAddr)
	rep, conn := socks5Connect(t, proxyAddr, user, 0x01, atypIPv4, host, port)
	if rep != 0x00 {
		t.Fatalf("direct 期望 REP=0x00，实际 0x%02x", rep)
	}
	body := httpGetOver(t, conn, host)
	if !contains(body, "HELLO-DEEPROXY") {
		t.Fatalf("direct 响应体不含目标内容: %q", body)
	}
	if fu.count.Load() != 0 {
		t.Fatalf("direct 不应经过上游，计数 = %d", fu.count.Load())
	}
}

// TestReject 覆盖 AC7：规则 reject 时客户端收到 REP=0x02 (RepRuleFailure)。
func TestReject(t *testing.T) {
	proxyAddr := startDeeproxy(t,
		[]config.RuleSpec{{Match: "domain-suffix:reject.test", Action: "reject"}},
		rule.ActionForward)

	user := encUser("uu", "pp", "127.0.0.1:1") // 上游地址无所谓，reject 在拨号前
	rep, conn := socks5Connect(t, proxyAddr, user, 0x01, atypDomain, "x.reject.test", 80)
	if conn != nil {
		_ = conn.Close()
	}
	if rep != 0x02 {
		t.Fatalf("reject 期望 REP=0x02 (RuleFailure)，实际 0x%02x", rep)
	}
}

// TestUpstreamUnreachable 覆盖 AC5b：上游不可达时回复非成功、非 0x02，且无 goroutine 泄漏。
func TestUpstreamUnreachable(t *testing.T) {
	proxyAddr := startDeeproxy(t,
		[]config.RuleSpec{{Match: "domain-suffix:forward.test", Action: "forward"}},
		rule.ActionReject)

	before := runtime.NumGoroutine()

	// 上游指向一个几乎肯定关闭的端口。
	user := encUser("uu", "pp", "127.0.0.1:1")
	rep, conn := socks5Connect(t, proxyAddr, user, 0x01, atypDomain, "x.forward.test", 80)
	if conn != nil {
		_ = conn.Close()
	}
	// 上游不可达应映射为 0x03/0x04/0x05，绝不能是成功(0x00)或策略拒绝(0x02)。
	if rep == 0x00 || rep == 0x02 {
		t.Fatalf("上游不可达期望网络类错误码，实际 0x%02x", rep)
	}

	// goroutine 数应回落到基线附近（无泄漏）。
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("疑似 goroutine 泄漏：before=%d after=%d", before, after)
	}
}

// TestNonConnectCommands 覆盖 AC3：BIND 与 ASSOCIATE 均被拒（REP=0x02）。
// 由于 go-socks5 在命令分发前先调用 Allow（已由源码确认），Allow 返回 false
// 会在 handleAssociate 创建 UDP 监听之前就回 RepRuleFailure，从而不会开启 UDP 端口。
func TestNonConnectCommands(t *testing.T) {
	proxyAddr := startDeeproxy(t,
		[]config.RuleSpec{{Match: "domain-suffix:any.test", Action: "forward"}},
		rule.ActionForward)

	user := encUser("uu", "pp", "127.0.0.1:1")
	for _, cmd := range []byte{0x02 /*BIND*/, 0x03 /*ASSOCIATE*/} {
		rep, conn := socks5Connect(t, proxyAddr, user, cmd, atypDomain, "x.any.test", 80)
		if conn != nil {
			_ = conn.Close()
		}
		if rep != 0x02 {
			t.Fatalf("命令 0x%02x 期望 REP=0x02，实际 0x%02x", cmd, rep)
		}
	}
}

// contains 是不依赖 strings 包的简单子串判断（保持测试自包含）。
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
