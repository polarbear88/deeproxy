// Package connreg 是「实时连接」功能的活跃连接登记表（中性叶子包）。
//
// 为什么独立成包、且只依赖标准库：
//   - 它由 SOCKS5 服务端（server 包）在连接生命周期点写入，由管理后端（api 包）读取。
//     若把类型放进 server 包，会逼出 api → server 的反向依赖，把整个数据面（go-socks5、
//     dialer、rule…）拖进只读管理后端的编译图。放进 stats 又会污染纯计数包。
//   - 故仿照既有 syslog.AuditBuffer（中性叶子，server 写、api 读）单独成包；
//     action 用 string 而非 rule.Action 承载，使本包零项目依赖、永不形成 import 环。
//
// 一号硬约束：Register/SetUpstream/SetAction/Deregister 均为 O(1)，只在连接
// 开始/拨号完成/结束处调用，绝不进入中继热路径（io.Copy / splice 零拷贝快路径）。
package connreg

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultLimit 是单次返回的活跃连接条数上限 N（截断上限，可配置常量）。
// 后端只返回 Top-N，避免活跃连接过多时前端渲染卡死（用户核心诉求）。
const DefaultLimit = 500

// ConnMeta 是登记一条活跃连接时已知的前置元数据（连接进入即可填）。
// action 用 string（非 rule.Action）以保持 connreg 零项目依赖（中性叶子包）。
type ConnMeta struct {
	Target string    // 目标主机（server.targetHost(req)：域名或 IP）
	Action string    // "forward"/"direct"（needsSniff 嗅探后由 SetAction 回填真值）
	User   string    // 代理用户名（d.auth.User）
	Group  string    // 分组名（d.auth.Group）
	Client string    // 客户端来源地址（writer.(net.Conn).RemoteAddr().String()）
	Start  time.Time // 连接开始时间（time.Now()，用于算时长与排序）
}

// activeConn 是登记表内部存储单元。
//
// upstream/action 为「可后填」字段：Type B 上游在 dialWithFailover 之后才确定，
// 嗅探路径动作在 peek 首包之后才确定，故用 atomic.Pointer[string] 承载，
// 使后填（SetUpstream/SetAction）与 Snapshot 的并发读天然安全，无需逐条 mutex。
//
// 其余不可变字段（id、meta 的各项）在 Store 入 map 之前一次性填好；一旦发布到
// sync.Map，任何并发 Snapshot 看到的都是完整不可变数据，读取无需同步。
type activeConn struct {
	id       int64
	meta     ConnMeta
	upstream atomic.Pointer[string] // 后填：实际上游地址；nil 表示未知/直连
	action   atomic.Pointer[string] // 后填：覆盖 meta.Action（仅嗅探解析出 forward/direct 时）
	// 后填：嗅探还原出的真实域名，覆盖 meta.Target，使实时列表目标列显示域名而非 IP。
	// 为什么用 atomic.Pointer 而非改 meta.Target：meta 在 Store 后被 Snapshot 无锁并发读
	// （文档声明「Store 后不可变、读无需同步」），直接写普通字段会触发 data race；
	// 故与 upstream/action 一致用 atomic.Pointer，使后填与并发读天然安全，无需逐条 mutex。
	// 不变量：SetTarget 调用前此指针恒为 nil（Register 绝不初始化为 &""），
	// toView 在 nil 时回退 meta.Target——即嗅探前显示登记时的原始 IP/host，嗅探成功后才被域名覆盖。
	target atomic.Pointer[string]
}

// ConnView 是对外的活跃连接快照视图（喂给 api handler 序列化）。
// 字段为值类型，复制后即与登记表解耦，调用方可安全持有。
type ConnView struct {
	ID          int64  // 连接唯一 ID（= 登记序号 seq，单调递增）
	Target      string // 目标主机
	Action      string // forward/direct（活跃连接不含 reject）
	Upstream    string // 实际上游地址；"" 表示未知/直连（前端渲染 "—"）
	User        string // 代理用户名
	Group       string // 分组名
	Client      string // 客户端来源地址
	StartUnix   int64  // 连接开始时间（unix 秒）
	DurationSec int64  // 连接时长（秒，快照时刻计算）
}

// Registry 是活跃连接登记表：纯活跃 map（connID→activeConn），不保留历史。
//
// 关连接即删，map 永远是「此刻快照」，绝非环形历史缓冲。注册/注销/回填 O(1)、
// 不阻塞中继热路径。权威活跃总数来自 Snapshot 的扫描计数（精确），与 stats.Counter
// 的 activeConns 在同一生命周期点同步；hint 仅作非权威快速提示。
type Registry struct {
	seq    atomic.Int64 // 单调递增 connID 生成器（永不复用存活 ID；亦作排序 tiebreaker）
	hint   atomic.Int64 // 非权威活跃数快速提示（权威 total 由 Snapshot 扫描精确得出）
	active sync.Map     // map[int64]*activeConn —— connID 为不相交键，store/delete 近无锁
}

