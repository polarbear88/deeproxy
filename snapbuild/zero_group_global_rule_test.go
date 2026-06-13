package snapbuild

import (
	"path/filepath"
	"testing"

	"deeproxy/store"
)

// zero_group_global_rule_test.go：回归 worker-3 在 T7 报的 gap——
// 当没有任何分组、但存在全局规则组时，Rebuild 必须仍校验全局规则（坏规则要让 Rebuild 失败，
// 而非静默通过）。修复见 rebuild.go 中对 globalSpecs 的独立编译校验。

// TestZeroGroupBadGlobalRuleRejected 验证：零分组 + 非法全局规则 → Rebuild 返回错误（G4 快速失败）。
func TestZeroGroupBadGlobalRuleRejected(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 不建任何分组；只有一个全局规则组，含一条非法 CIDR。
	rg := &store.RuleGroup{Name: "g", Scope: store.ScopeGlobal}
	if err := st.CreateRuleGroup(rg); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRule(&store.Rule{RuleGroupID: rg.ID, Match: "ip-cidr:not-a-cidr", Action: "direct", OrderIdx: 1}); err != nil {
		t.Fatal(err)
	}

	if _, err := Rebuild(st, baseCfg("forward")); err == nil {
		t.Fatal("零分组时非法全局规则应被 Rebuild 校验拒绝（G4 快速失败）")
	}
}

// TestZeroGroupValidGlobalRuleOK 验证：零分组 + 合法全局规则 → Rebuild 正常成功（不误伤）。
func TestZeroGroupValidGlobalRuleOK(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ok.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	rg := &store.RuleGroup{Name: "g", Scope: store.ScopeGlobal}
	_ = st.CreateRuleGroup(rg)
	_ = st.CreateRule(&store.Rule{RuleGroupID: rg.ID, Match: "ip-cidr:10.0.0.0/8", Action: "direct", OrderIdx: 1})

	if _, err := Rebuild(st, baseCfg("forward")); err != nil {
		t.Fatalf("零分组 + 合法全局规则应成功，得到错误: %v", err)
	}
}
