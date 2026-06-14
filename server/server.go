// Package server 负责把各组件装配成一个 go-socks5 服务，并实现核心转发逻辑。
//
// 架构（两阶段，顺应 go-socks5 设计）：
//  1. WithRule.Allow（策略阶段，在拨号之前）：
//     - 非 CONNECT 命令 → false，库回 RepRuleFailure(0x02)；
//     - 鉴权阶段已通过（Credential.Valid 内做密码比对）；此处用 auth.ParseOnly 对同一
//       用户名【只重解析、不再比对密码】（D0-0 + AC-43 性能修复：Allow 绝不重复鉴权/bcrypt）
//     - 取本连接所属分组的快照视图，按【该组预编译的扁平化规则引擎】做选路预判；
//     - 命中 reject → false(0x02)；forward/direct 存 ctx；
//       IP 未命中 ip-cidr 且启用嗅探 → 标记 needsSniff 存 ctx（放行，待 ConnectHandle 嗅探）。
//  2. WithConnectHandle（接管 CONNECT，自己回复+中继）：
//     - Type A：用鉴权阶段解出的动态上游（无尾段且动作 forward → 拒连 G1）；
//     - Type B：经 pool.Selector 从该组健康池 SWRR 选上游，用变量映射替换其用户名模板，
//       拨号失败自动重试下一个健康节点（AC-4 故障转移）；整组全挂 → 回 RepHostUnreachable(G6)；
//     - 中继前后埋点 stats（字节/请求/动作分布），连接结束写 audit。
//
// 之所以用 ConnectHandle 而非 WithDialAndRequest：嗅探必须在“回复 success 之后、
// 数据中继之前”读取客户端首包，这要求完全接管 connect 流程。
//
// 同时注入 nopResolver 跳过库的本地 DNS，使域名目标原样透传、避免 DNS 泄漏。
//
// 性能（一号硬约束）：转发侧每连接仅 holder.Load() 一次（atomic 无锁读快照）；
// 字节中继走 relayCounted（纯 io.Copy，零锁、零持久化）；统计为内存原子计数，
// 仅在建连/连接结束各埋点一次，绝不进入字节中继循环。
package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	"deeproxy/auth"
	"deeproxy/detect"
	"deeproxy/dialer"
	"deeproxy/domain"
	"deeproxy/pool"
	"deeproxy/rule"
	"deeproxy/snapshot"
	"deeproxy/stats"
	"deeproxy/syslog"
)

// nopResolver 实现 socks5.NameResolver，但不做任何实际 DNS 解析。
// 返回 nil IP 后，库的 AddrSpec.String() 会回退为 "FQDN:port"，
// 域名因此原样透传到 ConnectHandle，再交给上游 SOCKS5 解析（避免本机 DNS 泄漏）。
type nopResolver struct{}

