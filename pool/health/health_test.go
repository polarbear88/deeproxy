package health

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"deeproxy/store"
)

// health_test.go：健康检查 worker 测试（mock prober + 真实 store）。
// 由 AC-43 重构从 pool 包拆分到本 health 子包（health worker 依赖 store，
// 不属于转发热路径，与纯净的 pool.Selector 隔离）。

// scriptedProber 是按脚本返回结果的 mock 探测器（按 upstream ID 取下一个结果）。
type scriptedProber struct {
	mu      sync.Mutex
	results map[int64][]bool
	idx     map[int64]int
}

func newScriptedProber() *scriptedProber {
	return &scriptedProber{results: map[int64][]bool{}, idx: map[int64]int{}}
}
func (p *scriptedProber) set(id int64, seq ...bool) { p.results[id] = seq }
func (p *scriptedProber) Probe(_ context.Context, up store.UpstreamProxy, _ store.HealthMode, _ string) ProbeResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq := p.results[up.ID]
	if len(seq) == 0 {
		return ProbeResult{OK: true, Latency: time.Millisecond}
	}
	i := p.idx[up.ID]
	if i >= len(seq) {
		i = len(seq) - 1
	}
	ok := seq[i]
	p.idx[up.ID]++
	if ok {
		return ProbeResult{OK: true, Latency: time.Millisecond}
	}
	return ProbeResult{OK: false, Latency: time.Millisecond, Err: "mock fail"}
}

// fakeRefresher 记录 Refresh 调用次数。
type fakeRefresher struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeRefresher) Refresh() error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil
}

// newHealthTestStore 建测试库并插入一个 Type B 组 + 一条上游。
func newHealthTestStore(t *testing.T) (*store.Store, store.Group, store.UpstreamProxy) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	g := store.Group{
		Name: "poolB", Type: store.TypeB,
		HCEnabled: true, HCMode: store.HealthPing, HCInterval: 1, HCFailThld: 3, HCRecvThld: 2,
	}
	if err := st.CreateGroup(&g); err != nil {
		t.Fatalf("建组失败: %v", err)
	}

	u := store.UpstreamProxy{
		GroupID: g.ID, Host: "127.0.0.1", Port: 9,
		Weight: 1, Enabled: true, HealthState: true,
	}
	if err := st.CreateUpstream(&u); err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	return st, g, u
}

// TestHealthThresholdFailRecover 验证连续失败3次标记不可用、连续成功2次恢复，翻转时落库+刷新。
func TestHealthThresholdFailRecover(t *testing.T) {
	st, g, u := newHealthTestStore(t)
	prober := newScriptedProber()
	prober.set(u.ID, false, false, false, true, true)
	refresher := &fakeRefresher{}
	hc := NewHealthChecker(st, prober, refresher, nil)

	failThld, recvThld := 3, 2
	flips := 0
	for i := 0; i < 5; i++ {
		ups, _ := st.ListAllUpstreams()
		if hc.applyResult(ups[0], prober.Probe(context.Background(), ups[0], g.HCMode, g.HCURL), failThld, recvThld) {
			flips++
		}
	}
	if flips != 2 {
		t.Fatalf("期望 2 次健康翻转，得到 %d", flips)
	}

	ups, _ := st.ListAllUpstreams()
	if !ups[0].HealthState {
		t.Fatalf("最终应恢复健康，库中 HealthState=%v", ups[0].HealthState)
	}
}

// TestHealthSkipsTypeA 验证 G2：scanOnce 跳过 Type A 组（不探测）。
func TestHealthSkipsTypeA(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "typea.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()

	ga := store.Group{Name: "dynA", Type: store.TypeA, HCEnabled: true, HCInterval: 1}
	if err := st.CreateGroup(&ga); err != nil {
		t.Fatalf("建 Type A 组失败: %v", err)
	}

	probed := false
	prober := proberFunc(func(_ context.Context, _ store.UpstreamProxy, _ store.HealthMode, _ string) ProbeResult {
		probed = true
		return ProbeResult{OK: true}
	})
	hc := NewHealthChecker(st, prober, &fakeRefresher{}, nil)
	hc.scanOnce(context.Background(), map[int64]time.Time{})

	if probed {
		t.Fatal("G2：Type A 组不应被探测")
	}
}

// proberFunc 把函数适配为 Prober。
type proberFunc func(context.Context, store.UpstreamProxy, store.HealthMode, string) ProbeResult

func (f proberFunc) Probe(ctx context.Context, up store.UpstreamProxy, m store.HealthMode, url string) ProbeResult {
	return f(ctx, up, m, url)
}

// TestManualToggle 验证手动启停单条上游落库 + 组级停启健康检查开关。
func TestManualToggle(t *testing.T) {
	st, g, u := newHealthTestStore(t)
	hc := NewHealthChecker(st, newScriptedProber(), &fakeRefresher{}, nil)

	if err := hc.SetUpstreamEnabled(u.ID, false); err != nil {
		t.Fatalf("禁用上游失败: %v", err)
	}
	ups, _ := st.ListAllUpstreams()
	if ups[0].Enabled {
		t.Fatal("上游应被禁用")
	}

	hc.DisableGroup(g.ID)
	if !hc.isGroupDisabled(g.ID) {
		t.Fatal("组健康检查应被停掉")
	}
	hc.EnableGroup(g.ID)
	if hc.isGroupDisabled(g.ID) {
		t.Fatal("组健康检查应被恢复")
	}
}

