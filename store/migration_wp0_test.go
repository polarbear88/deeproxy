package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// migration_wp0_test.go 覆盖 WP-0 数据层迁移原语与新字段读写往返。
//
// 目标：
//   1. 旧库（缺 all_groups / server_addr / probe_pool_size 三列）启动两次幂等无报错；
//   2. 新字段读写往返正确（all_groups / server_addr / probe_pool_size）。

// TestColumnExists 验证 PRAGMA table_info 内省判断列是否存在。
func TestColumnExists(t *testing.T) {
	s := newTestStore(t)

	cases := []struct {
		table, column string
		want          bool
	}{
		{"proxy_user", "all_groups", true},     // 迁移后应存在
		{"proxy_user", "username", true},       // 原有列
		{"proxy_user", "no_such_col", false},   // 不存在
		{"system_setting", "server_addr", true},
		{"system_setting", "probe_pool_size", true},
	}
	for _, c := range cases {
		got, err := columnExists(s.db, c.table, c.column)
		if err != nil {
			t.Fatalf("columnExists(%s,%s) 报错: %v", c.table, c.column, err)
		}
		if got != c.want {
			t.Fatalf("columnExists(%s,%s)=%v，期望 %v", c.table, c.column, got, c.want)
		}
	}
}

// TestMigrateColumnsIdempotentOnOldDB 模拟旧库：手建缺新列的表，跑两次迁移应幂等无错且补齐列。
func TestMigrateColumnsIdempotentOnOldDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// 用裸 driver 打开，手建「旧版」表结构（无 v2 新增三列），模拟历史库。
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	oldSchema := []string{
		`CREATE TABLE proxy_user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			pwd TEXT NOT NULL,
			remark TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE system_setting (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			admin_user TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		);`,
		`INSERT INTO proxy_user (username, pwd) VALUES ('legacy', 'p');`,
		`INSERT INTO system_setting (id) VALUES (1);`,
	}
	for _, stmt := range oldSchema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("建旧库失败: %v\nSQL=%s", err, stmt)
		}
	}

	// 第一次迁移：应补齐三列。
	if err := migrateColumns(db); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	// 第二次迁移：列已存在，必须幂等（不报 duplicate column）。
	if err := migrateColumns(db); err != nil {
		t.Fatalf("二次迁移应幂等，却报错: %v", err)
	}

	// 验证三列均已补齐。
	for _, c := range []struct{ table, col string }{
		{"proxy_user", "all_groups"},
		{"system_setting", "server_addr"},
		{"system_setting", "probe_pool_size"},
	} {
		ok, err := columnExists(db, c.table, c.col)
		if err != nil {
			t.Fatalf("检查 %s.%s 失败: %v", c.table, c.col, err)
		}
		if !ok {
			t.Fatalf("迁移后 %s.%s 仍不存在", c.table, c.col)
		}
	}

	// 旧行 all_groups 应取默认 0。
	var ag int
	if err := db.QueryRow(`SELECT all_groups FROM proxy_user WHERE username='legacy'`).Scan(&ag); err != nil {
		t.Fatalf("读旧行 all_groups 失败: %v", err)
	}
	if ag != 0 {
		t.Fatalf("旧行 all_groups 默认应为 0，得到 %d", ag)
	}
	// probe_pool_size 默认应为 150（ADD COLUMN ... DEFAULT 150 对既有行生效）。
	var pps int
	if err := db.QueryRow(`SELECT probe_pool_size FROM system_setting WHERE id=1`).Scan(&pps); err != nil {
		t.Fatalf("读 probe_pool_size 失败: %v", err)
	}
	if pps != 150 {
		t.Fatalf("probe_pool_size 默认应为 150，得到 %d", pps)
	}
	_ = db.Close()
}

// TestProxyUserAllGroupsRoundTrip 验证 all_groups 字段读写往返。
func TestProxyUserAllGroupsRoundTrip(t *testing.T) {
	s := newTestStore(t)

	u := &ProxyUser{Username: "wildcard", Pwd: "p", AllGroups: true}
	if err := s.CreateProxyUser(u); err != nil {
		t.Fatalf("新增用户失败: %v", err)
	}
	got, err := s.GetProxyUser(u.ID)
	if err != nil || got == nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if !got.AllGroups {
		t.Fatalf("创建时 AllGroups=true 应往返为 true")
	}

	// 关闭 all_groups 后应往返为 false。
	got.AllGroups = false
	if err := s.UpdateProxyUser(got); err != nil {
		t.Fatalf("更新用户失败: %v", err)
	}
	again, _ := s.GetProxyUser(u.ID)
	if again.AllGroups {
		t.Fatalf("更新 AllGroups=false 后应往返为 false")
	}

	// 列表查询也应正确反映。
	list, err := s.ListProxyUsers()
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(list) != 1 || list[0].AllGroups {
		t.Fatalf("列表 AllGroups 往返不正确: %+v", list)
	}
}

// TestSystemSettingNewFieldsRoundTrip 验证 server_addr / probe_pool_size 读写往返。
func TestSystemSettingNewFieldsRoundTrip(t *testing.T) {
	s := newTestStore(t)

	ss, err := s.GetSystemSetting()
	if err != nil {
		t.Fatalf("读取系统设置失败: %v", err)
	}
	// 新库默认值：server_addr 空、probe_pool_size 150。
	if ss.ServerAddr != "" {
		t.Fatalf("server_addr 默认应为空，得到 %q", ss.ServerAddr)
	}
	if ss.ProbePoolSize != 150 {
		t.Fatalf("probe_pool_size 默认应为 150，得到 %d", ss.ProbePoolSize)
	}

	ss.ServerAddr = "proxy.example.com"
	ss.ProbePoolSize = 200
	if err := s.UpdateSystemSetting(ss); err != nil {
		t.Fatalf("更新系统设置失败: %v", err)
	}
	got, _ := s.GetSystemSetting()
	if got.ServerAddr != "proxy.example.com" {
		t.Fatalf("server_addr 往返失败: %q", got.ServerAddr)
	}
	if got.ProbePoolSize != 200 {
		t.Fatalf("probe_pool_size 往返失败: %d", got.ProbePoolSize)
	}
}
