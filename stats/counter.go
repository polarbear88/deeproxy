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
	"time"
)

// dimKey 是计数维度键：(分组, 用户)。
// 用值类型作 map key，避免指针逃逸与额外分配。
type dimKey struct {
	groupID int64
	userID  int64
}

// domainKey 是目标域名计数维度键：(完整主机名, 分组)。
// 与 dimKey 平行但分离——域名维度基数远高于 (group,user)，单独管理便于做 eviction。
type domainKey struct {
	domain  string
	groupID int64
}

// evictAfterIdleCycles 是域名 key 的闲置回收阈值：连续这么多个 flush 周期零增量即从内存
// map 删除（约 evictAfterIdleCycles × flushInterval ≈ 3×5s = 15s 闲置回收）。
//
// 为什么 domain 维度必须 eviction 而 (group,user) 维度不需要：(group,user) 基数有界
// （受配置的分组/用户数约束），CollectDeltas 从不删 key 是对的；而 domain 维度无界
// （长期运行会见到海量唯一主机名/IP），若照抄「从不删 key」会造成内存泄漏 + 差分遍历
// 循环越拉越长。故这里给 domain map 加按闲置周期回收，使其随「活跃域名集合」有界。
const evictAfterIdleCycles = 3

// domainCounter 是单个 (域名,分组) 的命中计数器。
//   - hits：自进程启动以来该维度累计命中数（atomic，转发侧无锁累加）。
//   - lastHits：上次 flush 读到的累计值（仅 flush worker 串行访问，用于差分）。
//   - idleCycles：连续零增量周期数（仅 flush worker 访问，达阈值触发 eviction）。
type domainCounter struct {
	hits atomic.Int64

	lastHits   int64
	idleCycles int
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

	// domMu 独立保护 domains map（不复用 c.mu）：eviction 需在 flush goroutine 内持写锁
	// 遍历删 key，若复用 c.mu 会与流量计数热路径（建首个维度的写锁）争锁。独立锁让
	// domain 维度的建/删 key 与流量计数互不干扰，吸收「独立计数结构」的锁隔离优点。
	domMu   sync.RWMutex
	domains map[domainKey]*domainCounter

	// 进程级实时活跃连接数（建连 +1，连接结束 -1），atomic 无锁。
	activeConns atomic.Int64

	// 进程级拒连计数（仪表盘“今日拒连”分两类）：
	//   - rejectRule：规则 reject 动作导致的拒连。
	//   - rejectAuth：鉴权失败（密码/授权/用户名解析）导致的拒连。
	// 注：这两类是进程累计瞬时值，今日值由 SQLite 聚合或单独埋点提供；此处供实时区快速读取。
	rejectRule atomic.Int64
	rejectAuth atomic.Int64

	// —— M2：今日拒连（按本地自然日归零）——
	// 拒连不落 SQLite 时间桶（与流量不同），故无法像 todayUp 那样从库聚合「今日」。
	// 为让仪表盘「今日拒连」真正按日归零，这里额外维护一组【今日】计数 + 当天日期戳：
	// 埋点与读取前都调 rollRejectDayLocked 检查是否跨日，跨日则把今日两类清零并更新日期戳。
	// 用一把轻量小锁（仅拒连路径与仪表盘读取走，远低频于字节中继，绝不在中继循环内）。
	rejectDayMu   sync.Mutex
	rejectDayKey  int64 // 当天日期键（年*10000+月*100+日，本地时区）
	todayRejRule  int64 // 今日规则拒连
	todayRejAuth  int64 // 今日鉴权拒连

	// 动作分布累计（forward/direct/reject 连接数），供仪表盘实时占比兜底。
	actForward atomic.Int64
	actDirect  atomic.Int64
	actReject  atomic.Int64

	// 进程级累计上/下行字节总量（跨所有维度，单调递增），供实时速率采样（AC-5.5）。
	// 为什么单独维护而不汇总 dims：实时速率需在「两次 overview 请求间」对累计字节做差分，
	// 若遍历 dims 汇总既慢又会与 flush 的差分基线纠缠；这里用两个进程级 atomic，
	// AddUp/AddDown 各多一次 atomic.Add（纳秒级、无锁），读取 O(1)，且与 SQLite 完全解耦。
	totalUpBytes   atomic.Int64
	totalDownBytes atomic.Int64

	// —— P4：实时速率（字节/秒）——
	// 旧实现由仪表盘 API 在【每次请求间】对累计字节做差分，速率因此耦合轮询节奏：
	// 不轮询则无速率、多标签页并发会互相污染基线。改由 flush worker 以【固定周期】
	// 调 SampleRates 差分一次、把结果存入这两个 atomic；仪表盘只读，O(1)、与轮询解耦。
	upRateBps   atomic.Int64 // 最近一次采样的上行速率（字节/秒）
	downRateBps atomic.Int64 // 最近一次采样的下行速率（字节/秒）

	// SampleRates 的上次采样基线（仅 flush worker 单 goroutine 访问，无需原子）。
	rateLastUp   int64
	rateLastDown int64
	rateLastTS   time.Time
}

