package rule

import (
	"testing"

	"deeproxy/config"
)

// global_compile_test.go 验证 D① 优化：全局规则段编译一次、各分组引擎复用，
// 行为必须与旧的「每组重新整体编译」路径（BuildGroupEngine）完全等价。

// TestNewEngineWithGlobal_EquivalentToBuildGroupEngine 验证：用「已编译全局段 + 分组规格」
// 构建的引擎，与「全局规格 + 分组规格整体编译」的引擎，对同一批目标给出完全相同的判定。
func TestNewEngineWithGlobal_EquivalentToBuildGroupEngine(t *testing.T) {
	globalSpecs := []config.RuleSpec{
		spec("domain-suffix:global.com", "reject"),
		spec("ip-cidr:10.0.0.0/8", "direct"),
	}
	groupSpecs := []config.RuleSpec{
		spec("domain:only-group.com", "forward"),
		spec("ip-cidr:192.168.0.0/16", "reject"),
	}
	def := ActionForward

	// 旧路径：全局 + 分组整体编译。
	oldEng, err := BuildGroupEngine(
		[]RuleGroupSpec{{Name: "g", Specs: globalSpecs}},
		[]RuleGroupSpec{{Name: "p", Specs: groupSpecs}},
		def,
	)
	if err != nil {
		t.Fatalf("BuildGroupEngine 失败: %v", err)
	}

	// 新路径：全局编译一次，复用 + 只编译分组段。
	compiled, err := CompileRules(globalSpecs)
	if err != nil {
		t.Fatalf("CompileRules 失败: %v", err)
	}
	newEng, err := NewEngineWithGlobal(compiled, groupSpecs, def)
	if err != nil {
		t.Fatalf("NewEngineWithGlobal 失败: %v", err)
	}

	// 覆盖：命中全局、命中分组、命中默认；域名与 IP 两类。
	targets := []string{
		"a.global.com",     // 命中全局 domain-suffix → reject
		"10.1.2.3",         // 命中全局 ip-cidr → direct
		"only-group.com",   // 命中分组 domain → forward
		"192.168.1.1",      // 命中分组 ip-cidr → reject
		"nomatch.test",     // 不命中 → 默认 forward
		"8.8.8.8",          // 不命中 → 默认 forward
	}
	for _, tgt := range targets {
		oa, om := oldEng.MatchRule(tgt)
		na, nm := newEng.MatchRule(tgt)
		if oa != na || om != nm {
			t.Errorf("目标 %q 判定不一致：旧=(%s,%v) 新=(%s,%v)", tgt, oa, om, na, nm)
		}
	}
}

// TestNewEngineWithGlobal_GlobalBeforeGroup 验证顺序语义：全局段排在分组段之前，
// 当全局与分组对同一目标都有规则时，全局先命中（与 MergeRuleGroups 顺序一致）。
func TestNewEngineWithGlobal_GlobalBeforeGroup(t *testing.T) {
	// 全局对 x.com → reject；分组对 x.com → forward。全局在前，应命中 reject。
	compiled, err := CompileRules([]config.RuleSpec{spec("domain:x.com", "reject")})
	if err != nil {
		t.Fatalf("CompileRules 失败: %v", err)
	}
	eng, err := NewEngineWithGlobal(compiled, []config.RuleSpec{spec("domain:x.com", "forward")}, ActionDirect)
	if err != nil {
		t.Fatalf("NewEngineWithGlobal 失败: %v", err)
	}
	if got := eng.Match("x.com"); got != ActionReject {
		t.Fatalf("x.com 应命中全局 reject（全局优先），实际 %s", got)
	}
}

// TestNewEngineWithGlobal_SharedGlobalIsolation 验证关键不变式：同一份 CompiledRules
// 被多个分组引擎复用时，各引擎互不影响——拼接到全新切片、绝不原地改共享的全局段。
// 这是 D① 复用安全性的核心保证（共享只读全局段 + 各引擎独立分组段）。
func TestNewEngineWithGlobal_SharedGlobalIsolation(t *testing.T) {
	compiled, err := CompileRules([]config.RuleSpec{spec("domain-suffix:shared.com", "direct")})
	if err != nil {
		t.Fatalf("CompileRules 失败: %v", err)
	}

	// 用同一份 compiled 构建两个分组引擎，各带不同的分组规则。
	engA, err := NewEngineWithGlobal(compiled, []config.RuleSpec{spec("domain:a-only.com", "forward")}, ActionReject)
	if err != nil {
		t.Fatalf("构建 engA 失败: %v", err)
	}
	engB, err := NewEngineWithGlobal(compiled, []config.RuleSpec{spec("domain:b-only.com", "reject")}, ActionForward)
	if err != nil {
		t.Fatalf("构建 engB 失败: %v", err)
	}

	// 共享全局段：两者对 shared.com 都命中 direct。
	if engA.Match("x.shared.com") != ActionDirect || engB.Match("x.shared.com") != ActionDirect {
		t.Fatal("两引擎都应命中共享全局段 direct")
	}
	// 各自分组段互不串：engA 只认 a-only.com，engB 只认 b-only.com。
	if engA.Match("a-only.com") != ActionForward {
		t.Fatal("engA 应命中自身分组规则 a-only.com→forward")
	}
	if engA.Match("b-only.com") != ActionReject { // 不命中分组，走 engA 默认 reject
		t.Fatal("engA 不应看到 engB 的分组规则（b-only.com 应落 engA 默认 reject）")
	}
	if engB.Match("b-only.com") != ActionReject {
		t.Fatal("engB 应命中自身分组规则 b-only.com→reject")
	}
	if engB.Match("a-only.com") != ActionForward { // 不命中分组，走 engB 默认 forward
		t.Fatal("engB 不应看到 engA 的分组规则（a-only.com 应落 engB 默认 forward）")
	}
}

// TestNewEngineWithGlobal_InvalidRejected 验证非法分组规则/默认动作在构建时被拦截（G4 前提）。
func TestNewEngineWithGlobal_InvalidRejected(t *testing.T) {
	good, _ := CompileRules([]config.RuleSpec{spec("domain:a.com", "direct")})

	if _, err := NewEngineWithGlobal(good, []config.RuleSpec{spec("ip-cidr:bad", "direct")}, ActionForward); err == nil {
		t.Fatal("非法分组 CIDR 应返回 error")
	}
	if _, err := NewEngineWithGlobal(good, nil, Action("bogus")); err == nil {
		t.Fatal("非法默认动作应返回 error")
	}
	// 非法全局规则应在 CompileRules 阶段即被拦截。
	if _, err := CompileRules([]config.RuleSpec{spec("unknown:x", "direct")}); err == nil {
		t.Fatal("非法全局规则前缀应在 CompileRules 返回 error")
	}
}
