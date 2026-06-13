package syslog

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// TestRingEvictionAndOrder 验证环形缓冲满淘汰最旧 + snapshot 按最旧→最新顺序。
func TestRingEvictionAndOrder(t *testing.T) {
	b := NewLogBuffer(3)
	for i := 0; i < 5; i++ {
		b.ring.push(LogEntry{Message: string(rune('A' + i))}) // A B C D E
	}
	if b.Len() != 3 {
		t.Fatalf("容量 3 应只保留 3 条，得到 %d", b.Len())
	}
	snap := b.Snapshot("")
	// 最旧两条 A、B 被淘汰，应剩 C D E（按顺序）。
	want := []string{"C", "D", "E"}
	if len(snap) != 3 {
		t.Fatalf("快照应为 3 条，得到 %d", len(snap))
	}
	for i, w := range want {
		if snap[i].Message != w {
			t.Fatalf("第 %d 条应为 %s，得到 %s", i, w, snap[i].Message)
		}
	}
}

// TestSnapshotNotFull 验证未写满时快照顺序正确。
func TestSnapshotNotFull(t *testing.T) {
	b := NewLogBuffer(10)
	b.ring.push(LogEntry{Message: "x"})
	b.ring.push(LogEntry{Message: "y"})
	snap := b.Snapshot("")
	if len(snap) != 2 || snap[0].Message != "x" || snap[1].Message != "y" {
		t.Fatalf("未写满快照不符: %+v", snap)
	}
}

// TestLevelFilter 验证按级别筛选。
func TestLevelFilter(t *testing.T) {
	b := NewLogBuffer(10)
	b.ring.push(LogEntry{Level: "info", Message: "1"})
	b.ring.push(LogEntry{Level: "error", Message: "2"})
	b.ring.push(LogEntry{Level: "info", Message: "3"})

	infos := b.Snapshot("info")
	if len(infos) != 2 {
		t.Fatalf("info 级别应有 2 条，得到 %d", len(infos))
	}
	errs := b.Snapshot("error")
	if len(errs) != 1 || errs[0].Message != "2" {
		t.Fatalf("error 级别筛选不符: %+v", errs)
	}
}

// TestSlogHandler 验证 slog 经本 Handler 写入缓冲，且级别/消息/字段正确。
func TestSlogHandler(t *testing.T) {
	b := NewLogBuffer(10)
	logger := slog.New(b.Handler())

	logger.Info("hello", "k", "v", "n", 42)
	logger.Error("boom", "code", 500)
	logger.Debug("dbg")
	logger.Warn("careful")

	snap := b.Snapshot("")
	if len(snap) != 4 {
		t.Fatalf("应写入 4 条，得到 %d", len(snap))
	}

	// 校验第一条 info 的字段。
	first := snap[0]
	if first.Level != "info" || first.Message != "hello" {
		t.Fatalf("首条不符: level=%s msg=%s", first.Level, first.Message)
	}
	if first.Fields["k"] != "v" {
		t.Fatalf("字段 k 应为 v，得到 %v", first.Fields["k"])
	}
	if first.Fields["n"] != int64(42) {
		t.Fatalf("字段 n 应为 42，得到 %v", first.Fields["n"])
	}

	// 校验级别字符串映射。
	levels := map[string]bool{}
	for _, e := range snap {
		levels[e.Level] = true
	}
	for _, want := range []string{"info", "error", "debug", "warn"} {
		if !levels[want] {
			t.Fatalf("缺少级别 %s 的日志", want)
		}
	}
}

// TestSlogHandlerWithAttrsGroup 验证 WithAttrs/WithGroup 的属性合并与前缀。
func TestSlogHandlerWithAttrsGroup(t *testing.T) {
	b := NewLogBuffer(10)
	logger := slog.New(b.Handler()).With("svc", "proxy").WithGroup("conn")

	logger.Info("opened", "id", 7)

	snap := b.Snapshot("")
	if len(snap) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(snap))
	}
	f := snap[0].Fields
	// With("svc") 在 WithGroup 之前，故 svc 不带前缀；WithGroup("conn") 之后的 id 带 conn. 前缀。
	if f["svc"] != "proxy" {
		t.Fatalf("svc 字段应为 proxy，得到 %v", f["svc"])
	}
	if f["conn.id"] != int64(7) {
		t.Fatalf("分组字段 conn.id 应为 7，得到 %v (fields=%+v)", f["conn.id"], f)
	}
}

// TestSubscribe 验证订阅能收到新日志，注销后 done 关闭。
func TestSubscribe(t *testing.T) {
	b := NewLogBuffer(10)
	ch, done, unsub := b.Subscribe(8)

	b.ring.push(LogEntry{Message: "live1"})

	select {
	case e := <-ch:
		if e.Message != "live1" {
			t.Fatalf("订阅收到的消息不符: %s", e.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("订阅未收到新日志")
	}

	unsub()
	// 注销后 done 应关闭；data 不会被 close（避免向已关闭 channel 发送 panic）。
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("注销后 done 应关闭")
	}
	// 注销后再 push 不应 panic，且不再投递给该订阅者。
	b.ring.push(LogEntry{Message: "live2"})
}

// TestSubscribeNonBlocking 验证慢订阅者（channel 满）不阻塞写入方。
func TestSubscribeNonBlocking(t *testing.T) {
	b := NewLogBuffer(100)
	_, _, unsub := b.Subscribe(1) // 极小缓冲，故意不消费
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.ring.push(LogEntry{Message: "flood"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("慢订阅者阻塞了日志写入")
	}
}

// TestAuditBuffer 验证审计缓冲记录、自动补时间、快照。
func TestAuditBuffer(t *testing.T) {
	b := NewAuditBuffer(2)
	b.Record(AuditEntry{User: "alice", Group: "g1", Action: "forward"})
	b.Record(AuditEntry{User: "bob", Group: "g2", Action: "direct"})
	b.Record(AuditEntry{User: "carol", Group: "g3", Action: "reject"}) // 淘汰 alice

	snap := b.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("容量 2 应保留 2 条，得到 %d", len(snap))
	}
	if snap[0].User != "bob" || snap[1].User != "carol" {
		t.Fatalf("淘汰顺序不符: %+v", snap)
	}
	if snap[0].Time.IsZero() {
		t.Fatal("Record 应自动补时间")
	}
}

// TestRingConcurrent 验证并发 push + subscribe + snapshot 无 data race（配合 -race）。
func TestRingConcurrent(t *testing.T) {
	b := NewLogBuffer(500)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup

	// 多写入者
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				b.ring.push(LogEntry{Level: "info", Message: "m"})
			}
		}()
	}

	// 多订阅者（不断订阅/消费/注销）
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				ch, done, unsub := b.Subscribe(16)
				consumed := make(chan struct{})
				go func() {
					for {
						select {
						case <-done:
							close(consumed)
							return
						case <-ch:
						}
					}
				}()
				time.Sleep(5 * time.Millisecond)
				unsub()
				<-consumed
			}
		}()
	}

	// 快照读取者
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			_ = b.Snapshot("info")
			_ = b.Len()
		}
	}()

	wg.Wait()
}
