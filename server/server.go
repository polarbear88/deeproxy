// Package server 负责把各组件装配成一个 go-socks5 服务，并实现核心转发逻辑。
//
// 架构（两阶段，顺应 go-socks5 设计）：
//  1. WithRule.Allow（策略阶段，在拨号之前）：
//     - 非 CONNECT 命令 / 上游解码失败 → false，库回 RepRuleFailure(0x02)；
//     - 目标是域名(FQDN) → 按域名规则；命中 reject → false(0x02)；forward/direct 存 ctx；
//     - 目标是 IP → 按 ip-cidr 规则；命中 reject → false(0x02)；forward/direct 存 ctx；
//     未命中 ip-cidr 且启用嗅探 → 标记 needsSniff 存 ctx（放行，待 ConnectHandle 嗅探）。
//  2. WithConnectHandle（接管 CONNECT，自己回复+中继）：
//     - 已知 forward/direct → 先拨号(失败回网络错误码)，再回 success，再双向中继；
//     - needsSniff → 先回 success（SOCKS5 要求服务端先应答客户端才发 ClientHello），
//     peek 客户端首包嗅探 SNI/Host 得域名 → 域名规则 → 否则默认动作 → 拨号 → 中继。
//
// 之所以用 ConnectHandle 而非 WithDialAndRequest：嗅探必须在“回复 success 之后、
// 数据中继之前”读取客户端首包，这要求完全接管 connect 流程。
//
// 同时注入 nopResolver 跳过库的本地 DNS，使域名目标原样透传、避免 DNS 泄漏。
package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"time"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	"deeproxy/auth"
	"deeproxy/config"
	"deeproxy/detect"
	"deeproxy/dialer"
	"deeproxy/rule"
)

// nopResolver 实现 socks5.NameResolver，但不做任何实际 DNS 解析。
// 返回 nil IP 后，库的 AddrSpec.String() 会回退为 "FQDN:port"，
// 域名因此原样透传到 ConnectHandle，再交给上游 SOCKS5 解析（避免本机 DNS 泄漏）。
type nopResolver struct{}

func (nopResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

// connectRule 实现 socks5.RuleSet，承担“策略判定”职责（命令过滤 + 选路预判）。
type connectRule struct {
	engine  *rule.Engine
	logger  *slog.Logger
	sniffOn bool
}

// Allow 在 ConnectHandle 之前被库调用，完成命令过滤、上游解码与规则预判，
// 并把放行类判定通过 context 传给 ConnectHandle。
func (r *connectRule) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	// ① 命令过滤：仅允许 CONNECT(TCP)。BIND/UDP ASSOCIATE 一律拒绝（回 0x02）。
	if req.Command != statute.CommandConnect {
		r.logger.Warn("拒绝非 CONNECT 命令", "command", req.Command, "from", req.RemoteAddr)
		return ctx, false
	}

	// ② 解码上游：从认证阶段保存的用户名解码出本连接动态上游。
	username := ""
	if req.AuthContext != nil {
		username = req.AuthContext.Payload["username"]
	}
	up, err := auth.DecodeUpstream(username)
	if err != nil {
		r.logger.Warn("上游解码失败，拒绝连接", "err", err, "from", req.RemoteAddr)
		return ctx, false
	}

	// ③ 目标探测：读取请求中的目标主机（域名或 IP）。
	host := targetHost(req)
	isIP := req.DestAddr != nil && req.DestAddr.FQDN == "" && len(req.DestAddr.IP) != 0

	// ④ 规则预判。
	action, matched := r.engine.MatchRule(host)

	// IP 目标且未命中 ip-cidr 规则、且启用嗅探：放行，待 ConnectHandle 嗅探域名再决定。
	if isIP && !matched && r.sniffOn {
		d := decision{upstream: up, host: host, needsSniff: true}
		return context.WithValue(ctx, decisionKey, d), true
	}

	// 其余情况按已知动作处理。
	switch action {
	case rule.ActionReject:
		r.logger.Info("规则拒绝", "host", host, "action", "reject")
		return ctx, false
	case rule.ActionForward, rule.ActionDirect:
		d := decision{action: action, upstream: up, host: host}
		return context.WithValue(ctx, decisionKey, d), true
	default:
		r.logger.Warn("未知动作，拒绝", "host", host, "action", action)
		return ctx, false
	}
}

// targetHost 从请求中取出纯目标主机：优先 FQDN（域名），否则用 IP 字面量。
func targetHost(req *socks5.Request) string {
	if req.DestAddr != nil {
		if req.DestAddr.FQDN != "" {
			return req.DestAddr.FQDN
		}
		if len(req.DestAddr.IP) != 0 {
			return req.DestAddr.IP.String()
		}
	}
	return ""
}

// handler 持有运行期依赖，实现 connect 处理。
type handler struct {
	engine       *rule.Engine
	logger       *slog.Logger
	idle         time.Duration
	sniffTimeout time.Duration
}

// connectHandle 接管 CONNECT：根据 Allow 阶段的判定执行拨号、嗅探、回复与中继。
func (h *handler) connectHandle(ctx context.Context, writer io.Writer, req *socks5.Request) error {
	d, ok := ctx.Value(decisionKey).(decision)
	if !ok {
		// 正常路径一定经过 Allow；缺判定说明流程异常。
		_ = socks5.SendReply(writer, statute.RepServerFailure, nil)
		return errNoDecision
	}

	// 实际拨号目标始终用客户端给的原始地址（嗅探到的域名只用于选路，不改变拨号目标，
	// 以免上游把域名重解析到与客户端不同的 IP）。
	target := req.DestAddr.String()

	if d.needsSniff {
		return h.handleSniff(ctx, writer, req, d, target)
	}
	return h.dialAndRelay(ctx, writer, req, d.action, d.upstream, d.host, target)
}

