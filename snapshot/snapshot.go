// Package snapshot 是 deeproxy v2 的运行期【不可变配置快照】与原子热替换中枢。
//
// 设计动机（一号硬约束：转发热路径零锁）：
//   - SQLite 是分组/池/规则/用户/授权的【权威数据源】，但转发路径绝不直接访问 DB。
//   - 启动时与每次后台写成功后，从 SQLite 物化出一份【不可变 Snapshot】，
//     用 atomic.Value 持有；转发侧 Load() 无锁读取，永远看到一份内部一致的视图。
//   - 后台写 → Rebuild（重新物化+预编译）→ Swap（原子替换）。G4：Rebuild 失败则不 Swap、
//     保留旧快照并返回错误，保证转发侧永不读到半成品。
//
// 为什么独立成包而非放 config：rule 包 import config（用 config.RuleSpec），
// 若 config 再 import rule 会形成包循环。snapshot 作为上层装配包同时 import
// config/rule/auth/store，既避免循环，又让 config 保持「纯 YAML 引导」、
// 让 rule 这一转发热路径包不沾染 store/database/sql（满足 AC-43 静态依赖约束）。
//
// SWRR 状态归属（D4 消歧）：Snapshot 只持有【各 Type B 组健康节点列表的不可变快照】，
// SWRR 的 currentWeight 可变状态【不】放进 Snapshot，由 pool 包的 per-group Selector
// 持有（自带小锁）。健康检查 worker 改变健康状态时触发 Rebuild+Swap 刷新节点列表快照。
package snapshot

import (
	"time"

	"deeproxy/auth"
	"deeproxy/domain"
	"deeproxy/rule"
)

// GroupView 是单个代理分组在快照中的不可变视图。
//
// 转发期消费方：
//   - T3 auth：用 Type 判定尾段语义（A 解 base64 / B 解命名变量）。
//   - T4/本包：Engine 是该组「全局规则组 → 分组规则组 → 默认动作」预编译出的扁平化引擎，
//     转发期只 Match 一次。
//   - T5 pool / T6 server：HealthyUpstreams 是该组当前【健康且启用】的上游不可变列表，
//     pool.Selector 据此做 SWRR；列表为空（整组全挂）→ 上层拒连回 RepHostUnreachable（G6）。
type GroupView struct {
	ID     int64            // 分组 ID
	Name   string           // 分组名（连接用户名 group 段按名匹配）
	Type   domain.GroupType // A=动态上游 / B=代理池
	Remark string           // 备注（日志/展示用）

	// Engine 是该组专属的扁平化规则引擎（全局组在前、分组组在后、默认动作兜底）。
	// 预编译于 Rebuild，转发期只读 Match，无锁并发安全。
	Engine *rule.Engine

	// HealthyUpstreams 是该组当前健康且启用的上游不可变快照（仅 Type B 有意义）。
	// 元素为值拷贝，避免外部修改影响快照；pool.Selector 选中后用其 User（本身即模板）
	// 做命名变量替换，再交给 dialer.DialUpstream。
	HealthyUpstreams []UpstreamView

	// AllUpstreams 是该组全部上游（含不健康/禁用），供后台展示与健康检查 worker 参考。
	AllUpstreams []UpstreamView
}

// UpstreamView 是上游代理在快照中的不可变视图。
//
// 提供 ToAuthUpstream 把视图转换为 dialer 可用的 auth.Upstream（用替换后的用户名）。
type UpstreamView struct {
	ID      int64
	Host    string
	Port    int
	User    string // 用户名，本身即模板（可含 {var} 占位；不含时为定值）
	Pwd     string
	Weight  int
	Enabled bool
	Healthy bool
}

// ResolveUser 计算该上游最终用于上游认证的用户名：
//   - User 本身即模板，用 vars 替换其中的 {name}（缺值补空、多余忽略，复用 auth.SubstituteTemplate）；
//   - 不含占位时 SubstituteTemplate 原样返回，等价于定值。
func (u UpstreamView) ResolveUser(vars map[string]string) string {
	return auth.SubstituteTemplate(u.User, vars)
}

// ToAuthUpstream 把上游视图（结合客户端命名变量 vars）转换为 dialer 可拨号的 auth.Upstream。
// vars 为 nil 或空时等价于无变量替换（模板中的 {name} 全部补空）。
func (u UpstreamView) ToAuthUpstream(vars map[string]string) auth.Upstream {
	return auth.Upstream{
		Host: u.Host,
		Port: u.Port,
		User: u.ResolveUser(vars),
		Pwd:  u.Pwd,
	}
}

// UserView 是代理用户在快照中的不可变视图（含明文连接密码，供鉴权直接比对）。
type UserView struct {
	ID       int64
	Username string
	Pwd      string // 明文连接密码（鉴权时直接 == 比对；ProxyUser 不用 bcrypt，仅管理员用）
}

