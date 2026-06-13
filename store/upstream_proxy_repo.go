package store

import (
	"database/sql"
	"fmt"
)

// upstream_proxy_repo.go 提供 Type B 分组下上游代理的 CRUD 与健康状态/启停更新。

// CreateUpstream 新增一条上游代理，回填自增 ID。
func (s *Store) CreateUpstream(u *UpstreamProxy) error {
	return s.Write(func(db *sql.DB) error {
		ts := fmtTime(now())
		res, err := db.Exec(
			`INSERT INTO upstream_proxy
			   (group_id, host, port, user, username_template, pwd, weight, enabled, health_state, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.GroupID, u.Host, u.Port, u.User, u.UsernameTemplate, u.Pwd,
			u.Weight, boolToInt(u.Enabled), boolToInt(u.HealthState), ts, ts,
		)
		if err != nil {
			return fmt.Errorf("新增上游代理失败: %w", err)
		}
		id, _ := res.LastInsertId()
		u.ID = id
		return nil
	})
}

// GetUpstream 按 ID 查询；不存在返回 (nil, nil)。
func (s *Store) GetUpstream(id int64) (*UpstreamProxy, error) {
	row := s.db.QueryRow(upstreamSelectCols+` WHERE id = ?`, id)
	u, err := scanUpstreamRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询上游代理失败: %w", err)
	}
	return u, nil
}

// ListUpstreamsByGroup 列出某分组下全部上游（快照物化、后台分组详情）。
func (s *Store) ListUpstreamsByGroup(groupID int64) ([]UpstreamProxy, error) {
	return s.queryUpstreams(upstreamSelectCols+` WHERE group_id = ? ORDER BY id`, groupID)
}

// ListAllUpstreams 列出全部上游（健康检查 worker 启动时一次性加载）。
func (s *Store) ListAllUpstreams() ([]UpstreamProxy, error) {
	return s.queryUpstreams(upstreamSelectCols + ` ORDER BY group_id, id`)
}

// UpdateUpstream 更新上游代理（主机/端口/凭据/模板/权重/启停）。
// 健康状态用独立方法 UpdateUpstreamHealth 更新，避免 CRUD 与探测互相覆盖。
func (s *Store) UpdateUpstream(u *UpstreamProxy) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE upstream_proxy SET
			   group_id = ?, host = ?, port = ?, user = ?, username_template = ?, pwd = ?,
			   weight = ?, enabled = ?, updated_at = ?
			 WHERE id = ?`,
			u.GroupID, u.Host, u.Port, u.User, u.UsernameTemplate, u.Pwd,
			u.Weight, boolToInt(u.Enabled), fmtTime(now()), u.ID,
		)
		if err != nil {
			return fmt.Errorf("更新上游代理失败: %w", err)
		}
		return nil
	})
}

// UpdateUpstreamHealth 仅更新某上游的健康状态（健康检查 worker 持久化探测结果）。
// 单列更新，避免与后台 CRUD 的全字段 UPDATE 互相覆盖。
func (s *Store) UpdateUpstreamHealth(id int64, healthy bool) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE upstream_proxy SET health_state = ?, updated_at = ? WHERE id = ?`,
			boolToInt(healthy), fmtTime(now()), id,
		)
		if err != nil {
			return fmt.Errorf("更新上游健康状态失败: %w", err)
		}
		return nil
	})
}

// SetUpstreamEnabled 手动启用/禁用单条上游（AC-18）。
func (s *Store) SetUpstreamEnabled(id int64, enabled bool) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE upstream_proxy SET enabled = ?, updated_at = ? WHERE id = ?`,
			boolToInt(enabled), fmtTime(now()), id,
		)
		if err != nil {
			return fmt.Errorf("启停上游失败: %w", err)
		}
		return nil
	})
}

// DeleteUpstream 删除一条上游代理。
func (s *Store) DeleteUpstream(id int64) error {
	return s.Write(func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM upstream_proxy WHERE id = ?`, id); err != nil {
			return fmt.Errorf("删除上游代理失败: %w", err)
		}
		return nil
	})
}

// upstreamSelectCols 是上游查询的统一列清单（DRY）。
const upstreamSelectCols = `SELECT id, group_id, host, port, user, username_template, pwd,
	weight, enabled, health_state, created_at, updated_at FROM upstream_proxy`

// queryUpstreams 执行查询并扫描成切片（多处复用，DRY）。
func (s *Store) queryUpstreams(query string, args ...any) ([]UpstreamProxy, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询上游代理列表失败: %w", err)
	}
	defer rows.Close()

	var list []UpstreamProxy
	for rows.Next() {
		u, err := scanUpstreamRow(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描上游代理失败: %w", err)
		}
		list = append(list, *u)
	}
	return list, rows.Err()
}

// scanUpstreamRow 把一行扫描成 *UpstreamProxy。
func scanUpstreamRow(sc rowScanner) (*UpstreamProxy, error) {
	var u UpstreamProxy
	var enabled, health int
	var createdAt, updatedAt string
	if err := sc.Scan(
		&u.ID, &u.GroupID, &u.Host, &u.Port, &u.User, &u.UsernameTemplate, &u.Pwd,
		&u.Weight, &enabled, &health, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	u.Enabled = enabled != 0
	u.HealthState = health != 0
	u.CreatedAt = parseTime(createdAt)
	u.UpdatedAt = parseTime(updatedAt)
	return &u, nil
}
