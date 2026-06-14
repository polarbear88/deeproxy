package store

import (
	"path/filepath"
	"testing"
	"time"
)

// domain_hit_test.go：domain_hit 聚合桶 upsert 累加、Top 查询（全局/分组/limit）、清理测试。

func TestFlushDomainHitsAndQueryTop(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "domain.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	base := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	// 第一批：a.com 命中 3、b.com 命中 1（均 group 1）。
	if err := st.FlushDomainHits([]DomainDelta{
		{Domain: "a.com", GroupID: 1, BucketTime: base, HitCount: 3},
		{Domain: "b.com", GroupID: 1, BucketTime: base, HitCount: 1},
	}); err != nil {
		t.Fatalf("flush 第一批失败: %v", err)
	}
	// 第二批：同一域名同一桶再 +2，验证 upsert 累加（a.com 应变 5）。
	if err := st.FlushDomainHits([]DomainDelta{
		{Domain: "a.com", GroupID: 1, BucketTime: base, HitCount: 2},
	}); err != nil {
		t.Fatalf("flush 第二批失败: %v", err)
	}

	win := func() (time.Time, time.Time) { return base.Add(-time.Hour), base.Add(time.Hour) }

	// 全局 Top（groupID<=0 不过滤）：a.com(5) > b.com(1)，降序。
	s, e := win()
	top, err := st.QueryTopDomains(s, e, 10, 0)
	if err != nil {
		t.Fatalf("QueryTopDomains 全局失败: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("期望 2 个域名，得到 %d: %+v", len(top), top)
	}
	if top[0].Domain != "a.com" || top[0].HitCount != 5 {
		t.Fatalf("Top1 应为 a.com=5，得到 %+v", top[0])
	}
	if top[1].Domain != "b.com" || top[1].HitCount != 1 {
		t.Fatalf("Top2 应为 b.com=1，得到 %+v", top[1])
	}

	// limit 生效。
	s, e = win()
	top1, _ := st.QueryTopDomains(s, e, 1, 0)
	if len(top1) != 1 || top1[0].Domain != "a.com" {
		t.Fatalf("limit=1 应只返回 a.com，得到 %+v", top1)
	}
}

func TestQueryTopDomainsPerGroup(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "domain_grp.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	base := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	// 同名域名 x.com：group 1 命中 2，group 2 命中 5。
	if err := st.FlushDomainHits([]DomainDelta{
		{Domain: "x.com", GroupID: 1, BucketTime: base, HitCount: 2},
		{Domain: "x.com", GroupID: 2, BucketTime: base, HitCount: 5},
		{Domain: "y.com", GroupID: 1, BucketTime: base, HitCount: 1},
	}); err != nil {
		t.Fatalf("flush 失败: %v", err)
	}

	s, e := base.Add(-time.Hour), base.Add(time.Hour)

	// 全局：x.com 合并两组 = 7，居首。
	g, err := st.QueryTopDomains(s, e, 10, 0)
	if err != nil {
		t.Fatalf("全局查询失败: %v", err)
	}
	if len(g) != 2 || g[0].Domain != "x.com" || g[0].HitCount != 7 {
		t.Fatalf("全局应为 x.com=7 居首，得到 %+v", g)
	}

	// 仅 group 1：x.com=2、y.com=1。
	g1, err := st.QueryTopDomains(s, e, 10, 1)
	if err != nil {
		t.Fatalf("group1 查询失败: %v", err)
	}
	if len(g1) != 2 {
		t.Fatalf("group1 应有 2 个域名，得到 %+v", g1)
	}
	if g1[0].Domain != "x.com" || g1[0].HitCount != 2 {
		t.Fatalf("group1 Top1 应为 x.com=2，得到 %+v", g1[0])
	}

	// 仅 group 2：只有 x.com=5。
	g2, err := st.QueryTopDomains(s, e, 10, 2)
	if err != nil {
		t.Fatalf("group2 查询失败: %v", err)
	}
	if len(g2) != 1 || g2[0].Domain != "x.com" || g2[0].HitCount != 5 {
		t.Fatalf("group2 应只有 x.com=5，得到 %+v", g2)
	}
}

func TestCleanupDomainHitsBefore(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "domain_clean.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	oldBucket := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	newBucket := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	if err := st.FlushDomainHits([]DomainDelta{
		{Domain: "old.com", GroupID: 1, BucketTime: oldBucket, HitCount: 9},
		{Domain: "new.com", GroupID: 1, BucketTime: newBucket, HitCount: 3},
	}); err != nil {
		t.Fatalf("flush 失败: %v", err)
	}

	// 清理 newBucket 之前的：old.com 桶应被删，new.com 保留。
	cutoff := newBucket.Add(-time.Hour)
	n, err := st.CleanupDomainHitsBefore(cutoff)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("清理行数期望 1，得到 %d", n)
	}

	// 清理后大窗口查询应只剩 new.com。
	all, _ := st.QueryTopDomains(oldBucket.Add(-time.Hour), newBucket.Add(time.Hour), 10, 0)
	if len(all) != 1 || all[0].Domain != "new.com" {
		t.Fatalf("清理后应只剩 new.com，得到 %+v", all)
	}
}
