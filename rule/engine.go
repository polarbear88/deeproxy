// Package rule 实现分流规则引擎：按配置顺序对目标做首匹配，输出动作。
//
// 匹配语义：
//   - 目标是域名（FQDN）时，只参与 domain（精确）/ domain-suffix（后缀）匹配；
//   - 目标是 IP 字面量时，只参与 ip-cidr 匹配；
//   - 顺序遍历规则，命中第一条即返回其动作；全不命中返回默认动作。
//
// 之所以“域名只走域名规则、IP 只走 CIDR 规则”，是因为本工具注入了 NopResolver
// 跳过本地 DNS（避免 DNS 泄漏），域名目标不会被解析成 IP，故二者天然互斥。
package rule

import (
	"fmt"
	"net"
	"strings"

	"deeproxy/config"
)

// Action 表示对一条连接的处置动作。
type Action string

const (
	ActionForward Action = "forward" // 经动态上游转发
	ActionDirect  Action = "direct"  // 本机直连目标
	ActionReject  Action = "reject"  // 拒绝连接
)

// ruleKind 是规则的匹配类型。
type ruleKind int

const (
	kindDomain       ruleKind = iota // 精确域名
	kindDomainSuffix                 // 域名后缀
	kindIPCIDR                       // IP 网段
)

// rule 是编译后的单条规则。ip-cidr 预编译为 *net.IPNet 以提升匹配性能。
type rule struct {
	kind    ruleKind
	pattern string     // domain / domain-suffix 的模式串
	ipNet   *net.IPNet // ip-cidr 预编译结果
	action  Action
}

// Engine 是规则引擎，持有有序规则列表与默认动作。
// 规则列表在启动时一次性构建、运行期只读，故 Match 并发安全、无需加锁。
type Engine struct {
	rules         []rule
	defaultAction Action
}

// NewEngine 由配置规则列表与默认动作构建引擎。
// ip-cidr 在此预编译；任何非法前缀或非法 CIDR/动作都会返回 error。
func NewEngine(specs []config.RuleSpec, def Action) (*Engine, error) {
	if !isValidAction(def) {
		return nil, fmt.Errorf("默认动作非法: %q", def)
	}
	rules, err := compileSpecs(specs)
	if err != nil {
		return nil, err
	}
	return &Engine{rules: rules, defaultAction: def}, nil
}

