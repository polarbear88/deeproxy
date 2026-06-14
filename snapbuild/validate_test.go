package snapbuild

import (
	"testing"

	"deeproxy/domain"
	"deeproxy/store"
)

// validate_test.go 覆盖 DEC-A1（AC-5.1）写前候选校验器与候选构造器。

// TestValidateRuleset_GoodAndBad 验证：合法规则集通过、坏规则（前缀/CIDR/action）被拒。
func TestValidateRuleset_GoodAndBad(t *testing.T) {
	rgs := []store.RuleGroup{
		{ID: 1, Name: "glob", Scope: domain.ScopeGlobal},
		{ID: 2, Name: "grp", Scope: domain.ScopeGroup},
	}

	// 合法：domain-suffix + ip-cidr + 合法 action。
	good := []store.Rule{
		{ID: 1, RuleGroupID: 1, Match: "domain-suffix:google.com", Action: "forward", OrderIdx: 0},
		{ID: 2, RuleGroupID: 2, Match: "ip-cidr:10.0.0.0/8", Action: "direct", OrderIdx: 0},
	}
	if err := ValidateRuleset(good, rgs, "forward"); err != nil {
		t.Fatalf("合法规则集不应报错: %v", err)
	}

	// 坏 CIDR（在全局组）→ 应被拒。
	badCIDR := []store.Rule{
		{ID: 1, RuleGroupID: 1, Match: "ip-cidr:not-a-cidr", Action: "direct", OrderIdx: 0},
	}
	if err := ValidateRuleset(badCIDR, rgs, "forward"); err == nil {
		t.Fatal("坏 CIDR 应被拒")
	}

	// 未知前缀（在分组组）→ 应被拒。
	badPrefix := []store.Rule{
		{ID: 3, RuleGroupID: 2, Match: "geoip:CN", Action: "reject", OrderIdx: 0},
	}
	if err := ValidateRuleset(badPrefix, rgs, "forward"); err == nil {
		t.Fatal("未知前缀应被拒")
	}

	// 非法 action → 应被拒。
	badAction := []store.Rule{
		{ID: 4, RuleGroupID: 2, Match: "domain:example.com", Action: "tunnel", OrderIdx: 0},
	}
	if err := ValidateRuleset(badAction, rgs, "forward"); err == nil {
		t.Fatal("非法 action 应被拒")
	}
}

// TestBuildCandidateRulesUpsert 验证候选构造：新增追加、按 ID 覆盖、覆盖时保留已提交 RuleGroupID。
func TestBuildCandidateRulesUpsert(t *testing.T) {
	committed := []store.Rule{
		{ID: 1, RuleGroupID: 5, Match: "domain:a.com", Action: "forward", OrderIdx: 0},
		{ID: 2, RuleGroupID: 5, Match: "domain:b.com", Action: "direct", OrderIdx: 1},
	}

	// 新增（ID=0）：候选应为 3 条。
	added := BuildCandidateRulesUpsert(committed, store.Rule{RuleGroupID: 5, Match: "domain:c.com", Action: "reject", OrderIdx: 2})
	if len(added) != 3 {
		t.Fatalf("新增后候选应为 3 条，得到 %d", len(added))
	}

	// 覆盖（ID=2）且不带 RuleGroupID：应覆盖同 ID 行并沿用已提交的 RuleGroupID=5。
	upd := BuildCandidateRulesUpsert(committed, store.Rule{ID: 2, Match: "domain:b2.com", Action: "reject", OrderIdx: 1})
	if len(upd) != 2 {
		t.Fatalf("覆盖后候选应仍为 2 条，得到 %d", len(upd))
	}
	var found bool
	for _, r := range upd {
		if r.ID == 2 {
			found = true
			if r.Match != "domain:b2.com" || r.Action != "reject" {
				t.Fatalf("ID=2 应被覆盖为新内容，得到 %+v", r)
			}
			if r.RuleGroupID != 5 {
				t.Fatalf("覆盖应沿用已提交 RuleGroupID=5，得到 %d", r.RuleGroupID)
			}
		}
	}
	if !found {
		t.Fatal("候选中应含被覆盖的 ID=2 行")
	}
}
