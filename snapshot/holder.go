package snapshot

import (
	"sync/atomic"

	"deeproxy/rule"
)

// Holder 用 atomic.Value 持有当前生效的 *Snapshot，提供无锁读与原子替换。
//
// 转发侧（server/auth/pool）持有 *Holder，每连接 Load() 一次拿到当前快照；
// 后台写成功后由管理 goroutine 调 snapbuild.RebuildAndSwap 刷新（物化需 store，
// 故放在上层 snapbuild 包，使本包零依赖 store —— 转发链脱离 store→database/sql，AC-43）。
// Load 无锁、Swap 原子，二者天然并发安全（atomic.Value 语义）。
type Holder struct {
	v atomic.Value // 实际存 *Snapshot
}

// NewHolder 创建一个持有初始快照的 Holder。
func NewHolder(initial *Snapshot) *Holder {
	h := &Holder{}
	h.v.Store(initial)
	return h
}

// Load 无锁读取当前快照（转发热路径每连接调用一次）。
func (h *Holder) Load() *Snapshot {
	return h.v.Load().(*Snapshot)
}

// Swap 原子替换当前快照。仅在重建成功后由管理 goroutine 调用。
func (h *Holder) Swap(s *Snapshot) {
	h.v.Store(s)
}

// NewSnapshot 由本包内部/同模块上层包（snapbuild）构造一份不可变快照。
//
// 为什么需要本构造器：Snapshot 的字段是非导出的（保证不可变性，外部只能读），
// 而物化逻辑（Rebuild）被拆到 snapbuild 包以隔离 store 依赖；snapbuild 通过本
// 构造器把物化好的索引交给 Snapshot。参数均为 snapbuild 组装好的不可变索引。
func NewSnapshot(
	groupsByName map[string]*GroupView,
	groupsByID map[int64]*GroupView,
	usersByName map[string]*UserView,
	authz map[AuthzKey]struct{},
	allGroupsUsers map[int64]struct{},
	defaultAction rule.Action,
	settings Settings,
) *Snapshot {
	return &Snapshot{
		groupsByName:   groupsByName,
		groupsByID:     groupsByID,
		usersByName:    usersByName,
		authz:          authz,
		allGroupsUsers: allGroupsUsers,
		defaultAction:  defaultAction,
		settings:       settings,
	}
}