// NewCounter 创建空计数器。
func NewCounter() *Counter {
	return &Counter{
		dims:    make(map[dimKey]*dimCounter),
		domains: make(map[domainKey]*domainCounter),
	}
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
	c.totalUpBytes.Add(n) // 进程级累计，供实时速率采样（AC-5.5）
}

// AddDown 累加某维度的下行字节（转发侧热路径调用，仅 atomic）。
func (c *Counter) AddDown(groupID, userID, n int64) {
	if n <= 0 {
		return
	}
	c.counterFor(groupID, userID).downBytes.Add(n)
	c.totalDownBytes.Add(n) // 进程级累计，供实时速率采样（AC-5.5）
}

// IncReq 累加某维度的请求（连接）数（建连成功时调用一次）。
func (c *Counter) IncReq(groupID, userID int64) {
	c.counterFor(groupID, userID).reqCount.Add(1)
}

// IncDomain 累加某 (域名,分组) 的目标域名命中数（建连成功时与 IncReq 同位调用）。
//
// host 为空直接返回（防御性，避免脏「空域名」行）；纯 IP 目标也作为 key 计入。
// 快路径走读锁取已存在计数器，仅首次出现该维度时升级写锁创建一次（double-check）；
// 真正的命中累加是 atomic，在锁外执行。被 eviction 删除后，下次命中经此路径以全新
// domainCounter（lastHits=0）重建，差分从命中数起算、不丢不重。
func (c *Counter) IncDomain(host string, groupID int64) {
	if host == "" {
		return
	}
	k := domainKey{domain: host, groupID: groupID}

	c.domMu.RLock()
	dc := c.domains[k]
	c.domMu.RUnlock()
	if dc == nil {
		c.domMu.Lock()
		if dc = c.domains[k]; dc == nil {
			dc = &domainCounter{}
			c.domains[k] = dc
		}
		c.domMu.Unlock()
	}
	dc.hits.Add(1)
}

// ConnOpened 在建连成功时调用：活跃连接 +1。
func (c *Counter) ConnOpened() { c.activeConns.Add(1) }

// ConnClosed 在连接结束时调用：活跃连接 -1。
func (c *Counter) ConnClosed() { c.activeConns.Add(-1) }

// ActiveConns 返回当前活跃连接数。
func (c *Counter) ActiveConns() int64 { return c.activeConns.Load() }

// IncRejectRule 规则 reject 拒连 +1（同时累加今日计数，M2）。
func (c *Counter) IncRejectRule() {
	c.rejectRule.Add(1)
	c.rejectDayMu.Lock()
	c.rollRejectDayLocked()
	c.todayRejRule++
	c.rejectDayMu.Unlock()
}

// IncRejectAuth 鉴权失败拒连 +1（同时累加今日计数，M2）。
func (c *Counter) IncRejectAuth() {
	c.rejectAuth.Add(1)
	c.rejectDayMu.Lock()
	c.rollRejectDayLocked()
	c.todayRejAuth++
	c.rejectDayMu.Unlock()
}

// rollRejectDayLocked 检查是否跨入新的本地自然日，跨日则把今日两类拒连清零并更新日期戳。
// 必须在持有 rejectDayMu 时调用。埋点与读取前都会先调它，保证「今日」语义随墙钟自然滚动。
func (c *Counter) rollRejectDayLocked() {
	now := time.Now()
	key := int64(now.Year())*10000 + int64(now.Month())*100 + int64(now.Day())
	if key != c.rejectDayKey {
		c.rejectDayKey = key
		c.todayRejRule = 0
		c.todayRejAuth = 0
	}
}

// TodayRejects 返回今日（本地自然日）规则/鉴权拒连数，跨日自动归零（M2）。
func (c *Counter) TodayRejects() (rule, auth int64) {
	c.rejectDayMu.Lock()
	defer c.rejectDayMu.Unlock()
	c.rollRejectDayLocked()
	return c.todayRejRule, c.todayRejAuth
}

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

	// 进程级累计上/下行字节总量（单调递增），供实时速率采样（AC-5.5，差分计算 KB/s）。
	TotalUpBytes   int64
	TotalDownBytes int64

	// P4：最近一次由 flush worker 采样的实时速率（字节/秒），与轮询解耦。
	UpRateBps   int64
	DownRateBps int64
}

