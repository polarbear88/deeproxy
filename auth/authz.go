package auth

import (
	"crypto/subtle" // 常量时间比较，防止密码比对的时序侧信道

	"deeproxy/domain"
)

// authz.go 定义 auth 包鉴权所需的【快照只读接口】并封装鉴权核心逻辑。
//
// 为什么定义接口而不直接 import snapshot 包（关键架构约束）：
//   - snapshot 包 import auth（用 auth.Upstream / auth.SubstituteTemplate），
//     若 auth 反过来 import snapshot 会形成【包循环】，无法编译。
//   - 故在 auth 侧用「依赖倒置」定义最小只读接口 SnapshotView，由 server/cmd 装配层
//     把具体的 *snapshot.Snapshot 适配进来（snapshot.Snapshot 的方法签名已与本接口对齐，
//     适配成本极低）。这样 auth 只依赖抽象、零依赖 snapshot 包，彻底断开循环。
//   - 枚举用零依赖叶子包 domain（GroupType 等）。ProxyUser 密码改为【明文存储】（用户决策，
//     AC-43：bcrypt 每连接 ~49ms 是转发建连瓶颈），鉴权用明文 == 比对，微秒级；
//     故 auth 不再 import pwhash/store，转发链脱离 store→database/sql 依赖（AC-43）。
//     注意：仅 ProxyUser 明文；后台管理员密码仍 bcrypt（在 api 包，非转发热路径）。

// UserInfo 是鉴权所需的代理用户最小信息（从快照查得）。
type UserInfo struct {
	ID  int64  // 用户 ID（用于 O(1) 授权判定）
	Pwd string // 明文连接密码（鉴权时直接 == 比对，ProxyUser 不用 bcrypt）
}

// GroupInfo 是鉴权与尾段分派所需的分组最小信息（从快照查得）。
type GroupInfo struct {
	ID   int64           // 分组 ID（用于 O(1) 授权判定）
	Type domain.GroupType // A=动态上游 / B=代理池，决定尾段解析方式
}

// SnapshotView 是 auth 鉴权依赖的【运行期快照只读视图】抽象。
//
// 由装配层（server/cmd）用具体 *snapshot.Snapshot 适配实现。每连接建连时由
// Provider 取当前快照视图，保证读到的是最新热替换后的配置（AC-10/AC-44）。
type SnapshotView interface {
	// LookupUser 按用户名查代理用户；不存在返回 (UserInfo{}, false)。
	LookupUser(username string) (UserInfo, bool)
	// LookupGroup 按分组名查分组；不存在返回 (GroupInfo{}, false)。
	LookupGroup(name string) (GroupInfo, bool)
	// IsAuthorized 判定用户是否被授权访问该分组（O(1)）。
	IsAuthorized(groupID, userID int64) bool
}

// SnapshotProvider 在每次建连鉴权时返回【当前生效】的快照视图。
//
// 为什么是函数而非直接持有 SnapshotView：配置经 atomic 热替换后，旧 SnapshotView
// 会被新的取代；Provider 每次调用返回最新快照，使鉴权天然读到热替换结果、无需重启、
// 无锁（atomic.Value 语义由装配层的 Holder 保证）。
type SnapshotProvider func() SnapshotView

