// Package server 负责把各组件装配成一个 go-socks5 服务，并实现核心转发逻辑。
//
// 核心采用 “A3 三件套” 架构（顺应 go-socks5 的两阶段设计）：
//  1. WithRule.Allow（策略阶段，在拨号之前）：
//     - 非 CONNECT 命令 → 返回 false，库回 RepRuleFailure(0x02)，从而拦截 BIND/UDP ASSOCIATE；
//     - 规则判定为 reject → 返回 false，库回 RepRuleFailure(0x02)，得到“策略拒绝”的正确回复码；
//     - 规则判定为 forward/direct → 把判定结果放入 context，返回 true 放行。
//  2. WithDialAndRequest（拨号阶段）：
//     从 context 取出判定结果，forward 经上游拨号、direct 本机直连，
//     返回的连接用 idleConn 包装以补齐库中继缺失的空闲超时；双向中继由库完成。
//  3. WithResolver = nopResolver：
//     跳过库的本地 DNS 解析，使域名目标原样透传给上游解析，避免 DNS 泄漏。
package server

import (
	"context"
	"log/slog"
	"net"
	"time"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	"deeproxy/auth"
	"deeproxy/config"
	"deeproxy/dialer"
	"deeproxy/rule"
)

// nopResolver 实现 socks5.NameResolver，但不做任何实际 DNS 解析。
// 返回 nil IP 后，库的 AddrSpec.String() 会回退为 "FQDN:port"，
// 域名因此原样透传到拨号 hook，再交给上游 SOCKS5 解析（避免本机 DNS 泄漏）。
type nopResolver struct{}

func (nopResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

// connectRule 实现 socks5.RuleSet，承担“策略判定”职责。
type connectRule struct {
	engine *rule.Engine
	logger *slog.Logger
}

// Allow 在拨号前被库调用。它完成命令过滤、上游解码与规则判定，
// 并把放行类判定通过 context 传递给拨号 hook。
func (r *connectRule) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	// ① 命令过滤：仅允许 CONNECT(TCP)。BIND/UDP ASSOCIATE 一律拒绝（回 0x02）。
	if req.Command != statute.CommandConnect {
		r.logger.Warn("拒绝非 CONNECT 命令", "command", req.Command, "from", req.RemoteAddr)
		return ctx, false
	}

	// ② 解码上游：从认证阶段保存的用户名解码出本连接动态上游。
	// 正常情况下认证已通过（Credential.Valid 已校验过），这里再次解码以取得上游对象。
	username := ""
	if req.AuthContext != nil {
		username = req.AuthContext.Payload["username"]
	}
	up, err := auth.DecodeUpstream(username)
	if err != nil {
		// 理论上不会发生（认证阶段已校验）；防御性拒绝。
		r.logger.Warn("上游解码失败，拒绝连接", "err", err, "from", req.RemoteAddr)
		return ctx, false
	}

	// ③ 目标探测：直接读取 SOCKS5 请求中的目标主机（域名或 IP），不做额外解析。
	host := targetHost(req)

	// ④ 规则判定。
	action := r.engine.Match(host)
	switch action {
	case rule.ActionReject:
		// 策略拒绝：返回 false，库回 RepRuleFailure(0x02)。
		r.logger.Info("规则拒绝", "host", host, "action", "reject")
		return ctx, false
	case rule.ActionForward, rule.ActionDirect:
		// 放行：把判定结果放入 context，供拨号 hook 复用（规则只跑这一次）。
		d := decision{action: action, upstream: up, host: host}
		return context.WithValue(ctx, decisionKey, d), true
	default:
		// 不应出现的动作，保守拒绝。
		r.logger.Warn("未知动作，拒绝", "host", host, "action", action)
		return ctx, false
	}
}

// targetHost 从请求中取出纯目标主机：优先 FQDN（域名），否则用 IP 字面量。
// 由于注入了 nopResolver，域名目标的 DestAddr.IP 为空，这里能拿到原始域名。
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

// New 用配置、规则引擎与日志器装配并返回一个 *socks5.Server。
func New(cfg *config.Config, engine *rule.Engine, logger *slog.Logger) *socks5.Server {
	idle := time.Duration(cfg.IdleTimeoutSec) * time.Second
	cr := &connectRule{engine: engine, logger: logger}

	// 拨号 hook：从 context 取出 Allow 阶段的判定，执行 forward/direct。
	dialAndRequest := func(ctx context.Context, _ /*network*/, addr string, _ *socks5.Request) (net.Conn, error) {
		d, ok := ctx.Value(decisionKey).(decision)
		if !ok {
			// 没有判定结果说明流程异常（正常路径一定经过 Allow）；返回错误让库关连接。
			logger.Error("拨号阶段缺少规则判定，拒绝", "addr", addr)
			return nil, &net.OpError{Op: "dial", Err: errNoDecision}
		}

		var (
			conn net.Conn
			err  error
		)
		switch d.action {
		case rule.ActionForward:
			conn, err = dialer.DialUpstream(ctx, d.upstream, addr)
			if err != nil {
				logger.Warn("经上游转发失败", "host", d.host, "upstream", d.upstream.Addr(), "err", err)
				return nil, err
			}
			logger.Info("转发", "host", d.host, "action", "forward", "upstream", d.upstream.Addr())
		case rule.ActionDirect:
			conn, err = dialer.DialDirect(ctx, addr)
			if err != nil {
				logger.Warn("本机直连失败", "host", d.host, "err", err)
				return nil, err
			}
			logger.Info("直连", "host", d.host, "action", "direct")
		default:
			return nil, &net.OpError{Op: "dial", Err: errNoDecision}
		}

		// 用空闲超时包装，补齐库中继缺失的 deadline。
		return dialer.WrapIdle(conn, idle), nil
	}

	return socks5.NewServer(
		socks5.WithCredential(auth.Credential{}),  // 强制认证 + 解码校验（失败即拒）
		socks5.WithRule(cr),                       // 策略判定：reject/非CONNECT 回 0x02
		socks5.WithResolver(nopResolver{}),        // 跳过本地 DNS，防泄漏
		socks5.WithDialAndRequest(dialAndRequest), // forward/direct 拨号
		socks5.WithLogger(newSocksLogger(logger)),
	)
}
