// Package domain 是 deeproxy v2 的零依赖领域枚举叶子包。
//
// 存在意义（AC-43 静态依赖约束）：分组类型 / 健康检查方式 / 规则作用域这三组枚举
// 被【转发热路径】包（snapshot/auth/pool/server）与【存储层】包（store）共同引用。
// 若把它们定义在 store 包，转发链就会因 import store 而静态拉入 database/sql + SQLite 驱动，
// 违反“转发热路径不沾 store/database”硬约束。把纯枚举抽到本叶子包（不 import 任何内部包、
// 不 import database/sql），各包改引本包，即可让转发链彻底脱离存储层依赖。
//
// 本包【只放与持久化无关的纯值类型与常量】，不放任何行结构体、不放任何 I/O。
package domain

// GroupType 是代理分组类型。
//   - TypeA：动态上游组——上游由客户端每连接通过用户名尾段 base64 携带，组内不配置固定代理。
//   - TypeB：代理池组——组内配置多条固定上游，按加权轮训选健康节点，支持命名变量模板。
type GroupType string

const (
	// TypeA 动态上游组：上游来自客户端用户名尾段 base64，不参与健康检查。
	TypeA GroupType = "A"
	// TypeB 代理池组：组内多条上游 + 权重 + 健康检查 + 命名变量模板。
	TypeB GroupType = "B"
)

// HealthMode 是健康检查探测方式。
type HealthMode string

const (
	// HealthPing 用 ICMP/TCP 探测上游连通性。
	HealthPing HealthMode = "ping"
	// HealthURL 通过上游发起一次 HTTP 请求探测可用性（默认方式）。
	HealthURL HealthMode = "url"
)

// RuleScope 是规则组的作用域。
//   - ScopeGlobal：全局规则组，对所有连接生效，匹配优先级最高（排在分组规则之前）。
//   - ScopeGroup：分组规则组，仅对被关联的分组生效。
type RuleScope string

const (
	// ScopeGlobal 全局规则组（匹配时排在分组规则之前）。
	ScopeGlobal RuleScope = "global"
	// ScopeGroup 分组规则组（仅对关联分组生效）。
	ScopeGroup RuleScope = "group"
)
