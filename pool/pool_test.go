package pool

import (
	"context"
	"sync"
	"testing"
	"time"

	"deeproxy/snapshot"
)

// mkUps 构造若干健康上游视图（便于测试）。
func mkUps(specs ...[2]int) []snapshot.UpstreamView {
	// 每个 spec = {id, weight}
	out := make([]snapshot.UpstreamView, 0, len(specs))
	for _, s := range specs {
		out = append(out, snapshot.UpstreamView{ID: int64(s[0]), Weight: s[1], Enabled: true, Healthy: true})
	}
	return out
}

// TestSWRRDistribution 验证大样本下选择比例趋近权重比例（平滑加权轮训）。
func TestSWRRDistribution(t *testing.T) {
	sel := NewSelector()
	healthy := mkUps([2]int{1, 5}, [2]int{2, 3}, [2]int{3, 2}) // 权重 5:3:2

	counts := map[int64]int{}
	const N = 10000
	for i := 0; i < N; i++ {
		u, err := sel.Pick(healthy)
		if err != nil {
			t.Fatalf("Pick 出错: %v", err)
		}
		counts[u.ID]++
	}

	// 期望比例 5:3:2 → 5000:3000:2000，允许 ±3% 偏差。
	check := func(id int64, wantRatio float64) {
		got := float64(counts[id]) / float64(N)
		if got < wantRatio-0.03 || got > wantRatio+0.03 {
			t.Fatalf("节点 %d 选中比例 %.3f 偏离期望 %.3f 过大", id, got, wantRatio)
		}
	}
	check(1, 0.5)
	check(2, 0.3)
	check(3, 0.2)
}

// TestSWRRSmoothness 验证平滑性：权重 1:1 时严格交替，不连续偏向同一节点。
func TestSWRRSmoothness(t *testing.T) {
	sel := NewSelector()
	healthy := mkUps([2]int{1, 1}, [2]int{2, 1})

	var seq []int64
	for i := 0; i < 6; i++ {
		u, _ := sel.Pick(healthy)
		seq = append(seq, u.ID)
	}
	for i := 2; i < len(seq); i++ {
		if seq[i] == seq[i-1] && seq[i-1] == seq[i-2] {
			t.Fatalf("1:1 权重不应连续 3 次选同一节点: %v", seq)
		}
	}
}

// TestEmptyListRejected 验证空健康列表返回 ErrNoUpstream（G6）。
func TestEmptyListRejected(t *testing.T) {
	sel := NewSelector()
	_, err := sel.Pick(nil)
	if err != ErrNoUpstream {
		t.Fatalf("空列表应返回 ErrNoUpstream，得到 %v", err)
	}
	_, err = sel.Pick([]snapshot.UpstreamView{})
	if err != ErrNoUpstream {
		t.Fatalf("空切片应返回 ErrNoUpstream，得到 %v", err)
	}
}

// TestNodeIDKeyedStability 验证 currentWeight 按 nodeID 绑定：列表替换后不张冠李戴、不越界。
func TestNodeIDKeyedStability(t *testing.T) {
	sel := NewSelector()
	full := mkUps([2]int{1, 1}, [2]int{2, 1}, [2]int{3, 1})

	for i := 0; i < 5; i++ {
		sel.Pick(full)
	}

	reduced := mkUps([2]int{1, 1}, [2]int{3, 1})
	for i := 0; i < 10; i++ {
		u, err := sel.Pick(reduced)
		if err != nil {
			t.Fatalf("Pick 出错: %v", err)
		}
		if u.ID == 2 {
			t.Fatalf("已剔除的节点 2 不应被选中")
		}
	}

	sel.mu.Lock()
	_, has2 := sel.currentWeight[2]
	sel.mu.Unlock()
	if has2 {
		t.Fatalf("节点 2 被剔除后其 currentWeight 应被清理")
	}

	seen2 := false
	for i := 0; i < 30; i++ {
		u, _ := sel.Pick(full)
		if u.ID == 2 {
			seen2 = true
		}
	}
	if !seen2 {
		t.Fatalf("节点 2 恢复后应能被选中")
	}
}

// TestInvalidWeightDefensive 验证权重<=0 按 1 处理，不导致除零或永不选中。
func TestInvalidWeightDefensive(t *testing.T) {
	sel := NewSelector()
	healthy := mkUps([2]int{1, 0}, [2]int{2, -5})
	seen := map[int64]bool{}
	for i := 0; i < 10; i++ {
		u, err := sel.Pick(healthy)
		if err != nil {
			t.Fatalf("Pick 出错: %v", err)
		}
		seen[u.ID] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("非法权重应按 1 处理，两节点都应被选到: %v", seen)
	}
}

// TestRegistry 验证注册表惰性创建且同组复用同一 Selector。
func TestRegistry(t *testing.T) {
	r := NewRegistry()
	s1 := r.For(1)
	s1b := r.For(1)
	s2 := r.For(2)
	if s1 != s1b {
		t.Fatal("同组应返回同一 Selector 实例")
	}
	if s1 == s2 {
		t.Fatal("不同组应返回不同 Selector 实例")
	}
}

// TestConcurrentPickWithListReplacement 是 AC-42 的核心：
// 高频替换健康列表 + 1000 并发对同组建连选上游，
// 断言无 data race（-race）、无 panic、不返回已剔除节点。
func TestConcurrentPickWithListReplacement(t *testing.T) {
	sel := NewSelector()

	listFull := mkUps([2]int{1, 2}, [2]int{2, 2}, [2]int{3, 2})
	listReduced := mkUps([2]int{1, 2}, [2]int{3, 2})

	var current sync.Map
	current.Store("list", listFull)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		toggle := false
		for ctx.Err() == nil {
			if toggle {
				current.Store("list", listFull)
			} else {
				current.Store("list", listReduced)
			}
			toggle = !toggle
		}
	}()

	errCh := make(chan error, 1000)
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				v, _ := current.Load("list")
				list := v.([]snapshot.UpstreamView)
				u, err := sel.Pick(list)
				if err == ErrNoUpstream {
					continue
				}
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				found := false
				for j := range list {
					if list[j].ID == u.ID {
						found = true
						break
					}
				}
				if !found {
					select {
					case errCh <- errUnexpectedNode:
					default:
					}
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		t.Fatalf("并发选择出错: %v", err)
	}
}

// errUnexpectedNode 测试用哨兵：选到了不在当前列表中的节点。
var errUnexpectedNode = &poolTestError{"选到了不在当前列表中的节点"}

type poolTestError struct{ msg string }

func (e *poolTestError) Error() string { return e.msg }
