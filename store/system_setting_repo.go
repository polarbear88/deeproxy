package store

import (
	"database/sql"
	"fmt"
)

// system_setting_repo.go 提供单行系统设置（含管理员凭据）的读写。
// 单行配置（id=1），读直查、写经单写协程串行（AC-14）。

// GetSystemSetting 读取单行系统设置。
func (s *Store) GetSystemSetting() (*SystemSetting, error) {
	row := s.db.QueryRow(`
		SELECT id, admin_user, admin_pwd_hash, stat_retention_days,
		       hc_default_mode, hc_default_url, hc_default_interval,
		       hc_default_fail_thld, hc_default_recv_thld,
		       default_action, log_level, idle_timeout_sec, sniff_domain, sniff_timeout_ms,
		       updated_at
		FROM system_setting WHERE id = 1`)

	var ss SystemSetting
	var mode, updatedAt string
	var sniffDomain int // SQLite 用 INTEGER 0/1 存布尔，扫到 int 再转 bool
	if err := row.Scan(
		&ss.ID, &ss.AdminUser, &ss.AdminPwdHash, &ss.StatRetentionDays,
		&mode, &ss.HCDefaultURL, &ss.HCDefaultInterval,
		&ss.HCDefaultFailThld, &ss.HCDefaultRecvThld,
		&ss.DefaultAction, &ss.LogLevel, &ss.IdleTimeoutSec, &sniffDomain, &ss.SniffTimeoutMs,
		&updatedAt,
	); err != nil {
		return nil, fmt.Errorf("读取系统设置失败: %w", err)
	}
	ss.HCDefaultMode = HealthMode(mode)
	ss.SniffDomain = sniffDomain != 0
	ss.UpdatedAt = parseTime(updatedAt)
	return &ss, nil
}

// UpdateSystemSetting 整体更新单行系统设置。
func (s *Store) UpdateSystemSetting(ss *SystemSetting) error {
	return s.Write(func(db *sql.DB) error {
		// bool→INTEGER 0/1（SQLite 无原生布尔）。
		sniffDomain := 0
		if ss.SniffDomain {
			sniffDomain = 1
		}
		_, err := db.Exec(`
			UPDATE system_setting SET
				admin_user = ?, admin_pwd_hash = ?, stat_retention_days = ?,
				hc_default_mode = ?, hc_default_url = ?, hc_default_interval = ?,
				hc_default_fail_thld = ?, hc_default_recv_thld = ?,
				default_action = ?, log_level = ?, idle_timeout_sec = ?,
				sniff_domain = ?, sniff_timeout_ms = ?,
				updated_at = ?
			WHERE id = 1`,
			ss.AdminUser, ss.AdminPwdHash, ss.StatRetentionDays,
			string(ss.HCDefaultMode), ss.HCDefaultURL, ss.HCDefaultInterval,
			ss.HCDefaultFailThld, ss.HCDefaultRecvThld,
			ss.DefaultAction, ss.LogLevel, ss.IdleTimeoutSec,
			sniffDomain, ss.SniffTimeoutMs,
			fmtTime(now()),
		)
		if err != nil {
			return fmt.Errorf("更新系统设置失败: %w", err)
		}
		return nil
	})
}

// SetAdminCredential 单独更新管理员账号与密码哈希（首次设置/改密用 AC-19/AC-40）。
func (s *Store) SetAdminCredential(user, pwdHash string) error {
	return s.Write(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE system_setting SET admin_user = ?, admin_pwd_hash = ?, updated_at = ? WHERE id = 1`,
			user, pwdHash, fmtTime(now()),
		)
		if err != nil {
			return fmt.Errorf("设置管理员凭据失败: %w", err)
		}
		return nil
	})
}
