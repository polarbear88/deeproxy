// validate.go 实现 DEC-A1（AC-5.1）「写前候选校验」：在把规则类写入落库【之前】，
// 用「当前已提交规则集 + 本次待写增量」在内存构建候选规则规格，喂给 rule.BuildGroupEngine
// 编译校验；编译通过才允许调用方继续 Write+RebuildAndSwap，不通过直接返错、DB 不写。
//
// 为什么需要（Principle 1 不分裂）：原流程是「先写 DB → 再 RebuildAndSwap」，
// 若一条非法规则（坏 CIDR / 未知前缀 / 非法 action）写入成功但随后 Rebuild 编译失败，
// 坏规则已持久留库、DB 与转发快照分裂。写前校验把「规则编译类坏配置」挡在落库之前。
//
// 守护范围（诚实声明）：本校验只覆盖【规则引擎编译】（match 前缀 / CIDR / action / 默认动作）。
// FK / 唯一性 / 跨实体引用由 SQLite 写入约束兜底（写失败即不 Swap，仍不分裂）。
//
// 复用既有原语：合并/编译逻辑直接调用 rule.BuildGroupEngine（与 Rebuild 同一校验器），
// 不重写读层、不引入事务（store 单写 + SetMaxOpenConns(1) + 直连读，无 Tx 读路径）。
package snapbuild

import (
	"fmt"
	"sort"

	"deeproxy/config"
	"deeproxy/domain"
	"deeproxy/rule"
	"deeproxy/store"
)

// ValidateRuleset 用给定的【候选】规则集、规则组集与默认动作做规则引擎编译校验。
//
// 调用方语义：传入的三个参数应是「把本次待写增量应用到当前已提交状态后」的候选全量
// （由 BuildCandidate* 辅助函数构造）。校验通过返回 nil，调用方方可继续 Write+Swap；
// 任一规则非法返回 error，调用方据此返错、不写 DB。
//
// 实现与 Rebuild 的编译路径完全一致（全局组独立校验 + 每分组合并编译），保证「校验通过 ⇒
// 之后的 Rebuild 在规则编译层不会再失败」，从根上消除「写后 rebuild 失败致分裂」。
func ValidateRuleset(candRules []store.Rule, candRuleGroups []store.RuleGroup, defaultAction string) error {
	// 默认动作：与 Rebuild 同样的兜底（非法值兜底 forward，不让坏默认动作阻断校验）。
	def := rule.Action(defaultAction)
	if def != rule.ActionForward && def != rule.ActionDirect && def != rule.ActionReject {
		def = rule.ActionForward
	}

	// 规则按规则组分桶（保持 order_idx 顺序：调用方构造候选时需已排序）。
	rulesByRG := make(map[int64][]config.RuleSpec)
	for _, r := range candRules {
		rulesByRG[r.RuleGroupID] = append(rulesByRG[r.RuleGroupID], config.RuleSpec{
			Match:  r.Match,
			Action: r.Action,
		})
	}

	// 全局组按 ID 升序拼装（与 Rebuild 一致）。
	var globalSpecs []rule.RuleGroupSpec
	for _, rg := range sortedRuleGroupsByScope(candRuleGroups, domain.ScopeGlobal) {
		globalSpecs = append(globalSpecs, rule.RuleGroupSpec{Name: rg.Name, Specs: rulesByRG[rg.ID]})
	}

	// 全局规则独立校验（无分组时也要触发，避免坏全局规则漏过）。
	if len(globalSpecs) > 0 {
		if _, err := rule.BuildGroupEngine(globalSpecs, nil, def); err != nil {
			return fmt.Errorf("全局规则校验失败: %w", err)
		}
	}

	// 分组规则组按作用域校验：对每个 scope=group 的规则组单独编译一次（合并全局在前），
	// 覆盖所有会进入分组 Engine 的分组规则；任一非法即拦截。
	for _, rg := range candRuleGroups {
		if rg.Scope != domain.ScopeGroup {
			continue
		}
		groupSpecs := []rule.RuleGroupSpec{{Name: rg.Name, Specs: rulesByRG[rg.ID]}}
		if _, err := rule.BuildGroupEngine(globalSpecs, groupSpecs, def); err != nil {
			return fmt.Errorf("规则组 %q(id=%d) 校验失败: %w", rg.Name, rg.ID, err)
		}
	}
	return nil
}

// BuildCandidateRulesUpsert 构造「将某条规则 upsert（新增或按 ID 覆盖）后」的候选规则全量。
//
//   - committed：当前已提交的全部规则（store.ListAllRules）。
//   - pending  ：本次待写的规则。pending.ID==0 视为新增；>0 视为覆盖同 ID 行。
//
// 返回候选全量（按 RuleGroupID, OrderIdx 排序，与 Rebuild 读序一致），供 ValidateRuleset 校验。
func BuildCandidateRulesUpsert(committed []store.Rule, pending store.Rule) []store.Rule {
	out := make([]store.Rule, 0, len(committed)+1)
	replaced := false
	for _, r := range committed {
		if pending.ID != 0 && r.ID == pending.ID {
			// 覆盖同 ID 行。更新接口可能只携带 match/action/order 而不带 RuleGroupID，
			// 此时沿用已提交行的 RuleGroupID，保证候选规则仍归属正确规则组（影响合并/校验归桶）。
			merged := pending
			if merged.RuleGroupID == 0 {
				merged.RuleGroupID = r.RuleGroupID
			}
			out = append(out, merged)
			replaced = true
			continue
		}
		out = append(out, r)
	}
	if !replaced {
		out = append(out, pending) // 新增
	}
	sortRules(out)
	return out
}

// sortRules 按 (RuleGroupID, OrderIdx, ID) 升序排序，复现 Rebuild 的读取顺序，
// 使候选校验的合并顺序与最终生效顺序一致。
func sortRules(rs []store.Rule) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].RuleGroupID != rs[j].RuleGroupID {
			return rs[i].RuleGroupID < rs[j].RuleGroupID
		}
		if rs[i].OrderIdx != rs[j].OrderIdx {
			return rs[i].OrderIdx < rs[j].OrderIdx
		}
		return rs[i].ID < rs[j].ID
	})
}