// handleSniff 处理“IP 目标未命中 ip-cidr”的嗅探路径：
// 先回 success（客户端据此才会发出 ClientHello/HTTP 首包），peek 首包嗅探域名，
// 用域名规则（否则默认动作）选路后拨号，并把已读的首包回放给上游。
func (h *handler) handleSniff(ctx context.Context, writer io.Writer, req *socks5.Request, d decision, target string) error {
	// SOCKS5 要求先回复，客户端才会继续发送应用层数据。
	if err := sendSuccess(writer); err != nil {
		return err
	}

	// 在限定超时内 peek 客户端首包。
	first := h.peekFirstPacket(req.Reader)

	// 默认动作兜底；嗅探到域名则用域名规则覆盖。
	action := h.engine.Match(d.host) // d.host 是 IP；未命中 ip-cidr 时即默认动作
	routeHost := d.host
	if host, ok := detect.Sniff(first); ok {
		action = h.engine.Match(host)
		routeHost = host
		h.logger.Info("嗅探到域名", "ip", d.host, "domain", host, "action", action)
	} else {
		h.logger.Debug("首包未嗅探到域名，走默认动作", "ip", d.host, "action", action)
	}

	if action == rule.ActionReject {
		// 已回过 success，无法再回 0x02；只能关闭连接。
		h.logger.Info("嗅探后规则拒绝，关闭连接", "host", routeHost)
		return errSniffReject
	}

	// 拨号到目标（success 已回，这里不再回复；失败直接关闭）。
	upConn, err := h.dial(ctx, action, d.upstream, target)
	if err != nil {
		h.logger.Warn("嗅探后拨号失败", "host", routeHost, "action", action, "err", err)
		return err
	}
	defer upConn.Close()

	// 把已 peek 的首包先写给上游，再开始双向中继。
	if len(first) > 0 {
		if _, err := upConn.Write(first); err != nil {
			return err
		}
	}
	h.logger.Info("嗅探转发", "host", routeHost, "action", action)
	return relay(writer, req.Reader, upConn)
}

// dialAndRelay 处理已知 forward/direct：先拨号（失败回网络错误码），再回 success，再中继。
func (h *handler) dialAndRelay(ctx context.Context, writer io.Writer, req *socks5.Request, action rule.Action, up auth.Upstream, host, target string) error {
	upConn, err := h.dial(ctx, action, up, target)
	if err != nil {
		// 拨号失败：映射到合适的 SOCKS5 回复码后关闭。
		_ = socks5.SendReply(writer, replyCodeFor(err), nil)
		h.logger.Warn("拨号失败", "host", host, "action", action, "err", err)
		return err
	}
	defer upConn.Close()

	if err := sendSuccess(writer); err != nil {
		return err
	}
	h.logger.Info("转发", "host", host, "action", action)
	return relay(writer, req.Reader, upConn)
}

// dial 按动作建立到目标的连接：forward 经上游、direct 本机直连；上游 conn 包 idleConn。
func (h *handler) dial(ctx context.Context, action rule.Action, up auth.Upstream, target string) (net.Conn, error) {
	switch action {
	case rule.ActionForward:
		c, err := dialer.DialUpstream(ctx, up, target)
		if err != nil {
			return nil, err
		}
		return dialer.WrapIdle(c, h.idle), nil
	case rule.ActionDirect:
		c, err := dialer.DialDirect(ctx, target)
		if err != nil {
			return nil, err
		}
		return dialer.WrapIdle(c, h.idle), nil
	default:
		return nil, errNoDecision
	}
}

// peekFirstPacket 在 sniffTimeout 内尽力读取一段客户端首包用于嗅探。
// 读不到（超时/无数据）返回 nil；无论结果如何都会清除读截止时间，不影响后续中继。
func (h *handler) peekFirstPacket(r io.Reader) []byte {
	type deadliner interface{ SetReadDeadline(time.Time) error }
	if dl, ok := r.(deadliner); ok {
		_ = dl.SetReadDeadline(time.Now().Add(h.sniffTimeout))
		defer dl.SetReadDeadline(time.Time{})
	}
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if n <= 0 || (err != nil && n == 0) {
		return nil
	}
	return buf[:n]
}

// New 用配置、规则引擎与日志器装配并返回一个 *socks5.Server。
func New(cfg *config.Config, engine *rule.Engine, logger *slog.Logger) *socks5.Server {
	h := &handler{
		engine:       engine,
		logger:       logger,
		idle:         time.Duration(cfg.IdleTimeoutSec) * time.Second,
		sniffTimeout: time.Duration(cfg.SniffTimeoutMs) * time.Millisecond,
	}
	cr := &connectRule{engine: engine, logger: logger, sniffOn: cfg.SniffEnabled()}

	return socks5.NewServer(
		socks5.WithCredential(auth.Credential{}),  // 强制认证 + 解码校验（失败即拒）
		socks5.WithRule(cr),                       // 策略判定：reject/非CONNECT 回 0x02
		socks5.WithResolver(nopResolver{}),        // 跳过本地 DNS，防泄漏
		socks5.WithConnectHandle(h.connectHandle), // 接管 connect：拨号/嗅探/中继
		socks5.WithLogger(newSocksLogger(logger)),
	)
}
