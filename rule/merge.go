// Package rule 的多对多规则合并（v2 阶段4）。
//
// 背景：v2 引入「规则组 ↔ 代理分组」多对多关系，且规则组可应用到「全局」。
// 一条连接选定 group 后，权威匹配顺序为：
//
//	全局规则组 → 该 group 被应用的规则组 → 默认动作
//
// 组内规则保持「书写顺序」，全局组整体排在分组组之前，最终落到默认动作兜底。
//
// 性能消歧（见 ralplan 阶段4 / 硬约束）：
// 绝不在转发热路径运行时「遍历多个 Engine」。而是在【配置快照重建时】(config.Snapshot.Rebuild)
// 按 group 维度把上述顺序一次性「预编译」成该 group 专属的【单一有序规则序列】
// （即每个 group 物化出一个扁平化的 *Engine）。转发期对该单一 Engine 只做一次
// 顺序首匹配（复用 v1 的 MatchRule 语义，零改动）。
//
// 本文件只提供「合并 + 预编译」的纯函数，不耦合 config.Snapshot 的具体类型，
// 也不触达 store/SQLite —— 调用方（T2 的 Rebuild）负责从存储取出规则组数据，
// 按 group 组装好顺序后调用此处函数拿到 *Engine 挂到快照上。
package rule

import (
	"fmt"

	"deeproxy/config"
)

// RuleGroupSpec 是「一个规则组」在合并时的原始形态：
// 一个有序的规则列表（组内书写顺序即 Specs 的切片顺序）。
//
// 之所以单独建模而非直接传 [][]config.RuleSpec，是为了让调用方语义清晰
// （区分「全局规则组集合」与「分组规则组集合」），并便于未来携带组名等元信息做日志/调试。
type RuleGroupSpec struct {
	Name  string            // 规则组名称（仅用于错误信息与调试，不参与匹配）
	Specs []config.RuleSpec // 组内规则，按书写顺序排列
}

// MergeRuleGroups 把「全局规则组集合」与「某 group 被应用的分组规则组集合」
// 按权威顺序（全局在前、分组在后；组间按传入切片顺序、组内按书写顺序）
// 扁平化拼接为单一有序的 []config.RuleSpec。
//
// 这是合并顺序的唯一真源（DRY）：任何需要「全局→分组」顺序的地方都应复用本函数，
// 避免在多处各自拼接导致顺序语义漂移。
//
// 参数：
//   - globalGroups：scope=global 的规则组集合（按其展示/书写顺序传入）。
//   - groupGroups ：该 group 被应用的 scope=group 规则组集合（按应用顺序传入）。
//
// 返回：拼接后的有序规则列表（可能为空，表示该 group 无任何显式规则，全部走默认动作）。
func MergeRuleGroups(globalGroups, groupGroups []RuleGroupSpec) []config.RuleSpec {
	// 预估容量：减少 append 扩容。
	total := 0
	for _, g := range globalGroups {
		total += len(g.Specs)
	}
	for _, g := range groupGroups {
		total += len(g.Specs)
	}

	merged := make([]config.RuleSpec, 0, total)
	// 全局组整体在前。
	for _, g := range globalGroups {
		merged = append(merged, g.Specs...)
	}
	// 分组组在后。
	for _, g := range groupGroups {
		merged = append(merged, g.Specs...)
	}
	return merged
}

// NewMergedEngine 是 T4 暴露给「快照重建」的核心入口：
// 接收「已按 全局→分组 顺序拼好的有序规则列表」与「默认动作」，
// 预编译为该 group 专属的扁平化 *Engine（ip-cidr 等在此一次性编译）。
//
// 调用方（config.Snapshot.Rebuild）对每个 group 调用一次，把返回的 *Engine
// 挂到该 group 的快照视图上；转发期对该 *Engine 只 Match 一次。
//
// 复用 v1 的 NewEngine —— 匹配核心（domain/domain-suffix/ip-cidr 顺序首匹配）零改动（DRY）。
// 任一规则非法（前缀未知 / CIDR 非法 / 动作非法）或默认动作非法都会返回 error，
// 由调用方在 Rebuild 阶段拦截（G4：Rebuild 失败则不 Swap、保留旧快照）。
func NewMergedEngine(orderedSpecs []config.RuleSpec, def Action) (*Engine, error) {
	eng, err := NewEngine(orderedSpecs, def)
	if err != nil {
		return nil, fmt.Errorf("预编译合并规则失败: %w", err)
	}
	return eng, nil
}

// BuildGroupEngine 是便捷组合函数：一步完成「合并顺序 + 预编译」。
// 等价于 NewMergedEngine(MergeRuleGroups(global, group), def)，
// 供 Rebuild 在「已分别取出全局组与分组组」时直接调用，少写一行拼接。
func BuildGroupEngine(globalGroups, groupGroups []RuleGroupSpec, def Action) (*Engine, error) {
	return NewMergedEngine(MergeRuleGroups(globalGroups, groupGroups), def)
}
