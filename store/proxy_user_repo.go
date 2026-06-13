package store

import (
	"database/sql"
	"fmt"
)

// proxy_user_repo.go 提供代理用户的 CRUD。
// 读直查、写经单写协程串行。

// CreateProxyUser 新增代理用户，回填自增 ID。
func (s *Store) CreateProxyUser(u *ProxyUser) error {
	return s.Write(func(db *sql.DB) error {
		ts := fmtTime(now())
		res, err := db.Exec(
			`INSERT INTO proxy_user (username, pwd, remark, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			u.Username, u.Pwd, u.Remark, ts, ts,
		)
		if err != nil {
			return fmt.Errorf("新增代理用户失败: %w", err)
		}
		id, _ := res.LastInsertId()
		u.ID = id
		return nil
	})
}

// GetProxyUser 按 ID 查询；不存在返回 (nil, nil)。
func (s *Store) GetProxyUser(id int64) (*ProxyUser, error) {
	return s.scanProxyUser(s.db.QueryRow(
		`SELECT id, username, pwd, remark, created_at, updated_at
		 FROM proxy_user WHERE id = ?`, id))
}

// GetProxyUserByName 按用户名查询（鉴权热点之一，但仅在快照重建时由 config 调用，非转发热路径）。
func (s *Store) GetProxyUserByName(name string) (*ProxyUser, error) {
	return s.scanProxyUser(s.db.QueryRow(
		`SELECT id, username, pwd, remark, created_at, updated_at
		 FROM proxy_user WHERE username = ?`, name))
}

// ListProxyUsers 列出全部代理用户（用于快照物化与后台列表）。
func (s *Store) ListProxyUsers() ([]ProxyUser, error) {
	rows, err := s.db.Query(
		`SELECT id, username, pwd, remark, created_at, updated_at
		 FROM proxy_user ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询代理用户列表失败: %w", err)
	}
	defer rows.Close()

	var list []ProxyUser
	for rows.Next() {
		var u ProxyUser
		var createdAt, updatedAt string
		if err := rows.Scan(&u.ID, &u.Username, &u.Pwd, &u.Remark, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("扫描代理用户失败: %w", err)
		}
		u.CreatedAt = parseTime(createdAt)
		u.UpdatedAt = parseTime(updatedAt)
		list = append(list, u)
	}
	return list, rows.Err()
}

// UpdateProxyUser 更新代理用户（含密码哈希与备注）。
func (s *Store) UpdateProxyUser(u *ProxyUser) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE proxy_user SET username = ?, pwd = ?, remark = ?, updated_at = ? WHERE id = ?`,
			u.Username, u.Pwd, u.Remark, fmtTime(now()), u.ID,
		)
		if err != nil {
			return fmt.Errorf("更新代理用户失败: %w", err)
		}
		return nil
	})
}

// DeleteProxyUser 删除代理用户（外键级联删除其授权关系）。
func (s *Store) DeleteProxyUser(id int64) error {
	return s.Write(func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM proxy_user WHERE id = ?`, id); err != nil {
			return fmt.Errorf("删除代理用户失败: %w", err)
		}
		return nil
	})
}

// scanProxyUser 扫描单行代理用户；sql.ErrNoRows 归一为 (nil, nil)。
func (s *Store) scanProxyUser(row *sql.Row) (*ProxyUser, error) {
	var u ProxyUser
	var createdAt, updatedAt string
	err := row.Scan(&u.ID, &u.Username, &u.Pwd, &u.Remark, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询代理用户失败: %w", err)
	}
	u.CreatedAt = parseTime(createdAt)
	u.UpdatedAt = parseTime(updatedAt)
	return &u, nil
}
