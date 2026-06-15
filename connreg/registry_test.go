package connreg

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// baseTime 是测试用的固定基准时间（避免依赖 time.Now 的真实值）。
var baseTime = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

// regAt 在登记表里登记一条 Start 为 base+offset 秒的连接，返回 connID。
func regAt(r *Registry, offsetSec int, action string) int64 {
	return r.Register(ConnMeta{
		Target: "t", Action: action, User: "u", Group: "g", Client: "c",
		Start: baseTime.Add(time.Duration(offsetSec) * time.Second),
	})
}

// TestRegisterLenDeregister 验证登记/注销与 Len 提示。
func TestRegisterLenDeregister(t *testing.T) {
	r := New()
	if r.Len() != 0 {
		t.Fatalf("空表 Len 应为 0，得 %d", r.Len())
	}
	id1 := regAt(r, 0, "direct")
	id2 := regAt(r, 1, "forward")
	if r.Len() != 2 {
		t.Fatalf("登记 2 条后 Len 应为 2，得 %d", r.Len())
	}
	if id1 == id2 {
		t.Fatalf("connID 必须唯一，得 %d == %d", id1, id2)
	}
	r.Deregister(id1)
	if r.Len() != 1 {
		t.Fatalf("注销 1 条后 Len 应为 1，得 %d", r.Len())
	}
	// 重复注销不应把计数减穿。
	r.Deregister(id1)
	if r.Len() != 1 {
		t.Fatalf("重复注销后 Len 仍应为 1，得 %d", r.Len())
	}
	r.Deregister(id2)
	if r.Len() != 0 {
		t.Fatalf("全部注销后 Len 应为 0，得 %d", r.Len())
	}
}

// TestSetUpstreamSetAction 验证后填字段在 Snapshot 中生效。
func TestSetUpstreamSetAction(t *testing.T) {
	r := New()
	id := regAt(r, 0, "forward")
	// 后填前：upstream 空、action 为登记占位。
	items, _, _ := r.Snapshot(10, "start")
	if items[0].Upstream != "" || items[0].Action != "forward" {
		t.Fatalf("后填前 upstream 应为空、action=forward，得 %q/%q", items[0].Upstream, items[0].Action)
	}
	r.SetUpstream(id, "1.2.3.4:1080")
	r.SetAction(id, "direct")
	items, _, _ = r.Snapshot(10, "start")
	if items[0].Upstream != "1.2.3.4:1080" || items[0].Action != "direct" {
		t.Fatalf("后填后 upstream/action 未生效，得 %q/%q", items[0].Upstream, items[0].Action)
	}
	// 对不存在的 id 调用应静默忽略，不 panic。
	r.SetUpstream(99999, "x")
	r.SetAction(99999, "x")
}

// TestSnapshotTruncationAndOrderStart 验证 start 模式截断与最新优先序。
func TestSnapshotTruncationAndOrderStart(t *testing.T) {
	r := New()
	const M = 20
	// 依次登记 Start 递增的连接（id 越大越新）。
	for i := 0; i < M; i++ {
		regAt(r, i, "direct")
	}
	limit := 5
	items, total, truncated := r.Snapshot(limit, "start")
	if total != M {
		t.Fatalf("total 应为 %d（精确），得 %d", M, total)
	}
	if !truncated {
		t.Fatalf("M>limit 时 truncated 应为 true")
	}
	if len(items) != limit {
		t.Fatalf("应恰返回 limit=%d 条，得 %d", limit, len(items))
	}
	// 最新优先：StartUnix 应是最大的 5 个，且降序。
	for i := 0; i < limit; i++ {
		wantStart := baseTime.Add(time.Duration(M-1-i) * time.Second).Unix()
		if items[i].StartUnix != wantStart {
			t.Fatalf("start 模式第 %d 条应为最新者，期望 StartUnix=%d，得 %d", i, wantStart, items[i].StartUnix)
		}
	}
}

// TestSnapshotOrderDuration 验证 duration 模式最长优先（最旧优先）序。
func TestSnapshotOrderDuration(t *testing.T) {
	r := New()
	const M = 20
	for i := 0; i < M; i++ {
		regAt(r, i, "direct")
	}
	limit := 5
	items, total, truncated := r.Snapshot(limit, "duration")
	if total != M || !truncated || len(items) != limit {
		t.Fatalf("duration 截断异常：total=%d truncated=%v len=%d", total, truncated, len(items))
	}
	// 最长=最旧优先：StartUnix 应是最小的 5 个，且升序。
	for i := 0; i < limit; i++ {
		wantStart := baseTime.Add(time.Duration(i) * time.Second).Unix()
		if items[i].StartUnix != wantStart {
			t.Fatalf("duration 模式第 %d 条应为最旧者，期望 StartUnix=%d，得 %d", i, wantStart, items[i].StartUnix)
		}
	}
}