// RealtimeSnapshot 原子读取各进程级瞬时计数（无锁）。
func (c *Counter) RealtimeSnapshot() Realtime {
	return Realtime{
		ActiveConns:    c.activeConns.Load(),
		RejectRule:     c.rejectRule.Load(),
		RejectAuth:     c.rejectAuth.Load(),
		ActForward:     c.actForward.Load(),
		ActDirect:      c.actDirect.Load(),
		ActReject:      c.actReject.Load(),
		TotalUpBytes:   c.totalUpBytes.Load(),
		TotalDownBytes: c.totalDownBytes.Load(),
		UpRateBps:      c.upRateBps.Load(),
		DownRateBps:    c.downRateBps.Load(),
	}
}

// SampleRates 由 flush worker 以固定周期调用一次：对累计字节做差分算出瞬时速率（字节/秒），
// 存入 upRateBps/downRateBps 供仪表盘 O(1) 读取（P4：把速率采样从「每请求差分」改为
// 「固定周期差分」，与轮询节奏彻底解耦，多标签页读取互不干扰）。
//
// 仅 flush worker 单 goroutine 调用，故 rateLast* 基线无需原子；结果用 atomic 存，读侧无锁。
// 首次调用（基线为零）只建立基线、速率记 0；负增量（理论上不会，计数单调）兜底为 0。
func (c *Counter) SampleRates() {
	now := time.Now()
	up := c.totalUpBytes.Load()
	down := c.totalDownBytes.Load()

	if !c.rateLastTS.IsZero() {
		if dt := now.Sub(c.rateLastTS).Seconds(); dt > 0 {
			if u := float64(up-c.rateLastUp) / dt; u > 0 {
				c.upRateBps.Store(int64(u))
			} else {
				c.upRateBps.Store(0)
			}
			if d := float64(down-c.rateLastDown) / dt; d > 0 {
				c.downRateBps.Store(int64(d))
			} else {
				c.downRateBps.Store(0)
			}
		}
	}
	c.rateLastUp, c.rateLastDown, c.rateLastTS = up, down, now
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

// DomainSnapshot 是某 (域名,分组) 本周期命中增量（flush 内部用）。
type DomainSnapshot struct {
	Domain   string
	GroupID  int64
	HitCount int64
}

// CollectDomainDeltas 遍历所有域名维度求差返回本周期非零增量并推进基线，
// 同时回收闲置 key（与 CollectDeltas 的关键差异：domain 无界，必须 eviction）。
// 仅 flush worker 单 goroutine 调用。
//
// 流程：
//  1. 持读锁复制键/指针快照，缩短持锁时间（之后对各 domainCounter 的 atomic 读不需 map 锁）。
//  2. 逐个差分：d>0 计入输出并清零 idleCycles；d==0 累加 idleCycles。
//  3. eviction：对 idleCycles 达阈值的 key，持写锁统一 delete；删前 double-check
//     hits.Load()==lastHits，确保「该 key 累计命中已全部落库」才丢弃（不等则不删、归零计数）。
//
// 注：存在一个有意接受的 ≤1 命中欠计窗口——某次 IncDomain 已取得指针、尚未 Add 时
// 与并发 eviction 竞争，该自增落在被删的孤儿计数器上而丢失。这等价于 flush.go 已接受的
// 「统计精度损失」，对 Top-N 展示无实质影响，-race 不会报（仍是合法指针上的 atomic）。
// 严禁为消除此窗口而给热路径 IncDomain 加重锁（会违反「转发热路径零成本」硬约束）。
func (c *Counter) CollectDomainDeltas() []DomainSnapshot {
	c.domMu.RLock()
	keys := make([]domainKey, 0, len(c.domains))
	ptrs := make([]*domainCounter, 0, len(c.domains))
	for k, dc := range c.domains {
		keys = append(keys, k)
		ptrs = append(ptrs, dc)
	}
	c.domMu.RUnlock()

	out := make([]DomainSnapshot, 0, len(ptrs))
	var evict []domainKey // 待回收的闲置 key
	for i, dc := range ptrs {
		h := dc.hits.Load()
		d := h - dc.lastHits
		dc.lastHits = h // 推进基线（仅本 goroutine 写）

		if d > 0 {
			dc.idleCycles = 0
			out = append(out, DomainSnapshot{
				Domain:   keys[i].domain,
				GroupID:  keys[i].groupID,
				HitCount: d,
			})
		} else {
			dc.idleCycles++
			if dc.idleCycles >= evictAfterIdleCycles {
				evict = append(evict, keys[i])
			}
		}
	}

	// eviction：持写锁统一回收闲置 key，删前再次确认无新命中（防删除窗口内的命中丢失）。
	if len(evict) > 0 {
		c.domMu.Lock()
		for _, k := range evict {
			dc := c.domains[k]
			if dc == nil {
				continue
			}
			if dc.hits.Load() == dc.lastHits {
				delete(c.domains, k)
			} else {
				// 删除窗口内又有新命中：不删，归零闲置计数，下个周期正常差分。
				dc.idleCycles = 0
			}
		}
		c.domMu.Unlock()
	}
	return out
}
