package main

import (
	"context"
	"log/slog"
	"time"
)

// refresher.go 实现健康检查触发的【去抖快照重建】（P2）。
//
// 背景：每次健康状态翻转都会调 refresher.Refresh() → snapbuild.RebuildAndSwap，
// 而 Rebuild 是【全量物化】（读全部 group/upstream/user/rule 重编译所有引擎）。一轮健康
// 扫描里若有多条上游近乎同时翻转，会触发多次相邻的全量重建，在大池/频繁抖动下造成
// CPU 与 rebuildMu 锁竞争。去抖把「一段静默窗口内的多次 Refresh」合并为【一次】重建。
//
// 为什么只对健康触发去抖、不动 API 写：API 写经 a.rebuildAndSwap 同步重建并需把结果
// （成功/失败）回给前端，必须同步、不可合并；健康翻转是后台事件、无人等待返回，
// 合并为异步一次重建对正确性无影响（最终仍收敛到含全部翻转的最新快照）。

// debouncedRefresher 把多次 Refresh 合并为窗口内一次实际重建，实现 health.SnapshotRefresher。
type debouncedRefresher struct {
	fn      func() error  // 实际重建（snapbuild.RebuildAndSwap 闭包）
	delay   time.Duration // 静默合并窗口
	logger  *slog.Logger
	trigger chan struct{} // 触发信号（带缓冲 1，非阻塞投递，自然合并突发）
}

// newDebouncedRefresher 创建去抖刷新器。delay 是合并窗口（如 2s）。
func newDebouncedRefresher(fn func() error, delay time.Duration, logger *slog.Logger) *debouncedRefresher {
	return &debouncedRefresher{
		fn:      fn,
		delay:   delay,
		logger:  logger,
		trigger: make(chan struct{}, 1),
	}
}

// Refresh 非阻塞投递一次重建请求（合并：缓冲已满说明已有待处理请求，直接丢弃即可）。
// 永远返回 nil——健康触发方不关心重建结果（失败仅记日志，下次翻转会再次触发）。
func (d *debouncedRefresher) Refresh() error {
	select {
	case d.trigger <- struct{}{}:
	default: // 已有待处理触发，合并入同一次重建
	}
	return nil
}

// run 是去抖循环：收到首个触发后等 delay 静默窗口（期间继续吸收触发），到点执行一次重建。
// 重建期间到达的新触发会在本次完成后被下一轮立即处理（trigger 缓冲保证不丢）。阻塞至 ctx 取消。
func (d *debouncedRefresher) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.trigger:
			// 固定窗口合并：等 delay，期间到达的触发都并入本次（不重置计时，保证有界延迟）。
			timer := time.NewTimer(d.delay)
		collect:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-d.trigger:
					// 吸收并入本次重建
				case <-timer.C:
					break collect
				}
			}
			if err := d.fn(); err != nil {
				d.logger.Warn("健康去抖重建快照失败（已保留旧快照）", "err", err)
			}
		}
	}
}
