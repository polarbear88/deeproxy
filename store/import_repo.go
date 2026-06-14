// 本文件实现配置的【整体覆盖式导入】（AC-37 / G4）。
//
// 设计：导入是「迁移/恢复」语义——把当前所有配置表清空后按导入包重建，
// 在单个事务内完成（任一步失败整体回滚，配置不变）。保留导入包中的主键 ID，
// 以使分组↔用户、分组↔规则组、规则↔规则组等关联在覆盖后依然自洽。
//
// 不涉及表：system_setting（管理员凭据不随配置迁移）、traffic_stat（统计历史不迁移）。
package store

import (
	"database/sql"
	"fmt"
)

// ConfigBundle 是导入覆盖所需的全量配置实体集合（store 层 DTO）。
type ConfigBundle struct {
	Groups          []Group
	Upstreams       []UpstreamProxy
	RuleGroups      []RuleGroup
	Rules           []Rule
	Users           []ProxyUser
	GroupUsers      []GroupUser
	GroupRuleGroups []GroupRuleGroup
}

// ImportBundle 在单事务内整体覆盖配置表（G4：失败回滚、配置不变）。
//
// 顺序：先清空（含关联表），再按「被引用方先插」的顺序插入，避免外键约束失败：
// proxy_user / group → upstream_proxy / rule_group → rule → group_user / group_rulegroup。
// 保留原始 ID（显式插入 id 列）以维持关联完整性。
func (s *Store) ImportBundle(b ConfigBundle) error {
	return s.WriteTx(func(tx *sql.Tx) error {
		ts := fmtTime(now())

		// —— 1. 清空配置表（关联表会因 ON DELETE CASCADE 一并清，但显式清更稳妥） ——
		for _, t := range []string{
			"group_rulegroup", "group_user", "rule", "rule_group",
			"upstream_proxy", `"group"`, "proxy_user",
		} {
			if _, err := tx.Exec("DELETE FROM " + t); err != nil {
				return fmt.Errorf("清空表 %s 失败: %w", t, err)
			}
		}

		// —— 2. 代理用户 ——
		for _, u := range b.Users {
			if _, err := tx.Exec(
				`INSERT INTO proxy_user (id, username, pwd, remark, all_groups, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				u.ID, u.Username, u.Pwd, u.Remark, boolToInt(u.AllGroups), ts, ts,
			); err != nil {
				return fmt.Errorf("导入用户 %q 失败: %w", u.Username, err)
			}
		}

		// —— 3. 分组 ——
		for _, g := range b.Groups {
			if _, err := tx.Exec(
				`INSERT INTO "group"
				   (id, name, remark, type, hc_enabled, hc_mode, hc_url, hc_interval, hc_fail_thld, hc_recv_thld, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				g.ID, g.Name, g.Remark, string(g.Type), boolToInt(g.HCEnabled), string(g.HCMode),
				g.HCURL, g.HCInterval, g.HCFailThld, g.HCRecvThld, ts, ts,
			); err != nil {
				return fmt.Errorf("导入分组 %q 失败: %w", g.Name, err)
			}
		}

		// —— 4. 上游代理 ——
		for _, u := range b.Upstreams {
			if _, err := tx.Exec(
				`INSERT INTO upstream_proxy
				   (id, group_id, host, port, user, username_template, pwd, weight, enabled, health_state, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				u.ID, u.GroupID, u.Host, u.Port, u.User, u.UsernameTemplate, u.Pwd,
				u.Weight, boolToInt(u.Enabled), boolToInt(u.HealthState), ts, ts,
			); err != nil {
				return fmt.Errorf("导入上游 %d 失败: %w", u.ID, err)
			}
		}

		// —— 5. 规则组 ——
		for _, rg := range b.RuleGroups {
			if _, err := tx.Exec(
				`INSERT INTO rule_group (id, name, scope, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
				rg.ID, rg.Name, string(rg.Scope), ts, ts,
			); err != nil {
				return fmt.Errorf("导入规则组 %q 失败: %w", rg.Name, err)
			}
		}

		// —— 6. 规则 ——
		for _, r := range b.Rules {
			if _, err := tx.Exec(
				`INSERT INTO rule (id, rule_group_id, match, action, order_idx, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				r.ID, r.RuleGroupID, r.Match, r.Action, r.OrderIdx, ts, ts,
			); err != nil {
				return fmt.Errorf("导入规则 %d 失败: %w", r.ID, err)
			}
		}

		// —— 7. 授权关系 ——
		for _, gu := range b.GroupUsers {
			if _, err := tx.Exec(
				`INSERT INTO group_user (group_id, user_id) VALUES (?, ?)`, gu.GroupID, gu.UserID,
			); err != nil {
				return fmt.Errorf("导入授权关系失败: %w", err)
			}
		}

		// —— 8. 分组↔规则组关联 ——
		for _, grg := range b.GroupRuleGroups {
			if _, err := tx.Exec(
				`INSERT INTO group_rulegroup (group_id, rule_group_id) VALUES (?, ?)`,
				grg.GroupID, grg.RuleGroupID,
			); err != nil {
				return fmt.Errorf("导入分组规则组关联失败: %w", err)
			}
		}

		return nil
	})
}
