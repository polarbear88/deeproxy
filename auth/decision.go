package auth

import "deeproxy/domain"

// Decision 是【鉴权阶段】对一条连接产出的解析结果（D0-0 定稿的连接级判定）。
//
// 它在 Credential.Valid 阶段一次性算出（此处 user+password 同时可得），随后经
// server 的 context(decisionKey) 机制传给 Allow / ConnectHandle 阶段消费。
// 因 Valid 与 Allow 同 goroutine、同连接、顺序执行，本结构【不跨连接共享】，
// 无并发风险（禁用 sync.Map 等共享态，符合 D0-0）。
//
// 字段消费方：
//   - User/Group：日志、审计、统计埋点维度（T5/T6）。
//   - UserID/GroupID：授权判定与统计维度（O(1)）。
//   - GroupType：尾段语义与拨号分派（A 用动态上游 / B 用代理池+变量替换）。
//   - DynamicUpstream（Type A）：客户端尾段 base64 解出的本连接上游，T6 forward 时直接拨号；
//     HasDynamicUpstream=false 表示 Type A 无尾段（G1：若规则命中 forward 则 T6 拒连）。
//   - Variables（Type B）：尾段命名变量 map，T6 选定池内上游后用 UpstreamView.ResolveUser
//     做模板 {name} 替换（替换延迟到拨号阶段，不在鉴权阶段做）。
type Decision struct {
	User      string          // 代理用户名
	UserID    int64           // 代理用户 ID
	Group     string          // 分组名
	GroupID   int64           // 分组 ID
	GroupType domain.GroupType // A / B

	// Type A 专用：客户端尾段解出的动态上游。
	DynamicUpstream    Upstream
	HasDynamicUpstream bool

	// Type B 专用：客户端尾段解析出的命名变量映射（可为空 map）。
	Variables map[string]string
}

// AuthError 表示鉴权失败，携带内部原因（仅用于服务端日志，不回传客户端，
// 避免泄露「用户是否存在/密码是否正确」等可被枚举利用的信息）。
type AuthError struct {
	Reason string
}

func (e *AuthError) Error() string {
	return "鉴权失败: " + e.Reason
}
