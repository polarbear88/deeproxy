package snapbuild

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"deeproxy/config"
	"deeproxy/rule"
	"deeproxy/snapshot"
	"deeproxy/store"
)

// rebuild_test.go：从 store 物化快照 + 热替换 + G4 回滚测试（按 AC-43 从 snapshot 包拆来）。

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "snap.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// baseCfg 返回测试用启动引导配置。
// 注：取消配置文件后，默认动作/空闲/嗅探等已迁入 system_setting，由 Rebuild 从库读取；
// 故 def 参数不再写入 Config（保留入参仅为兼容既有调用点），默认动作以测试库 system_setting
// 的列默认值（forward）为准。需要非 forward 默认动作的用例应直接更新 store 后再 Rebuild。
func baseCfg(def string) *config.Config { _ = def; return &config.Config{} }

// TestRebuildMaterializesViews 验证从 SQLite 物化出分组/用户/授权/规则引擎。
func TestRebuildMaterializesViews(t *testing.T) {
	st := newTestStore(t)

	g := &store.Group{Name: "poolA", Type: store.TypeB}
	if err := st.CreateGroup(g); err != nil {
		t.Fatal(err)
	}
	up1 := &store.UpstreamProxy{GroupID: g.ID, Host: "1.1.1.1", Port: 1080, User: "acct-{region}", Weight: 3, Enabled: true, HealthState: true}
	up2 := &store.UpstreamProxy{GroupID: g.ID, Host: "2.2.2.2", Port: 1080, Weight: 1, Enabled: true, HealthState: false}
	_ = st.CreateUpstream(up1)
	_ = st.CreateUpstream(up2)

	u := &store.ProxyUser{Username: "alice", Pwd: "h"}
	_ = st.CreateProxyUser(u)
	_ = st.AddGroupUser(g.ID, u.ID)

	rg := &store.RuleGroup{Name: "rg", Scope: store.ScopeGroup}
	_ = st.CreateRuleGroup(rg)
	_ = st.CreateRule(&store.Rule{RuleGroupID: rg.ID, Match: "domain-suffix:example.com", Action: "direct", OrderIdx: 1})
	_ = st.AddGroupRuleGroup(g.ID, rg.ID)

	snap, err := Rebuild(st, baseCfg("forward"))
	if err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}

	gv, ok := snap.LookupGroup("poolA")
	if !ok {
		t.Fatal("应能按名查到分组")
	}
	if gv.Type != store.TypeB {
		t.Fatalf("分组类型错误: %v", gv.Type)
	}
	if len(gv.HealthyUpstreams) != 1 || gv.HealthyUpstreams[0].Host != "1.1.1.1" {
		t.Fatalf("健康节点列表错误: %+v", gv.HealthyUpstreams)
	}
	if len(gv.AllUpstreams) != 2 {
		t.Fatalf("全部上游应为 2，得到 %d", len(gv.AllUpstreams))
	}
	if act := gv.Engine.Match("www.example.com"); act != rule.ActionDirect {
		t.Fatalf("规则匹配错误: 期望 direct，得到 %v", act)
	}
	if act := gv.Engine.Match("other.com"); act != rule.ActionForward {
		t.Fatalf("默认动作错误: 期望 forward，得到 %v", act)
	}

	uv, ok := snap.LookupUser("alice")
	if !ok || uv.Pwd != "h" {
		t.Fatalf("用户视图错误: %+v %v", uv, ok)
	}
	if !snap.IsAuthorized(g.ID, u.ID) {
		t.Fatal("应判定为已授权")
	}
	if snap.IsAuthorized(g.ID, 9999) {
		t.Fatal("未授权用户不应通过")
	}
}

// TestGlobalBeforeGroupOrder 验证全局规则组排在分组规则组之前（AC-7 顺序）。
func TestGlobalBeforeGroupOrder(t *testing.T) {
	st := newTestStore(t)
	g := &store.Group{Name: "g", Type: store.TypeB}
	_ = st.CreateGroup(g)

	gr := &store.RuleGroup{Name: "global", Scope: store.ScopeGlobal}
	_ = st.CreateRuleGroup(gr)
	_ = st.CreateRule(&store.Rule{RuleGroupID: gr.ID, Match: "domain-suffix:example.com", Action: "reject", OrderIdx: 1})

	lr := &store.RuleGroup{Name: "local", Scope: store.ScopeGroup}
	_ = st.CreateRuleGroup(lr)
	_ = st.CreateRule(&store.Rule{RuleGroupID: lr.ID, Match: "domain-suffix:example.com", Action: "direct", OrderIdx: 1})
	_ = st.AddGroupRuleGroup(g.ID, lr.ID)

	snap, err := Rebuild(st, baseCfg("forward"))
	if err != nil {
		t.Fatal(err)
	}
	gv, _ := snap.LookupGroup("g")
	if act := gv.Engine.Match("www.example.com"); act != rule.ActionReject {
		t.Fatalf("全局应优先于分组：期望 reject，得到 %v", act)
	}
}

