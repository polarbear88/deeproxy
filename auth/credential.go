package auth

import (
	"errors"
	"log/slog"
)

// Package auth：v2 鉴权与解析内核。
//
// credential.go 实现 go-socks5 的 CredentialStore 接口（方法 Valid），是 SOCKS5
// 用户名/密码认证阶段的入口。v2 与 v1 的根本差异：
//   - v1：用户名整段 = base64 上游，Valid 只判 base64 可解码。
//   - v2：用户名 = user-group[-尾段]，Valid 需读运行期快照完成
//     「解析用户名 → 查 ProxyUser + bcrypt 验密码 → 验分组授权 → 按组类型解析尾段」
//     的完整鉴权（D0-0 定稿：此阶段 user+password 同时可得，一次算清）。
//
// 鉴权结果（*Decision）的跨阶段传递：沿用 v1 的 context(decisionKey) 机制（由 server
// 包负责），不引入任何跨连接共享状态。因 Valid 与 Allow 同 goroutine、同连接、顺序
// 执行，Allow 阶段可用同一纯函数 Authenticate 再解析一次拿到 Decision（仅字符串解析+
// 内存查表，无 I/O，非字节中继热路径，开销可忽略；DRY 且零并发风险）。

// Credential 实现 go-socks5 的 CredentialStore 接口。
// 持有 SnapshotProvider，每次 Valid 时取【当前生效】的快照做鉴权（读到热替换结果）。
type Credential struct {
	provider SnapshotProvider
	// logger 用于在鉴权失败时打结构化日志（AC-1.5 未授权访问分组）。可为 nil（不打日志）。
	logger *slog.Logger
}

// NewCredential 用快照提供者构造 Credential。
// 装配层（server/cmd）传入「返回当前 *snapshot.Snapshot 适配视图」的函数。
func NewCredential(provider SnapshotProvider) Credential {
	return Credential{provider: provider}
}

// NewCredentialWithLogger 与 NewCredential 相同，但额外注入 logger 用于鉴权失败日志（AC-1.5）。
// logger 为 nil 时退化为不打日志（等价于 NewCredential）。
func NewCredentialWithLogger(provider SnapshotProvider, logger *slog.Logger) Credential {
	return Credential{provider: provider, logger: logger}
}

// Valid 实现 CredentialStore 接口。
//
// 参数 user 为客户端用户名（user-group[-尾段]），password 为 SOCKS5 密码字段
// （v2 中作为代理用户的真实密码参与 bcrypt 校验）；userAddr 无业务语义，忽略。
//
// 返回 true=鉴权通过（连接放行到后续规则/拨号阶段）；false=鉴权失败，库直接拒连。
// 鉴权细节（解析 + 验密码 + 授权 + 尾段分派）全部委托给纯函数 Authenticate，
// 此处只关心成败布尔，符合接口约定。
func (c Credential) Valid(user, password, _ /*userAddr*/ string) bool {
	// 防御：未经 NewCredential 构造的零值 Credential（provider 为 nil）一律拒连，
	// 避免空函数调用 panic（v2 装配须用 NewCredential 注入 provider）。
	if c.provider == nil {
		return false
	}
	snap := c.provider()
	if snap == nil {
		// 快照尚未就绪（理论上启动后即有初始快照），保守拒连。
		return false
	}
	_, err := Verify(snap, user, password)
	if err != nil {
		// AC-1.5：仅对「用户存在但未被授权访问目标分组」这一类打结构化日志，
		// 便于运维定位授权配置问题。其余失败（用户名非法/用户不存在/密码不符）不在此细化日志，
		// 避免对外暴露可枚举信息、也避免与 server 层的泛化拒连日志重复。
		// 只在 Valid 阶段打（此处是唯一拿得到原始 user/group 且不会与 Allow/ParseOnly 重复的点）。
		var ae *AuthError
		if c.logger != nil && errors.As(err, &ae) && ae.Kind == AuthErrUnauthorizedGroup {
			// 解析用户名取 user/group 段用于日志（纯字符串解析，无 I/O）。
			if pu, perr := ParseUsername(user); perr == nil {
				c.logger.Warn("用户访问分组未授权", "user", pu.User, "group", pu.Group)
			}
		}
		return false
	}
	return true
}

// Verify 是【完整鉴权】公共入口（解析 + 授权 + 明文密码比对），仅 Valid 阶段调用。
// 成功返回 (*Decision, nil)；失败返回 (nil, *AuthError)。
func Verify(snap SnapshotView, user, password string) (*Decision, error) {
	return verify(snap, user, password)
}

// ParseOnly 是【纯解析】公共入口（解析 + 授权 + 尾段分派，**无密码校验、零 bcrypt**），
// 供 server 的 Allow 阶段调用以重建 *Decision（鉴权已在 Valid 阶段通过；ParseOnly 失败
// 仅作 Allow 防御性拒连）。
//
// 这是 AC-43 修复的关键：Allow 用 ParseOnly 而非 Verify，绝不重复密码校验（决策 #1）。
func ParseOnly(snap SnapshotView, user string) (*Decision, error) {
	return parse(snap, user)
}