func (nopResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

// connectRule 实现 socks5.RuleSet，承担“策略判定”职责（命令过滤 + 鉴权结果消费 + 选路预判）。
type connectRule struct {
	holder   *snapshot.Holder      // 运行期快照（无锁读）
	provider auth.SnapshotProvider // 鉴权用快照视图提供者（适配自同一 holder）
	counter  *stats.Counter        // 统计埋点（拒连分类计数）
	logger   *slog.Logger
}

// Allow 在 ConnectHandle 之前被库调用，完成命令过滤、鉴权结果消费与规则预判，
// 并把放行类判定通过 context 传给 ConnectHandle。
func (r *connectRule) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	// ① 命令过滤：仅允许 CONNECT(TCP)。BIND/UDP ASSOCIATE 一律拒绝（回 0x02）。
	if req.Command != statute.CommandConnect {
		r.logger.Warn("拒绝非 CONNECT 命令", "command", req.Command, "from", req.RemoteAddr)
		return ctx, false
	}

	// ② 消费鉴权结果：用 ParseOnly 重新解析出 auth.Decision（D0-0，纯函数 DRY）。
	// 关键（AC-43 性能修复）：Allow 阶段【绝不重复密码校验】——bcrypt/明文比对已在
	// Credential.Valid 阶段做过。ParseOnly 只做用户名解析 + 快照 O(1) 查（user/group/授权）
	// + 按组型解尾段，零密码比对、零 I/O，避免双重鉴权开销。
	// 鉴权失败理论上已在 Valid 阶段被库拒绝、不会到这里；此处失败属防御性拒连。
	username := ""
	if req.AuthContext != nil {
		username = req.AuthContext.Payload["username"]
	}
	dec, err := auth.ParseOnly(r.provider(), username)
	if err != nil {
		r.counter.IncRejectAuth()
		r.logger.Warn("Allow 阶段鉴权结果消费失败，拒绝连接", "err", err, "from", req.RemoteAddr)
		return ctx, false
	}

	// ③ 取本连接所属分组的快照视图（含该组预编译的扁平化规则引擎与健康池）。
	snap := r.holder.Load()
	gv, ok := snap.GroupByID(dec.GroupID)
	if !ok {
		// 鉴权时分组还在、Allow 时被删（极端热替换竞态）——保守拒连。
		r.counter.IncRejectAuth()
		r.logger.Warn("分组在鉴权后消失，拒绝连接", "group", dec.Group)
		return ctx, false
	}

	// 运行期动态设置（来自 system_setting，随快照物化）：从同一份已 Load 的快照取出，
	// 随判定带入 ConnectHandle，保证本连接 idle/sniff 用同一份配置，且支持后台热改。
	settings := snap.Settings()

	// ④ 目标探测：读取请求中的目标主机（域名或 IP）。
	host := targetHost(req)
	isIP := req.DestAddr != nil && req.DestAddr.FQDN == "" && len(req.DestAddr.IP) != 0

	// ⑤ 规则预判：用该组专属引擎（全局组→分组组→默认动作）做一次顺序首匹配。
	action, matched := gv.Engine.MatchRule(host)

	// IP 目标且未命中 ip-cidr 规则、且启用嗅探：放行，待 ConnectHandle 嗅探域名再决定（AC-8）。
	if isIP && !matched && settings.SniffDomain {
		d := decision{
			host: host, needsSniff: true, auth: dec, group: gv,
			idle: settings.IdleTimeout, sniffTimeout: settings.SniffTimeout,
		}
		return context.WithValue(ctx, decisionKey, d), true
	}

	// 其余情况按已知动作处理。
	switch action {
	case rule.ActionReject:
		r.counter.IncRejectRule()
		r.counter.IncActionReject() // M1：同时计入动作分布，使饼图 reject 占比不再恒为 0
		r.logger.Info("规则拒绝", "host", host, "action", "reject", "group", dec.Group)
		return ctx, false
	case rule.ActionForward, rule.ActionDirect:
		d := decision{
			action: action, host: host, auth: dec, group: gv,
			idle: settings.IdleTimeout, sniffTimeout: settings.SniffTimeout,
		}
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
	registry *pool.Registry      // per-group SWRR Selector 注册表（Type B）
	counter  *stats.Counter      // 统计埋点
	audit    *syslog.AuditBuffer // 连接审计
	logger   *slog.Logger
	holder   *snapshot.Holder // 运行期快照（无锁读）：建连时读 idle/sniff 等动态设置
}

// errNoUpstreamSource 表示 Type A 组无尾段（无动态上游来源）却需 forward（G1）。
var errNoUpstreamSource = errors.New("Type A 无尾段上游来源，无法 forward")

// connectHandle 接管 CONNECT：根据 Allow 阶段的判定执行拨号、嗅探、回复与中继。
func (h *handler) connectHandle(ctx context.Context, writer io.Writer, req *socks5.Request) error {
	// C2：握手已成功（CONNECT 已解析到此），清除 accept 时设的握手读截止时间，
	// 否则它会在后续嗅探/中继读取时误触发超时（中继空闲超时另由 idleConn 负责）。
	clearHandshakeDeadline(writer)

	d, ok := ctx.Value(decisionKey).(decision)
	if !ok {
		// 正常路径一定经过 Allow；缺判定说明流程异常。
		_ = socks5.SendReply(writer, statute.RepServerFailure, nil)
		return errNoDecision
	}

	// 连接计数：进入即 +1，退出 -1（活跃连接数仪表盘实时值）。
	h.counter.ConnOpened()
	defer h.counter.ConnClosed()

	// 实际拨号目标始终用客户端给的原始地址（嗅探到的域名只用于选路，不改变拨号目标，
	// 以免上游把域名重解析到与客户端不同的 IP）。
	target := req.DestAddr.String()

	if d.needsSniff {
		return h.handleSniff(ctx, writer, req, d, target)
	}
	return h.dialAndRelay(ctx, writer, req, d, target)
}

// resolveUpstream 按组类型与动作选出本次拨号要用的上游（forward 时）。
//
// excluded 是本连接内【已拨号失败】的上游 ID 集合（故障转移用）：Type B 选择时
// 从健康池中先剔除这些节点再做 SWRR，保证每次重试切到尚未试过的节点（AC-4）。
//
// 返回 (上游, 选中视图指针, error)：
//   - direct 动作：无需上游，返回零值上游 + nil 视图 + nil err。
//   - Type A forward：用鉴权解出的动态上游；无尾段来源 → errNoUpstreamSource（G1）。
//   - Type B forward：经该组 Selector 从【剔除 excluded 后的健康池】SWRR 选一个，
//     用变量映射替换模板用户名；可选池为空（全挂或全部已试败）→ pool.ErrNoUpstream（G6）。
func (h *handler) resolveUpstream(d decision, excluded map[int64]struct{}) (auth.Upstream, *snapshot.UpstreamView, error) {
	if d.action != rule.ActionForward {
		return auth.Upstream{}, nil, nil // direct 无需上游
	}
	switch d.auth.GroupType {
	case domain.TypeA:
		if !d.auth.HasDynamicUpstream {
			return auth.Upstream{}, nil, errNoUpstreamSource // G1
		}
		return d.auth.DynamicUpstream, nil, nil
	case domain.TypeB:
		// 剔除本连接已失败的节点，再交给 SWRR（DRY：复用 Selector.Pick）。
		candidates := filterExcluded(d.group.HealthyUpstreams, excluded)
		sel := h.registry.For(d.group.ID)
		view, err := sel.Pick(candidates)
		if err != nil {
			return auth.Upstream{}, nil, err // ErrNoUpstream（G6 / 全部已试败）
		}
		up := view.ToAuthUpstream(d.auth.Variables)
		return up, &view, nil
	default:
		return auth.Upstream{}, nil, errNoUpstreamSource
	}
}

// filterExcluded 返回剔除了 excluded 中 ID 的健康上游列表（不修改原切片）。
// excluded 为空时直接返回原切片，零分配（绝大多数“首次拨号成功”走此快路径）。
func filterExcluded(healthy []snapshot.UpstreamView, excluded map[int64]struct{}) []snapshot.UpstreamView {
	if len(excluded) == 0 {
		return healthy
	}
	out := make([]snapshot.UpstreamView, 0, len(healthy))
	for _, u := range healthy {
		if _, bad := excluded[u.ID]; !bad {
			out = append(out, u)
		}
	}
	return out
}

// dialAndRelay 处理已知 forward/direct：拨号（forward 含故障转移）→ 回 success → 中继 → 埋点。
func (h *handler) dialAndRelay(ctx context.Context, writer io.Writer, req *socks5.Request, d decision, target string) error {
	upConn, usedUpstream, err := h.dialWithFailover(ctx, d, target)
	if err != nil {
		// 拨号失败：映射到合适的 SOCKS5 回复码后关闭。
		// 整组全挂（pool.ErrNoUpstream）与 G1（无上游来源）均回 RepHostUnreachable（G6/AC-17）。
		_ = socks5.SendReply(writer, dialReplyCode(err), nil)
		h.logger.Warn("拨号失败", "host", d.host, "action", d.action, "err", err)
		h.recordAudit(d, target, usedUpstream, 0, 0) // 记一笔失败审计（字节 0）
		return err
	}
	defer upConn.Close()

	if err := sendSuccess(writer); err != nil {
		return err
	}

	// 动作分布 + 请求数埋点（连接成功建立时）。
	h.markAction(d)
	h.counter.IncReq(d.auth.GroupID, d.auth.UserID)
	h.counter.IncDomain(d.host, d.auth.GroupID) // 目标域名命中埋点（纯 IP 也计入）
	h.logger.Info("转发", "host", d.host, "action", d.action, "group", d.auth.Group)

	// 双向中继（纯 io.Copy 热路径）；结束后一次性埋点字节数 + 写审计。
	up, down, relErr := relayCounted(writer, req.Reader, upConn)
	h.recordTraffic(d, up, down)
	h.recordAudit(d, target, usedUpstream, up, down)
	return relErr
}

// maxFailoverTry 是 Type B 故障转移的【最大拨号尝试次数上限】（H2）。
//
// 为什么必须有上限：原实现对整组每个健康节点都重试一次（maxTry=len(healthy)）。
// 当组内有大量「TCP 可连但建立后挂起」的死节点时，单条客户端连接会顺序耗尽
// dialTimeout(10s)×N，最坏占用一个 goroutine + fd 长达 N×10s（如 20 节点=200s），
// 即便客户端早已放弃。这是放大型资源耗尽风险。故把单连接的拨号尝试封顶为本值，
// 多于此数的健康节点不在本连接内继续试（SWRR 已保证每连接起点轮转，整体仍均摊到全池）。
const maxFailoverTry = 3

// failoverBudget 是单条连接【全部故障转移拨号合计】的墙钟预算（H2）。
//
// 即便尝试次数未达上限，也用此预算给整个拨号阶段一个总截止：派生带 deadline 的 ctx
// 传给 dialer，任一节点拨号都受其约束，避免少数慢节点累计拖垮单连接的拨号阶段。
const failoverBudget = 25 * time.Second

//
// 故障转移语义（AC-4）：
//   - 仅在【拨号阶段】重试；一旦 success 回复并开始中继，连接中途断开不重试（避免重复中继数据）。
//   - Type B 最多尝试该组健康节点数次，每次经 SWRR 选一个（平滑分布 + 跳过已选失败节点）；
//     全部健康节点拨号失败 → 返回最后错误（上层回网络错误码）。
//   - Type A / direct 无池概念，单次拨号，不重试。
//
// 返回选中的上游视图指针（Type B，用于审计展示实际上游）；Type A/direct 为 nil。
func (h *handler) dialWithFailover(ctx context.Context, d decision, target string) (net.Conn, *snapshot.UpstreamView, error) {
	// direct：直连，无上游。
	if d.action == rule.ActionDirect {
		c, err := dialer.DialDirect(ctx, target)
		if err != nil {
			return nil, nil, err
		}
		return dialer.WrapIdle(c, d.idle), nil, nil
	}

	// forward：Type A 单次；Type B 故障转移。
	maxTry := 1
	if d.auth.GroupType == domain.TypeB {
		maxTry = len(d.group.HealthyUpstreams)
		if maxTry == 0 {
			return nil, nil, pool.ErrNoUpstream // 整组全挂（G6）
		}
		// H2：单连接拨号尝试封顶，避免大量死节点时 N×dialTimeout 长时间占用 goroutine+fd。
		if maxTry > maxFailoverTry {
			maxTry = maxFailoverTry
		}
		// H2：给整个故障转移拨号阶段一个总墙钟预算（派生带 deadline 的 ctx 传给 dialer），
		// 避免少数慢节点累计拖垮单连接的拨号阶段。中继阶段不用此 ctx（其超时由 idleConn 负责）。
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, failoverBudget)
		defer cancel()
	}

	var lastErr error
	excluded := make(map[int64]struct{}) // 本连接内已拨号失败的节点 ID（故障转移剔除）
	for i := 0; i < maxTry; i++ {
		up, view, err := h.resolveUpstream(d, excluded)
		if err != nil {
			// 来源错误（G1）或候选池耗尽（全部已试败）→ 不再重试。
			if lastErr != nil {
				return nil, nil, lastErr
			}
			return nil, nil, err
		}
		c, derr := dialer.DialUpstream(ctx, up, target)
		if derr == nil {
			return dialer.WrapIdle(c, d.idle), view, nil
		}
		lastErr = derr
		if view != nil {
			excluded[view.ID] = struct{}{} // 标记该节点本连接内失败，下次重试跳过
		}
		h.logger.Warn("上游拨号失败，尝试故障转移",
			"attempt", i+1, "max", maxTry, "upstream", up.Addr(), "err", derr)
	}
	if lastErr == nil {
		lastErr = pool.ErrNoUpstream
	}
	return nil, nil, lastErr
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
	// 传入底层 writer（即 go-socks5 透传进来的原始 net.Conn），以便对真实连接设置硬读超时：
	// req.Reader 是包装该 conn 的 *bufio.Reader，自身无 SetReadDeadline，必须落到底层 conn 上才生效。
	// sniffTimeout 取本连接判定里携带的运行期动态值（来自 system_setting 快照，可后台热改）。
	first := h.peekFirstPacket(writer, req.Reader, d.sniffTimeout)

	// 默认动作兜底（用该组引擎对 IP 再匹配一次 = 默认动作）；嗅探到域名则用域名规则覆盖。
	action, _ := d.group.Engine.MatchRule(d.host)
	routeHost := d.host
	if host, ok := detect.Sniff(first); ok {
		action, _ = d.group.Engine.MatchRule(host)
		routeHost = host
		h.logger.Info("嗅探到域名", "ip", d.host, "domain", host, "action", action)
	} else {
		h.logger.Debug("首包未嗅探到域名，走默认动作", "ip", d.host, "action", action)
	}

	if action == rule.ActionReject {
		// 已回过 success，无法再回 0x02；只能关闭连接。
		h.counter.IncRejectRule()
		h.counter.IncActionReject() // M1：嗅探后 reject 也计入动作分布
		h.logger.Info("嗅探后规则拒绝，关闭连接", "host", routeHost)
		return errSniffReject
	}

	// 用嗅探后的动作覆盖 decision，复用故障转移拨号逻辑。
	d.action = action
	upConn, usedUpstream, err := h.dialWithFailover(ctx, d, target)
	if err != nil {
		h.logger.Warn("嗅探后拨号失败", "host", routeHost, "action", action, "err", err)
		h.recordAudit(d, target, usedUpstream, 0, 0)
		return err
	}
	defer upConn.Close()

	// 把已 peek 的首包先写给上游，再开始双向中继。
	if len(first) > 0 {
		if _, err := upConn.Write(first); err != nil {
			return err
		}
	}
	h.markAction(d)
	h.counter.IncReq(d.auth.GroupID, d.auth.UserID)
	h.counter.IncDomain(routeHost, d.auth.GroupID) // 嗅探后 routeHost 更准（嗅出域名则记域名，否则记 IP）
	h.logger.Info("嗅探转发", "host", routeHost, "action", action)

	up, down, relErr := relayCounted(writer, req.Reader, upConn)
	// 首包字节也计入上行。
	up += int64(len(first))
	h.recordTraffic(d, up, down)
	h.recordAudit(d, target, usedUpstream, up, down)
	return relErr
}