// compileSpecs 把规则规格列表逐条编译为内部 rule（ip-cidr 预编译 *net.IPNet、域名规范化）。
// 任一规则非法（缺前缀 / 前缀未知 / CIDR 非法 / 动作非法）返回 error。
//
// 抽出为独立函数（DRY）：NewEngine 与 CompileRules 共用同一编译核心，避免两份编译逻辑漂移。
func compileSpecs(specs []config.RuleSpec) ([]rule, error) {
	rules := make([]rule, 0, len(specs))
	for i, s := range specs {
		prefix, pattern, ok := strings.Cut(s.Match, ":")
		if !ok {
			return nil, fmt.Errorf("第 %d 条规则 match 缺少前缀: %q", i+1, s.Match)
		}
		act := Action(s.Action)
		if !isValidAction(act) {
			return nil, fmt.Errorf("第 %d 条规则 action 非法: %q", i+1, s.Action)
		}

		r := rule{pattern: pattern, action: act}
		switch prefix {
		case "domain":
			r.kind = kindDomain
			// 域名规范化（AC-5.4）：统一小写 + 去尾点，使规则与目标在同一形态下比较，
			// 避免「Google.com / google.com.」因大小写或 FQDN 尾点导致漏匹配。
			r.pattern = canonicalizeDomain(pattern)
		case "domain-suffix":
			r.kind = kindDomainSuffix
			r.pattern = canonicalizeDomain(pattern)
		case "ip-cidr":
			r.kind = kindIPCIDR
			_, ipNet, err := net.ParseCIDR(pattern)
			if err != nil {
				return nil, fmt.Errorf("第 %d 条规则 ip-cidr 非法: %q: %w", i+1, pattern, err)
			}
			r.ipNet = ipNet
		default:
			return nil, fmt.Errorf("第 %d 条规则 match 前缀未知: %q", i+1, prefix)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// CompiledRules 是一段【已编译】的规则序列，可被多个 Engine 复用拼接（P2/D① 优化用）。
//
// 为什么需要：全局规则段在每个分组的合并引擎里都排在最前，原实现对每个分组都重新编译
// 一遍全局规则（O(分组数 × 全局规则数) 的 net.ParseCIDR/canonicalize 浪费）。把全局段
// 【编译一次】封装为 CompiledRules，再让各分组引擎复用，即把全局编译降为 O(全局规则数)。
//
// 不可变性：内部 rules 是只读编译产物（与 Engine.rules 同类），构造后不再修改；
// NewEngineWithGlobal 拼接时复制到新切片、绝不原地改 CompiledRules，故可被任意多个
// Engine 安全共享（与现有「快照不可变 + 跨快照共享只读对象」语义一致）。
type CompiledRules struct {
	rules []rule
}

// CompileRules 把规则规格列表编译为可复用的 CompiledRules（编译一次、多处拼接）。
// 编译同时即完成校验：非法规则返回 error（供 Rebuild 在写前/重建时快速失败 G4）。
func CompileRules(specs []config.RuleSpec) (CompiledRules, error) {
	rules, err := compileSpecs(specs)
	if err != nil {
		return CompiledRules{}, err
	}
	return CompiledRules{rules: rules}, nil
}

// NewEngineWithGlobal 用【已编译的全局段】+【本分组规则规格】构建分组引擎（D① 核心入口）。
//
// 语义等价于 NewEngine(global规格 ++ groupSpecs, def)，但全局段不再重复编译——直接复用
// 传入的 CompiledRules，只编译本分组自己的（通常很少的）规则。顺序保持「全局在前、分组在后」，
// 与 MergeRuleGroups 一致。任一分组规则非法或默认动作非法返回 error（G4）。
func NewEngineWithGlobal(global CompiledRules, groupSpecs []config.RuleSpec, def Action) (*Engine, error) {
	if !isValidAction(def) {
		return nil, fmt.Errorf("默认动作非法: %q", def)
	}
	groupRules, err := compileSpecs(groupSpecs)
	if err != nil {
		return nil, err
	}
	// 拼接到全新切片：绝不原地改 global.rules（其被所有分组共享、必须保持只读）。
	rules := make([]rule, 0, len(global.rules)+len(groupRules))
	rules = append(rules, global.rules...)
	rules = append(rules, groupRules...)
	return &Engine{rules: rules, defaultAction: def}, nil
}

// Match 对目标主机做顺序首匹配，返回命中规则的动作；不命中返回默认动作。
// host 为纯主机部分（不含端口）：域名或 IP 字面量。
func (e *Engine) Match(host string) Action {
	a, _ := e.MatchRule(host)
	return a
}

// MatchRule 与 Match 相同，但额外返回是否命中了某条显式规则。
// matched=false 表示走的是默认动作。嗅探逻辑用它来判断：
// 当 IP 目标未命中任何 ip-cidr 规则时，才需要嗅探域名再判一次。
func (e *Engine) MatchRule(host string) (action Action, matched bool) {
	ip := net.ParseIP(host) // 非 nil 表示 host 是 IP 字面量
	// 仅当目标是域名时做规范化（小写 + 去尾点），与入库时规范化的 pattern 同形比较（AC-5.4）。
	// IP 字面量不走此路径（net.ParseIP 已解析，大小写/尾点不适用）。
	if ip == nil {
		host = canonicalizeDomain(host)
	}
	for _, r := range e.rules {
		switch r.kind {
		case kindDomain:
			// 精确域名：仅当目标是域名且完全相等时命中。
			if ip == nil && host == r.pattern {
				return r.action, true
			}
		case kindDomainSuffix:
			// 域名后缀：host == pattern 或 host 以 "."+pattern 结尾。
			// 后者保证 google.com 命中 www.google.com 但不误命中 notgoogle.com。
			if ip == nil && (host == r.pattern || strings.HasSuffix(host, "."+r.pattern)) {
				return r.action, true
			}
		case kindIPCIDR:
			// IP 网段：仅当目标是 IP 且落在网段内时命中。
			if ip != nil && r.ipNet.Contains(ip) {
				return r.action, true
			}
		}
	}
	return e.defaultAction, false
}

// isValidAction 判断动作是否为三枚举之一。
func isValidAction(a Action) bool {
	return a == ActionForward || a == ActionDirect || a == ActionReject
}

// canonicalizeDomain 规范化域名形态（AC-5.4）：统一转小写并去除末尾的根点（FQDN 尾点）。
//
// 为什么需要：DNS 域名大小写不敏感，且 "google.com." 与 "google.com" 指向同一域名；
// 客户端经 socks5h 发来的目标域名可能含尾点或混合大小写，若不归一会漏匹配规则。
// 规则 pattern 入库时与目标 host 匹配时都过此函数，保证两侧同形比较。
func canonicalizeDomain(host string) string {
	host = strings.ToLower(host)
	// 去掉末尾根点；保留中间点。空串或单独 "." 归一为空串（不影响匹配语义）。
	host = strings.TrimSuffix(host, ".")
	return host
}