// New 创建一个空的活跃连接登记表。
func New() *Registry { return &Registry{} }

// Register 登记一条新活跃连接，返回其唯一 connID。
// 调用点：connectHandle 进入处（紧随 counter.ConnOpened()）。O(1)。
func (r *Registry) Register(m ConnMeta) int64 {
	id := r.seq.Add(1) // 单调递增，绝不复用存活 ID
	// 先填好不可变字段再 Store：保证并发 Snapshot 看到的条目数据完整。
	r.active.Store(id, &activeConn{id: id, meta: m})
	r.hint.Add(1)
	return id
}

// SetUpstream 回填指定连接的实际上游地址（拨号成功后调用）。O(1)。
// 连接已注销（id 不存在）时静默忽略——竞态下连接可能已结束。
func (r *Registry) SetUpstream(id int64, upstream string) {
	if v, ok := r.active.Load(id); ok {
		v.(*activeConn).upstream.Store(&upstream)
	}
}

// SetAction 回填指定连接的最终动作（嗅探解析出 forward/direct 后调用）。O(1)。
// 注意：调用方只在 reject 守卫之后调用本方法，故此处永不会写入 "reject"。
func (r *Registry) SetAction(id int64, action string) {
	if v, ok := r.active.Load(id); ok {
		v.(*activeConn).action.Store(&action)
	}
}

// SetTarget 回填嗅探还原出的真实域名（IP 目标经首包嗅探得到域名后调用）。O(1)。
// 为什么用 atomic 而非改 meta.Target：meta 在 Store 后被 Snapshot 无锁并发读，
// 直接写普通字段会触发 data race；故与 upstream/action 一致用 atomic.Pointer。
// 连接已注销（id 不存在）时静默忽略——竞态下连接可能已结束。
func (r *Registry) SetTarget(id int64, target string) {
	if v, ok := r.active.Load(id); ok {
		v.(*activeConn).target.Store(&target)
	}
}

// Deregister 注销一条活跃连接（连接结束时调用，置于 connectHandle 的 defer 区）。O(1)。
func (r *Registry) Deregister(id int64) {
	if _, ok := r.active.LoadAndDelete(id); ok {
		r.hint.Add(-1) // 仅在确实删除了存活条目时递减，避免重复 Deregister 把计数减穿
	}
}

// Len 返回活跃连接数的非权威快速提示（权威值请用 Snapshot 的 total）。
func (r *Registry) Len() int { return int(r.hint.Load()) }

// Snapshot 返回当前活跃连接的有界 Top-N 视图、精确总数与是否被截断。
//
// 有界 top-K 算法（用户「数量过大卡死」诉求在后端侧的落地）：
//   - 单趟 Range 扫描全部活跃连接，维护一个大小 ≤ limit 的堆；
//   - total 为扫描所见条目数（精确，与 stats.ActiveConns 同步），truncated = total > limit；
//   - 全程 O(N) 时间、O(K) 空间，绝不分配「全部 N 条」的切片再排序，
//     即便 N=5万，每 5s 轮询的内存/CPU 也只与 K(=limit) 相关。
//
// 堆方向按 sortBy 参数化（关键正确性，二者比较方向相反）：
//   - "start"（默认）：要 K 个 start_ts 最大者（最新）→ 用「最小堆」，堆顶=所留里最旧者，
//     堆满时新连接更「新」则驱逐堆顶（最旧）。
//   - "duration"：时长最长 = start_ts 最小（最旧）→ 要 K 个最旧者 → 用「最大堆」，
//     堆顶=所留里最新者，堆满时新连接更「旧」则驱逐堆顶（最新）。
//
// start_ts 相等时一律以 seq(connID) 破平局，使跨轮选择确定、行不抖动。
func (r *Registry) Snapshot(limit int, sortBy string) (items []ConnView, total int, truncated bool) {
	// 钳制 limit 到 [1, DefaultLimit]，未知 sortBy 归 "start"。
	if limit <= 0 || limit > DefaultLimit {
		limit = DefaultLimit
	}
	if sortBy != "duration" {
		sortBy = "start"
	}

	now := time.Now()
	h := &boundedHeap{less: worseFunc(sortBy)} // 堆顶=「最该被淘汰」者（即所留 K 个里最差者）

	// 单趟扫描：计 total，构建 ConnView，维护大小 ≤ limit 的有界堆。
	r.active.Range(func(_, value any) bool {
		total++
		ac := value.(*activeConn)
		view := ac.toView(now)
		if h.Len() < limit {
			h.push(view) // 未满直接入堆
		} else if h.better(view, h.peek()) {
			// 已满：新连接「优于」当前堆顶（最差者）才替换，保证始终留 K 个最优。
			h.pop()
			h.push(view)
		}
		return true
	})

	truncated = total > limit

	// 出堆无序，按展示序最终稳定排序（seq 作 tiebreaker，确定无抖动）。
	items = h.drain()
	sortViews(items, sortBy)
	return items, total, truncated
}

