// 本文件补充「用户维度」与「规则组维度」的覆盖式授权/关联设置（事务原子）。
//
// 已有 assoc_repo.go 提供分组维度的 SetGroupUsers / SetGroupRuleGroups；
// 前端按「用户维度」（设置某用户授权哪些分组）与「规则组维度」（设置某规则组应用到哪些分组）
// 操作，故在此提供对应的覆盖式 setter 与反查列表，语义与分组维度对称（DRY 复用同两张关联表）。
package store

import (
	"database/sql"
	"fmt"
)

// SetUserGroups 覆盖式设置某代理用户被授权的分组集合（用户维度，AC-30）。
func (s *Store) SetUserGroups(userID int64, groupIDs []int64) error {
	return s.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM group_user WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("清空用户授权失败: %w", err)
		}
		for _, gid := range groupIDs {
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO group_user (group_id, user_id) VALUES (?, ?)`, gid, userID,
			); err != nil {
				return fmt.Errorf("写入用户授权失败: %w", err)
			}
		}
		return nil
	})
}

// SetRuleGroupGroups 覆盖式设置某规则组应用到的分组集合（规则组维度，AC-29）。
func (s *Store) SetRuleGroupGroups(ruleGroupID int64, groupIDs []int64) error {
	return s.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM group_rulegroup WHERE rule_group_id = ?`, ruleGroupID); err != nil {
			return fmt.Errorf("清空规则组关联失败: %w", err)
		}
		for _, gid := range groupIDs {
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO group_rulegroup (group_id, rule_group_id) VALUES (?, ?)`, gid, ruleGroupID,
			); err != nil {
				return fmt.Errorf("写入规则组关联失败: %w", err)
			}
		}
		return nil
	})
}
