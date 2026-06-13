package health

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"deeproxy/store"
)

// health.go 实现 Type B 代理池的健康检查 worker（AC-15~18，G2）。
//
// 设计：
//   - 每个 Type B 分组按其配置的间隔独立周期探测组内每条上游。
//   - 探测方式 ping（TCP 连上游端口）或 url（经上游 SOCKS5 拉取探测 URL）。
//   - 连续失败 failThld 次 → 标记不可用；连续成功 recvThld 次 → 恢复可用（抗抖动）。
//   - 健康状态变化时：写 store.UpdateUpstreamHealth(id, healthy) → 触发快照重建+原子替换，
//     使 snapshot.GroupView.HealthyUpstreams 刷新（Selector 下次选择自然只在新列表上轮训）。
//   - G2：Type A 组池为空、上游由客户端动态给出，health worker 跳过。
//   - 与转发热路径完全解耦：本 worker 是独立后台 goroutine。

// defaultProbeURL 是 URL 探测默认地址（spec 默认 bing carousel）。
const defaultProbeURL = "https://www.bing.com/hp/api/v1/carousel?&format=json"

// defaultProbeTimeout 是单次探测超时。
const defaultProbeTimeout = 10 * time.Second

// ProbeResult 是一次探测的结果（供 TestProxy 立即探测 AC-38 返回）。
type ProbeResult struct {
	OK        bool          // 是否连通
	Latency   time.Duration // 探测耗时
	Err       string        // 失败原因（OK=false 时）
}

// Prober 抽象单条上游的探测，便于测试注入 mock。
type Prober interface {
	// Probe 探测一条上游：mode=ping 时仅测 TCP 连通；mode=url 时经上游拉取 probeURL。
	Probe(ctx context.Context, up store.UpstreamProxy, mode store.HealthMode, probeURL string) ProbeResult
}

// netProber 是基于真实网络的默认探测实现。
type netProber struct{}

// NewNetProber 返回默认网络探测器。
func NewNetProber() Prober { return netProber{} }

