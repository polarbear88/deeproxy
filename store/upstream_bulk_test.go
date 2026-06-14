package store

import (
	"path/filepath"
	"testing"
)

// upstream_bulk_test.go 覆盖 WP-3 上游分页（AC-3.3）与批量改权重/启停（AC-3.4）。

// newBulkTestStore 建库 + 一个 Type B 组 + n 条上游（host 形如 hN.com）。
func newBulkTestStore(t *testing.T, n int) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "bulk.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	g := Group{Name: "poolB", Type: TypeB}
	if err := s.CreateGroup(&g); err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	for i := 0; i < n; i++ {
		u := UpstreamProxy{
			GroupID: g.ID, Host: fmtHost(i), Port: 1080 + i,
			Weight: 1, Enabled: true, HealthState: i%2 == 0, // 偶数健康、奇数不健康
		}
		if err := s.CreateUpstream(&u); err != nil {
			t.Fatalf("建上游失败: %v", err)
		}
	}
	return s, g.ID
}

func fmtHost(i int) string {
	// 固定前缀便于 keyword 测试；编号补足排序稳定。
	return "host" + itoa(i) + ".com"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestListUpstreamsPaged 验证分页 total/limit/offset 与筛选。
func TestListUpstreamsPaged(t *testing.T) {
	s, gid := newBulkTestStore(t, 25)

	// 第 1 页，每页 10：应返回 10 条，total=25。
	page1, total, err := s.ListUpstreamsPaged(UpstreamFilter{GroupID: gid}, 1, 10)
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if total != 25 {
		t.Fatalf("total 应为 25，得到 %d", total)
	}
	if len(page1) != 10 {
		t.Fatalf("第1页应 10 条，得到 %d", len(page1))
	}

	// 第 3 页（offset 20）：应返回 5 条。
	page3, _, _ := s.ListUpstreamsPaged(UpstreamFilter{GroupID: gid}, 3, 10)
	if len(page3) != 5 {
		t.Fatalf("第3页应 5 条，得到 %d", len(page3))
	}

	// 健康筛选：偶数 index 健康 → 13 条（index 0,2,...,24）。
	_, healthyTotal, _ := s.ListUpstreamsPaged(UpstreamFilter{GroupID: gid, HealthState: "healthy"}, 1, 100)
	if healthyTotal != 13 {
		t.Fatalf("健康总数应 13，得到 %d", healthyTotal)
	}

	// keyword 筛选 host5.com：精确一条（host5.com 不与 host15/25 冲突需含完整串）。
	_, kwTotal, _ := s.ListUpstreamsPaged(UpstreamFilter{GroupID: gid, Keyword: "host5.com"}, 1, 100)
	if kwTotal != 1 {
		t.Fatalf("keyword host5.com 应命中 1 条，得到 %d", kwTotal)
	}
}

// TestBulkUpdateByFilter 验证按筛选批量改权重为单条 SQL、受影响行数正确。
func TestBulkUpdateByFilter(t *testing.T) {
	s, gid := newBulkTestStore(t, 20)

	// 把全部健康上游权重改为 5（健康=偶数 index=10 条）。
	affected, err := s.BulkUpdateUpstreamsByFilter(
		UpstreamFilter{GroupID: gid, HealthState: "healthy"}, BulkFieldWeight, 5, false)
	if err != nil {
		t.Fatalf("按筛选批量改权重失败: %v", err)
	}
	if affected != 10 {
		t.Fatalf("应影响 10 条健康上游，得到 %d", affected)
	}

	// 校验：健康上游权重均为 5，不健康仍为 1。
	all, err := s.ListUpstreamsByGroup(gid)
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	for _, u := range all {
		want := 1
		if u.HealthState {
			want = 5
		}
		if u.Weight != want {
			t.Fatalf("上游 %s 权重应 %d，得到 %d", u.Host, want, u.Weight)
		}
	}
}

// TestBulkUpdateByIDs 验证按 id 列表批量启停（含分块路径）受影响行数正确且不跨组。
func TestBulkUpdateByIDs(t *testing.T) {
	s, gid := newBulkTestStore(t, 10)
	all, _ := s.ListUpstreamsByGroup(gid)

	ids := []int64{all[0].ID, all[1].ID, all[2].ID}
	affected, err := s.BulkUpdateUpstreamsByIDs(gid, ids, BulkFieldEnabled, 0, false)
	if err != nil {
		t.Fatalf("按 id 批量禁用失败: %v", err)
	}
	if affected != 3 {
		t.Fatalf("应影响 3 条，得到 %d", affected)
	}

	got, _ := s.ListUpstreamsByGroup(gid)
	disabled := 0
	for _, u := range got {
		if !u.Enabled {
			disabled++
		}
	}
	if disabled != 3 {
		t.Fatalf("应有 3 条被禁用，得到 %d", disabled)
	}

	// 跨组保护：用错误的 groupID 调用应不影响任何行。
	affected2, _ := s.BulkUpdateUpstreamsByIDs(gid+999, ids, BulkFieldEnabled, 0, true)
	if affected2 != 0 {
		t.Fatalf("错误 groupID 不应影响任何行，却影响 %d", affected2)
	}
}
