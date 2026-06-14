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
	"golang.org/x/sync/semaphore"

	"deeproxy/auth"
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

// defaultProbePoolSize 是健康检查全局探测并发池默认大小（DEC-C1，AC-5.3）。
// 当系统设置未配置或值非法（<=0）时兜底为此值。
const defaultProbePoolSize = 150

// ProbeResult 是一次探测的结果（供 TestProxy 立即探测 AC-38 返回）。
type ProbeResult struct {
	OK      bool          // 是否连通
	Latency time.Duration // 探测耗时
	Err     string        // 失败原因（OK=false 时）
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
	// 用户名本身即模板：服务端主动探测无客户端变量，对 {var} 做空值填充（缺值补空）后认证。
	probeUser := auth.SubstituteTemplate(up.User, nil)
	var pAuth *proxy.Auth
	if probeUser != "" {
		pAuth = &proxy.Auth{User: probeUser, Password: up.Pwd}
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

	// DEC-C1（AC-5.3）：本轮探测共用一个全局并发池（信号量），所有分组所有探测受同一上限约束，
	// 避免单组上游数千时串行拖垮、也避免无上限并发打爆 fd。池大小每轮开头从系统设置重新读取生效
	// （「下一轮采用新大小」语义），不做在途 resize，避免过度工程。
	poolSize := h.readProbePoolSize()
	sem := semaphore.NewWeighted(int64(poolSize))

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

		if h.probeGroup(ctx, sem, g, byGroup[g.ID]) {
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
//
// 并发模型（DEC-C1，AC-5.3）：组内每条启用的上游各起一个 goroutine 探测，
// 但都需先从【全局共享信号量 sem】获取一个令牌（acquire），探完释放（release）；
// 因此总并发被 sem 容量（probe_pool_size）严格限制，所有分组共用同一池、互不串行拖累。
// applyResult 内部持 h.mu，故并发写 h.states 安全；changed/healthyCount 用各自互斥保护。
func (h *HealthChecker) probeGroup(ctx context.Context, sem *semaphore.Weighted, g store.Group, ups []store.UpstreamProxy) bool {
	failThld := g.HCFailThld
	if failThld <= 0 {
		failThld = 3
	}
	recvThld := g.HCRecvThld
	if recvThld <= 0 {
		recvThld = 2
	}

	var (
		wg           sync.WaitGroup
		aggMu        sync.Mutex // 保护 changed / healthyCount 的并发累加
		changed      bool
		healthyCount int
	)
	for _, u := range ups {
		// 被手动禁用的上游不探测，但仍计入“不可用”（不参与选择）。
		if !u.Enabled {
			continue
		}
		// 从全局池获取令牌；ctx 取消（关停）时退出，不再发起新探测。
		if err := sem.Acquire(ctx, 1); err != nil {
			break
		}
		wg.Add(1)
		go func(u store.UpstreamProxy) {
			defer wg.Done()
			defer sem.Release(1)

			// per-probe context：单条探测超时独立，避免一条慢探测拖住令牌过久。
			pctx, cancel := context.WithTimeout(ctx, defaultProbeTimeout)
			defer cancel()

			res := h.prober.Probe(pctx, u, g.HCMode, g.HCURL)
			flipped := h.applyResult(u, res, failThld, recvThld)
			healthy := h.isHealthy(u.ID)

			aggMu.Lock()
			if flipped {
				changed = true
			}
			if healthy {
				healthyCount++
			}
			aggMu.Unlock()
		}(u)
	}
	wg.Wait()

	// 整组全挂告警（AC-17 的拒连由 Selector 在选择期返回 ErrNoUpstream 实现；此处仅日志）。
	if len(ups) > 0 && healthyCount == 0 {
		h.logger.Warn("代理组全部上游不可用", "group", g.Name, "groupID", g.ID)
	}
	return changed
}

// readProbePoolSize 从系统设置读取健康检查全局池大小；读失败或非法值兜底为默认 150。
//
// 每轮 scanOnce 开头调用一次（DEC-C1「下一轮采用新大小」语义）：后台改了池大小后，
// 下一轮探测即采用新容量，无需重启、也不做在途信号量 resize（避免过度工程）。
func (h *HealthChecker) readProbePoolSize() int {
	ss, err := h.store.GetSystemSetting()
	if err != nil || ss == nil || ss.ProbePoolSize <= 0 {
		return defaultProbePoolSize
	}
	return ss.ProbePoolSize
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

// TestProxy 立即对单条上游发起一次探测并返回结果（AC-38）。
//
// 与周期判定不同：手动测试本质即一次探测，结果立即生效——把测得延迟与健康状态直接写回内存态，
// 健康状态有变化时同步落库，使前端列表刷新后立即看到新延迟与健康标记（不走连续阈值计数）。
// 返回值 flipped 表示本次测试是否翻转了健康状态，供调用方决定是否需要重建路由快照（未翻转则无需重建）。
func (h *HealthChecker) TestProxy(ctx context.Context, u store.UpstreamProxy, mode store.HealthMode, probeURL string) (ProbeResult, bool) {
	res := h.prober.Probe(ctx, u, mode, probeURL)
	flipped := h.applyImmediate(u, res)
	return res, flipped
}

// applyImmediate 把一次探测结果【立即生效】地写回内存态（延迟+健康），健康翻转时落库。返回健康是否翻转。
//
// 用于手动测试与「新增即检查」：这两种场景都期望单次探测结果立刻反映，单次即翻转、不经周期判定的
// 连续阈值计数；为避免污染周期判定的抗抖动窗口，本方法【不修改】consecutiveOK/Fail 计数，
// 只更新 healthy 与 latencyMs。落库失败回滚内存态，保持「内存态 = DB 态」不变式（同 applyResult）。
func (h *HealthChecker) applyImmediate(u store.UpstreamProxy, res ProbeResult) bool {
	h.mu.Lock()
	st := h.states[u.ID]
	if st == nil {
		st = &nodeState{healthy: u.HealthState}
		h.states[u.ID] = st
	}
	prevHealthy := st.healthy
	if res.OK {
		st.latencyMs = res.Latency.Milliseconds() // 成功：写回本次延迟供列表展示
	}
	// 单次即翻转，不动周期判定的连续计数窗口（与 applyResult 的阈值计数互不干扰）。
	st.healthy = res.OK
	newHealthy := st.healthy
	h.mu.Unlock()

	if newHealthy == prevHealthy {
		return false
	}
	if err := h.store.UpdateUpstreamHealth(u.ID, newHealthy); err != nil {
		h.mu.Lock()
		if cur := h.states[u.ID]; cur != nil && cur.healthy == newHealthy {
			cur.healthy = prevHealthy
		}
		h.mu.Unlock()
		h.logger.Warn("即时探测后更新上游健康状态失败，已回滚内存态",
			"upstreamID", u.ID, "host", u.Host, "port", u.Port, "err", err)
		return false // 落库失败视为未翻转，避免上层据此重建快照（DB 仍是旧值）
	}
	return true
}

// CheckNow 对指定上游【异步并发探测】一次（新增代理后调用，AC-10..AC-12）。
//
// 设计要点：
//   - 异步——立即返回，不阻塞新增请求路径（探测单条最长 defaultProbeTimeout，批量更久）；
//     探测在后台 goroutine 进行，与周期检查走同一套「翻转→落库→Refresh 刷新快照」通道。
//   - 自含——内部用独立的 context.Background 派生 ctx（含整体超时），不依赖调用方请求 ctx，
//     避免请求结束后 ctx 被 cancel 导致探测半途夭折。
//   - 仅当分组开启健康检查（HCEnabled 且未被运行期手动停掉）时才探测；被手动禁用（!Enabled）的上游跳过。
//   - 复用全局探测信号量限流；本批有任一健康翻转才 Refresh 一次（避免无谓全量重建）。
func (h *HealthChecker) CheckNow(g store.Group, ups []store.UpstreamProxy) {
	if !g.HCEnabled || h.isGroupDisabled(g.ID) || len(ups) == 0 {
		return
	}
	go func() {
		// 独立 ctx：不挂请求 ctx，整体设较宽超时覆盖批量并发探测。
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		sem := semaphore.NewWeighted(int64(h.readProbePoolSize()))
		var (
			wg      sync.WaitGroup
			aggMu   sync.Mutex
			changed bool
		)
		for _, u := range ups {
			if !u.Enabled {
				continue
			}
			if err := sem.Acquire(ctx, 1); err != nil {
				break // ctx 取消/超时时停止发起新探测
			}
			wg.Add(1)
			go func(u store.UpstreamProxy) {
				defer wg.Done()
				defer sem.Release(1)
				pctx, pcancel := context.WithTimeout(ctx, defaultProbeTimeout)
				defer pcancel()
				res := h.prober.Probe(pctx, u, g.HCMode, g.HCURL)
				if h.applyImmediate(u, res) {
					aggMu.Lock()
					changed = true
					aggMu.Unlock()
				}
			}(u)
		}
		wg.Wait()

		// 有任一健康翻转才刷新快照（与 scanOnce 一致的通道）。
		if changed && h.refresher != nil {
			if err := h.refresher.Refresh(); err != nil {
				h.logger.Warn("新增即检查后刷新快照失败", "groupID", g.ID, "err", err)
			}
		}
	}()
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
