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

// tcpUserTimeoutMs 是「死连接清理上限」的毫秒值（90s），由 ControlTCPUserTimeout 在三接入点的
// 真实 TCP socket 上设置。各平台用各自等价的 socket 选项实现【同一语义】（见 tcpopt_*.go）：
//   - Linux：  TCP_USER_TIMEOUT   （毫秒）→ 直接用本常量 tcpUserTimeoutMs；
//   - Windows：TCP_MAXRTMS        （毫秒）→ 直接用本常量 tcpUserTimeoutMs；
//   - darwin： TCP_RXT_CONNDROPTIME（秒） → 用派生常量 tcpUserTimeoutSec（= ms/1000）；
//   - 其余平台：空实现降级，仅靠 keepalive 75s（见 tcpopt_other.go）。
// 三者语义不逐字节等价（USER_TIMEOUT=未确认数据计时器，TCP_MAXRT/RXT_CONNDROPTIME=重传放弃计时器），
// 但对「≤90s 回收彻底 ACK 停滞的死连接」这一目标足够接近，且都不触及 relay 热路径（仅 socket 选项）。
//
// 语义不同于 keepalive：USER_TIMEOUT 限定「连接上有未确认在途数据」时的最长存活上限，
// 覆盖 keepalive 在发送缓冲尚有未确认数据时被抑制（不发探测）的窗口；keepalive 则仅在
// 连接【空闲且无未确认数据】时才探测死连接。两者并存、互补，共同把死连接清理压在 AC「≤90s」内。
//
// ── 一条连接上的四层超时真值表（各层作用相位不重叠，故不互相替代）──
//   握手 10s（server/lifecycle.go handshakeTimeout，仅 SOCKS5 握手期的读超时）
//     → keepalive 75s（KeepAliveConfig：连接【空闲且无未确认数据】时探测死连接，30 + 15×3）
//       → USER_TIMEOUT 90s（本常量：连接【有未确认在途数据】时上限化其存活，补 keepalive 被抑制的盲区）
//         → idleConn 300s（dialer/idleconn.go：双向【无任何数据移动】超时）
//
// 为何 idleConn(300s) 不能覆盖 USER_TIMEOUT 这一层：idleConn.touch()（idleconn.go）在每次成功
// Read/Write 时把 300s 读截止向后滚动——它按「数据是否移动」keyed，不按「ACK 是否存活」keyed。
// 下载场景中（服务端→客户端仍在发数据）会不断重置该 300s 截止，因此无法检出「客户端已死、但服务端
// 仍在向其发送、数据全卡在未确认（无 ACK 回来）」的情形；这恰是 USER_TIMEOUT 90s 负责兜底的盲区。
//
// 【已接受的权衡】三接入点（listener / DialDirect / DialUpstream.fwd）全量接入 USER_TIMEOUT 90s：
// 收益是任何「整条 TCP 彻底 ACK 停滞」的死连接 ≤90s 被内核重置回收；代价是「极端弱网下、连续 90s
// 零 ACK 进展的活跃连接」可能被误断。判断：90s 零 ACK 进展对健康连接（含移动端切网/拥塞）在实践中
// 罕见，绝大多数停滞秒级恢复；forward 隧道里的长轮询/WebSocket 只要 TCP 层仍有 ACK 心跳即不触发，
// 仅在底层 TCP 彻底停滞 90s 时重置——此时连接事实上已不可用。90s 已是 AC 上限，不再加配置项
// （避免 net-new config + 后台 UI 面）。若未来出现误断投诉，再做成 dead_conn_timeout_sec 可配置（Follow-ups）。
const tcpUserTimeoutMs = 90000

// tcpUserTimeoutSec 是 tcpUserTimeoutMs 的秒值派生（90s），供 darwin 的 TCP_RXT_CONNDROPTIME 使用
// （该选项单位是秒，而非毫秒）。以毫秒常量为单一事实源派生，确保三平台「≤90s」口径一致、不写两份魔数。
const tcpUserTimeoutSec = tcpUserTimeoutMs / 1000

// DialDirect 本机直接 TCP 连接目标地址（direct 动作）。
// addr 形如 "host:port"，host 可为域名或 IP，由本机解析。
func DialDirect(ctx context.Context, addr string) (net.Conn, error) {
	// 直连目标 TCP 连接启用 keepalive：覆盖 idle_timeout_sec=0（WrapIdle 此时返裸 conn、无读超时）
	// 配置下死连接无人探测的空档，并提供 <300s 的更快探测（KeepAlive:-1 见 KeepAliveConfig 注释）。
	// Control: ControlTCPUserTimeout —— 在本机→直连目标这一跳 TCP 上加死连接清理上限 90s（各平台原生实现，
	// 详见 tcpUserTimeoutMs 的平台真值表注释 / tcpopt_*.go），与三接入点共用同一辅助函数（DRY）。
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: -1, KeepAliveConfig: KeepAliveConfig, Control: ControlTCPUserTimeout}
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
	// Control: ControlTCPUserTimeout —— 在本机→上游 SOCKS5 这一跳 TCP 上加死连接清理上限 90s（各平台原生实现，
	// 详见 tcpUserTimeoutMs 的平台真值表注释 / tcpopt_*.go）；仅作用于这跳真实 TCP，不触及隧道内被中继的
	// SOCKS 载荷流（与三接入点共用同一辅助函数，DRY）。
	fwd := &net.Dialer{Timeout: dialTimeout, KeepAlive: -1, KeepAliveConfig: KeepAliveConfig, Control: ControlTCPUserTimeout}
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