// Probe 实现真实网络探测。
func (netProber) Probe(ctx context.Context, up store.UpstreamProxy, mode store.HealthMode, probeURL string) ProbeResult {
	start := time.Now()
	upAddr := net.JoinHostPort(up.Host, fmt.Sprintf("%d", up.Port))

	if mode == store.HealthPing {
		// ping 模式：直接 TCP 连上游端口判定连通（非 ICMP，避免权限问题，纯 Go 跨平台）。
		d := net.Dialer{Timeout: defaultProbeTimeout}
		conn, err := d.DialContext(ctx, "tcp", upAddr)
		if err != nil {
			return ProbeResult{OK: false, Latency: time.Since(start), Err: err.Error()}
		}
		_ = conn.Close()
		return ProbeResult{OK: true, Latency: time.Since(start)}
	}

	// url 模式：经上游 SOCKS5 拨号拉取探测 URL，真实验证上游可用性。
	if probeURL == "" {
		probeURL = defaultProbeURL
	}
	var pAuth *proxy.Auth
	if up.User != "" {
		pAuth = &proxy.Auth{User: up.User, Password: up.Pwd}
	}
	dialer, err := proxy.SOCKS5("tcp", upAddr, pAuth, proxy.Direct)
	if err != nil {
		return ProbeResult{OK: false, Latency: time.Since(start), Err: fmt.Sprintf("构造上游拨号器失败: %v", err)}
	}
	cd, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return ProbeResult{OK: false, Latency: time.Since(start), Err: "上游拨号器不支持 ContextDialer"}
	}

	// 用经上游的 DialContext 构造 http.Client，发起一次 GET。
	transport := &http.Transport{
		DialContext: cd.DialContext,
		// 探测不复用连接，避免污染；超时由 ctx 控制。
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: transport, Timeout: defaultProbeTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return ProbeResult{OK: false, Latency: time.Since(start), Err: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{OK: false, Latency: time.Since(start), Err: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // 读少量即可，确认链路通

	// 2xx/3xx 视为通；其他状态码视为上游异常。
	if resp.StatusCode >= 400 {
		return ProbeResult{OK: false, Latency: time.Since(start), Err: fmt.Sprintf("探测 URL 返回状态码 %d", resp.StatusCode)}
	}
	return ProbeResult{OK: true, Latency: time.Since(start)}
}

// SnapshotRefresher 抽象“健康状态变化后刷新运行期快照”的能力，
// 由 snapshot.Holder.RebuildAndSwap 实现（health worker 不直接依赖 snapshot 包，避免耦合）。
type SnapshotRefresher interface {
	Refresh() error
}

// nodeState 记录单条上游的连续成功/失败计数与当前健康判定。
type nodeState struct {
	consecutiveFail int
	consecutiveOK   int
	healthy         bool // 当前健康判定（初值取自 store.HealthState）
	latencyMs       int64
}

// HealthChecker 是健康检查 worker。
type HealthChecker struct {
	store     *store.Store
	prober    Prober
	refresher SnapshotRefresher
	logger    *slog.Logger

	mu     sync.Mutex
	states map[int64]*nodeState // 上游 ID → 状态

	// disabledGroups 记录被手动停掉健康检查的分组（运行期开关，AC-18 的组级停启）。
	disabledGroups map[int64]struct{}
}

// NewHealthChecker 创建健康检查器。logger 可为 nil。
func NewHealthChecker(st *store.Store, prober Prober, refresher SnapshotRefresher, logger *slog.Logger) *HealthChecker {
	if prober == nil {
		prober = NewNetProber()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HealthChecker{
		store:          st,
		prober:         prober,
		refresher:      refresher,
		logger:         logger,
		states:         make(map[int64]*nodeState),
		disabledGroups: make(map[int64]struct{}),
	}
}

// Run 启动健康检查主循环，阻塞直到 ctx 取消。
//
// 实现：用一个最小公共 tick（默认按各组配置取最小间隔的近似——这里简化为固定基准 tick，
// 每 tick 检查“到期需探测”的组）。首版用每组独立到期判断的统一 30s 基准扫描，
// 实际探测间隔以各组 HCInterval 为准（到期才探）。
func (h *HealthChecker) Run(ctx context.Context) {
	const baseTick = 30 * time.Second
	ticker := time.NewTicker(baseTick)
	defer ticker.Stop()

	// 记录各组上次探测时间，到期才探。
	lastRun := make(map[int64]time.Time)

	// 启动即先跑一轮（避免等一个 baseTick 才开始）。
	h.scanOnce(ctx, lastRun)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.scanOnce(ctx, lastRun)
		}
	}
}

// scanOnce 扫描所有 Type B 组，对到期且启用健康检查的组探测其上游。
func (h *HealthChecker) scanOnce(ctx context.Context, lastRun map[int64]time.Time) {
	groups, err := h.store.ListGroups()
	if err != nil {
		h.logger.Warn("健康检查读取分组失败", "err", err)
		return
	}
	upstreams, err := h.store.ListAllUpstreams()
	if err != nil {
		h.logger.Warn("健康检查读取上游失败", "err", err)
		return
	}
	// 上游按组分桶。
	byGroup := make(map[int64][]store.UpstreamProxy)
	for _, u := range upstreams {
		byGroup[u.GroupID] = append(byGroup[u.GroupID], u)
	}

	changed := false
	now := time.Now()
	for _, g := range groups {
		// G2：跳过 Type A 组（池为空、不参与健康检查）。
		if g.Type != store.TypeB {
			continue
		}
		// 组级未开启健康检查 或 被手动停掉 → 跳过。
		if !g.HCEnabled || h.isGroupDisabled(g.ID) {
			continue
		}
		// 到期判断：距上次探测 >= HCInterval 才探。
		interval := time.Duration(g.HCInterval) * time.Second
		if interval <= 0 {
			interval = 600 * time.Second
		}
		if last, ok := lastRun[g.ID]; ok && now.Sub(last) < interval {
			continue
		}
		lastRun[g.ID] = now

		if h.probeGroup(ctx, g, byGroup[g.ID]) {
			changed = true
		}
	}

	// 有任何健康状态变化 → 刷新快照（重建 HealthyUpstreams）。
	if changed && h.refresher != nil {
		if err := h.refresher.Refresh(); err != nil {
			h.logger.Warn("健康状态变化后刷新快照失败", "err", err)
		}
	}
}

// probeGroup 探测一个组的所有上游，按阈值更新健康判定并落库。返回该组是否有状态变化。
func (h *HealthChecker) probeGroup(ctx context.Context, g store.Group, ups []store.UpstreamProxy) bool {
	failThld := g.HCFailThld
	if failThld <= 0 {
		failThld = 3
	}
	recvThld := g.HCRecvThld
	if recvThld <= 0 {
		recvThld = 2
	}

	changed := false
	healthyCount := 0
	for _, u := range ups {
		// 被手动禁用的上游不探测，但仍计入“不可用”（不参与选择）。
		if !u.Enabled {
			continue
		}
		res := h.prober.Probe(ctx, u, g.HCMode, g.HCURL)
		if h.applyResult(u, res, failThld, recvThld) {
			changed = true
		}
		if h.isHealthy(u.ID) {
			healthyCount++
		}
	}

	// 整组全挂告警（AC-17 的拒连由 Selector 在选择期返回 ErrNoUpstream 实现；此处仅日志）。
	if len(ups) > 0 && healthyCount == 0 {
		h.logger.Warn("代理组全部上游不可用", "group", g.Name, "groupID", g.ID)
	}
	return changed
}

// applyResult 按阈值更新某上游的连续计数与健康判定；判定翻转时落库。返回是否【已成功落库的】翻转。
//
// FIX-H6 一致性保证：翻转后落库失败会回滚内存态并返回 false，确保「内存态 = DB 态」，
// 避免快照重建从未更新的 DB 读到与 health worker 内存态不一致的健康状态。
func (h *HealthChecker) applyResult(u store.UpstreamProxy, res ProbeResult, failThld, recvThld int) bool {
	h.mu.Lock()
	st := h.states[u.ID]
	if st == nil {
		// 初值取库中状态，避免重启后误判。
		st = &nodeState{healthy: u.HealthState}
		h.states[u.ID] = st
	}

	prev := st.healthy
	if res.OK {
		st.consecutiveOK++
		st.consecutiveFail = 0
		st.latencyMs = res.Latency.Milliseconds()
		if !st.healthy && st.consecutiveOK >= recvThld {
			st.healthy = true
		}
	} else {
		st.consecutiveFail++
		st.consecutiveOK = 0
		if st.healthy && st.consecutiveFail >= failThld {
			st.healthy = false
		}
	}
	flipped := prev != st.healthy
	newHealthy := st.healthy
	h.mu.Unlock()

	if flipped {
		if err := h.store.UpdateUpstreamHealth(u.ID, newHealthy); err != nil {
			// FIX-H6：落库失败必须回滚内存态，保证「内存态 = DB 态」不变式。
			// 为什么必须回滚：applyResult 在锁内先翻转了 st.healthy，落库在锁外执行；
			// 若落库失败而内存态保持翻转，则内存认为已翻转、DB 仍是旧值，
			// 下次 Rebuild 从 DB 读快照时该节点状态与 health worker 内存态将【永久不一致】，
			// 直到下次再次翻转才偶然对齐。回滚后内存态退回 prev，与 DB 一致；
			// 同时把判定结果视为「未翻转」返回 false，避免上层据此触发快照重建
			// （重建会从未更新的 DB 读到旧值，与「已翻转」语义矛盾）。
			h.mu.Lock()
			if cur := h.states[u.ID]; cur != nil {
				// 仅当内存态仍是本次翻转后的值时才回滚，避免覆盖期间其他探测的更新。
				if cur.healthy == newHealthy {
					cur.healthy = prev
				}
			}
			h.mu.Unlock()
			h.logger.Warn("更新上游健康状态失败，已回滚内存态以保持与DB一致",
				"upstreamID", u.ID, "host", u.Host, "port", u.Port, "rolledBackTo", prev, "err", err)
			return false
		} else if newHealthy {
			// 恢复：连续成功达阈值，重新纳入加权轮训（运维关注：哪个上游何时恢复）。
			h.logger.Info("上游已恢复可用，重新纳入轮训",
				"upstreamID", u.ID, "host", u.Host, "port", u.Port, "groupID", u.GroupID)
		} else {
			// 剔除：连续失败达阈值，移出加权轮训（Warn 级，运维关注：哪个上游何时挂了）。
			h.logger.Warn("上游标记为不可用，已从轮训剔除",
				"upstreamID", u.ID, "host", u.Host, "port", u.Port, "groupID", u.GroupID)
		}
	}
	return flipped
}

// isHealthy 返回某上游当前健康判定（内存态）。
func (h *HealthChecker) isHealthy(id int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.states[id]; st != nil {
		return st.healthy
	}
	return false
}

// LatencyMs 返回某上游最近一次探测延迟（毫秒，未探测过返回 0），供前端展示。
func (h *HealthChecker) LatencyMs(id int64) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.states[id]; st != nil {
		return st.latencyMs
	}
	return 0
}

