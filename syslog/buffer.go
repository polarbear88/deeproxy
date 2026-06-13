// Package syslog 提供【仅内存】的系统日志与连接审计缓冲，配合 SSE 实时推送（AC-33/34/35/36）。
//
// 设计要点：
//   - 日志/审计只保留在内存环形缓冲（默认 5000 条），满则淘汰最旧；不落 SQLite、重启丢失。
//   - 对转发热路径零影响：slog 写入只做一次加锁的环形写（非字节中继循环内）；
//     转发的字节中继不经过本包。
//   - 实时推送：缓冲支持订阅（subscribe），新日志通过 channel 广播给各 SSE 连接。
//
// 本文件实现泛型环形缓冲 + 发布订阅，供日志(LogEntry)与审计(AuditEntry)复用（DRY）。
package syslog

import "sync"

// defaultCapacity 是环形缓冲默认容量（与 spec 默认 5000 一致）。
const defaultCapacity = 5000

// subscriber 是一个订阅句柄：data 收新条目，done 关闭表示注销。
//
// 设计（避免“向已关闭 channel 发送”panic）：
//   - 只有注销侧会 close(done)，生产者(push)只读 done；data 永不被 close（由 GC 回收）。
//   - push 发送时 select 同时监听 done，注销后立即停止向该订阅者投递，且非阻塞丢弃。
type subscriber[T any] struct {
	data chan T
	done chan struct{}
}

// ringBuffer 是带发布订阅的并发安全环形缓冲（泛型，供日志与审计复用）。
//
// 并发模型：所有读写经 mu 保护。写入是 O(1) 覆盖；订阅者通过带缓冲 channel 接收新条目，
// channel 满时丢弃该条推送（不阻塞写入方）——保证慢订阅者不拖慢日志写入与转发。
type ringBuffer[T any] struct {
	mu       sync.Mutex
	buf      []T
	capacity int
	size     int // 当前有效条数（<=capacity）
	next     int // 下一个写入位置（环形）

	// 订阅者集合：每个 SSE 连接注册一个 subscriber；用 map 便于注销。
	subs map[*subscriber[T]]struct{}
}

// newRingBuffer 创建指定容量的环形缓冲（capacity<=0 时用默认值）。
func newRingBuffer[T any](capacity int) *ringBuffer[T] {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &ringBuffer[T]{
		buf:      make([]T, capacity),
		capacity: capacity,
		subs:     make(map[*subscriber[T]]struct{}),
	}
}

// push 写入一条，满则覆盖最旧，并向所有订阅者广播（非阻塞）。
func (r *ringBuffer[T]) push(item T) {
	r.mu.Lock()
	r.buf[r.next] = item
	r.next = (r.next + 1) % r.capacity
	if r.size < r.capacity {
		r.size++
	}
	// 复制订阅者句柄，缩短持锁时间后再发送，避免在持锁期间因订阅者处理慢而阻塞。
	subs := make([]*subscriber[T], 0, len(r.subs))
	for s := range r.subs {
		subs = append(subs, s)
	}
	r.mu.Unlock()

	for _, s := range subs {
		// 同时监听 done：注销后立即停止投递；data 满则丢弃该条，绝不阻塞写入方。
		select {
		case <-s.done:
		case s.data <- item:
		default:
		}
	}
}

// snapshot 返回当前缓冲内全部条目的按时间顺序拷贝（最旧→最新）。
func (r *ringBuffer[T]) snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]T, 0, r.size)
	if r.size < r.capacity {
		// 未写满：有效区间是 [0, next)。
		out = append(out, r.buf[:r.next]...)
	} else {
		// 已写满：最旧在 next 处，环形读一圈。
		out = append(out, r.buf[r.next:]...)
		out = append(out, r.buf[:r.next]...)
	}
	return out
}

// subscribe 注册一个订阅，返回数据 channel、done channel 与注销函数。
//   - 数据 channel 收新条目（带缓冲，满则该条被丢弃，不阻塞写入方）。
//   - done channel 关闭表示已注销，消费者应据此停止读取（data 不会被 close，避免发送 panic）。
//   - 注销函数从订阅集合移除并 close(done)，幂等。
func (r *ringBuffer[T]) subscribe(bufSize int) (<-chan T, <-chan struct{}, func()) {
	if bufSize <= 0 {
		bufSize = 256
	}
	s := &subscriber[T]{
		data: make(chan T, bufSize),
		done: make(chan struct{}),
	}
	r.mu.Lock()
	r.subs[s] = struct{}{}
	r.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.subs, s)
			r.mu.Unlock()
			close(s.done) // 仅注销侧关闭 done；data 永不 close。
		})
	}
	return s.data, s.done, unsub
}

// len 返回当前有效条数（测试用）。
func (r *ringBuffer[T]) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}
