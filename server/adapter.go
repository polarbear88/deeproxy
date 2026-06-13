package server

import (
	"deeproxy/auth"
	"deeproxy/snapshot"
)

// adapter.go 把 *snapshot.Snapshot 适配为 auth 包定义的只读视图接口 auth.SnapshotView。
//
// 为什么需要适配（依赖倒置，破包循环）：snapshot 包 import auth（用 auth.Upstream /
// SubstituteTemplate），故 auth 不能反向 import snapshot；T3 在 auth 侧用接口
// auth.SnapshotView（返回 auth.UserInfo / auth.GroupInfo）声明依赖。装配层（server）
// 同时认识两个包，在此把具体快照适配成该接口，让 auth 鉴权读到运行期快照（含热替换）。
//
// 适配是零拷贝的薄封装：每次查询转调 snapshot 的同名方法，再把 *snapshot.UserView /
// *GroupView 映射成 auth 的最小信息结构。每连接建连查一两次，非字节中继热路径。

// snapView 适配单份快照为 auth.SnapshotView。
type snapView struct {
	snap *snapshot.Snapshot
}

// LookupUser 实现 auth.SnapshotView：按用户名查代理用户最小信息。
func (v snapView) LookupUser(username string) (auth.UserInfo, bool) {
	uv, ok := v.snap.LookupUser(username)
	if !ok {
		return auth.UserInfo{}, false
	}
	return auth.UserInfo{ID: uv.ID, Pwd: uv.Pwd}, true
}

// LookupGroup 实现 auth.SnapshotView：按分组名查分组最小信息。
func (v snapView) LookupGroup(name string) (auth.GroupInfo, bool) {
	gv, ok := v.snap.LookupGroup(name)
	if !ok {
		return auth.GroupInfo{}, false
	}
	return auth.GroupInfo{ID: gv.ID, Type: gv.Type}, true
}

// IsAuthorized 实现 auth.SnapshotView：O(1) 授权判定。
func (v snapView) IsAuthorized(groupID, userID int64) bool {
	return v.snap.IsAuthorized(groupID, userID)
}

// authProvider 返回一个 auth.SnapshotProvider，每次调用取 Holder 当前快照适配为视图。
//
// 闭包捕获 Holder：配置热替换后 Load 自然返回新快照，鉴权读到最新配置（AC-10/44），
// 无锁（atomic.Value 语义）。
func authProvider(holder *snapshot.Holder) auth.SnapshotProvider {
	return func() auth.SnapshotView {
		return snapView{snap: holder.Load()}
	}
}
