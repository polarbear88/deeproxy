package rule

import (
	"testing"

	"deeproxy/config"
)

// spec 是构造 config.RuleSpec 的简写，减少测试样板。
func spec(match, action string) config.RuleSpec {
	return config.RuleSpec{Match: match, Action: action}
}

// TestMergeRuleGroups_Order 验证合并顺序：全局组整体在前、分组组在后；
// 组间按传入顺序、组内按书写顺序。这是 AC-7「全局→分组」顺序的核心保证。
func TestMergeRuleGroups_Order(t *testing.T) {
	global := []RuleGroupSpec{
		{Name: "g1", Specs: []config.RuleSpec{spec("domain:a.com", "reject"), spec("domain:b.com", "direct")}},
		{Name: "g2", Specs: []config.RuleSpec{spec("domain:c.com", "forward")}},
	}
	group := []RuleGroupSpec{
		{Name: "p1", Specs: []config.RuleSpec{spec("domain:d.com", "direct")}},
	}

	merged := MergeRuleGroups(global, group)

	want := []string{"domain:a.com", "domain:b.com", "domain:c.com", "domain:d.com"}
	if len(merged) != len(want) {
		t.Fatalf("合并后规则数 = %d, 期望 %d", len(merged), len(want))
	}
	for i, w := range want {
		if merged[i].Match != w {
			t.Errorf("第 %d 条 = %q, 期望 %q（顺序错误）", i, merged[i].Match, w)
		}
	}
}

// TestMergeRuleGroups_Empty 验证空输入（无任何规则组）返回空列表，
// 对应「该 group 无显式规则、全部走默认动作」的场景。
func TestMergeRuleGroups_Empty(t *testing.T) {
	if got := MergeRuleGroups(nil, nil); len(got) != 0 {
		t.Errorf("空输入应返回空列表, 实际长度 %d", len(got))
	}
}

// TestBuildGroupEngine_GlobalBeforeGroup 验证「全局组优先于分组组」的最终行为：
// 同一目标 a.com 同时被全局组(reject)与分组组(direct)覆盖时，全局先匹配 → reject。
// 这是 AC-7 的关键真值表条目（全局优先级最高）。
func TestBuildGroupEngine_GlobalBeforeGroup(t *testing.T) {
	global := []RuleGroupSpec{
		{Name: "global", Specs: []config.RuleSpec{spec("domain-suffix:a.com", "reject")}},
	}
	group := []RuleGroupSpec{
		{Name: "groupRules", Specs: []config.RuleSpec{spec("domain-suffix:a.com", "direct")}},
	}

	eng, err := BuildGroupEngine(global, group, ActionForward)
	if err != nil {
		t.Fatalf("BuildGroupEngine 失败: %v", err)
	}

	if got := eng.Match("www.a.com"); got != ActionReject {
		t.Errorf("www.a.com 动作 = %q, 期望 reject（全局应优先于分组）", got)
	}
}

// TestBuildGroupEngine_DefaultFallback 验证不命中任何规则时走默认动作兜底。
func TestBuildGroupEngine_DefaultFallback(t *testing.T) {
	group := []RuleGroupSpec{
		{Name: "p", Specs: []config.RuleSpec{spec("domain:only.com", "reject")}},
	}
	eng, err := BuildGroupEngine(nil, group, ActionDirect)
	if err != nil {
		t.Fatalf("BuildGroupEngine 失败: %v", err)
	}

	action, matched := eng.MatchRule("nomatch.example")
	if matched {
		t.Errorf("nomatch.example 不应命中任何规则")
	}
	if action != ActionDirect {
		t.Errorf("默认动作 = %q, 期望 direct", action)
	}
}

// TestBuildGroupEngine_TruthTable 覆盖三类匹配（domain / domain-suffix / ip-cidr）
// 在合并引擎下的判定，对应 AC-7 真值表。
func TestBuildGroupEngine_TruthTable(t *testing.T) {
	global := []RuleGroupSpec{
		{Name: "g", Specs: []config.RuleSpec{
			spec("domain:exact.com", "reject"),         // 精确域名
			spec("domain-suffix:google.com", "forward"), // 域名后缀
			spec("ip-cidr:203.0.113.0/24", "direct"),    // IP 网段
		}},
	}
	eng, err := BuildGroupEngine(global, nil, ActionForward)
	if err != nil {
		t.Fatalf("BuildGroupEngine 失败: %v", err)
	}

	cases := []struct {
		host string
		want Action
		desc string
	}{
		{"exact.com", ActionReject, "精确域名命中"},
		{"www.exact.com", ActionForward, "精确域名不应误命中子域 → 默认"},
		{"www.google.com", ActionForward, "域名后缀命中"},
		{"notgoogle.com", ActionForward, "后缀不应误命中 → 默认（此处默认恰为 forward）"},
		{"203.0.113.5", ActionDirect, "IP 在网段内命中"},
		{"8.8.8.8", ActionForward, "IP 不在网段 → 默认"},
	}
	for _, c := range cases {
		if got := eng.Match(c.host); got != c.want {
			t.Errorf("%s: Match(%q) = %q, 期望 %q", c.desc, c.host, got, c.want)
		}
	}
}

// TestNewMergedEngine_InvalidRuleRejected 验证非法规则在预编译阶段被拦截（G4 前提）：
// Rebuild 调用本函数若返回 error，调用方应不 Swap、保留旧快照。
func TestNewMergedEngine_InvalidRuleRejected(t *testing.T) {
	bad := []config.RuleSpec{spec("ip-cidr:not-a-cidr", "direct")}
	if _, err := NewMergedEngine(bad, ActionForward); err == nil {
		t.Error("非法 ip-cidr 应返回 error，以便 Rebuild 拒绝并保留旧快照")
	}

	// 默认动作非法也应被拒绝。
	if _, err := NewMergedEngine(nil, Action("bogus")); err == nil {
		t.Error("非法默认动作应返回 error")
	}
}
