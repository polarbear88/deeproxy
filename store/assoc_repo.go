package store

import (
	"database/sql"
	"fmt"
)

// assoc_repo.go 提供两类多对多关联的读写：
//   - group_user：分组↔代理用户授权（鉴权时判定 user 是否被授权访问 group）。
//   - group_rulegroup：分组↔规则组关联（合并匹配时取该 group 应用的分组规则组）。

// AddGroupUser 新增一条分组↔用户授权（已存在则忽略，幂等）。
func (s *Store) AddGroupUser(groupID, userID int64) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`INSERT OR IGNORE INTO group_user (group_id, user_id) VALUES (?, ?)`,
			groupID, userID,
		)
		if err != nil {
			return fmt.Errorf("新增分组授权失败: %w", err)
		}
		return nil
	})
}

// RemoveGroupUser 删除一条分组↔用户授权。
func (s *Store) RemoveGroupUser(groupID, userID int64) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`DELETE FROM group_user WHERE group_id = ? AND user_id = ?`, groupID, userID)
		if err != nil {
			return fmt.Errorf("删除分组授权失败: %w", err)
		}
		return nil
	})
}

// SetGroupUsers 把某分组的授权用户整体替换为给定集合（覆盖式更新，事务保证原子）。
func (s *Store) SetGroupUsers(groupID int64, userIDs []int64) error {
	return s.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM group_user WHERE group_id = ?`, groupID); err != nil {
			return fmt.Errorf("清空分组授权失败: %w", err)
		}
		for _, uid := range userIDs {
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO group_user (group_id, user_id) VALUES (?, ?)`, groupID, uid); err != nil {
				return fmt.Errorf("写入分组授权失败: %w", err)
			}
		}
		return nil
	})
}

// ListGroupUsers 列出全部分组↔用户授权（快照物化）。
func (s *Store) ListGroupUsers() ([]GroupUser, error) {
	rows, err := s.db.Query(`SELECT group_id, user_id FROM group_user`)
	if err != nil {
		return nil, fmt.Errorf("查询分组授权失败: %w", err)
	}
	defer rows.Close()

	var list []GroupUser
	for rows.Next() {
		var gu GroupUser
		if err := rows.Scan(&gu.GroupID, &gu.UserID); err != nil {
			return nil, fmt.Errorf("扫描分组授权失败: %w", err)
		}
		list = append(list, gu)
	}
	return list, rows.Err()
}

// AddGroupRuleGroup 关联一个规则组到分组（已存在则忽略，幂等）。
func (s *Store) AddGroupRuleGroup(groupID, ruleGroupID int64) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`INSERT OR IGNORE INTO group_rulegroup (group_id, rule_group_id) VALUES (?, ?)`,
			groupID, ruleGroupID,
		)
		if err != nil {
			return fmt.Errorf("关联规则组到分组失败: %w", err)
		}
		return nil
	})
}

// RemoveGroupRuleGroup 取消一个规则组与分组的关联。
func (s *Store) RemoveGroupRuleGroup(groupID, ruleGroupID int64) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`DELETE FROM group_rulegroup WHERE group_id = ? AND rule_group_id = ?`,
			groupID, ruleGroupID)
		if err != nil {
			return fmt.Errorf("取消规则组关联失败: %w", err)
		}
		return nil
	})
}

// SetGroupRuleGroups 把某分组关联的规则组整体替换为给定集合（覆盖式更新，事务保证原子）。
func (s *Store) SetGroupRuleGroups(groupID int64, ruleGroupIDs []int64) error {
	return s.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM group_rulegroup WHERE group_id = ?`, groupID); err != nil {
			return fmt.Errorf("清空分组规则组关联失败: %w", err)
		}
		for _, rgid := range ruleGroupIDs {
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO group_rulegroup (group_id, rule_group_id) VALUES (?, ?)`,
				groupID, rgid); err != nil {
				return fmt.Errorf("写入分组规则组关联失败: %w", err)
			}
		}
		return nil
	})
}

// ListGroupRuleGroups 列出全部分组↔规则组关联（快照物化）。
func (s *Store) ListGroupRuleGroups() ([]GroupRuleGroup, error) {
	rows, err := s.db.Query(`SELECT group_id, rule_group_id FROM group_rulegroup`)
	if err != nil {
		return nil, fmt.Errorf("查询分组规则组关联失败: %w", err)
	}
	defer rows.Close()

	var list []GroupRuleGroup
	for rows.Next() {
		var g GroupRuleGroup
		if err := rows.Scan(&g.GroupID, &g.RuleGroupID); err != nil {
			return nil, fmt.Errorf("扫描分组规则组关联失败: %w", err)
		}
		list = append(list, g)
	}
	return list, rows.Err()
}
