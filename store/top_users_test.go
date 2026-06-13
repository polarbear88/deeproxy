package store

import (
	"path/filepath"
	"testing"
	"time"
)

// top_users_test.go：QueryTopUsers 排行查询测试（补 worker-3 仪表盘 kind=user 缺口）。

func TestQueryTopUsers(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "topu.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	base := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	// user 2 总流量(900+100=1000) > user 1(100+200=300)，应排第一。
	err = st.FlushTrafficStats([]StatDelta{
		{GroupID: 1, UserID: 1, BucketTime: base, UpBytes: 100, DownBytes: 200, ReqCount: 1},
		{GroupID: 1, UserID: 2, BucketTime: base, UpBytes: 900, DownBytes: 100, ReqCount: 2},
	})
	if err != nil {
		t.Fatalf("flush 失败: %v", err)
	}

	top, err := st.QueryTopUsers(base.Add(-time.Hour), base.Add(time.Hour), 5)
	if err != nil {
		t.Fatalf("QueryTopUsers 失败: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("期望 2 个用户，得到 %d", len(top))
	}
	if top[0].UserID != 2 {
		t.Fatalf("Top1 应为 user 2，得到 user %d", top[0].UserID)
	}
	if top[0].UpBytes != 900 || top[0].DownBytes != 100 || top[0].ReqCount != 2 {
		t.Fatalf("Top1 聚合值错误: %+v", top[0])
	}

	// limit 生效。
	top1, _ := st.QueryTopUsers(base.Add(-time.Hour), base.Add(time.Hour), 1)
	if len(top1) != 1 || top1[0].UserID != 2 {
		t.Fatalf("limit=1 应只返回 user 2，得到 %+v", top1)
	}
}
