package flush

import (
	"context"
	"log/slog"
	"time"

	"deeproxy/stats"
	"deeproxy/store"
)

// flush.go 实现统计落库 worker 与过期清理调度：
//   - Flusher 周期（默认 5s）把 Counter 的本周期增量批量写入 SQLite 分钟桶（经 store 单写协程 AC-12）。
//   - 同一 worker 兼做按保留期清理（AC-13），低频触发（默认每小时）调 store.CleanupBefore。
//
// 与转发热路径完全解耦：本 worker 是独立后台 goroutine，转发侧只对 Counter 做 atomic 累加。

const (
	// defaultFlushInterval 是统计 flush 周期（计划要求 5~10s）。
	defaultFlushInterval = 5 * time.Second
	// defaultCleanupInterval 是过期桶清理周期（低频即可）。
	defaultCleanupInterval = 1 * time.Hour
)

// Flusher 负责把内存计数周期性落库并清理过期桶。
type Flusher struct {
	counter *stats.Counter
	store   *store.Store
	logger  *slog.Logger

	flushInterval   time.Duration
	cleanupInterval time.Duration
	// retentionDays 是统计保留天数（清理 cutoff = now - retentionDays），由系统设置提供。
	retentionDays int
}

// FlusherOption 用于可选配置 Flusher（间隔、保留期）。
type FlusherOption func(*Flusher)

// WithFlushInterval 自定义 flush 周期。
func WithFlushInterval(d time.Duration) FlusherOption {
	return func(f *Flusher) {
		if d > 0 {
			f.flushInterval = d
		}
	}
}

// WithCleanupInterval 自定义清理周期。
func WithCleanupInterval(d time.Duration) FlusherOption {
	return func(f *Flusher) {
		if d > 0 {
			f.cleanupInterval = d
		}
	}
}

// WithRetentionDays 设置统计保留天数（<=0 时不清理）。
func WithRetentionDays(days int) FlusherOption {
	return func(f *Flusher) { f.retentionDays = days }
}

// NewFlusher 创建 Flusher。logger 可为 nil（则用 slog.Default）。
func NewFlusher(counter *stats.Counter, st *store.Store, logger *slog.Logger, opts ...FlusherOption) *Flusher {
	if logger == nil {
		logger = slog.Default()
	}
	f := &Flusher{
		counter:         counter,
		store:           st,
		logger:          logger,
		flushInterval:   defaultFlushInterval,
		cleanupInterval: defaultCleanupInterval,
		retentionDays:   30,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Run 启动 flush + cleanup 循环，阻塞直到 ctx 取消。
// 退出前做最后一次 flush，避免丢失尾部增量。
func (f *Flusher) Run(ctx context.Context) {
	flushTicker := time.NewTicker(f.flushInterval)
	cleanupTicker := time.NewTicker(f.cleanupInterval)
	defer flushTicker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			// 退出前 flush 一次，尽量不丢尾部数据。
			f.flushOnce()
			return
		case <-flushTicker.C:
			f.flushOnce()
		case <-cleanupTicker.C:
			f.cleanupOnce()
		}
	}
}

// flushOnce 收集本周期增量并批量落库。无增量时直接返回。
func (f *Flusher) flushOnce() {
	dims := f.counter.CollectDeltas()
	if len(dims) == 0 {
		return
	}

	// 当前分钟桶时间（所有本周期增量归入此桶；store 侧会 upsert 累加）。
	bucket := store.TruncateToMinute(time.Now())

	deltas := make([]store.StatDelta, 0, len(dims))
	for _, d := range dims {
		deltas = append(deltas, store.StatDelta{
			GroupID:    d.GroupID,
			UserID:     d.UserID,
			BucketTime: bucket,
			UpBytes:    d.UpBytes,
			DownBytes:  d.DownBytes,
			ReqCount:   d.ReqCount,
		})
	}

	if err := f.store.FlushTrafficStats(deltas); err != nil {
		// 落库失败不影响转发；记录告警，增量已从计数器差分扣除会丢失——
		// 这是可接受的统计精度损失（统计非关键路径，且失败应极罕见）。
		f.logger.Warn("统计 flush 落库失败", "err", err, "dims", len(deltas))
	}
}

// cleanupOnce 按保留期删除过期聚合桶（retentionDays<=0 时跳过）。
func (f *Flusher) cleanupOnce() {
	if f.retentionDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -f.retentionDays)
	n, err := f.store.CleanupBefore(cutoff)
	if err != nil {
		f.logger.Warn("清理过期统计桶失败", "err", err)
		return
	}
	if n > 0 {
		f.logger.Info("已清理过期统计桶", "rows", n, "cutoff", cutoff.Format(time.RFC3339))
	}
}