// parse 是【纯解析】核心：解析用户名 + 查用户/分组 + 按组类型解析尾段，产出 *Decision。
//
// 关键：parse【不校验密码、零 bcrypt、无 password 参数】。它只做廉价操作——
// 字符串解析 + 内存 map 查表 + base64/变量解析；授权判定（IsAuthorized，O(1) map）也在此做，
// 作为防御性校验（快照热替换竞态下分组/授权可能已变）。
//
// 设计动机（AC-43 性能修复，决策 #1）：Valid 与 Allow 同 goroutine 顺序执行。原先两阶段
// 各调一次含密码校验的 authenticate；拆分后 Allow 只调 parse（零密码校验），不重复鉴权。
// 配合 ProxyUser 明文存储（决策 #2），整条建连鉴权路径已无 bcrypt。
//
// 失败返回 (nil, error)：用户名非法 / 用户不存在 / 分组不存在 / 未授权 / Type A 尾段非法。
// 「用户不存在」在此即返回（供 Allow 防御）；密码正确性由 verify 的明文比对负责。
func parse(snap SnapshotView, username string) (*Decision, error) {
	// 步骤 1：解析用户名（纯函数）。
	pu, err := ParseUsername(username)
	if err != nil {
		return nil, err
	}

	// 步骤 2：查用户（仅取 ID，用于授权判定与埋点维度；密码比对不在此做）。
	ui, ok := snap.LookupUser(pu.User)
	if !ok {
		return nil, &AuthError{Reason: "用户不存在"}
	}

	// 步骤 3：查分组 + 授权校验（O(1) map，廉价，可在 Allow 阶段重做以防热替换竞态）。
	gi, ok := snap.LookupGroup(pu.Group)
	if !ok {
		return nil, &AuthError{Reason: "分组不存在"}
	}
	if !snap.IsAuthorized(gi.ID, ui.ID) {
		return nil, &AuthError{Reason: "用户未授权访问该分组"}
	}

	// 步骤 4：按组类型解析尾段，产出连接级判定。
	d := &Decision{
		User:      pu.User,
		UserID:    ui.ID,
		Group:     pu.Group,
		GroupID:   gi.ID,
		GroupType: gi.Type,
	}
	switch gi.Type {
	case domain.TypeA:
		// Type A：尾段为 base64 动态上游。无尾段则本连接无上游来源，
		// 此处【不】拒连——留待规则判定：若最终动作 forward 而无上游则拒（G1，在 T6 server 处理）。
		if pu.HasTail && pu.Tail != "" {
			up, err := DecodeUpstream(pu.Tail)
			if err != nil {
				// 尾段存在却解不出合法上游 → 视为非法输入，拒连。
				return nil, &AuthError{Reason: "Type A 尾段非法 base64 上游: " + err.Error()}
			}
			d.DynamicUpstream = up
			d.HasDynamicUpstream = true
		}
	case domain.TypeB:
		// Type B：尾段为命名变量串（可空）。仅解析为 map，模板替换延迟到 T6 拨号阶段。
		d.Variables = ParseVariables(pu.Tail) // 空尾段返回空 map，安全
	default:
		// 理论不可达（快照只会装载 A/B）；防御性拒连。
		return nil, &AuthError{Reason: "未知分组类型: " + string(gi.Type)}
	}

	return d, nil
}

// verify 是【完整鉴权】核心：parse + 明文密码比对。仅 Valid 阶段调用。
//
// ProxyUser 密码明文存储（决策 #2），故密码校验是微秒级的 == 比对，非 bcrypt。
// 安全说明：用户不存在与密码错误都返回「鉴权失败」，不向客户端区分两者，避免用户名枚举。
func verify(snap SnapshotView, username, password string) (*Decision, error) {
	// 先做廉价的 parse（含授权），失败即返回。
	d, err := parse(snap, username)
	if err != nil {
		return nil, err
	}
	// 明文密码比对（parse 已确保用户存在）。
	ui, _ := snap.LookupUser(d.User)
	// 为什么用 crypto/subtle.ConstantTimeCompare 而非普通 != 比较：
	//   代理用户认证是对客户端/公网暴露的鉴权点，普通字符串比较会短路（首个不同字节即返回），
	//   攻击者配合可枚举接口可借由响应时间差做时序侧信道，逐字节猜测密码前缀。
	//   ConstantTimeCompare 的耗时只与输入长度相关、与内容是否匹配无关，消除该侧信道。
	//   返回 1 表示相等；不同长度会快速返回 0（长度本身非机密，可接受）。
	//   本路径是建连鉴权（非字节中继热路径），常量时间比对开销可忽略，不影响 AC-43。
	if subtle.ConstantTimeCompare([]byte(password), []byte(ui.Pwd)) != 1 {
		return nil, &AuthError{Reason: "密码不匹配"}
	}
	return d, nil
}