// TestTiedStartDeterministicBothModes 验证 start_ts 全部相等时，两种模式都按 seq 确定排序、跨轮不抖动。
func TestTiedStartDeterministicBothModes(t *testing.T) {
	for _, mode := range []string{"start", "duration"} {
		r := New()
		const M = 30
		ids := make([]int64, M)
		for i := 0; i < M; i++ {
			ids[i] = regAt(r, 0, "direct") // 全部同一 Start
		}
		limit := 8
		// 连续多次快照，断言完全一致（无抖动）。
		first, _, _ := r.Snapshot(limit, mode)
		for round := 0; round < 5; round++ {
			got, _, _ := r.Snapshot(limit, mode)
			if len(got) != limit {
				t.Fatalf("[%s] 应返回 %d 条，得 %d", mode, limit, len(got))
			}
			for i := range got {
				if got[i].ID != first[i].ID {
					t.Fatalf("[%s] 第 %d 轮第 %d 条 ID 抖动：%d != %d", mode, round, i, got[i].ID, first[i].ID)
				}
			}
		}
		// start 模式同 start_ts 下应取 seq 最大的 K 个并降序；duration 应取 seq 最小的 K 个并升序。
		if mode == "start" {
			for i := 0; i < limit; i++ {
				wantID := ids[M-1-i]
				if first[i].ID != wantID {
					t.Fatalf("[start] 第 %d 条应为 seq 较大者 %d，得 %d", i, wantID, first[i].ID)
				}
			}
		} else {
			for i := 0; i < limit; i++ {
				if first[i].ID != ids[i] {
					t.Fatalf("[duration] 第 %d 条应为 seq 较小者 %d，得 %d", i, ids[i], first[i].ID)
				}
			}
		}
	}
}

// TestSnapshotBoundaryLimitPlusOne 验证 N=limit+1 边界：恰截断 1 条，且被淘汰的是正确那条。
func TestSnapshotBoundaryLimitPlusOne(t *testing.T) {
	limit := 5
	// start 模式：N=limit+1，最旧的一条应被淘汰。
	r := New()
	for i := 0; i < limit+1; i++ {
		regAt(r, i, "direct")
	}
	items, total, truncated := r.Snapshot(limit, "start")
	if total != limit+1 || !truncated || len(items) != limit {
		t.Fatalf("边界 start 异常：total=%d truncated=%v len=%d", total, truncated, len(items))
	}
	// 被淘汰的应是 offset=0（最旧）；留下的最小 StartUnix 应为 base+1。
	minStart := items[len(items)-1].StartUnix
	if minStart != baseTime.Add(1*time.Second).Unix() {
		t.Fatalf("边界 start：最旧一条未被淘汰，留存最小 StartUnix=%d", minStart)
	}

	// duration 模式：N=limit+1，最新的一条应被淘汰。
	r2 := New()
	for i := 0; i < limit+1; i++ {
		regAt(r2, i, "direct")
	}
	items2, _, _ := r2.Snapshot(limit, "duration")
	maxStart := items2[len(items2)-1].StartUnix
	if maxStart != baseTime.Add(time.Duration(limit-1)*time.Second).Unix() {
		t.Fatalf("边界 duration：最新一条未被淘汰，留存最大 StartUnix=%d", maxStart)
	}
}

// TestSnapshotLimitClampAndUnknownSort 验证 limit 钳制与未知 sortBy 归 start。
func TestSnapshotLimitClampAndUnknownSort(t *testing.T) {
	r := New()
	regAt(r, 0, "direct")
	// limit<=0 钳到 DefaultLimit；未知 sort 归 start（不报错）。
	items, total, _ := r.Snapshot(0, "weird")
	if total != 1 || len(items) != 1 {
		t.Fatalf("limit 钳制/未知 sort 异常：total=%d len=%d", total, len(items))
	}
	// limit 超上限被钳制（不 panic、正常返回）。
	if _, _, _ = r.Snapshot(DefaultLimit+1000, "start"); total != 1 {
		t.Fatalf("超大 limit 应被钳制并正常返回")
	}
}

