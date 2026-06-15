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

// KeepAliveConfig 是「TCP 检活」语义的单一事实源（DRY），被三处共用：
// server/lifecycle.go 的客户端监听器、本包 DialDirect、本包 DialUpstream 的 forward dialer。
//
// 为什么集中在此（无 import cycle）：server 已 import 本包（dialer），而本包仅依赖 auth + stdlib，
// 反向不依赖 server；故 lifecycle.go 引用 dialer.KeepAliveConfig 不会成环，无需新建叶子包。
// 为什么必须集中：30/15/3 是检活探测周期的唯一定义，若客户端侧与上游侧各写一份会导致
// 探测时窗漂移、AC 失配；集中后三处周期严格一致。
//
// 为什么用 KeepAliveConfig 而非裸 net.Dialer.KeepAlive(duration)：裸 KeepAlive 只设 Idle，
// Interval/Count 走 Go 默认（15s × 9）→ 探测耗时 30 + 15×9 = 165s，超出 AC「死连接 ≤90s 清理」窗；
// 显式 Idle=30/Interval=15/Count=3 → 30 + 15×3 = 75s，落在窗口内。go.mod 为 go 1.26.4，本类型可用。
//
// 使用方须同时设 KeepAlive:-1：当 KeepAliveConfig.Enable=true 时裸 KeepAlive 被忽略，
// 显式置 -1 仅为消除歧义、明确「完全交由 KeepAliveConfig 决定」。
var KeepAliveConfig = net.KeepAliveConfig{
	Enable:   true,
	Idle:     30 * time.Second,
	Interval: 15 * time.Second,
	Count:    3,
}

// DialDirect 本机直接 TCP 连接目标地址（direct 动作）。
// addr 形如 "host:port"，host 可为域名或 IP，由本机解析。
func DialDirect(ctx context.Context, addr string) (net.Conn, error) {
	// 直连目标 TCP 连接启用 keepalive：覆盖 idle_timeout_sec=0（WrapIdle 此时返裸 conn、无读超时）
	// 配置下死连接无人探测的空档，并提供 <300s 的更快探测（KeepAlive:-1 见 KeepAliveConfig 注释）。
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: -1, KeepAliveConfig: KeepAliveConfig}
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

	// forward dialer 决定「本机 → 上游 SOCKS5 代理」这一跳 TCP 连接如何建立。
	// 把原 proxy.Direct 换成带 keepalive 的 &net.Dialer：使该 TCP 连接启用检活探测。
	// 为什么必须从构造层注入而非事后断言：DialUpstream 返回的是 x/net/proxy 包装的 SOCKS5 conn，
	// 对其断言 *net.TCPConn 必失败，事后 SetKeepAliveConfig 形同虚设；故在拨号器构造点注入。
	// 定位说明：客户端 keepalive（lifecycle.go）才是连接泄漏的真正修复（死客户端读永久阻塞 io.Copy）；
	// 上游 keepalive 仅覆盖 idle_timeout_sec=0 的空档 + 提供 <300s 的更快上游探测，非泄漏主修复。
	// 已验证 *net.Dialer 满足 proxy.Dialer/ContextDialer 接口。
	fwd := &net.Dialer{Timeout: dialTimeout, KeepAlive: -1, KeepAliveConfig: KeepAliveConfig}
	d, err := proxy.SOCKS5("tcp", up.Addr(), pAuth, fwd)
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
