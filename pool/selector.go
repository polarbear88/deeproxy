// Package pool 实现 Type B 代理池的【平滑加权轮训选择（SWRR）】与【健康检查 worker】。
//
// 职责边界（与一号硬约束对齐）：
//   - 选择上游仅发生在【建连阶段】（每连接一次），不在字节中继循环内，故 Selector 用 per-group 小锁。
//   - SWRR 的可变状态（各节点 currentWeight）【不入不可变 Snapshot】，由本包 per-group Selector 持有。
//   - 健康节点列表的【不可变快照】由 snapshot.GroupView.HealthyUpstreams 提供，健康检查 worker
//     改变健康状态后触发 Snapshot 重建+原子替换；Selector 每次选择时读入“当前健康列表”这一份不可变快照，
//     本次选择只用这一份，绝不跨替换缓存索引（防越界/选到死节点，对应 Pre-mortem 场景 3 与 AC-42）。
//
// 关键消歧（D4）：currentWeight 槽位按【节点身份 nodeID】绑定，不按列表索引位置。
// 这样即便健康列表被替换（节点增删/顺序变化），同一节点的累积权重也不会张冠李戴。
package pool

import (
	"errors"
	"sync"

	"deeproxy/snapshot"
)

// ErrNoUpstream 表示当前无可用（健康且启用）上游——整组全挂或池为空。
// 上层据此拒连并回 statute.RepHostUnreachable（G6 / AC-17）。
var ErrNoUpstream = errors.New("无可用上游（整组全挂或池为空）")

// Selector 是单个 Type B 分组的 SWRR 选择器，长生命周期、自带小锁。
//
// 并发模型：Pick 在建连阶段调用（每连接一次），用 mu 串行化 currentWeight 的读改写。
// currentWeight 以 nodeID 为键，跨健康列表替换稳定保留各节点的累积权重。
type Selector struct {
	mu sync.Mutex
	// currentWeight 是 SWRR 各节点的当前权重累加值，键为上游 nodeID。
	// 列表替换后，已不在健康列表中的节点条目会在下次 Pick 时被惰性清理（见 prune）。
	currentWeight map[int64]int
}

// NewSelector 创建一个空选择器。
func NewSelector() *Selector {
	return &Selector{currentWeight: make(map[int64]int)}
}

// Pick 用平滑加权轮训从【当前健康列表 healthy】中选一个上游。
//
// healthy 是调用方从最新 Snapshot 取出的该组 HealthyUpstreams（不可变快照）；
// 本次选择只基于这一份列表。空列表返回 ErrNoUpstream（G6）。
//
// SWRR 算法（nginx 同款，O(n)）：
//  1. 每个节点 currentWeight += effectiveWeight(=Weight)；
//  2. 选 currentWeight 最大的节点 best；
//  3. best.currentWeight -= totalWeight；
//  4. 返回 best。
// 权重<=0 的节点按 1 计（防御非法权重导致永不被选/除零）。
func (s *Selector) Pick(healthy []snapshot.UpstreamView) (snapshot.UpstreamView, error) {
	if len(healthy) == 0 {
		return snapshot.UpstreamView{}, ErrNoUpstream
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 惰性清理：移除已不在当前健康列表中的节点的累积权重，防止 map 无限增长 + 张冠李戴。
	s.prune(healthy)

	total := 0
	bestIdx := -1
	bestCW := 0
	for i := range healthy {
		u := healthy[i]
		w := u.Weight
		if w <= 0 {
			w = 1 // 防御非法权重
		}
		total += w

		cw := s.currentWeight[u.ID] + w
		s.currentWeight[u.ID] = cw

		if bestIdx == -1 || cw > bestCW {
			bestIdx = i
			bestCW = cw
		}
	}

	// 选中节点扣减总权重（SWRR 平滑核心）。
	best := healthy[bestIdx]
	s.currentWeight[best.ID] = bestCW - total
	return best, nil
}

// prune 移除当前健康列表中不存在的节点的累积权重条目。
// 仅在持锁的 Pick 内调用。健康列表通常很小，O(n) 可接受。
func (s *Selector) prune(healthy []snapshot.UpstreamView) {
	if len(s.currentWeight) == 0 {
		return
	}
	alive := make(map[int64]struct{}, len(healthy))
	for i := range healthy {
		alive[healthy[i].ID] = struct{}{}
	}
	for id := range s.currentWeight {
		if _, ok := alive[id]; !ok {
			delete(s.currentWeight, id)
		}
	}
}

// Registry 是按分组 ID 索引的 Selector 注册表（per-group 长生命周期）。
//
// 为什么需要注册表：Selector 持有跨连接累积的 SWRR 状态，必须在多次建连间复用同一实例，
// 故不能每连接新建。注册表用读写锁保护 map，Selector 首次访问某组时惰性创建。
type Registry struct {
	mu        sync.RWMutex
	selectors map[int64]*Selector
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{selectors: make(map[int64]*Selector)}
}

// For 返回分组 groupID 的 Selector（惰性创建，长生命周期复用）。
func (r *Registry) For(groupID int64) *Selector {
	r.mu.RLock()
	sel := r.selectors[groupID]
	r.mu.RUnlock()
	if sel != nil {
		return sel
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if sel = r.selectors[groupID]; sel == nil {
		sel = NewSelector()
		r.selectors[groupID] = sel
	}
	return sel
}

// Remove 删除分组 groupID 的 Selector（M5：分组被删除时回收其 SWRR 状态，防注册表无界增长）。
//
// 为什么需要：For 惰性创建的 Selector 长生命周期复用，但分组删除后从无回收路径，
// 每个曾出现过的 Type B 分组会永久残留一个 Selector{map}。虽单条占用极小，但分组频繁
// 增删时累积成有界泄漏。删除分组的管理 handler 调用本方法即可清理。幂等：不存在则无操作。
func (r *Registry) Remove(groupID int64) {
	r.mu.Lock()
	delete(r.selectors, groupID)
	r.mu.Unlock()
}
