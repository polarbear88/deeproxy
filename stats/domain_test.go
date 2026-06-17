package stats

import (
	"sync"
	"testing"
)

// domain_test.go：目标域名计数维度 IncDomain / CollectDomainDeltas / eviction 测试。

// TestIncDomainAndCollectDelta 验证命中差分：首次返回增量、二次空、推进基线。
func TestIncDomainAndCollectDelta(t *testing.T) {
	c := NewCounter()

	// 空 host 不计入（防御性守卫）。
	c.IncDomain("", 1)
	if len(c.CollectDomainDeltas()) != 0 {
		t.Fatal("空 host 不应产生增量")
	}

	c.IncDomain("a.com", 1)
	c.IncDomain("a.com", 1)
	c.IncDomain("b.com", 2)

	d1 := c.CollectDomainDeltas()
	if len(d1) != 2 {
		t.Fatalf("首轮应有 2 个域名增量，得到 %d: %+v", len(d1), d1)
	}
	got := map[string]int64{}
	for _, s := range d1 {
		got[s.Domain] = s.HitCount
	}
	if got["a.com"] != 2 || got["b.com"] != 1 {
		t.Fatalf("增量错误: %+v", got)
	}

	// 二次无新命中：差分为空（基线已推进）。
	if d2 := c.CollectDomainDeltas(); len(d2) != 0 {
		t.Fatalf("无新命中应返回空增量，得到 %+v", d2)
	}

	// 再命中 a.com 一次：仅 a.com 返回增量 1。
	c.IncDomain("a.com", 1)
	d3 := c.CollectDomainDeltas()
	if len(d3) != 1 || d3[0].Domain != "a.com" || d3[0].HitCount != 1 {
		t.Fatalf("第三轮应仅 a.com=1，得到 %+v", d3)
	}
}

// TestDomainGroupDimensionSeparate 验证 (域名,分组) 是独立维度：同名不同组分开计数。
func TestDomainGroupDimensionSeparate(t *testing.T) {
	c := NewCounter()
	c.IncDomain("x.com", 1)
	c.IncDomain("x.com", 2)
	c.IncDomain("x.com", 2)

	d := c.CollectDomainDeltas()
	if len(d) != 2 {
		t.Fatalf("同名不同组应为 2 个维度，得到 %d: %+v", len(d), d)
	}
	got := map[int64]int64{}
	for _, s := range d {
		if s.Domain != "x.com" {
			t.Fatalf("域名应均为 x.com，得到 %q", s.Domain)
		}
		got[s.GroupID] = s.HitCount
	}
	if got[1] != 1 || got[2] != 2 {
		t.Fatalf("分组维度计数错误: %+v", got)
	}
}

// TestDomainEviction 验证内存有界：闲置达阈值后 key 被回收，重建后计数从新增量起算。
func TestDomainEviction(t *testing.T) {
	c := NewCounter()

	c.IncDomain("idle.com", 1)
	// 第一轮 collect：得增量、idleCycles 归零。
	if d := c.CollectDomainDeltas(); len(d) != 1 || d[0].HitCount != 1 {
		t.Fatalf("首轮应有 idle.com=1，得到 %+v", d)
	}

	// 此后停止命中，连续 collect：每轮零增量累加 idleCycles，达 evictAfterIdleCycles 触发回收。
	// 需要 evictAfterIdleCycles 轮零增量后，下一轮的 eviction 才执行（该轮把 idleCycles 推到阈值并加入回收）。
	for i := 0; i < evictAfterIdleCycles; i++ {
		c.CollectDomainDeltas()
	}

	c.domMu.RLock()
	n := len(c.domains)
	c.domMu.RUnlock()
	if n != 0 {
		t.Fatalf("闲置 %d 周期后 domains map 应收缩到 0，得到 %d", evictAfterIdleCycles, n)
	}

	// 回收后再命中同域名：以全新计数器重建，差分从新增量起算（不丢不重）。
	c.IncDomain("idle.com", 1)
	c.IncDomain("idle.com", 1)
	d := c.CollectDomainDeltas()
	if len(d) != 1 || d[0].Domain != "idle.com" || d[0].HitCount != 2 {
		t.Fatalf("重建后应为 idle.com=2，得到 %+v", d)
	}
}

