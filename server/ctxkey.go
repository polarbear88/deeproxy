package server

import (
	"deeproxy/auth"
	"deeproxy/rule"
)

// decision 是 connectRule.Allow 阶段做出的判定结果，通过 context 传给 ConnectHandle，
// 避免规则重复求值。reject 与非 CONNECT 在 Allow 阶段已直接拒绝，不会进入 ConnectHandle。
type decision struct {
	action   rule.Action   // forward 或 direct（needsSniff 时此字段无意义，待嗅探后确定）
	upstream auth.Upstream // 本连接动态上游（forward 时使用）
	host     string        // 目标主机（域名或 IP，用于日志与回退）
	// needsSniff 为 true 表示：目标是 IP、未命中任何 ip-cidr 规则、且启用了嗅探，
	// 需要在 ConnectHandle 中先回 success、再 peek 客户端首包嗅探域名后才能选路。
	needsSniff bool
}

// ctxKey 是放入 context 的私有 key 类型，避免与其他包的 key 冲突。
type ctxKey struct{}

// decisionKey 是 decision 在 context 中的唯一键。
var decisionKey = ctxKey{}