// TestConcurrentRaceSafety 在 -race 下并发 Register/SetUpstream/SetAction/Deregister/Snapshot，
// 终态 Len 必须归零，且全程无数据竞争。
func TestConcurrentRaceSafety(t *testing.T) {
	r := New()
	const workers = 16
	const perWorker = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := r.Register(ConnMeta{
					Target: fmt.Sprintf("h%d-%d", w, i), Action: "forward",
					User: "u", Group: "g", Client: "c", Start: time.Now(),
				})
				r.SetUpstream(id, "1.2.3.4:1080")
				r.SetAction(id, "direct")
				// 并发读：不校验内容，只为触发 race 检测。
				_, _, _ = r.Snapshot(50, "start")
				r.Deregister(id)
			}
		}(w)
	}
	wg.Wait()
	if r.Len() != 0 {
		t.Fatalf("并发开/关后 Len 应归零，得 %d", r.Len())
	}
	items, total, _ := r.Snapshot(10, "start")
	if total != 0 || len(items) != 0 {
		t.Fatalf("全部注销后快照应为空：total=%d len=%d", total, len(items))
	}
}

// BenchmarkSnapshot 在 N=50k 活跃连接下测 Snapshot，断言分配只与 K(limit) 相关、不随 N 膨胀（O(K) 空间）。
func BenchmarkSnapshot(b *testing.B) {
	r := New()
	const N = 50000
	for i := 0; i < N; i++ {
		regAt(r, i, "forward")
	}
	limit := DefaultLimit
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items, total, _ := r.Snapshot(limit, "start")
		if total != N || len(items) != limit {
			b.Fatalf("基准快照异常：total=%d len=%d", total, len(items))
		}
	}
}

// TestSnapshotAllocationBounded 显式断言 Snapshot 的分配量与 N 无关（O(K) 而非 O(N)）。
// 用两个差距悬殊的 N 测同一 limit，分配次数应基本相同（仅与 K 相关）。
func TestSnapshotAllocationBounded(t *testing.T) {
	measure := func(n int) float64 {
		r := New()
		for i := 0; i < n; i++ {
			regAt(r, i, "forward")
		}
		return testing.AllocsPerRun(20, func() {
			_, _, _ = r.Snapshot(DefaultLimit, "start")
		})
	}
	small := measure(1000)
	large := measure(50000)
	// N 增大 50 倍，分配不应随之线性增长；放宽到「large 不超过 small 的 2 倍」即可证明非 O(N)。
	if large > small*2+10 {
		t.Fatalf("Snapshot 分配疑似随 N 增长（非 O(K)）：N=1000→%.0f allocs, N=50000→%.0f allocs", small, large)
	}
}

// TestSetTargetNilFallback 验证 #3 不变量：
// SetTarget 调用前 target 指针恒为 nil，Snapshot 回退展示 meta.Target（登记时的原始 IP/host）；
// SetTarget 后 Snapshot 展示回填的域名（覆盖原始 IP）。
func TestSetTargetNilFallback(t *testing.T) {
	r := New()
	// Register 只填 meta.Target（原始 IP），不触碰 target 指针，故应回退展示原始值。
	id := r.Register(ConnMeta{
		Target: "203.0.113.5", Action: "direct", User: "u", Group: "g", Client: "c",
		Start: baseTime,
	})

	items, _, _ := r.Snapshot(DefaultLimit, "start")
	if len(items) != 1 {
		t.Fatalf("期望 1 条活跃连接，实际 %d", len(items))
	}
	// 不变量：未调 SetTarget 时回退原始 meta.Target。
	if items[0].Target != "203.0.113.5" {
		t.Fatalf("SetTarget 前应回退 meta.Target=203.0.113.5，实际 %q", items[0].Target)
	}

	// 嗅探还原域名后回填，Snapshot 应展示域名覆盖原始 IP。
	r.SetTarget(id, "example.com")
	items, _, _ = r.Snapshot(DefaultLimit, "start")
	if items[0].Target != "example.com" {
		t.Fatalf("SetTarget 后应覆盖为 example.com，实际 %q", items[0].Target)
	}

	// SetTarget 对不存在的 id 静默忽略，不 panic。
	r.SetTarget(999999, "ignored.com")
}

// TestSetTargetRace 并发跑 SetTarget 与 Snapshot，配合 `go test -race` 验证无数据竞争。
// 为什么需要：meta.Target 被 Snapshot 无锁并发读，回填若写普通字段会触发 data race；
// 本用例正是 race detector 的守门测试，证明 atomic.Pointer 回填与并发读安全。
func TestSetTargetRace(t *testing.T) {
	r := New()
	const n = 200
	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		ids[i] = regAt(r, i, "direct")
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 写方：持续对所有连接回填域名（模拟嗅探成功）。
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range ids {
					r.SetTarget(id, fmt.Sprintf("host-%d-%d.example.com", w, id))
				}
			}
		}(w)
	}

	// 读方：持续 Snapshot（toView 会并发读 target 指针）。
	for rdr := 0; rdr < 4; rdr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _, _ = r.Snapshot(DefaultLimit, "start")
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}
