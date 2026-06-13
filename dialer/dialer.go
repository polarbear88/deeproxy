// Package dialer 提供两种出站拨号方式，供 SOCKS5 服务的拨号 hook 使用：
//   - DialDirect：本机直连目标（direct 动作）；
//   - DialUpstream：经“本连接动态上游 SOCKS5 代理”拨号到目标（forward 动作）。
//
// 注意：本包只负责“建立到目标的 net.Conn”，真正的双向数据中继由 go-socks5
// 库内部用 io.Copy 完成（含 half-close）。因此本包不做 copy 逻辑。
package dialer

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/proxy"

	"deeproxy/auth"
)

// dialTimeout 是建立出站连接（直连或连上游）的拨号超时。
const dialTimeout = 10 * time.Second

// DialDirect 本机直接 TCP 连接目标地址（direct 动作）。
// addr 形如 "host:port"，host 可为域名或 IP，由本机解析。
func DialDirect(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, "tcp", addr)
}

// DialUpstream 经动态上游 SOCKS5 代理拨号到目标（forward 动作）。
//
// 实现：用 golang.org/x/net/proxy.SOCKS5 构造一个上游 dialer（携带上游认证），
// 再断言为 proxy.ContextDialer 以支持 context 超时/取消。
// addr 可为域名——它会被原样交给上游 SOCKS5 由上游解析，从而避免本机 DNS 泄漏。
func DialUpstream(ctx context.Context, up auth.Upstream, addr string) (net.Conn, error) {
	// 上游用户名为空表示上游免认证，此时传 nil auth。
	var pAuth *proxy.Auth
	if up.User != "" {
		pAuth = &proxy.Auth{User: up.User, Password: up.Pwd}
	}

	// forward 字段传 proxy.Direct：上游 dialer 自身到上游代理的 TCP 连接走本机直连。
	d, err := proxy.SOCKS5("tcp", up.Addr(), pAuth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("构造上游 SOCKS5 拨号器失败 %q: %w", up.Addr(), err)
	}

	// x/net/proxy 的 SOCKS5 dialer 实现了 ContextDialer，优先用带 context 的拨号。
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		// 理论上不会发生；保留兜底以防库行为变化。
		return nil, fmt.Errorf("上游拨号器不支持 ContextDialer")
	}
	return cd.DialContext(ctx, "tcp", addr)
}
