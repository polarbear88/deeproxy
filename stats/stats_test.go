package stats

import (
	"sync"
	"testing"
)

// stats_test.go：内存原子计数器 Counter 测试（落库 Flusher 测试已按 AC-43 拆到 stats/flush 子包）。

// TestCounterConcurrentAdd 验证并发累加计数正确（配合 -race 检测数据竞争）。
func TestCounterConcurrentAdd(t *testing.T) {
	c := NewCounter()

	const (
		goroutines = 50
		perG       = 1000
		upPer      = 3
		downPer    = 5
	)

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				c.AddUp(1, 1, upPer)
				c.AddDown(1, 1, downPer)
				c.IncReq(1, 1)
			}
		}()
	}
	wg.Wait()

	dims := c.CollectDeltas()
	if len(dims) != 1 {
		t.Fatalf("期望 1 个维度增量，得到 %d", len(dims))
	}
	d := dims[0]
	wantUp := int64(goroutines * perG * upPer)
	wantDown := int64(goroutines * perG * downPer)
	wantReq := int64(goroutines * perG)
	if d.UpBytes != wantUp || d.DownBytes != wantDown || d.ReqCount != wantReq {
		t.Fatalf("增量不符：up=%d(want %d) down=%d(want %d) req=%d(want %d)",
			d.UpBytes, wantUp, d.DownBytes, wantDown, d.ReqCount, wantReq)
	}
}

// TestCounterDeltaBaseline 验证差分基线推进：第二次 CollectDeltas 只返回两次之间的新增量。
func TestCounterDeltaBaseline(t *testing.T) {
	c := NewCounter()
	c.AddUp(1, 2, 100)
	c.AddDown(1, 2, 200)

	first := c.CollectDeltas()
	if len(first) != 1 || first[0].UpBytes != 100 || first[0].DownBytes != 200 {
		t.Fatalf("首次增量不符: %+v", first)
	}

	if d := c.CollectDeltas(); len(d) != 0 {
		t.Fatalf("无新增量应返回空，得到 %+v", d)
	}

	c.AddUp(1, 2, 50)
	second := c.CollectDeltas()
	if len(second) != 1 || second[0].UpBytes != 50 || second[0].DownBytes != 0 {
		t.Fatalf("二次增量应为 up=50 down=0，得到 %+v", second)
	}
}

// TestCounterMultiDim 验证多维度独立累加与收集。
func TestCounterMultiDim(t *testing.T) {
	c := NewCounter()
	c.AddUp(1, 1, 10)
	c.AddUp(1, 2, 20)
	c.AddUp(2, 1, 30)

	dims := c.CollectDeltas()
	if len(dims) != 3 {
		t.Fatalf("期望 3 个维度，得到 %d", len(dims))
	}
	sum := int64(0)
	for _, d := range dims {
		sum += d.UpBytes
	}
	if sum != 60 {
		t.Fatalf("三维度上行合计应为 60，得到 %d", sum)
	}
}

// TestRealtimeCounters 验证进程级实时计数（活跃连接/拒连/动作分布）。
func TestRealtimeCounters(t *testing.T) {
	c := NewCounter()
	c.ConnOpened()
	c.ConnOpened()
	c.ConnClosed()
	c.IncRejectRule()
	c.IncRejectAuth()
	c.IncRejectAuth()
	c.IncActionForward()
	c.IncActionDirect()
	c.IncActionReject()

	rt := c.RealtimeSnapshot()
	if rt.ActiveConns != 1 {
		t.Fatalf("活跃连接应为 1，得到 %d", rt.ActiveConns)
	}
	if rt.RejectRule != 1 || rt.RejectAuth != 2 {
		t.Fatalf("拒连计数不符: rule=%d auth=%d", rt.RejectRule, rt.RejectAuth)
	}
	if rt.ActForward != 1 || rt.ActDirect != 1 || rt.ActReject != 1 {
		t.Fatalf("动作分布不符: %+v", rt)
	}
}