// TestProxy 立即对单条上游发起一次探测并返回结果（AC-38），不影响周期判定计数。
func (h *HealthChecker) TestProxy(ctx context.Context, u store.UpstreamProxy, mode store.HealthMode, probeURL string) ProbeResult {
	return h.prober.Probe(ctx, u, mode, probeURL)
}

// DisableGroup 手动停掉某组的健康检查（AC-18 组级开关）。
func (h *HealthChecker) DisableGroup(groupID int64) {
	h.mu.Lock()
	h.disabledGroups[groupID] = struct{}{}
	h.mu.Unlock()
}

// EnableGroup 恢复某组的健康检查。
func (h *HealthChecker) EnableGroup(groupID int64) {
	h.mu.Lock()
	delete(h.disabledGroups, groupID)
	h.mu.Unlock()
}

// isGroupDisabled 判断某组健康检查是否被手动停掉。
func (h *HealthChecker) isGroupDisabled(groupID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.disabledGroups[groupID]
	return ok
}

// SetUpstreamEnabled 手动启用/禁用单条上游（AC-18），落库并刷新快照。
func (h *HealthChecker) SetUpstreamEnabled(id int64, enabled bool) error {
	if err := h.store.SetUpstreamEnabled(id, enabled); err != nil {
		return err
	}
	if h.refresher != nil {
		return h.refresher.Refresh()
	}
	return nil
}
