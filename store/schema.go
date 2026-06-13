package store

import (
	"database/sql"
	"fmt"
)

// migrate 创建全部表与索引（幂等：均用 IF NOT EXISTS）。
//
// 为什么不引入迁移框架：首版表结构稳定（~9 张表），用 IF NOT EXISTS 建表足以满足；
// 后续如需版本化迁移再引入轻量方案。建表经单写协程串行执行，避免与运行期写竞争。
func (s *Store) migrate() error {
	return s.Write(func(db *sql.DB) error {
		for _, stmt := range schemaStmts {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("执行建表语句失败: %w\nSQL: %s", err, stmt)
			}
		}
		// 确保系统设置单行存在（id=1），便于后续 UPDATE 语义统一。
		if _, err := db.Exec(seedSystemSetting); err != nil {
			return fmt.Errorf("初始化系统设置行失败: %w", err)
		}
		return nil
	})
}

// schemaStmts 是建表与建索引语句集合。
//
// 设计说明：
//   - 时间字段统一用 TEXT 存 RFC3339（modernc 驱动对 time.Time 的处理依赖此约定，repo 层负责格式化/解析）。
//   - 布尔字段用 INTEGER 0/1。
//   - 关联表用复合主键防重复，并开启外键级联删除保证一致性。
//   - traffic_stat 配 (group_id,user_id,bucket_time) 复合主键即唯一约束，支撑 upsert 累加与按时间清理。
var schemaStmts = []string{
	// 系统设置（单行，id 固定为 1）：含全局唯一管理员凭据与各默认值。
	`CREATE TABLE IF NOT EXISTS system_setting (
		id                  INTEGER PRIMARY KEY CHECK (id = 1),
		admin_user          TEXT    NOT NULL DEFAULT '',
		admin_pwd_hash      TEXT    NOT NULL DEFAULT '',
		stat_retention_days INTEGER NOT NULL DEFAULT 30,
		hc_default_mode     TEXT    NOT NULL DEFAULT 'url',
		hc_default_url      TEXT    NOT NULL DEFAULT 'https://www.bing.com/hp/api/v1/carousel?&format=json',
		hc_default_interval INTEGER NOT NULL DEFAULT 600,
		hc_default_fail_thld INTEGER NOT NULL DEFAULT 3,
		hc_default_recv_thld INTEGER NOT NULL DEFAULT 2,
		default_action      TEXT    NOT NULL DEFAULT 'forward',
		log_level           TEXT    NOT NULL DEFAULT 'info',
		idle_timeout_sec    INTEGER NOT NULL DEFAULT 300,
		sniff_domain        INTEGER NOT NULL DEFAULT 1,
		sniff_timeout_ms    INTEGER NOT NULL DEFAULT 300,
		updated_at          TEXT    NOT NULL DEFAULT ''
	);`,

	// 代理用户：连 SOCKS5 代理的身份（不能登录后台）。pwd 为明文连接密码（非哈希）。
	`CREATE TABLE IF NOT EXISTS proxy_user (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT    NOT NULL UNIQUE,
		pwd        TEXT    NOT NULL,
		remark     TEXT    NOT NULL DEFAULT '',
		created_at TEXT    NOT NULL DEFAULT '',
		updated_at TEXT    NOT NULL DEFAULT ''
	);`,

	// 代理分组：A=动态上游 / B=代理池（含内嵌健康检查配置）。
	// name 加 UNIQUE：鉴权时按用户名 group 段名查分组（LookupGroup(name)），
	// 快照以 map[name]*GroupView 索引；若允许重名，map 后写覆盖前者且遍历无序，
	// 会把请求路由到与管理员预期不同的分组（不同上游池/规则/授权）→ 真实越权/路由缺陷。
	// 故在 DB 层强制分组名全局唯一，从根上杜绝重名进入快照。
	`CREATE TABLE IF NOT EXISTS "group" (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		name         TEXT    NOT NULL UNIQUE,
		remark       TEXT    NOT NULL DEFAULT '',
		type         TEXT    NOT NULL CHECK (type IN ('A','B')),
		hc_enabled   INTEGER NOT NULL DEFAULT 0,
		hc_mode      TEXT    NOT NULL DEFAULT 'url',
		hc_url       TEXT    NOT NULL DEFAULT '',
		hc_interval  INTEGER NOT NULL DEFAULT 600,
		hc_fail_thld INTEGER NOT NULL DEFAULT 3,
		hc_recv_thld INTEGER NOT NULL DEFAULT 2,
		created_at   TEXT    NOT NULL DEFAULT '',
		updated_at   TEXT    NOT NULL DEFAULT ''
	);`,

	// 上游代理：仅属于 Type B 分组；含权重、启停、健康状态、用户名模板。
	`CREATE TABLE IF NOT EXISTS upstream_proxy (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id          INTEGER NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
		host              TEXT    NOT NULL,
		port              INTEGER NOT NULL,
		user              TEXT    NOT NULL DEFAULT '',
		username_template TEXT    NOT NULL DEFAULT '',
		pwd               TEXT    NOT NULL DEFAULT '',
		weight            INTEGER NOT NULL DEFAULT 1,
		enabled           INTEGER NOT NULL DEFAULT 1,
		health_state      INTEGER NOT NULL DEFAULT 1,
		created_at        TEXT    NOT NULL DEFAULT '',
		updated_at        TEXT    NOT NULL DEFAULT ''
	);`,
	`CREATE INDEX IF NOT EXISTS idx_upstream_group ON upstream_proxy(group_id);`,

	// 规则组：scope=global 对所有连接生效；scope=group 仅对关联分组生效。
	// name 加 UNIQUE：与 group 同理，快照按规则组名索引，重名会静默覆盖导致选错规则集；
	// 故强制规则组名全局唯一。
	`CREATE TABLE IF NOT EXISTS rule_group (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT    NOT NULL UNIQUE,
		scope      TEXT    NOT NULL CHECK (scope IN ('global','group')),
		created_at TEXT    NOT NULL DEFAULT '',
		updated_at TEXT    NOT NULL DEFAULT ''
	);`,

	// 规则：属于一个规则组，order_idx 升序即组内书写顺序（顺序首匹配）。
	`CREATE TABLE IF NOT EXISTS rule (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		rule_group_id INTEGER NOT NULL REFERENCES rule_group(id) ON DELETE CASCADE,
		match         TEXT    NOT NULL,
		action        TEXT    NOT NULL CHECK (action IN ('forward','direct','reject')),
		order_idx     INTEGER NOT NULL DEFAULT 0,
		created_at    TEXT    NOT NULL DEFAULT '',
		updated_at    TEXT    NOT NULL DEFAULT ''
	);`,
	`CREATE INDEX IF NOT EXISTS idx_rule_group ON rule(rule_group_id, order_idx);`,

	// 分组↔代理用户授权（多对多）。
	`CREATE TABLE IF NOT EXISTS group_user (
		group_id INTEGER NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
		user_id  INTEGER NOT NULL REFERENCES proxy_user(id) ON DELETE CASCADE,
		PRIMARY KEY (group_id, user_id)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_group_user_user ON group_user(user_id);`,

	// 分组↔规则组关联（多对多；global 规则组无需在此关联）。
	`CREATE TABLE IF NOT EXISTS group_rulegroup (
		group_id      INTEGER NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
		rule_group_id INTEGER NOT NULL REFERENCES rule_group(id) ON DELETE CASCADE,
		PRIMARY KEY (group_id, rule_group_id)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_grp_rg_rg ON group_rulegroup(rule_group_id);`,

	// 流量聚合桶（分钟级唯一粒度）：复合主键即唯一约束，支撑 upsert 累加与按时间清理。
	`CREATE TABLE IF NOT EXISTS traffic_stat (
		group_id    INTEGER NOT NULL,
		user_id     INTEGER NOT NULL,
		bucket_time TEXT    NOT NULL,
		up_bytes    INTEGER NOT NULL DEFAULT 0,
		down_bytes  INTEGER NOT NULL DEFAULT 0,
		req_count   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (group_id, user_id, bucket_time)
	);`,
	// 按时间的辅助索引：清理过期行与时间窗口聚合查询（仪表盘 1h/24h/7d）走它。
	`CREATE INDEX IF NOT EXISTS idx_stat_bucket ON traffic_stat(bucket_time);`,
}

// seedSystemSetting 确保系统设置单行存在（首次建库时插入默认行；已存在则忽略）。
const seedSystemSetting = `INSERT OR IGNORE INTO system_setting (id) VALUES (1);`