// toView 把内部存储单元投影为对外视图（读取后填字段的原子值，做兜底）。
func (ac *activeConn) toView(now time.Time) ConnView {
	upstream := ""
	if p := ac.upstream.Load(); p != nil {
		upstream = *p
	}
	action := ac.meta.Action
	if p := ac.action.Load(); p != nil {
		action = *p // 嗅探回填的动作覆盖登记时的占位动作
	}
	target := ac.meta.Target
	if p := ac.target.Load(); p != nil {
		target = *p // 嗅探回填的域名覆盖登记时的原始 IP（指针为 nil 时回退原始值）
	}
	return ConnView{
		ID:          ac.id,
		Target:      target,
		Action:      action,
		Upstream:    upstream,
		User:        ac.meta.User,
		Group:       ac.meta.Group,
		Client:      ac.meta.Client,
		StartUnix:   ac.meta.Start.Unix(),
		DurationSec: int64(now.Sub(ac.meta.Start).Seconds()),
	}
}

// worseFunc 返回判定「a 是否比 b 更该被淘汰」的比较器（堆顶为最该淘汰者）。
//   - start 模式：要留最新，故最旧(start 最小，平局 seq 最小)者最该淘汰。
//   - duration 模式：要留最久=最旧，故最新(start 最大，平局 seq 最大)者最该淘汰。
func worseFunc(sortBy string) func(a, b ConnView) bool {
	if sortBy == "duration" {
		return func(a, b ConnView) bool { // a 更该淘汰 ⇔ a 更「新」
			if a.StartUnix != b.StartUnix {
				return a.StartUnix > b.StartUnix
			}
			return a.ID > b.ID
		}
	}
	return func(a, b ConnView) bool { // start：a 更该淘汰 ⇔ a 更「旧」
		if a.StartUnix != b.StartUnix {
			return a.StartUnix < b.StartUnix
		}
		return a.ID < b.ID
	}
}

// sortViews 把 ≤K 条结果排成最终展示序（稳定排序 + seq tiebreaker，跨轮确定无抖动）。
//   - start：最新优先（start 降序，平局 seq 降序）。
//   - duration：最长优先=最旧优先（start 升序，平局 seq 升序）。
func sortViews(items []ConnView, sortBy string) {
	if sortBy == "duration" {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].StartUnix != items[j].StartUnix {
				return items[i].StartUnix < items[j].StartUnix
			}
			return items[i].ID < items[j].ID
		})
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].StartUnix != items[j].StartUnix {
			return items[i].StartUnix > items[j].StartUnix
		}
		return items[i].ID > items[j].ID
	})
}

// boundedHeap 是一个大小有界的二叉堆，堆顶（buf[0]）恒为「最该被淘汰」者。
//
// 为什么手写而不用 container/heap：本堆语义简单（push/peek/pop 堆顶/drain），
// 手写避免 interface{} 装箱与额外分配，且 less 已是闭包，无需再套 sort.Interface。
type boundedHeap struct {
	buf  []ConnView
	less func(a, b ConnView) bool // less(a,b)==true 表示 a 比 b 更该被淘汰（更靠近堆顶）
}

func (h *boundedHeap) Len() int        { return len(h.buf) }
func (h *boundedHeap) peek() ConnView  { return h.buf[0] }
func (h *boundedHeap) drain() []ConnView { return h.buf } // 出堆顺序无意义，交由 sortViews 重排

// better 判定 v 是否比 ref 更「优」（即 ref 比 v 更该被淘汰）。
func (h *boundedHeap) better(v, ref ConnView) bool { return h.less(ref, v) }

// push 入堆并上浮维持堆序。
func (h *boundedHeap) push(v ConnView) {
	h.buf = append(h.buf, v)
	h.up(len(h.buf) - 1)
}

// pop 弹出并丢弃堆顶（最该被淘汰者），用末元素补位后下沉。
func (h *boundedHeap) pop() {
	last := len(h.buf) - 1
	h.buf[0] = h.buf[last]
	h.buf = h.buf[:last]
	if len(h.buf) > 0 {
		h.down(0)
	}
}

// up 自下标 i 上浮：当 i 比父更该被淘汰时与父交换。
func (h *boundedHeap) up(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !h.less(h.buf[i], h.buf[parent]) {
			break
		}
		h.buf[i], h.buf[parent] = h.buf[parent], h.buf[i]
		i = parent
	}
}

// down 自下标 i 下沉：与「更该被淘汰」的子节点交换，维持堆顶为最该淘汰者。
func (h *boundedHeap) down(i int) {
	n := len(h.buf)
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		worst := left
		if right := left + 1; right < n && h.less(h.buf[right], h.buf[left]) {
			worst = right
		}
		if !h.less(h.buf[worst], h.buf[i]) {
			break
		}
		h.buf[i], h.buf[worst] = h.buf[worst], h.buf[i]
		i = worst
	}
}
