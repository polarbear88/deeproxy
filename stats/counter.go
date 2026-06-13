// Package stats 提供与转发热路径解耦的【内存原子计数 + 异步批量落库】统计子系统。
//
// 设计要点（对应计划一号硬约束：转发热路径零锁、零持久化）：
//   - 转发侧只调用 Counter.Add* 做 atomic.Int64 累加，绝不加锁、绝不碰 SQLite。
//   - flush worker（见 flush.go）每 5~10s 把内存增量批量写入 SQLite 分钟桶（经 store 单写协程）。
//   - 实时速率（KB/s）由 flush 周期对累计字节做差分得到瞬时值，供仪表盘实时区读取。
//
// 本文件实现纯内存计数器：按 (groupID,userID) 维度累加上/下行字节与请求数，
// 并维护实时速率瞬时值。计数器对转发侧的 Add 路径只有 atomic 操作，无锁。
package stats

import (
	"sync"
	"sync/atomic"
)

// dimKey 是计数维度键：(分组, 用户)。
// 用值类型作 map key，避免指针逃逸与额外分配。
type dimKey struct {
	groupID int64
	userID  int64
}

// dimCounter 是单个维度的累计计数器（全部 atomic，转发侧无锁累加）。
//
// 字段语义：
//   - upBytes/downBytes：自进程启动以来该维度累计的上/下行字节（单调递增）。
//   - reqCount：累计请求（连接）数。
//   - 这些是“总累计值”；flush 时与上次快照做差分得到“本周期增量”落桶，
//     差分用 lastUp/lastDown/lastReq 记录上次 flush 读到的累计值（仅 flush worker 访问，无需原子）。
type dimCounter struct {
	upBytes   atomic.Int64
	downBytes atomic.Int64
	reqCount  atomic.Int64

	// 下面三个仅 flush worker 串行访问，用于差分出本周期增量，非热路径、无需原子。
	lastUp   int64
	lastDown int64
	lastReq  int64
}

// Counter 是全局统计计数器。
//
// 并发模型：
//   - 转发侧（多 goroutine）调用 AddUp/AddDown/IncReq：先以 RLock 在 map 中取/建维度计数器，
//     再对该计数器做 atomic 累加。map 仅在“首次出现某维度”时加写锁创建，绝大多数情况走读锁；
//     真正的字节累加是 atomic，不在任何锁内。故对转发吞吐无可测影响。
//   - flush worker（单 goroutine）周期遍历 map 做差分，见 Snapshot/snapshotDeltas。
type Counter struct {
	mu   sync.RWMutex
	dims map[dimKey]*dimCounter

	// 进程级实时活跃连接数（建连 +1，连接结束 -1），atomic 无锁。
	activeConns atomic.Int64

	// 进程级拒连计数（仪表盘“今日拒连”分两类）：
	//   - rejectRule：规则 reject 动作导致的拒连。
	//   - rejectAuth：鉴权失败（密码/授权/用户名解析）导致的拒连。
	// 注：这两类是进程累计瞬时值，今日值由 SQLite 聚合或单独埋点提供；此处供实时区快速读取。
	rejectRule atomic.Int64
	rejectAuth atomic.Int64

	// 动作分布累计（forward/direct/reject 连接数），供仪表盘实时占比兜底。
	actForward atomic.Int64
	actDirect  atomic.Int64
	actReject  atomic.Int64
}

// NewCounter 创建空计数器。
func NewCounter() *Counter {
	return &Counter{dims: make(map[dimKey]*dimCounter)}
}

// counterFor 取（或惰性创建）某维度的计数器。
// 快路径走读锁（维度已存在）；仅首次出现该维度时升级为写锁创建一次。
func (c *Counter) counterFor(groupID, userID int64) *dimCounter {
	k := dimKey{groupID: groupID, userID: userID}

	c.mu.RLock()
	dc := c.dims[k]
	c.mu.RUnlock()
	if dc != nil {
		return dc
	}

	// 慢路径：维度首次出现，加写锁创建（double-check 避免并发重复创建）。
	c.mu.Lock()
	defer c.mu.Unlock()
	if dc = c.dims[k]; dc == nil {
		dc = &dimCounter{}
		c.dims[k] = dc
	}
	return dc
}

