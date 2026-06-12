package detect

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// captureClientHello 用 crypto/tls 生成一个真实的 ClientHello 字节流，
// 通过 net.Pipe 捕获客户端写出的第一段数据（即 ClientHello 记录）。
func captureClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		cfg := &tls.Config{ServerName: serverName, InsecureSkipVerify: true}
		// Handshake 会先写出 ClientHello，然后阻塞等待 ServerHello。
		_ = tls.Client(c1, cfg).Handshake()
	}()

	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := c2.Read(buf)
	if err != nil {
		t.Fatalf("捕获 ClientHello 失败: %v", err)
	}
	return buf[:n]
}

// TestSNI_Valid 覆盖：真实 ClientHello 能解析出 SNI。
func TestSNI_Valid(t *testing.T) {
	for _, name := range []string{"example.com", "a.b.c.block.test", "xn--fiqs8s.example"} {
		hello := captureClientHello(t, name)
		got, ok := SNI(hello)
		if !ok || got != name {
			t.Fatalf("SNI() = (%q,%v), 期望 (%q,true)", got, ok, name)
		}
	}
}

// TestSNI_NoServerName 覆盖：ServerName 为空时 crypto/tls 不发 SNI，应返回 false。
func TestSNI_NoServerName(t *testing.T) {
	// ServerName 为空 → 无 SNI 扩展。
	hello := captureClientHello(t, "")
	if got, ok := SNI(hello); ok {
		t.Fatalf("无 SNI 时应返回 false，却得到 %q", got)
	}
}

// TestSNI_Truncated 覆盖：截断的 ClientHello 不 panic 且返回 false。
func TestSNI_Truncated(t *testing.T) {
	hello := captureClientHello(t, "example.com")
	for _, n := range []int{0, 1, 5, 10, 20, len(hello) / 2, len(hello) - 1} {
		if got, ok := SNI(hello[:n]); ok {
			t.Fatalf("截断到 %d 字节仍解析出 %q，期望 false", n, got)
		}
	}
}

// TestSNI_Malformed 覆盖：畸形/恶意输入安全返回 false。
func TestSNI_Malformed(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x16},                               // 只有记录类型
		{0x16, 0x03, 0x01, 0xff, 0xff},       // 声明超长但无 body
		{0x17, 0x03, 0x01, 0x00, 0x00},       // 非 handshake 记录类型
		{0x16, 0x03, 0x01, 0x00, 0x01, 0x02}, // body 太短不含握手头
	}
	for i, c := range cases {
		if got, ok := SNI(c); ok {
			t.Fatalf("用例 %d 期望 false，却得到 %q", i, got)
		}
	}
}

// TestHTTPHost 覆盖：HTTP 首包解析 Host（去端口）。
func TestHTTPHost(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"基本GET", "GET / HTTP/1.1\r\nHost: h.test\r\n\r\n", "h.test", true},
		{"带端口", "GET / HTTP/1.1\r\nHost: h.test:8080\r\n\r\n", "h.test", true},
		{"大小写头", "POST /x HTTP/1.1\r\nhOsT:  up.test \r\n\r\n", "up.test", true},
		{"非HTTP", "\x16\x03\x01...", "", false},
		{"无Host", "GET / HTTP/1.1\r\nUser-Agent: x\r\n\r\n", "", false},
		{"空", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := HTTPHost([]byte(c.raw))
			if ok != c.ok || got != c.want {
				t.Fatalf("HTTPHost() = (%q,%v), 期望 (%q,%v)", got, ok, c.want, c.ok)
			}
		})
	}
}

// TestSniff_Dispatch 覆盖：Sniff 按首字节分流到 SNI 或 Host。
func TestSniff_Dispatch(t *testing.T) {
	hello := captureClientHello(t, "tls.test")
	if got, ok := Sniff(hello); !ok || got != "tls.test" {
		t.Fatalf("Sniff(TLS) = (%q,%v), 期望 tls.test", got, ok)
	}
	if got, ok := Sniff([]byte("GET / HTTP/1.1\r\nHost: http.test\r\n\r\n")); !ok || got != "http.test" {
		t.Fatalf("Sniff(HTTP) = (%q,%v), 期望 http.test", got, ok)
	}
	if _, ok := Sniff([]byte{0x00, 0x01, 0x02, 0x03}); ok {
		t.Fatal("Sniff(未知) 应返回 false")
	}
	if _, ok := Sniff(nil); ok {
		t.Fatal("Sniff(nil) 应返回 false")
	}
}
