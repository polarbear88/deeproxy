package flush

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"deeproxy/stats"
	"deeproxy/store"
)

// flush_test.go：统计落库 worker 测试（真实 store）。按 AC-43 从 stats 包拆到本子包。

// TestFlusherWritesBuckets 验证 flush worker 把内存增量写入 SQLite 分钟桶。
func TestFlusherWritesBuckets(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "flush.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()

	c := stats.NewCounter()
	c.AddUp(1, 1, 1000)
	c.AddDown(1, 1, 2000)
	c.IncReq(1, 1)

	f := NewFlusher(c, st, nil, WithFlushInterval(10*time.Millisecond))
	f.flushOnce()

	now := time.Now()
	start := now.Add(-2 * time.Minute)
	end := now.Add(2 * time.Minute)
	totals, err := st.QueryTotals(start, end, 1, 1)
	if err != nil {
		t.Fatalf("查询汇总失败: %v", err)
	}
	if totals.UpBytes != 1000 || totals.DownBytes != 2000 || totals.ReqCount != 1 {
		t.Fatalf("落库汇总不符: %+v", totals)
	}

	// 再次 flush 无新增量，桶不应变化。
	f.flushOnce()
	totals2, _ := st.QueryTotals(start, end, 1, 1)
	if totals2.UpBytes != 1000 {
		t.Fatalf("无新增量时桶不应变化，得到 up=%d", totals2.UpBytes)
	}
}

// TestFlusherRunAndStop 验证 Run 循环可被 ctx 取消并在退出前 flush。
func TestFlusherRunAndStop(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()

	c := stats.NewCounter()
	f := NewFlusher(c, st, nil, WithFlushInterval(20*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	c.AddUp(5, 5, 500)
	c.AddDown(5, 5, 500)
	c.IncReq(5, 5)
	time.Sleep(60 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Flusher.Run 未在取消后退出")
	}

	now := time.Now()
	totals, _ := st.QueryTotals(now.Add(-2*time.Minute), now.Add(2*time.Minute), 5, 5)
	if totals.UpBytes != 500 || totals.DownBytes != 500 {
		t.Fatalf("运行期 flush 落库不符: %+v", totals)
	}
}