// TestRebuildFailsOnBadRule 验证非法规则导致 Rebuild 返回错误（G4 前提）。
func TestRebuildFailsOnBadRule(t *testing.T) {
	st := newTestStore(t)
	g := &store.Group{Name: "g", Type: store.TypeB}
	_ = st.CreateGroup(g)
	rg := &store.RuleGroup{Name: "rg", Scope: store.ScopeGroup}
	_ = st.CreateRuleGroup(rg)
	_ = st.CreateRule(&store.Rule{RuleGroupID: rg.ID, Match: "ip-cidr:not-a-cidr", Action: "direct", OrderIdx: 1})
	_ = st.AddGroupRuleGroup(g.ID, rg.ID)

	if _, err := Rebuild(st, baseCfg("forward")); err == nil {
		t.Fatal("非法规则应导致 Rebuild 失败")
	}
}

// TestHolderG4Rollback 验证 RebuildAndSwap 失败时不替换、保留旧快照（G4）。
func TestHolderG4Rollback(t *testing.T) {
	st := newTestStore(t)
	g := &store.Group{Name: "g", Type: store.TypeB}
	_ = st.CreateGroup(g)

	snap0, err := Rebuild(st, baseCfg("forward"))
	if err != nil {
		t.Fatal(err)
	}
	h := snapshot.NewHolder(snap0)

	rg := &store.RuleGroup{Name: "rg", Scope: store.ScopeGroup}
	_ = st.CreateRuleGroup(rg)
	_ = st.CreateRule(&store.Rule{RuleGroupID: rg.ID, Match: "ip-cidr:bad", Action: "direct", OrderIdx: 1})
	_ = st.AddGroupRuleGroup(g.ID, rg.ID)

	err = RebuildAndSwap(h, st, baseCfg("forward"))
	if err == nil {
		t.Fatal("应返回 Rebuild 失败错误")
	}
	if h.Load() != snap0 {
		t.Fatal("G4 回滚失败：旧快照应被保留")
	}
}

// TestConcurrentLoadSwap 在 -race 下并发 Load + RebuildAndSwap，断言无 data race（AC-10）。
func TestConcurrentLoadSwap(t *testing.T) {
	st := newTestStore(t)
	g := &store.Group{Name: "g", Type: store.TypeB}
	_ = st.CreateGroup(g)
	snap0, _ := Rebuild(st, baseCfg("forward"))
	h := snapshot.NewHolder(snap0)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				s := h.Load()
				_, _ = s.LookupGroup("g")
			}
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = RebuildAndSwap(h, st, baseCfg("forward"))
			}
		}()
	}
	wg.Wait()
}

// TestRebuildAndSwap_MutualExclusion 验证 CRITICAL #2 修复：RebuildAndSwap 临界区严格互斥。
//
// 为什么用临界区探针而非「比对最终快照」：本项目 store 用单连接（SetMaxOpenConns(1)）串行化
// 读写，单纯并发「写库+重建」难以稳定复现 lost-update（读 DB 这一慢步骤已被连接池排队），
// 测试会偶发通过、抓不住 bug。lost-update 的本质是「读 DB→Swap」这段临界区被并发交叠：
// 晚完成但读了旧 DB 的重建覆盖了新结果。因此这里直接断言【临界区不可重入交叠】——
// 用 rebuildCritHook 在持锁期间探测当前并发进入数，只要曾 >1 就说明互斥失效（即修复回归）。
func TestRebuildAndSwap_MutualExclusion(t *testing.T) {
	st := newTestStore(t)
	_ = st.CreateGroup(&store.Group{Name: "g", Type: store.TypeB})
	snap0, err := Rebuild(st, baseCfg("forward"))
	if err != nil {
		t.Fatal(err)
	}
	h := snapshot.NewHolder(snap0)

	var inside int32  // 当前处于临界区的 goroutine 数
	var maxSeen int32 // 观测到的临界区内最大并发数（正确实现应恒为 1）

	// 注入探针：进入临界区 +1，停留一小会儿放大交叠窗口，记录峰值并发，再 -1 退出。
	rebuildCritHook = func() {
		cur := atomic.AddInt32(&inside, 1)
		for {
			old := atomic.LoadInt32(&maxSeen)
			if cur <= old || atomic.CompareAndSwapInt32(&maxSeen, old, cur) {
				break
			}
		}
		time.Sleep(time.Millisecond) // 若互斥失效，停留期间别的 goroutine 会同时进来使 cur>1
		atomic.AddInt32(&inside, -1)
	}
	t.Cleanup(func() { rebuildCritHook = nil }) // 复位，避免污染其他测试

	const n = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := RebuildAndSwap(h, st, baseCfg("forward")); err != nil {
				t.Errorf("RebuildAndSwap 失败: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&maxSeen); got != 1 {
		t.Fatalf("临界区互斥失效：观测到最多 %d 个 RebuildAndSwap 同时在临界区内（应为 1）—— lost-update 风险", got)
	}
}