// Snapshot 是一份不可变的运行期配置视图。构建后只读、并发安全，转发期无锁 Load。
//
// 索引设计（解释为什么用 map）：转发期按【名字】查 group、按【用户名】查 user 是热点
// （每连接建连一次），用 map 做 O(1) 查找；授权关系用 set 做 O(1) 判定。
type Snapshot struct {
	// groupsByName：分组名 → 分组视图（连接用户名 group 段按名匹配）。
	groupsByName map[string]*GroupView
	// groupsByID：分组 ID → 分组视图（健康检查/后台按 ID 取用）。
	groupsByID map[int64]*GroupView
	// usersByName：用户名 → 用户视图（鉴权按名查 + bcrypt 验密码）。
	usersByName map[string]*UserView
	// authz：授权集合，键为 "groupID:userID"，存在即已授权（O(1) 判定）。
	authz map[AuthzKey]struct{}

	// allGroupsUsers：开启「授权全部分组」通配标志的用户 ID 集合（DEC-B1）。
	// 命中即对【任意】分组放行，与逐组 authz【并存】（独立标志，不互相清除）。
	// 鉴权判定 = all_groups 命中 OR authz 精细命中（见 IsAuthorized）。
	allGroupsUsers map[int64]struct{}

	// defaultAction 是全局默认动作（规则全不命中时的最终兜底），来自配置引导。
	defaultAction rule.Action

	// settings 是运行期全局动态设置（空闲超时 / 嗅探开关 / 嗅探超时）的不可变快照。
	// 取消配置文件后，这些原 YAML 引导项迁入 system_setting 表，物化进快照供转发侧建连时读取，
	// 后台改后经 RebuildAndSwap 热生效。放进快照而非读 *Config，是为了支持动态修改且仍零热路径开销
	// （建连本就 Load() 一次快照，多读几个字段免费；字节中继 io.Copy 循环完全不碰）。
	settings Settings
}

// Settings 是运行期全局动态设置的不可变值（随快照原子替换）。
//
// 说明：default_action 仍由 defaultAction 字段（已编进各组规则引擎兜底）承载，不在此重复；
// log_level 经 slog.LevelVar 原子生效、不进快照。故此处只放建连时需读的 idle / sniff 三项。
type Settings struct {
	IdleTimeout  time.Duration // 连接双向空闲超时（建连时给 dialer.WrapIdle 用）
	SniffDomain  bool          // 是否启用域名嗅探（IP 未命中 ip-cidr 时嗅 SNI/Host）
	SniffTimeout time.Duration // 嗅探首包等待超时
}

// AuthzKey 是授权关系的复合键（分组+用户）。导出以便上层 snapbuild 包构造授权集合。
type AuthzKey struct {
	GroupID int64
	UserID  int64
}

// NewAuthzKey 构造一个授权键（供 snapbuild 物化授权集合用）。
func NewAuthzKey(groupID, userID int64) AuthzKey {
	return AuthzKey{GroupID: groupID, UserID: userID}
}

// LookupGroup 按分组名返回视图；不存在返回 (nil, false)。
func (s *Snapshot) LookupGroup(name string) (*GroupView, bool) {
	g, ok := s.groupsByName[name]
	return g, ok
}

// GroupByID 按分组 ID 返回视图；不存在返回 (nil, false)。
func (s *Snapshot) GroupByID(id int64) (*GroupView, bool) {
	g, ok := s.groupsByID[id]
	return g, ok
}

// LookupUser 按用户名返回视图；不存在返回 (nil, false)。
func (s *Snapshot) LookupUser(name string) (*UserView, bool) {
	u, ok := s.usersByName[name]
	return u, ok
}

// IsAuthorized 判定 user 是否被授权访问 group（鉴权第三步，O(1)）。
//
// 判定语义（DEC-B1「并存」）：先查 all_groups 通配集合命中即放行（覆盖未来新增分组）；
// 否则查逐组 authz 精细命中。两者独立并存——用户既可开 all_groups 又保留逐组授权，
// 关掉 all_groups 后逐组授权仍生效。本函数被 Valid 阶段与 Allow(ParseOnly) 阶段共用，
// 改这一处即两条鉴权路径同时生效，避免只在单路径短路导致的不一致。
func (s *Snapshot) IsAuthorized(groupID, userID int64) bool {
	// all_groups 通配标志命中：对任意分组放行。
	if _, ok := s.allGroupsUsers[userID]; ok {
		return true
	}
	// 逐组精细授权命中。
	_, ok := s.authz[AuthzKey{GroupID: groupID, UserID: userID}]
	return ok
}

// DefaultAction 返回全局默认动作。
func (s *Snapshot) DefaultAction() rule.Action {
	return s.defaultAction
}

// Settings 返回运行期全局动态设置（idle/sniff）。转发侧建连时调用一次。
func (s *Snapshot) Settings() Settings {
	return s.settings
}

// Groups 返回全部分组视图（后台列表/健康检查 worker 遍历用；返回的是内部指针，
// 调用方只读，不得修改）。
func (s *Snapshot) Groups() []*GroupView {
	out := make([]*GroupView, 0, len(s.groupsByID))
	for _, g := range s.groupsByID {
		out = append(out, g)
	}
	return out
}
