package auth

// Credential 实现 go-socks5 的 CredentialStore 接口（方法 Valid）。
//
// 它在 SOCKS5 用户名/密码认证阶段被调用，职责仅有一个：判断客户端给的用户名
// 是否能被解码为合法上游。能解码 → 认证通过（连接放行到后续规则/拨号阶段）；
// 不能解码 → 认证失败，库直接拒绝连接（满足“解码失败则拒”的需求）。
//
// 这里把“格式校验”前置到认证阶段，是为了让非法连接尽早被拒绝；真正取用上游对象
// 的解码发生在更后面的规则/拨号阶段，两处共用 DecodeUpstream 同一纯函数（DRY）。
type Credential struct{}

// Valid 实现 CredentialStore 接口。
// 参数 password、userAddr 在本工具中无业务语义（密码字段仅占位），故忽略。
// 返回 true 表示用户名可解码为合法上游、认证通过。
func (Credential) Valid(user, _ /*password*/, _ /*userAddr*/ string) bool {
	_, err := DecodeUpstream(user)
	return err == nil
}