// AddUp 累加某维度的上行字节（转发侧热路径调用，仅 atomic）。
func (c *Counter) AddUp(groupID, userID, n int64) {
	if n <= 0 {
		return
	}
	c.counterFor(groupID, userID).upBytes.Add(n)
}

// AddDown 累加某维度的下行字节（转发侧热路径调用，仅 atomic）。
func (c *Counter) AddDown(groupID, userID, n int64) {
	if n <= 0 {
		return
	}
	c.counterFor(groupID, userID).downBytes.Add(n)
}

// IncReq 累加某维度的请求（连接）数（建连成功时调用一次）。
func (c *Counter) IncReq(groupID, userID int64) {
	c.counterFor(groupID, userID).reqCount.Add(1)
}

// ConnOpened 在建连成功时调用：活跃连接 +1。
func (c *Counter) ConnOpened() { c.activeConns.Add(1) }

// ConnClosed 在连接结束时调用：活跃连接 -1。
func (c *Counter) ConnClosed() { c.activeConns.Add(-1) }

// ActiveConns 返回当前活跃连接数。
func (c *Counter) ActiveConns() int64 { return c.activeConns.Load() }

// IncRejectRule 规则 reject 拒连 +1。
func (c *Counter) IncRejectRule() { c.rejectRule.Add(1) }

// IncRejectAuth 鉴权失败拒连 +1。
func (c *Counter) IncRejectAuth() { c.rejectAuth.Add(1) }

// IncActionForward forward 动作连接 +1。
func (c *Counter) IncActionForward() { c.actForward.Add(1) }

// IncActionDirect direct 动作连接 +1。
func (c *Counter) IncActionDirect() { c.actDirect.Add(1) }

// IncActionReject reject 动作连接 +1（同时通常 IncRejectRule）。
func (c *Counter) IncActionReject() { c.actReject.Add(1) }

// Realtime 是供仪表盘实时区读取的瞬时快照。
type Realtime struct {
	ActiveConns int64 // 当前活跃连接
	RejectRule  int64 // 累计规则拒连
	RejectAuth  int64 // 累计鉴权拒连
	ActForward  int64 // 累计 forward
	ActDirect   int64 // 累计 direct
	ActReject   int64 // 累计 reject
}

// RealtimeSnapshot 原子读取各进程级瞬时计数（无锁）。
func (c *Counter) RealtimeSnapshot() Realtime {
	return Realtime{
		ActiveConns: c.activeConns.Load(),
		RejectRule:  c.rejectRule.Load(),
		RejectAuth:  c.rejectAuth.Load(),
		ActForward:  c.actForward.Load(),
		ActDirect:   c.actDirect.Load(),
		ActReject:   c.actReject.Load(),
	}
}

// DimSnapshot 是某维度本周期增量（flush 内部用）。
type DimSnapshot struct {
	GroupID   int64
	UserID    int64
	UpBytes   int64
	DownBytes int64
	ReqCount  int64
}

// CollectDeltas 遍历所有维度，对累计值与上次基线求差，返回本周期非零增量并推进基线。
// 仅 flush worker 单 goroutine 调用。
func (c *Counter) CollectDeltas() []DimSnapshot {
	c.mu.RLock()
	// 复制维度指针快照，缩短持锁时间；之后对各 dimCounter 的 atomic 读不需要 map 锁。
	keys := make([]dimKey, 0, len(c.dims))
	ptrs := make([]*dimCounter, 0, len(c.dims))
	for k, dc := range c.dims {
		keys = append(keys, k)
		ptrs = append(ptrs, dc)
	}
	c.mu.RUnlock()

	out := make([]DimSnapshot, 0, len(ptrs))
	for i, dc := range ptrs {
		up := dc.upBytes.Load()
		down := dc.downBytes.Load()
		req := dc.reqCount.Load()

		dUp := up - dc.lastUp
		dDown := down - dc.lastDown
		dReq := req - dc.lastReq
		// 推进基线（仅本 goroutine 写）
		dc.lastUp = up
		dc.lastDown = down
		dc.lastReq = req

		if dUp == 0 && dDown == 0 && dReq == 0 {
			continue // 本周期无增量，跳过
		}
		out = append(out, DimSnapshot{
			GroupID:   keys[i].groupID,
			UserID:    keys[i].userID,
			UpBytes:   dUp,
			DownBytes: dDown,
			ReqCount:  dReq,
		})
	}
	return out
}