// markAction 按动作累加动作分布计数（forward/direct，reject 在 Allow/嗅探处单独计）。
func (h *handler) markAction(d decision) {
	switch d.action {
	case rule.ActionForward:
		h.counter.IncActionForward()
	case rule.ActionDirect:
		h.counter.IncActionDirect()
	}
}

// recordTraffic 把本连接累计的上/下行字节一次性埋点到 stats（按 group/user 维度）。
// 仅在连接结束后调用一次，不进入字节中继循环（一号硬约束）。
func (h *handler) recordTraffic(d decision, up, down int64) {
	if up > 0 {
		h.counter.AddUp(d.auth.GroupID, d.auth.UserID, up)
	}
	if down > 0 {
		h.counter.AddDown(d.auth.GroupID, d.auth.UserID, down)
	}
}

// recordAudit 写一条连接审计记录（内存环形缓冲，不落库）。
func (h *handler) recordAudit(d decision, target string, view *snapshot.UpstreamView, up, down int64) {
	upstreamStr := ""
	if view != nil {
		upstreamStr = net.JoinHostPort(view.Host, strconv.Itoa(view.Port))
	} else if d.action == rule.ActionForward && d.auth.GroupType == domain.TypeA && d.auth.HasDynamicUpstream {
		upstreamStr = d.auth.DynamicUpstream.Addr()
	}
	h.audit.Record(syslog.AuditEntry{
		User:      d.auth.User,
		Group:     d.auth.Group,
		Target:    target,
		Action:    string(d.action),
		Upstream:  upstreamStr,
		UpBytes:   up,
		DownBytes: down,
	})
}

