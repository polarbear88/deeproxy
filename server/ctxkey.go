package server

import (
	"deeproxy/auth"
	"deeproxy/rule"
)

// decision 是 WithRule.Allow 阶段做出的“放行类”判定结果，
// 通过 context 传递给后续的拨号 hook，避免规则被重复求值（规则只在 Allow 跑一次）。
//
// 仅当动作为 forward/direct 时才会被放入 context；reject 与非 CONNECT 命令
// 在 Allow 阶段直接返回 false，由库回 RepRuleFailure，不会走到拨号 hook。
type decision struct {
	action   rule.Action   // forward 或 direct
	upstream auth.Upstream // 本连接动态上游（forward 时使用）
	host     string        // 目标主机（用于日志）
}

// ctxKey 是放入 context 的私有 key 类型，避免与其他包的 key 冲突。
type ctxKey struct{}

// decisionKey 是 decision 在 context 中的唯一键。
var decisionKey = ctxKey{}
