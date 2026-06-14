package store

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestStore 在临时目录建一个真实 SQLite 库（非 :memory:，便于 WAL 行为贴近生产）。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestMigrateAndSystemSetting 验证建表成功且系统设置单行被初始化。
func TestMigrateAndSystemSetting(t *testing.T) {
	s := newTestStore(t)

	ss, err := s.GetSystemSetting()
	if err != nil {
		t.Fatalf("读取系统设置失败: %v", err)
	}
	if ss.ID != 1 {
		t.Fatalf("系统设置 id 期望 1，得到 %d", ss.ID)
	}
	if ss.StatRetentionDays != 30 {
		t.Fatalf("默认保留期期望 30，得到 %d", ss.StatRetentionDays)
	}
	if ss.IsAdminConfigured() {
		t.Fatalf("初始库不应有管理员配置")
	}

	// 设置管理员凭据后应判定为已配置。
	if err := s.SetAdminCredential("admin", "hash"); err != nil {
		t.Fatalf("设置管理员凭据失败: %v", err)
	}
	ss, _ = s.GetSystemSetting()
	if !ss.IsAdminConfigured() {
		t.Fatalf("设置后应判定为已配置管理员")
	}
}

// TestProxyUserCRUD 验证代理用户增删改查。
func TestProxyUserCRUD(t *testing.T) {
	s := newTestStore(t)

	u := &ProxyUser{Username: "alice", Pwd: "h1", Remark: "测试"}
	if err := s.CreateProxyUser(u); err != nil {
		t.Fatalf("新增用户失败: %v", err)
	}
	if u.ID == 0 {
		t.Fatalf("新增后应回填自增 ID")
	}

	got, err := s.GetProxyUserByName("alice")
	if err != nil || got == nil {
		t.Fatalf("按名查询失败: %v / %v", err, got)
	}
	if got.Pwd != "h1" {
		t.Fatalf("密码哈希不一致")
	}

	got.Remark = "改后"
	if err := s.UpdateProxyUser(got); err != nil {
		t.Fatalf("更新用户失败: %v", err)
	}
	got2, _ := s.GetProxyUser(u.ID)
	if got2.Remark != "改后" {
		t.Fatalf("更新未生效")
	}

	list, _ := s.ListProxyUsers()
	if len(list) != 1 {
		t.Fatalf("列表期望 1 条，得到 %d", len(list))
	}

	if err := s.DeleteProxyUser(u.ID); err != nil {
		t.Fatalf("删除用户失败: %v", err)
	}
	gone, _ := s.GetProxyUser(u.ID)
	if gone != nil {
		t.Fatalf("删除后仍可查到")
	}
}

// TestGroupAndUpstreamCRUD 验证分组与其上游池的增删改查及级联删除。
func TestGroupAndUpstreamCRUD(t *testing.T) {
	s := newTestStore(t)

	g := &Group{Name: "poolA", Type: TypeB, HCEnabled: true, HCMode: HealthURL, HCInterval: 600, HCFailThld: 3, HCRecvThld: 2}
	if err := s.CreateGroup(g); err != nil {
		t.Fatalf("新增分组失败: %v", err)
	}

	up := &UpstreamProxy{GroupID: g.ID, Host: "1.2.3.4", Port: 1080, User: "acct-{region}", Weight: 5, Enabled: true, HealthState: true}
	if err := s.CreateUpstream(up); err != nil {
		t.Fatalf("新增上游失败: %v", err)
	}

	// 健康状态独立更新不应影响其他字段。
	if err := s.UpdateUpstreamHealth(up.ID, false); err != nil {
		t.Fatalf("更新健康状态失败: %v", err)
	}
	got, _ := s.GetUpstream(up.ID)
	if got.HealthState != false || got.Weight != 5 {
		t.Fatalf("健康更新串改了其他字段: %+v", got)
	}

	ups, _ := s.ListUpstreamsByGroup(g.ID)
	if len(ups) != 1 {
		t.Fatalf("分组上游数期望 1，得到 %d", len(ups))
	}

	// 删除分组应级联删除其上游（外键 ON DELETE CASCADE）。
	if err := s.DeleteGroup(g.ID); err != nil {
		t.Fatalf("删除分组失败: %v", err)
	}
	all, _ := s.ListAllUpstreams()
	if len(all) != 0 {
		t.Fatalf("级联删除未生效，仍有 %d 条上游", len(all))
	}
}

// TestRuleCRUDAndOrder 验证规则组/规则 CRUD 与组内顺序。
func TestRuleCRUDAndOrder(t *testing.T) {
	s := newTestStore(t)

	rg := &RuleGroup{Name: "rg1", Scope: ScopeGlobal}
	if err := s.CreateRuleGroup(rg); err != nil {
		t.Fatalf("新增规则组失败: %v", err)
	}

	// 故意乱序插入，验证按 order_idx 升序返回。
	_ = s.CreateRule(&Rule{RuleGroupID: rg.ID, Match: "domain:b.com", Action: "direct", OrderIdx: 2})
	_ = s.CreateRule(&Rule{RuleGroupID: rg.ID, Match: "domain:a.com", Action: "forward", OrderIdx: 1})

	rules, _ := s.ListRulesByGroup(rg.ID)
	if len(rules) != 2 || rules[0].Match != "domain:a.com" {
		t.Fatalf("规则未按 order_idx 升序返回: %+v", rules)
	}

	// 删除规则组级联删除其规则。
	if err := s.DeleteRuleGroup(rg.ID); err != nil {
		t.Fatalf("删除规则组失败: %v", err)
	}
	allRules, _ := s.ListAllRules()
	if len(allRules) != 0 {
		t.Fatalf("级联删除规则未生效，剩 %d 条", len(allRules))
	}
}