// TestDomainBytesSurviveIdleHitsThenFlush 复现并守护 C1：命中在「连接打开」埋点、字节在
// 「连接关闭」埋点，二者时间错位。模拟长连接——先记一次命中（hits 增量落库后连续多轮零增量
// 进入闲置临界），随后连接关闭才到来的字节必须仍被 EMIT 出来，且该 key 不得在尾部字节
// 落库前被 eviction 回收（旧 hits-only 逻辑会误删并静默丢字节）。
func TestDomainBytesSurviveIdleHitsThenFlush(t *testing.T) {
	c := NewCounter()

	const host = "longlived.com"
	const grp int64 = 1

	// 连接打开：记一次命中。首轮 collect 应得命中增量 1、字节 0。
	c.IncDomain(host, grp)
	d0 := c.CollectDomainDeltas()
	if len(d0) != 1 || d0[0].HitCount != 1 || d0[0].Bytes != 0 {
		t.Fatalf("首轮应为命中=1 字节=0，得到 %+v", d0)
	}

	// 连接仍在中继：命中侧连续多轮零增量。跑到 eviction 阈值减 1 轮——
	// 此时该 key 的 idleCycles 已逼近阈值，但还未触发回收（再多一轮零增量才会被删）。
	for i := 0; i < evictAfterIdleCycles-1; i++ {
		if d := c.CollectDomainDeltas(); len(d) != 0 {
			t.Fatalf("闲置周期不应有增量，第 %d 轮得到 %+v", i, d)
		}
	}

	// 连接关闭：尾部字节到来。此刻 key 仍应存活（字节差分依赖它）。
	c.AddDomainBytes(host, grp, 4096)

	d1 := c.CollectDomainDeltas()
	if len(d1) != 1 {
		t.Fatalf("尾部字节到来后应 EMIT 1 条增量（C1：不可因命中闲置丢弃），得到 %+v", d1)
	}
	if d1[0].Domain != host || d1[0].Bytes != 4096 {
		t.Fatalf("应为 %s 字节=4096，得到 %+v", host, d1[0])
	}
	if d1[0].HitCount != 0 {
		t.Fatalf("本轮无新命中，HitCount 应为 0，得到 %d", d1[0].HitCount)
	}

	// 关键断言：携带未 flush 字节期间，key 不得被回收（字节增量已重置 idleCycles）。
	c.domMu.RLock()
	_, alive := c.domains[domainKey{domain: host, groupID: grp}]
	c.domMu.RUnlock()
	if !alive {
		t.Fatal("携带过未 flush 字节的 key 被误删——C1 eviction 安全闸门失效")
	}
}

// TestDomainBytesEvictionGate 验证字节维度的 eviction 安全闸门：当 lastBytes 尚未追平
// 累计 bytes 时（即有未 flush 字节），即便命中早已闲置，也绝不删除该 key。
func TestDomainBytesEvictionGate(t *testing.T) {
	c := NewCounter()
	const host = "evict.com"
	const grp int64 = 1

	// 先制造一条只在字节维度活跃、命中维度长期零增量的 key。
	c.AddDomainBytes(host, grp, 100)
	c.CollectDomainDeltas() // 字节增量被收走，lastBytes 追平，idleCycles=0

	// 此后命中、字节都不再增长，连续闲置达阈值——此时两个基线都已追平，允许回收。
	for i := 0; i < evictAfterIdleCycles; i++ {
		c.CollectDomainDeltas()
	}
	c.domMu.RLock()
	n := len(c.domains)
	c.domMu.RUnlock()
	if n != 0 {
		t.Fatalf("命中与字节均追平且闲置达阈值后应被回收，仍剩 %d", n)
	}
}
// （含 eviction），供 -race 检测数据竞争。验证累计总命中数守恒（落库总和 == 注入总数）。
func TestDomainConcurrentIncAndCollect(t *testing.T) {
	c := NewCounter()

	const (
		goroutines = 20
		perG       = 500
	)

	var wg sync.WaitGroup
	var collected int64 // 经 CollectDomainDeltas 收集的命中总和
	var mu sync.Mutex
	stop := make(chan struct{})

	// flush goroutine：周期性收集增量（含 eviction），累加到 collected。
	var fwg sync.WaitGroup
	fwg.Add(1)
	go func() {
		defer fwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				for _, s := range c.CollectDomainDeltas() {
					mu.Lock()
					collected += s.HitCount
					mu.Unlock()
				}
			}
		}
	}()

	// 并发命中：每个 goroutine 命中固定一组域名 perG 次。
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			host := []string{"a.com", "b.com", "c.com"}[g%3]
			for i := 0; i < perG; i++ {
				c.IncDomain(host, int64(g%2)+1)
			}
		}(g)
	}
	wg.Wait()
	close(stop)
	fwg.Wait()

	// 收尾再收集一次，确保尾部增量与未回收的活跃 key 都计入。
	for _, s := range c.CollectDomainDeltas() {
		collected += s.HitCount
	}

	want := int64(goroutines * perG)
	if collected != want {
		t.Fatalf("命中总数应守恒为 %d，实际收集 %d", want, collected)
	}
}
