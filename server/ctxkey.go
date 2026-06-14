package server

import (
	"time"

	"deeproxy/auth"
	"deeproxy/rule"
	"deeproxy/snapshot"
)

// decision 是 connectRule.Allow 阶段做出的判定结果，通过 context 传给 ConnectHandle，
// 避免规则重复求值。reject 与非 CONNECT 在 Allow 阶段已直接拒绝，不会进入 ConnectHandle。
//
// v2 扩展（T3 鉴权结果承载，沿用 v1 context(decisionKey) 机制，零跨连接共享）：
// 新增 auth 字段承载鉴权阶段（Credential.Valid / 由 auth.Authenticate 纯函数算出）的
// 完整结果——分组/用户/组类型/Type A 动态上游/Type B 变量映射。Allow 与 Valid 同
// goroutine 同连接顺序执行，可在 Allow 阶段对同一用户名再调一次 auth.Authenticate 取得
// *auth.Decision 填入此处（DRY，D0-0）。ConnectHandle（T6）据此分派拨号：
//   - Type A：用 auth.DynamicUpstream（HasDynamicUpstream=false 且动作 forward → G1 拒连）；
//   - Type B：用 pool.Selector 选健康上游 + auth.Variables 经 UpstreamView.ResolveUser 替换模板。
type decision struct {
	action rule.Action // forward 或 direct（needsSniff 时此字段无意义，待嗅探后确定）
	host   string      // 目标主机（域名或 IP，用于日志与回退）
	// needsSniff 为 true 表示：目标是 IP、未命中任何 ip-cidr 规则、且启用了嗅探，
	// 需要在 ConnectHandle 中先回 success、再 peek 客户端首包嗅探域名后才能选路。
	needsSniff bool

	// auth 是本连接鉴权阶段的解析结果。承载组类型与上游来源，
	// 转发分派、统计/审计埋点维度（group/user）均从此取。
	auth *auth.Decision
	// group 是本连接所属分组的快照视图引用（含预编译 Engine 与健康池快照）。
	// 拨号阶段 Type B 从 group.HealthyUpstreams 经 pool.Selector 选健康上游。
	group *snapshot.GroupView

	// idle / sniffTimeout 是本连接生效的运行期动态设置，在 Allow 阶段从同一份快照
	// （已 Load）一并取出随判定传入，避免 ConnectHandle 再 Load 一次、并保证「同一连接
	// 用同一份设置」。取消配置文件后这些值来自 system_setting，可后台热改（新连接生效）。
	idle         time.Duration // 连接双向空闲超时（拨号后 WrapIdle 用）
	sniffTimeout time.Duration // 嗅探首包等待超时（needsSniff 路径用）

	// connID 是本连接在活跃连接登记表（connreg.Registry）中的唯一 id。
	// 在 connectHandle 进入处登记后写入，按值随 decision 流向 dialAndRelay/handleSniff，
	// 用于拨号成功后回填上游、嗅探解析后回填动作——零签名改动。
	connID int64
}

// ctxKey 是放入 context 的私有 key 类型，避免与其他包的 key 冲突。
type ctxKey struct{}

// decisionKey 是 decision 在 context 中的唯一键。
var decisionKey = ctxKey{}