// TestAssociations 验证两类多对多关联的覆盖式设置。
func TestAssociations(t *testing.T) {
	s := newTestStore(t)

	g := &Group{Name: "g", Type: TypeB}
	_ = s.CreateGroup(g)
	u1 := &ProxyUser{Username: "u1", Pwd: "h"}
	u2 := &ProxyUser{Username: "u2", Pwd: "h"}
	_ = s.CreateProxyUser(u1)
	_ = s.CreateProxyUser(u2)

	if err := s.SetGroupUsers(g.ID, []int64{u1.ID, u2.ID}); err != nil {
		t.Fatalf("设置授权失败: %v", err)
	}
	gus, _ := s.ListGroupUsers()
	if len(gus) != 2 {
		t.Fatalf("授权数期望 2，得到 %d", len(gus))
	}
	// 覆盖式：只留 u1。
	_ = s.SetGroupUsers(g.ID, []int64{u1.ID})
	gus, _ = s.ListGroupUsers()
	if len(gus) != 1 || gus[0].UserID != u1.ID {
		t.Fatalf("覆盖式授权未生效: %+v", gus)
	}

	rg := &RuleGroup{Name: "rg", Scope: ScopeGroup}
	_ = s.CreateRuleGroup(rg)
	if err := s.SetGroupRuleGroups(g.ID, []int64{rg.ID}); err != nil {
		t.Fatalf("设置规则组关联失败: %v", err)
	}
	grg, _ := s.ListGroupRuleGroups()
	if len(grg) != 1 {
		t.Fatalf("规则组关联数期望 1，得到 %d", len(grg))
	}
}

// TestTrafficStatUpsertAndQuery 验证聚合桶 upsert 累加、汇总、时序与降采样、清理。
func TestTrafficStatUpsertAndQuery(t *testing.T) {
	s := newTestStore(t)

	base := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)

	// 同一分钟桶两次 flush，应累加而非覆盖。
	d1 := StatDelta{GroupID: 1, UserID: 1, BucketTime: base, UpBytes: 100, DownBytes: 200, ReqCount: 1}
	if err := s.FlushTrafficStats([]StatDelta{d1}); err != nil {
		t.Fatalf("首次 flush 失败: %v", err)
	}
	d2 := StatDelta{GroupID: 1, UserID: 1, BucketTime: base.Add(30 * time.Second), UpBytes: 50, DownBytes: 60, ReqCount: 2}
	if err := s.FlushTrafficStats([]StatDelta{d2}); err != nil {
		t.Fatalf("二次 flush 失败: %v", err)
	}

	// 汇总应为累加值。
	tot, err := s.QueryTotals(base.Add(-time.Hour), base.Add(time.Hour), 0, 0)
	if err != nil {
		t.Fatalf("查询汇总失败: %v", err)
	}
	if tot.UpBytes != 150 || tot.DownBytes != 260 || tot.ReqCount != 3 {
		t.Fatalf("聚合桶累加错误: %+v", tot)
	}

	// 再写另一分钟、另一分组，验证时序与 Top。
	_ = s.FlushTrafficStats([]StatDelta{
		{GroupID: 1, UserID: 1, BucketTime: base.Add(2 * time.Minute), UpBytes: 10, DownBytes: 10, ReqCount: 1},
		{GroupID: 2, UserID: 1, BucketTime: base.Add(time.Minute), UpBytes: 999, DownBytes: 1, ReqCount: 1},
	})

	// 分钟粒度时序：group1 应有 2 个时间点（base 桶 + base+2min 桶）。
	pts, err := s.QueryTimeSeries(base.Add(-time.Hour), base.Add(time.Hour), 1, 0, false)
	if err != nil {
		t.Fatalf("查询时序失败: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("分钟时序点数期望 2，得到 %d: %+v", len(pts), pts)
	}

	// 小时降采样：group1 两个分钟桶都在同一小时，应聚成 1 个点。
	hourPts, _ := s.QueryTimeSeries(base.Add(-time.Hour), base.Add(time.Hour), 1, 0, true)
	if len(hourPts) != 1 {
		t.Fatalf("小时降采样点数期望 1，得到 %d: %+v", len(hourPts), hourPts)
	}
	if hourPts[0].UpBytes != 160 { // 150 + 10
		t.Fatalf("降采样汇总错误: %+v", hourPts[0])
	}

	// Top 分组：group2 单桶 1000 总流量 > group1，应排第一。
	top, _ := s.QueryTopGroups(base.Add(-time.Hour), base.Add(time.Hour), 5)
	if len(top) != 2 || top[0].GroupID != 2 {
		t.Fatalf("Top 分组排序错误: %+v", top)
	}

	// 清理：删除 base+90s 之前的桶，应删掉 base 桶与 group2(base+1min) 桶，保留 base+2min。
	cutoff := base.Add(90 * time.Second)
	affected, err := s.CleanupBefore(cutoff)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if affected != 2 {
		t.Fatalf("清理行数期望 2，得到 %d", affected)
	}
	remain, _ := s.QueryTotals(base.Add(-time.Hour), base.Add(time.Hour), 0, 0)
	if remain.UpBytes != 10 {
		t.Fatalf("清理后剩余汇总错误: %+v", remain)
	}
}

// TestPasswordHash 验证 bcrypt 封装可用（被 auth/api 复用）。
func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("secret")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if !VerifyPassword(h, "secret") {
		t.Fatalf("正确密码应校验通过")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatalf("错误密码不应通过")
	}
}