// TestApplyResult_PersistFailRollback 覆盖 FIX-H6：落库失败时回滚内存态，保证内存态=DB态。
//
// 构造手法：先建好健康(HealthState=true)的上游，再【DROP upstream_proxy 表】，
// 使后续 UpdateUpstreamHealth 的 UPDATE 返回 “no such table” 错误（注意：不能用
// st.Close() 制造失败——关闭后单写协程退出，而 Write 仍可能把 op 投进缓冲 writeCh
// 却永远等不到 done，导致测试死锁；DROP 表则让单写协程存活、Exec 确定性报错）。
// 然后连续投喂 failThld 次失败探测触发「true→false」翻转：因落库失败，applyResult
// 必须回滚内存态（isHealthy 仍为 true）并返回 false（视为未翻转，避免触发快照重建）。
func TestApplyResult_PersistFailRollback(t *testing.T) {
	st, g, u := newHealthTestStore(t) // 初始 HealthState=true
	prober := newScriptedProber()
	refresher := &fakeRefresher{}
	hc := NewHealthChecker(st, prober, refresher, nil)

	failThld, recvThld := 3, 2

	// DROP 表：经单写协程串行执行，确保后续 UpdateUpstreamHealth 的 UPDATE 确定性失败，
	// 且单写协程仍存活（不会像 Close 那样使 Write 死锁）。
	if err := st.Write(func(db *sql.DB) error {
		_, e := db.Exec(`DROP TABLE upstream_proxy`)
		return e
	}); err != nil {
		t.Fatalf("DROP upstream_proxy 失败: %v", err)
	}

	// 连续 failThld 次失败：前 failThld-1 次未达阈值不翻转、不落库（返回 false 属正常）；
	// 第 failThld 次本应翻转 true→false 并落库，但落库失败 → 必须回滚 + 返回 false。
	failRes := ProbeResult{OK: false, Latency: time.Millisecond, Err: "mock fail"}
	var lastFlip bool
	for i := 0; i < failThld; i++ {
		lastFlip = hc.applyResult(u, failRes, failThld, recvThld)
	}

	// 断言 1：达阈值那次因落库失败被视为「未翻转」。
	if lastFlip {
		t.Fatalf("落库失败时 applyResult 应返回 false（视为未翻转），却返回 true")
	}
	// 断言 2：内存态已回滚，仍为健康 true（与未更新的 DB 一致），未发生不一致翻转。
	if !hc.isHealthy(u.ID) {
		t.Fatalf("落库失败后内存态应回滚为 true(healthy)，实际为 unhealthy —— 内存态与DB不一致")
	}
	_ = g
}

// TestProbePoolBoundsConcurrency 验证 DEC-C1（AC-5.3）：全局探测池限制并发数 ≤ probe_pool_size。
//
// 构造：一个 Type B 组含 N 条上游，把 probe_pool_size 设为较小的 limit；用一个会阻塞片刻并
// 记录「当前在飞探测数」峰值的 prober 跑一轮 scanOnce，断言观测到的峰值并发不超过 limit，
// 且确实达到了并发（>1，证明不是退回串行）。
func TestProbePoolBoundsConcurrency(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()

	// 设置全局池大小为 limit。
	const limit = 4
	ss, _ := st.GetSystemSetting()
	ss.ProbePoolSize = limit
	if err := st.UpdateSystemSetting(ss); err != nil {
		t.Fatalf("设置 probe_pool_size 失败: %v", err)
	}

	g := store.Group{
		Name: "bigpool", Type: store.TypeB,
		HCEnabled: true, HCMode: store.HealthPing, HCInterval: 1, HCFailThld: 3, HCRecvThld: 2,
	}
	if err := st.CreateGroup(&g); err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	const n = 20
	for i := 0; i < n; i++ {
		u := store.UpstreamProxy{GroupID: g.ID, Host: "127.0.0.1", Port: 9, Weight: 1, Enabled: true, HealthState: true}
		if err := st.CreateUpstream(&u); err != nil {
			t.Fatalf("建上游失败: %v", err)
		}
	}

	// 记录在飞并发峰值的 prober：进入时 +1、记录峰值、阻塞片刻、退出时 -1。
	var (
		mu       sync.Mutex
		inflight int
		maxSeen  int
	)
	prober := proberFunc(func(_ context.Context, _ store.UpstreamProxy, _ store.HealthMode, _ string) ProbeResult {
		mu.Lock()
		inflight++
		if inflight > maxSeen {
			maxSeen = inflight
		}
		mu.Unlock()

		time.Sleep(30 * time.Millisecond) // 制造重叠窗口，逼出并发峰值

		mu.Lock()
		inflight--
		mu.Unlock()
		return ProbeResult{OK: true, Latency: time.Millisecond}
	})

	hc := NewHealthChecker(st, prober, &fakeRefresher{}, nil)
	hc.scanOnce(context.Background(), map[int64]time.Time{})

	mu.Lock()
	got := maxSeen
	mu.Unlock()

	if got > limit {
		t.Fatalf("并发峰值 %d 超过池上限 %d（全局池未生效）", got, limit)
	}
	if got <= 1 {
		t.Fatalf("并发峰值仅 %d，说明退回串行而非并发探测", got)
	}
}

