// Package config 承载 deeproxy 的【启动引导项】并提供内置默认值。
//
// v2 重大变化（取消配置文件）：分组 / 规则 / 用户 / 授权早已迁入 SQLite；本次进一步把
// 原 YAML 仅剩的运行期项（default_action / log_level / idle_timeout / sniff_*）也迁入
// system_setting 表，由后台动态修改（见 store.SystemSetting / snapshot.Settings）。
// 因此本包不再读写任何配置文件，只保留【无法做成数据库设置的引导项】：
//   - Listen：本地 SOCKS5 监听地址（由 --socks5 端口 + 固定 host 组装）；
//   - AdminListen：Web 后台监听地址（由 --web 端口 + 固定 host 组装）；
//   - DBPath：SQLite 数据库文件路径（鸡生蛋问题——设置都存在库里，库路径本身不能做成设置）。
//
// 注意：上游 SOCKS5 代理不在此（由客户端用户名动态携带）。
package config

// RuleSpec 是单条规则的原始形态（match/action）。
//
// 为什么仍放在 config 包：rule 包（转发热路径）import config 用此类型做规则编译输入；
// 若把它移走会牵动 rule/snapbuild 等多包的依赖关系。规则数据本身来自 SQLite，
// 物化时由 snapbuild 转成本类型喂给 rule 引擎，本类型仅作「规则的数据载体」。
type RuleSpec struct {
	Match  string // 形如 "domain-suffix:google.com"
	Action string // forward/direct/reject
}

// Config 是 deeproxy 的启动引导配置（仅监听地址与数据库路径，无运行期可变项）。
//
// 运行期可变设置（默认动作 / 日志级别 / 空闲超时 / 嗅探开关与超时）一律不在此，
// 而由 system_setting 表承载、物化进 snapshot.Snapshot，支持后台热改。
type Config struct {
	Listen      string // 本地 SOCKS5 监听地址，如 "0.0.0.0:1768"
	AdminListen string // Web 后台监听地址，如 "0.0.0.0:1769"
	DBPath      string // SQLite 数据库文件路径
}
