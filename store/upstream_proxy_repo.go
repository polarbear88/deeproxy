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
			   (group_id, host, port, user, pwd, weight, enabled, health_state, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.GroupID, u.Host, u.Port, u.User, u.Pwd,
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

// UpstreamFilter 是上游分页/批量筛选条件（管理面用，AC-3.3/3.4）。
//
// 字段语义：
//   - GroupID  ：限定分组（必填，>0）。
//   - Keyword  ：按 host 模糊匹配（LIKE %kw%）；为空则不限。
//   - HealthState：健康状态筛选，"healthy"/"unhealthy" 命中对应 health_state；其余值（含空）不限。
type UpstreamFilter struct {
	GroupID     int64
	Keyword     string
	HealthState string
}

// buildUpstreamWhere 由筛选条件拼出 WHERE 子句与参数（DRY：分页查询与批量更新共用）。
// 始终包含 group_id 限定，避免跨组误操作。
func buildUpstreamWhere(f UpstreamFilter) (string, []any) {
	where := " WHERE group_id = ?"
	args := []any{f.GroupID}
	if f.Keyword != "" {
		where += " AND host LIKE ?"
		args = append(args, "%"+f.Keyword+"%")
	}
	switch f.HealthState {
	case "healthy":
		where += " AND health_state = 1"
	case "unhealthy":
		where += " AND health_state = 0"
	}
	return where, args
}

// ListUpstreamsPaged 分页查询某分组的上游，返回当前页切片与匹配总数（AC-3.3）。
//
// page 从 1 开始；pageSize<=0 兜底 100。SQL 用 LIMIT/OFFSET，total 单独 COUNT。
func (s *Store) ListUpstreamsPaged(f UpstreamFilter, page, pageSize int) ([]UpstreamProxy, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	where, args := buildUpstreamWhere(f)

	// 先查总数（用于前端分页器）。
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM upstream_proxy`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计上游总数失败: %w", err)
	}

	// 再查当前页（按 id 稳定排序）。
	pagedArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	list, err := s.queryUpstreams(upstreamSelectCols+where+` ORDER BY id LIMIT ? OFFSET ?`, pagedArgs...)
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// UpdateUpstream 更新上游代理（主机/端口/凭据/权重/启停）。
// 健康状态用独立方法 UpdateUpstreamHealth 更新，避免 CRUD 与探测互相覆盖。
func (s *Store) UpdateUpstream(u *UpstreamProxy) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE upstream_proxy SET
			   group_id = ?, host = ?, port = ?, user = ?, pwd = ?,
			   weight = ?, enabled = ?, updated_at = ?
			 WHERE id = ?`,
			u.GroupID, u.Host, u.Port, u.User, u.Pwd,
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

// BulkDeleteUpstreamsByIDs 按 id 列表批量删除上游（镜像 BulkUpdateUpstreamsByIDs 的执行策略）。
//
// 执行策略：DELETE ... WHERE group_id=? AND id IN (...)，按 SQLite 参数上限【分块】，每块一条 SQL，
// 全部分块在【同一事务】内执行，保证 all-or-nothing 原子；groupID 限定避免误删跨组。返回受影响行数。
func (s *Store) BulkDeleteUpstreamsByIDs(groupID int64, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var affected int64
	err := s.WriteTx(func(tx *sql.Tx) error {
		// 按参数上限分块；每块预留 groupID 共 1 个固定参数。
		const reserved = 1
		chunkMax := sqliteMaxVars - reserved
		for start := 0; start < len(ids); start += chunkMax {
			end := start + chunkMax
			if end > len(ids) {
				end = len(ids)
			}
			chunk := ids[start:end]

			placeholders := make([]string, len(chunk))
			args := make([]any, 0, len(chunk)+reserved)
			args = append(args, groupID)
			for i, id := range chunk {
				placeholders[i] = "?"
				args = append(args, id)
			}
			q := fmt.Sprintf(
				`DELETE FROM upstream_proxy WHERE group_id = ? AND id IN (%s)`,
				joinComma(placeholders),
			)
			res, err := tx.Exec(q, args...)
			if err != nil {
				return fmt.Errorf("按 id 批量删除上游失败: %w", err)
			}
			n, _ := res.RowsAffected()
			affected += n
		}
		return nil
	})
	return affected, err
}

// BulkDeleteUpstreamsByFilter 按筛选条件【一条 SQL】批量删除上游（镜像 BulkUpdateUpstreamsByFilter，
// 支持跨页全选场景）。DELETE ... WHERE group_id=? AND <筛选>，一次写操作、非 N 次。返回受影响行数。
func (s *Store) BulkDeleteUpstreamsByFilter(f UpstreamFilter) (int64, error) {
	where, whereArgs := buildUpstreamWhere(f)

	var affected int64
	err := s.Write(func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM upstream_proxy`+where, whereArgs...)
		if err != nil {
			return fmt.Errorf("按筛选批量删除上游失败: %w", err)
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	return affected, err
}

// UpstreamBulkField 标识批量更新的目标字段。
type UpstreamBulkField int

const (
	BulkFieldWeight  UpstreamBulkField = iota // 批量改权重
	BulkFieldEnabled                          // 批量改启停
)

// sqliteMaxVars 是 SQLite 默认参数上限的保守值（实际默认 999/32766，取 900 留余量）。
// id 列表模式按此分块，避免 "too many SQL variables"。
const sqliteMaxVars = 900

// bulkSetColumn 由字段枚举与值拼出 "col = ?" 片段与绑定值（DRY）。
func bulkSetColumn(field UpstreamBulkField, weight int, enabled bool) (string, any) {
	if field == BulkFieldEnabled {
		return "enabled = ?", boolToInt(enabled)
	}
	return "weight = ?", weight
}

// BulkUpdateUpstreamsByFilter 按筛选条件【一条 SQL】批量更新上游的权重或启停（AC-3.4 筛选模式）。
//
// 执行策略（Critic 钉死）：UPDATE ... SET <field>=? WHERE group_id=? AND <筛选> —— 一次写操作，
// 非 N 次（避免单写协程 writeCh 串行数千次的管理面停顿）。匹配行在事务内原子全改。
// 返回受影响行数。
func (s *Store) BulkUpdateUpstreamsByFilter(f UpstreamFilter, field UpstreamBulkField, weight int, enabled bool) (int64, error) {
	setClause, setVal := bulkSetColumn(field, weight, enabled)
	where, whereArgs := buildUpstreamWhere(f)

	var affected int64
	err := s.Write(func(db *sql.DB) error {
		// 参数顺序：SET 值、updated_at、再 WHERE 参数。
		args := append([]any{setVal, fmtTime(now())}, whereArgs...)
		res, err := db.Exec(`UPDATE upstream_proxy SET `+setClause+`, updated_at = ?`+where, args...)
		if err != nil {
			return fmt.Errorf("按筛选批量更新上游失败: %w", err)
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	return affected, err
}

// BulkUpdateUpstreamsByIDs 按 id 列表批量更新上游的权重或启停（AC-3.4 id 列表模式）。
//
// 执行策略（Critic 钉死）：UPDATE ... WHERE id IN (...)，按 SQLite 参数上限【分块】，
// 每块一条 SQL；全部分块在【同一事务】内执行，保证 all-or-nothing 原子。groupID 限定避免跨组。
// 返回受影响行数。
func (s *Store) BulkUpdateUpstreamsByIDs(groupID int64, ids []int64, field UpstreamBulkField, weight int, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	setClause, setVal := bulkSetColumn(field, weight, enabled)

	var affected int64
	err := s.WriteTx(func(tx *sql.Tx) error {
		ts := fmtTime(now())
		// 按参数上限分块；每块预留 setVal+updated_at+groupID 共 3 个固定参数。
		const reserved = 3
		chunkMax := sqliteMaxVars - reserved
		for start := 0; start < len(ids); start += chunkMax {
			end := start + chunkMax
			if end > len(ids) {
				end = len(ids)
			}
			chunk := ids[start:end]

			placeholders := make([]string, len(chunk))
			args := make([]any, 0, len(chunk)+reserved)
			args = append(args, setVal, ts, groupID)
			for i, id := range chunk {
				placeholders[i] = "?"
				args = append(args, id)
			}
			q := fmt.Sprintf(
				`UPDATE upstream_proxy SET %s, updated_at = ? WHERE group_id = ? AND id IN (%s)`,
				setClause, joinComma(placeholders),
			)
			res, err := tx.Exec(q, args...)
			if err != nil {
				return fmt.Errorf("按 id 批量更新上游失败: %w", err)
			}
			n, _ := res.RowsAffected()
			affected += n
		}
		return nil
	})
	return affected, err
}

// joinComma 用逗号连接占位符（避免引入 strings 依赖到本文件的额外 import，局部小工具）。
func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// upstreamSelectCols 是上游查询的统一列清单（DRY）。
const upstreamSelectCols = `SELECT id, group_id, host, port, user, pwd,
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
		&u.ID, &u.GroupID, &u.Host, &u.Port, &u.User, &u.Pwd,
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
