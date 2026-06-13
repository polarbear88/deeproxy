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
	e := &Engine{defaultAction: def}
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
		case "domain-suffix":
			r.kind = kindDomainSuffix
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
		e.rules = append(e.rules, r)
	}
	return e, nil
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
