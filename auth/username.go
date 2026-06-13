package auth

import (
	"fmt"
	"strings"
)

// ParsedUsername 表示 v2 SOCKS5 用户名按位置语法解析后的结果。
//
// v2 用户名语法（权威契约，见 spec「用户名契约」）：
//
//	user-group         // 无尾段
//	user-group-尾段    // 含尾段；尾段整体不拆（即使尾段里还有 '-' 也不再切分）
//
// 切分规则：前两个 '-' 按【位置】切出 user / group，第三段（第二个 '-' 之后的
// 全部内容）作为尾段整体保留。尾段语义由 group 类型决定：
//   - Type A：尾段 = base64("u:p@host:port")（动态上游），交给 DecodeUpstream 解析。
//   - Type B：尾段 = 命名变量串 name_value#name_value...，交给 ParseVariables 解析。
//
// 之所以做成纯函数：D0-0 决策要求用户名解析在 Valid 与 Allow 两阶段可复用（DRY），
// 且不引入任何跨连接共享状态——纯函数天然满足。
type ParsedUsername struct {
	User    string // 首段：代理用户名（ProxyUser.username）
	Group   string // 次段：分组名（Group.name）
	Tail    string // 尾段：第二个 '-' 之后的全部内容，整体不拆，可为空
	HasTail bool   // 是否存在尾段（区分「无尾段」与「尾段为空字符串」两种情形）
}

// ParseUsername 把 SOCKS5 用户名解析为 {user, group, 尾段}。
//
// 为什么用 SplitN 而非按所有 '-' 切：尾段本身可能含 '-'（Type A 的 base64、
// Type B 的变量值都可能出现 '-'），必须保证只在前两个 '-' 处切分，第三段整体保留。
//
// 失败场景（返回 error，调用方据此拒连）：
//   - 空串；
//   - 缺少分组段（没有第一个 '-'，无法切出 group）；
//   - user 段为空（如以 '-' 开头）；
//   - group 段为空（如 "user--尾段" 中间组名为空）。
//
// 注意：尾段允许为空（"user-group-" 形式 HasTail=true 且 Tail=""），也允许整体缺省
// （"user-group" 形式 HasTail=false）；这两种都不是错误，由上层按组类型决定如何处理。
func ParseUsername(username string) (ParsedUsername, error) {
	if username == "" {
		return ParsedUsername{}, fmt.Errorf("用户名为空")
	}

	// 最多切成 3 段：user / group / 尾段（尾段整体保留，含其中的 '-'）。
	parts := strings.SplitN(username, "-", 3)
	if len(parts) < 2 {
		// 只有一段，说明没有 '-'，无法切出 group。
		return ParsedUsername{}, fmt.Errorf("用户名缺少分组段，应为 user-group[-尾段]: %q", username)
	}

	user := parts[0]
	group := parts[1]
	if user == "" {
		return ParsedUsername{}, fmt.Errorf("用户名 user 段为空: %q", username)
	}
	if group == "" {
		return ParsedUsername{}, fmt.Errorf("用户名 group 段为空: %q", username)
	}

	res := ParsedUsername{User: user, Group: group}
	if len(parts) == 3 {
		// 存在第二个 '-'，第三段即尾段（可能为空字符串，如 "user-group-"）。
		res.Tail = parts[2]
		res.HasTail = true
	}
	return res, nil
}
