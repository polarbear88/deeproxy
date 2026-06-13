package store

import (
	"database/sql"
	"fmt"
)

// group_repo.go 提供代理分组的 CRUD（健康检查配置内嵌于分组行）。

// CreateGroup 新增分组，回填自增 ID。
func (s *Store) CreateGroup(g *Group) error {
	return s.Write(func(db *sql.DB) error {
		ts := fmtTime(now())
		res, err := db.Exec(
			`INSERT INTO "group"
			   (name, remark, type, hc_enabled, hc_mode, hc_url, hc_interval, hc_fail_thld, hc_recv_thld, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.Name, g.Remark, string(g.Type), boolToInt(g.HCEnabled), string(g.HCMode),
			g.HCURL, g.HCInterval, g.HCFailThld, g.HCRecvThld, ts, ts,
		)
		if err != nil {
			return fmt.Errorf("新增分组失败: %w", err)
		}
		id, _ := res.LastInsertId()
		g.ID = id
		return nil
	})
}

// GetGroup 按 ID 查询；不存在返回 (nil, nil)。
func (s *Store) GetGroup(id int64) (*Group, error) {
	return s.scanGroup(s.db.QueryRow(groupSelectCols+` WHERE id = ?`, id))
}

// GetGroupByName 按名称查询；不存在返回 (nil, nil)。
// 连接的 group 段是名称还是 ID 由上层（auth/config 快照）约定，此处提供按名查询能力。
func (s *Store) GetGroupByName(name string) (*Group, error) {
	return s.scanGroup(s.db.QueryRow(groupSelectCols+` WHERE name = ?`, name))
}

// ListGroups 列出全部分组（快照物化与后台列表）。
func (s *Store) ListGroups() ([]Group, error) {
	rows, err := s.db.Query(groupSelectCols + ` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询分组列表失败: %w", err)
	}
	defer rows.Close()

	var list []Group
	for rows.Next() {
		g, err := scanGroupRow(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *g)
	}
	return list, rows.Err()
}

// UpdateGroup 更新分组（含健康检查配置）。
func (s *Store) UpdateGroup(g *Group) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE "group" SET
			   name = ?, remark = ?, type = ?, hc_enabled = ?, hc_mode = ?, hc_url = ?,
			   hc_interval = ?, hc_fail_thld = ?, hc_recv_thld = ?, updated_at = ?
			 WHERE id = ?`,
			g.Name, g.Remark, string(g.Type), boolToInt(g.HCEnabled), string(g.HCMode),
			g.HCURL, g.HCInterval, g.HCFailThld, g.HCRecvThld, fmtTime(now()), g.ID,
		)
		if err != nil {
			return fmt.Errorf("更新分组失败: %w", err)
		}
		return nil
	})
}

// DeleteGroup 删除分组（外键级联删除其上游、授权、规则组关联）。
func (s *Store) DeleteGroup(id int64) error {
	return s.Write(func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM "group" WHERE id = ?`, id); err != nil {
			return fmt.Errorf("删除分组失败: %w", err)
		}
		return nil
	})
}

// groupSelectCols 是分组查询的统一列清单（DRY）。
const groupSelectCols = `SELECT id, name, remark, type, hc_enabled, hc_mode, hc_url,
	hc_interval, hc_fail_thld, hc_recv_thld, created_at, updated_at FROM "group"`

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的 Scan，便于单行/多行复用同一扫描逻辑（DRY）。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanGroupRow 把一行扫描成 *Group。
func scanGroupRow(sc rowScanner) (*Group, error) {
	var g Group
	var typ, mode, createdAt, updatedAt string
	var hcEnabled int
	if err := sc.Scan(
		&g.ID, &g.Name, &g.Remark, &typ, &hcEnabled, &mode, &g.HCURL,
		&g.HCInterval, &g.HCFailThld, &g.HCRecvThld, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	g.Type = GroupType(typ)
	g.HCMode = HealthMode(mode)
	g.HCEnabled = hcEnabled != 0
	g.CreatedAt = parseTime(createdAt)
	g.UpdatedAt = parseTime(updatedAt)
	return &g, nil
}

// scanGroup 扫描单行分组；sql.ErrNoRows 归一为 (nil, nil)。
func (s *Store) scanGroup(row *sql.Row) (*Group, error) {
	g, err := scanGroupRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询分组失败: %w", err)
	}
	return g, nil
}
