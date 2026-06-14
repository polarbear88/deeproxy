// 本文件提供「分组名 / 代理用户名」的字符合法性校验（组件⑦，AC-7.1/7.2）。
//
// 为什么需要它：v2 的 SOCKS5 用户名语法为 user-group[-尾段]，连接鉴权时由
// auth/username.go 的 ParseUsername 用 strings.SplitN(username, "-", 3) 按前两个
// '-' 切出 user / group 两段。若分组名或用户名自身含 '-'，会破坏这一切分语义
// （把名字的一部分误当成分隔符，导致 user/group 段解析错位、鉴权失败或越权）。
//
// 因此真正的结构性约束是「名字不得含 '-'」。这里采用 ^[A-Za-z0-9]+$ —— 一个
// 刻意更严的超集：除了排除 '-'，顺带排除了下划线 '_'、井号 '#'、点 '.'、中文等
// 其它字符，规避未来若新增分隔符或在展示层（URL / 模板拼接）引入歧义时的隐患。
//
// 【对后人的警告，请勿放宽】不得为了「兼容某些字符」而把规则放宽到允许 '_' 或 '#'。
// 这两个字符虽然在 v2 用户名「尾段」的命名变量串里合法（形如 name_value#name_value，
// 见 CLAUDE.md 用户名编码契约与 auth/variables.go 的 ParseVariables），但那是尾段的
// 语法，user / group 段【绝不允许】出现它们——一旦放宽，连接鉴权的 SplitN 切分与
// 变量串解析都会产生歧义。
package auth

import "regexp"

// idRe 是分组名 / 用户名允许的字符集合：仅英文字母与数字，至少一个字符。
// ^...+$ 锚定整串，确保不含 '-' 等任何分隔符（见文件头注释说明的不变式）。
var idRe = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// ValidIdentifier 校验 s 是否为合法的分组名 / 用户名（仅含英文字母与数字、非空）。
// 用于新增/编辑分组与代理用户时的服务端边界校验（仅作用于实际变更的输入，AC-7.4）。
func ValidIdentifier(s string) bool {
	return idRe.MatchString(s)
}
