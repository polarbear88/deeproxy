package store

import (
	"database/sql"
	"fmt"
)

// rule_repo.go 提供规则组与规则的 CRUD。
// 规则组 scope=global 对所有连接生效；scope=group 经 group_rulegroup 关联到分组。

// CreateRuleGroup 新增规则组，回填自增 ID。
func (s *Store) CreateRuleGroup(rg *RuleGroup) error {
	return s.Write(func(db *sql.DB) error {
		ts := fmtTime(now())
		res, err := db.Exec(
			`INSERT INTO rule_group (name, scope, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			rg.Name, string(rg.Scope), ts, ts,
		)
		if err != nil {
			return fmt.Errorf("新增规则组失败: %w", err)
		}
		id, _ := res.LastInsertId()
		rg.ID = id
		return nil
	})
}

// GetRuleGroupByName 按名称查询规则组；不存在返回 (nil, nil)。
// 供 API handler 在创建/改名前做重名预校验（配合 rule_group.name 的 UNIQUE 约束，
// 给出清晰的 409 错误，而非让 UNIQUE 冲突冒泡成 500）。
func (s *Store) GetRuleGroupByName(name string) (*RuleGroup, error) {
	var rg RuleGroup
	var scope, createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, name, scope, created_at, updated_at FROM rule_group WHERE name = ?`, name,
	).Scan(&rg.ID, &rg.Name, &scope, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询规则组失败: %w", err)
	}
	rg.Scope = RuleScope(scope)
	rg.CreatedAt = parseTime(createdAt)
	rg.UpdatedAt = parseTime(updatedAt)
	return &rg, nil
}

// ListRuleGroups 列出全部规则组。
func (s *Store) ListRuleGroups() ([]RuleGroup, error) {
	rows, err := s.db.Query(`SELECT id, name, scope, created_at, updated_at FROM rule_group ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询规则组列表失败: %w", err)
	}
	defer rows.Close()

	var list []RuleGroup
	for rows.Next() {
		var rg RuleGroup
		var scope, createdAt, updatedAt string
		if err := rows.Scan(&rg.ID, &rg.Name, &scope, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("扫描规则组失败: %w", err)
		}
		rg.Scope = RuleScope(scope)
		rg.CreatedAt = parseTime(createdAt)
		rg.UpdatedAt = parseTime(updatedAt)
		list = append(list, rg)
	}
	return list, rows.Err()
}

// UpdateRuleGroup 更新规则组（名称/作用域）。
func (s *Store) UpdateRuleGroup(rg *RuleGroup) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE rule_group SET name = ?, scope = ?, updated_at = ? WHERE id = ?`,
			rg.Name, string(rg.Scope), fmtTime(now()), rg.ID,
		)
		if err != nil {
			return fmt.Errorf("更新规则组失败: %w", err)
		}
		return nil
	})
}

// DeleteRuleGroup 删除规则组（外键级联删除其规则与分组关联）。
func (s *Store) DeleteRuleGroup(id int64) error {
	return s.Write(func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM rule_group WHERE id = ?`, id); err != nil {
			return fmt.Errorf("删除规则组失败: %w", err)
		}
		return nil
	})
}

// CreateRule 在某规则组下新增一条规则，回填自增 ID。
func (s *Store) CreateRule(r *Rule) error {
	return s.Write(func(db *sql.DB) error {
		ts := fmtTime(now())
		res, err := db.Exec(
			`INSERT INTO rule (rule_group_id, match, action, order_idx, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			r.RuleGroupID, r.Match, r.Action, r.OrderIdx, ts, ts,
		)
		if err != nil {
			return fmt.Errorf("新增规则失败: %w", err)
		}
		id, _ := res.LastInsertId()
		r.ID = id
		return nil
	})
}

// ListRulesByGroup 列出某规则组下的规则（按 order_idx 升序 = 书写顺序）。
func (s *Store) ListRulesByGroup(ruleGroupID int64) ([]Rule, error) {
	return s.queryRules(
		`SELECT id, rule_group_id, match, action, order_idx, created_at, updated_at
		 FROM rule WHERE rule_group_id = ? ORDER BY order_idx, id`, ruleGroupID)
}

// ListAllRules 列出全部规则（快照物化时一次性加载，再按规则组分桶）。
func (s *Store) ListAllRules() ([]Rule, error) {
	return s.queryRules(
		`SELECT id, rule_group_id, match, action, order_idx, created_at, updated_at
		 FROM rule ORDER BY rule_group_id, order_idx, id`)
}

// UpdateRule 更新一条规则。
func (s *Store) UpdateRule(r *Rule) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE rule SET match = ?, action = ?, order_idx = ?, updated_at = ? WHERE id = ?`,
			r.Match, r.Action, r.OrderIdx, fmtTime(now()), r.ID,
		)
		if err != nil {
			return fmt.Errorf("更新规则失败: %w", err)
		}
		return nil
	})
}

// DeleteRule 删除一条规则。
func (s *Store) DeleteRule(id int64) error {
	return s.Write(func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM rule WHERE id = ?`, id); err != nil {
			return fmt.Errorf("删除规则失败: %w", err)
		}
		return nil
	})
}

// queryRules 执行规则查询并扫描成切片（DRY）。
func (s *Store) queryRules(query string, args ...any) ([]Rule, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询规则列表失败: %w", err)
	}
	defer rows.Close()

	var list []Rule
	for rows.Next() {
		var r Rule
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.RuleGroupID, &r.Match, &r.Action, &r.OrderIdx, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("扫描规则失败: %w", err)
		}
		r.CreatedAt = parseTime(createdAt)
		r.UpdatedAt = parseTime(updatedAt)
		list = append(list, r)
	}
	return list, rows.Err()
}
