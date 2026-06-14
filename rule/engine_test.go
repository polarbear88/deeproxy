package rule

import (
	"testing"

	"deeproxy/config"
)

// specs 是测试辅助：快速构造规则列表。
func specs(pairs ...[2]string) []config.RuleSpec {
	out := make([]config.RuleSpec, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, config.RuleSpec{Match: p[0], Action: p[1]})
	}
	return out
}

// TestMatchTruthTable 覆盖 AC4：编码 plan §3 的真值表（含交叉用例）。
func TestMatchTruthTable(t *testing.T) {
	rules := specs(
		[2]string{"domain-suffix:google.com", "forward"},
		[2]string{"domain:ads.example.com", "reject"},
		[2]string{"ip-cidr:203.0.113.0/24", "direct"},
	)
	eng, err := NewEngine(rules, ActionForward)
	if err != nil {
		t.Fatalf("NewEngine 报错: %v", err)
	}

	cases := []struct {
		host string
		want Action
		why  string
	}{
		{"www.google.com", ActionForward, "后缀命中"},
		{"google.com", ActionForward, "后缀含自身"},
		{"notgoogle.com", ActionForward, "不误命中后缀→默认forward"},
		{"ads.example.com", ActionReject, "精确域名命中"},
		{"203.0.113.5", ActionDirect, "IP 落在 CIDR 内"},
		{"203.0.114.5", ActionForward, "IP 不在 CIDR→默认forward"},
		{"unknown.site.com", ActionForward, "无命中→默认forward"},
	}
	for _, c := range cases {
		if got := eng.Match(c.host); got != c.want {
			t.Errorf("Match(%q) = %q, 期望 %q（%s）", c.host, got, c.want, c.why)
		}
	}
}

// TestMatchCross 覆盖 AC4 交叉用例：域名目标不匹配 ip-cidr；IP 目标不匹配 domain。
func TestMatchCross(t *testing.T) {
	// 域名目标遇 ip-cidr 规则：不命中，应走默认（这里默认设 reject 以放大区分）。
	eng1, _ := NewEngine(specs([2]string{"ip-cidr:0.0.0.0/0", "direct"}), ActionReject)
	if got := eng1.Match("example.com"); got != ActionReject {
		t.Errorf("域名目标遇 ip-cidr：Match=%q, 期望默认 reject", got)
	}

	// IP 目标遇 domain 规则：不命中，应走默认。
	eng2, _ := NewEngine(specs([2]string{"domain:1.2.3.4", "reject"}), ActionForward)
	if got := eng2.Match("1.2.3.4"); got != ActionForward {
		t.Errorf("IP 目标遇 domain：Match=%q, 期望默认 forward", got)
	}
}

// TestDefaultActionConfigurable 覆盖 AC4：默认动作可配置。
func TestDefaultActionConfigurable(t *testing.T) {
	for _, def := range []Action{ActionForward, ActionDirect, ActionReject} {
		eng, err := NewEngine(nil, def)
		if err != nil {
			t.Fatalf("NewEngine(默认=%q) 报错: %v", def, err)
		}
		if got := eng.Match("anything.com"); got != def {
			t.Errorf("无规则时 Match=%q, 期望默认 %q", got, def)
		}
	}
}

// TestNewEngineInvalid 覆盖：非法前缀/CIDR/动作应报错。
func TestNewEngineInvalid(t *testing.T) {
	if _, err := NewEngine(specs([2]string{"ip-cidr:not-a-cidr", "direct"}), ActionForward); err == nil {
		t.Error("非法 CIDR 期望报错")
	}
	if _, err := NewEngine(specs([2]string{"unknown:x", "direct"}), ActionForward); err == nil {
		t.Error("未知前缀期望报错")
	}
	if _, err := NewEngine(specs([2]string{"domain:a.com", "drop"}), ActionForward); err == nil {
		t.Error("非法动作期望报错")
	}
	if _, err := NewEngine(nil, Action("bogus")); err == nil {
		t.Error("非法默认动作期望报错")
	}
}

// TestMatchIPv6CIDR 覆盖 IPv6 网段匹配。
func TestMatchIPv6CIDR(t *testing.T) {
	eng, err := NewEngine(specs([2]string{"ip-cidr:2001:db8::/32", "direct"}), ActionForward)
	if err != nil {
		t.Fatalf("NewEngine 报错: %v", err)
	}
	if got := eng.Match("2001:db8::1"); got != ActionDirect {
		t.Errorf("IPv6 命中：Match=%q, 期望 direct", got)
	}
	if got := eng.Match("2001:dead::1"); got != ActionForward {
		t.Errorf("IPv6 不命中：Match=%q, 期望默认 forward", got)
	}
}

// TestDomainCanonicalize 覆盖 AC-5.4：域名匹配大小写不敏感 + FQDN 尾点归一。
func TestDomainCanonicalize(t *testing.T) {
	// pattern 故意写成混合大小写 + 带尾点，验证入库时被规范化。
	rules := specs(
		[2]string{"domain-suffix:Google.COM.", "forward"},
		[2]string{"domain:Ads.Example.com.", "reject"},
	)
	eng, err := NewEngine(rules, ActionDirect)
	if err != nil {
		t.Fatalf("NewEngine 报错: %v", err)
	}

	cases := []struct {
		host string
		want Action
		why  string
	}{
		{"WWW.GOOGLE.COM", ActionForward, "目标全大写应命中后缀"},
		{"www.google.com.", ActionForward, "目标带尾点应命中后缀"},
		{"Google.Com", ActionForward, "后缀含自身、混合大小写"},
		{"ADS.EXAMPLE.COM.", ActionReject, "精确域名大小写+尾点归一后命中"},
		{"ads.example.com", ActionReject, "精确域名小写命中"},
		{"notgoogle.com", ActionDirect, "不误命中后缀→默认 direct"},
	}
	for _, c := range cases {
		if got := eng.Match(c.host); got != c.want {
			t.Errorf("Match(%q) = %q, 期望 %q（%s）", c.host, got, c.want, c.why)
		}
	}
}
