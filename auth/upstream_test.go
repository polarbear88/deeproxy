package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

// b64 是测试辅助函数：把明文编码为 base64 用户名。
func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// TestDecodeUpstream_Valid 覆盖 AC1：各类合法用户名都能正确解码。
func TestDecodeUpstream_Valid(t *testing.T) {
	cases := []struct {
		name  string
		plain string
		want  Upstream
	}{
		{"IPv4主机", "u:p@127.0.0.1:1080", Upstream{"127.0.0.1", 1080, "u", "p"}},
		{"域名主机", "user:pwd@aa.com:888", Upstream{"aa.com", 888, "user", "pwd"}},
		{"IPv6主机", "u:p@[::1]:888", Upstream{"::1", 888, "u", "p"}},
		{"空密码", "u:@host.com:80", Upstream{"host.com", 80, "u", ""}},
		{"空用户名(上游免认证)", ":@host.com:80", Upstream{"host.com", 80, "", ""}},
		{"密码含冒号", "u:p:a:ss@host.com:443", Upstream{"host.com", 443, "u", "p:a:ss"}},
		{"用户名含@(LastIndex切分)", "u@x:p@host.com:443", Upstream{"host.com", 443, "u@x", "p"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DecodeUpstream(b64(c.plain))
			if err != nil {
				t.Fatalf("DecodeUpstream(%q) 意外报错: %v", c.plain, err)
			}
			if got != c.want {
				t.Fatalf("DecodeUpstream(%q) = %+v, 期望 %+v", c.plain, got, c.want)
			}
		})
	}
}

// TestDecodeUpstream_Invalid 覆盖 AC2/R10：非法输入必须返回 error。
func TestDecodeUpstream_Invalid(t *testing.T) {
	cases := []struct {
		name     string
		username string
	}{
		{"空串", ""},
		{"非法base64", "@@@not-base64@@@"},
		{"缺@分隔符", b64("u:p_host.com:80")},
		{"凭据缺冒号", b64("userpwd@host.com:80")},
		{"端口非数字", b64("u:p@host.com:abc")},
		{"端口越界", b64("u:p@host.com:70000")},
		{"端口为0", b64("u:p@host.com:0")},
		{"主机为空", b64("u:p@:80")},
		{"缺端口", b64("u:p@host.com")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DecodeUpstream(c.username); err == nil {
				t.Fatalf("DecodeUpstream(%q) 期望报错，但成功了", c.username)
			}
		})
	}
}

// TestDecodeUpstream_TooLong 覆盖 R10：编码后用户名超过 255 字节应被拒绝。
func TestDecodeUpstream_TooLong(t *testing.T) {
	long := b64("u:p@" + strings.Repeat("a", 300) + ".com:80")
	if len(long) <= maxUsernameLen {
		t.Fatalf("测试用例构造失败：长度 %d 未超过上限", len(long))
	}
	if _, err := DecodeUpstream(long); err == nil {
		t.Fatal("超长用户名期望报错，但成功了")
	}
}

// TestEncodeDecodeRoundTrip 覆盖 AC1：编解码往返一致。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []Upstream{
		{"127.0.0.1", 1080, "u", "p"},
		{"proxy.example.com", 8443, "alice", "secret"},
		{"::1", 1080, "", ""},
	}
	for _, want := range cases {
		got, err := DecodeUpstream(EncodeUpstream(want))
		if err != nil {
			t.Fatalf("往返解码 %+v 报错: %v", want, err)
		}
		if got != want {
			t.Fatalf("往返结果 = %+v, 期望 %+v", got, want)
		}
	}
}

// TestUpstreamAddr 覆盖 AC1：Addr() 正确拼接，IPv6 加方括号。
func TestUpstreamAddr(t *testing.T) {
	if got := (Upstream{Host: "a.com", Port: 80}).Addr(); got != "a.com:80" {
		t.Fatalf("Addr() = %q, 期望 a.com:80", got)
	}
	if got := (Upstream{Host: "::1", Port: 80}).Addr(); got != "[::1]:80" {
		t.Fatalf("Addr() = %q, 期望 [::1]:80", got)
	}
}

// 说明：v1 的 TestCredentialValid（整段用户名 = base64 上游）已随 v2 用户名契约
// 重写而移除——v2 用户名为 user-group[-尾段]，Credential.Valid 需读快照做 bcrypt
// 鉴权。新的 Credential/Authenticate 测试见 credential_test.go。
// DecodeUpstream/EncodeUpstream 仍保留并由 Type A 尾段复用，其用例见本文件其余部分。