// dialReplyCode 把拨号链路错误映射为 SOCKS5 回复码。
// 无可用上游（整组全挂 G6 / Type A 无来源 G1）统一回 RepHostUnreachable（AC-17 E2E 断言）；
// 其余 syscall 级网络错误复用 replyCodeFor 的细分映射。
func dialReplyCode(err error) byte {
	if errors.Is(err, pool.ErrNoUpstream) || errors.Is(err, errNoUpstreamSource) {
		return statute.RepHostUnreachable
	}
	return replyCodeFor(err)
}

// peekFirstPacket 在 sniffTimeout 内尽力读取一段客户端首包用于嗅探。
// 读不到（超时/无数据）返回 nil；无论结果如何都会清除读截止时间，不影响后续中继。
//
// 为什么需要 underlying（底层 net.Conn）：
//
//	go-socks5 把 req.Reader 设为包装连接的 *bufio.Reader，它本身没有 SetReadDeadline 方法。
//	旧实现对 req.Reader 做 deadliner 类型断言恒为 false，sniffTimeout 形同虚设——客户端连上后
//	不发首包（TCP 半开 / 扫描器 / 慢速攻击）会让 r.Read 永久阻塞，ServeConn 不返回、conn 不关闭，
//	goroutine + fd 永久泄漏，少量并发即可耗尽（CRITICAL DoS）。
//	因此这里把硬超时落到真正的底层 conn 上：bufio.Read 最终从该 conn 读，conn 的读截止时间一到，
//	阻塞的 Read 会立刻以超时错误返回，连接随后由 ServeConn 的 defer conn.Close() 正常回收。
//
// 兜底：若底层 writer 拿不到 net.Conn（理论上不会发生，仅为健壮性），退化为 AfterFunc 强制 Close，
// 保证超时后阻塞的 Read 一定能被唤醒、连接一定被关闭，绝不泄漏。
func (h *handler) peekFirstPacket(underlying io.Writer, r io.Reader, sniffTimeout time.Duration) []byte {
	buf := make([]byte, 4096)

	if conn, ok := underlying.(net.Conn); ok {
		// 首选：对真实连接设硬读超时；读完（无论成败）清除，不影响后续双向中继的读写。
		_ = conn.SetReadDeadline(time.Now().Add(sniffTimeout))
		defer conn.SetReadDeadline(time.Time{})

		n, err := r.Read(buf)
		if n <= 0 || (err != nil && n == 0) {
			return nil
		}
		return buf[:n]
	}

	// 兜底：无法拿到底层 conn 时，用定时器到点强制关闭连接，避免 Read 永久阻塞导致泄漏。
	// 注意：这里 underlying 至少是 io.Writer，无法 Close；故只能依赖 r 若也实现了 io.Closer 时关闭。
	// 实际运行路径一定走上面的 net.Conn 分支，此分支仅为防御性兜底。
	if c, ok := r.(io.Closer); ok {
		timer := time.AfterFunc(sniffTimeout, func() { _ = c.Close() })
		defer timer.Stop()
	}
	n, err := r.Read(buf)
	if n <= 0 || (err != nil && n == 0) {
		return nil
	}
	return buf[:n]
}

