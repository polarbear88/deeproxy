package detect

import (
	"encoding/binary"
	"testing"
)

// makeTLSRecord 造一个「记录头声明 bodyLen 字节、实际给 have 字节 body」的 TLS 记录头部+片段，
// 用于精确构造「完整 / 差一字节」的边界用例（不依赖 body 内容能否解析）。
func makeTLSRecord(bodyLen, have int) []byte {
	rec := make([]byte, 5+have)
	rec[0] = tlsRecordHandshake // 0x16
	rec[1], rec[2] = 0x03, 0x01 // version
	binary.BigEndian.PutUint16(rec[3:5], uint16(bodyLen))
	// body 用 0 填充即可，FirstPacketComplete 只看长度不看内容。
	return rec
}

// TestFirstPacketComplete 覆盖「读够了吗」门控在各边界下的判定。
func TestFirstPacketComplete(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"空", nil, false},
		{"空切片", []byte{}, false},
		// —— TLS ——
		{"TLS仅类型字节", []byte{0x16}, false},
		{"TLS记录头未集齐", []byte{0x16, 0x03, 0x01}, false},
		{"TLS记录头齐body为0声明", makeTLSRecord(0, 0), true},
		{"TLS完整记录", makeTLSRecord(10, 10), true},
		{"TLS记录差一字节", makeTLSRecord(10, 9), false},
		{"TLS记录多余字节也算齐", makeTLSRecord(4, 10), true},
		// —— HTTP ——
		{"HTTP头收全", []byte("GET / HTTP/1.1\r\nHost: h.test\r\n\r\n"), true},
		{"HTTP头未收全", []byte("GET / HTTP/1.1\r\nHost: h.test"), false},
		{"HTTP仅方法+空格", []byte("GET "), false},
		{"HTTP方法前缀GE", []byte("GE"), false},
		{"HTTP方法前缀POS", []byte("POS"), false},
		{"HTTP方法前缀OPTION", []byte("OPTION"), false},
		// —— 非可嗅探 ——
		{"非协议二进制", []byte{0x00, 0x01, 0x02, 0x03}, true},
		{"非方法字母开头", []byte("XYZ abc"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FirstPacketComplete(c.data); got != c.want {
				t.Fatalf("FirstPacketComplete(%q) = %v, 期望 %v", c.data, got, c.want)
			}
		})
	}
}

// TestFirstPacketComplete_RealHello 用真实 ClientHello 验证：完整时 true、任意截断时 false。
// 这正是修复要保证的：循环读必须读到「完整记录」才停手。
func TestFirstPacketComplete_RealHello(t *testing.T) {
	hello := captureClientHello(t, "example.com")
	if !FirstPacketComplete(hello) {
		t.Fatal("完整 ClientHello 应判定为已读够(true)")
	}
	// 任意非满长度的截断都应判「还要再读」(false)，否则会过早交给 SNI 解析而失败。
	for _, n := range []int{1, 4, 5, 10, len(hello) / 2, len(hello) - 1} {
		if FirstPacketComplete(hello[:n]) {
			t.Fatalf("截断到 %d 字节(共 %d)不应判为读够", n, len(hello))
		}
	}
}

// TestCouldBecomeHTTP 单独覆盖方法前缀判据：严格前缀→true，完整/非前缀→false。
func TestCouldBecomeHTTP(t *testing.T) {
	cases := []struct {
		data string
		want bool
	}{
		{"", false},
		{"G", true},
		{"GE", true},
		{"GET", true},
		{"GET ", false}, // 已是完整 token，不算「还没发全」
		{"POS", true},
		{"CONNEC", true},
		{"X", false},
		{"HELLO", false},
	}
	for _, c := range cases {
		if got := couldBecomeHTTP([]byte(c.data)); got != c.want {
			t.Errorf("couldBecomeHTTP(%q) = %v, 期望 %v", c.data, got, c.want)
		}
	}
}
