package netutil

import (
	"net"
	"testing"
)

// TestDetectLocalIP 验证探测结果（若有）为合法的非回环 IPv4；无网卡环境允许返回空串。
func TestDetectLocalIP(t *testing.T) {
	got := DetectLocalIP()
	if got == "" {
		t.Skip("当前环境无非回环 IPv4 地址，跳过（探测允许返回空串由用户手填）")
	}
	ip := net.ParseIP(got)
	if ip == nil {
		t.Fatalf("探测结果应为合法 IP，得到 %q", got)
	}
	if ip.To4() == nil {
		t.Fatalf("探测结果应为 IPv4，得到 %q", got)
	}
	if ip.IsLoopback() {
		t.Fatalf("探测结果不应为回环地址，得到 %q", got)
	}
	if ip.IsLinkLocalUnicast() {
		t.Fatalf("探测结果不应为链路本地地址，得到 %q", got)
	}
}