// New 用运行期快照、代理池注册表、统计与审计、日志器装配并返回一个 *socks5.Server。
//
// 取消配置文件后，空闲超时 / 嗅探开关 / 嗅探超时 / 默认动作均改为【运行期动态设置】，
// 不再从启动配置读：转发侧在 Allow/建连时从 holder.Load().Settings() 取（可后台热改）。
//
// 参数：
//   - holder：运行期不可变快照的原子持有者（转发侧无锁 Load，含动态设置）。
//   - registry：per-group SWRR Selector 注册表（Type B 选上游）。
//   - counter：内存原子统计计数器（埋点）。
//   - audit：连接审计内存环形缓冲。
//   - logger：日志器（同时接 syslog 内存缓冲 Handler，由 cmd 装配）。
func New(
	holder *snapshot.Holder,
	registry *pool.Registry,
	counter *stats.Counter,
	audit *syslog.AuditBuffer,
	logger *slog.Logger,
) *socks5.Server {
	h := &handler{
		registry: registry,
		counter:  counter,
		audit:    audit,
		logger:   logger,
		holder:   holder,
	}
	provider := authProvider(holder)
	cr := &connectRule{
		holder:   holder,
		provider: provider,
		counter:  counter,
		logger:   logger,
	}

	return socks5.NewServer(
		socks5.WithCredential(auth.NewCredentialWithLogger(provider, logger)), // 强制认证 + v2 鉴权（失败即拒）；注入 logger 打未授权日志（AC-1.5）
		socks5.WithRule(cr),                       // 策略判定：reject/非CONNECT 回 0x02
		socks5.WithResolver(nopResolver{}),        // 跳过本地 DNS，防泄漏
		socks5.WithConnectHandle(h.connectHandle), // 接管 connect：拨号/嗅探/中继
		socks5.WithGPool(recoverPool{logger: logger}), // C1：每连接 goroutine panic 兜底，防单连接 panic 崩溃整进程
		socks5.WithLogger(newSocksLogger(logger)),
	)
}
