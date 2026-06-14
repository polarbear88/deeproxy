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
		// 对旧库做幂等列迁移（新增列）。必须在 seed 之后，保证 system_setting 行已存在。
		if err := migrateColumns(db); err != nil {
			return fmt.Errorf("列迁移失败: %w", err)
		}
		return nil
	})
}

// columnMigration 描述一条「为已有表新增列」的幂等迁移。
type columnMigration struct {
	table  string // 目标表名
	column string // 列名
	ddl    string // 完整的 ALTER TABLE ... ADD COLUMN 语句
}

// pendingColumnMigrations 是 v2 批量增强需要为旧库补充的列。
//
// 为什么需要这一层：原 migrate() 只跑 CREATE TABLE IF NOT EXISTS，对已存在的旧库
// 不会补新列；而裸 ALTER TABLE ADD COLUMN 在列已存在时（二次启动）会直接报错。
// 故先用 columnExists 守卫，再幂等执行；逐列独立，单列失败不影响其余列（下次重启续加）。
var pendingColumnMigrations = []columnMigration{
	// proxy_user.all_groups：用户级「授权全部分组」通配标志（DEC-B1）。
	{table: "proxy_user", column: "all_groups",
		ddl: `ALTER TABLE proxy_user ADD COLUMN all_groups INTEGER NOT NULL DEFAULT 0`},
	// system_setting.server_addr：后台展示用的服务器域名/IP（仅作连接示例文案，非绑定地址）。
	{table: "system_setting", column: "server_addr",
		ddl: `ALTER TABLE system_setting ADD COLUMN server_addr TEXT NOT NULL DEFAULT ''`},
	// system_setting.probe_pool_size：健康检查全局协程池大小（DEC-C1，默认 150）。
	{table: "system_setting", column: "probe_pool_size",
		ddl: `ALTER TABLE system_setting ADD COLUMN probe_pool_size INTEGER NOT NULL DEFAULT 150`},
}

// migrateColumns 逐条幂等地为旧库补齐新增列。
//
// 在单写协程内执行（由 migrate 调用），与运行期写串行，无并发问题。
func migrateColumns(db *sql.DB) error {
	for _, m := range pendingColumnMigrations {
		exists, err := columnExists(db, m.table, m.column)
		if err != nil {
			return fmt.Errorf("检查列 %s.%s 是否存在失败: %w", m.table, m.column, err)
		}
		if exists {
			continue // 已存在则跳过，保证幂等（二次启动不报错）
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("新增列 %s.%s 失败: %w\nSQL: %s", m.table, m.column, err, m.ddl)
		}
	}
	return nil
}

// columnExists 通过 PRAGMA table_info 判断指定表是否已含某列。
//
// 为什么用 PRAGMA 而非捕获 ALTER 报错：PRAGMA table_info 是 SQLite 标准内省方式，
// 干净、可移植；裸跑 ADD COLUMN 靠捕获报错来判重则脆弱（错误文案随版本变化）。
// table 名直接拼入 PRAGMA（PRAGMA 不接受占位参数），调用方只传内部常量、无注入风险。
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return false, fmt.Errorf("读取表信息失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		// PRAGMA table_info 返回列：cid, name, type, notnull, dflt_value, pk。
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("扫描表信息失败: %w", err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
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
		server_addr         TEXT    NOT NULL DEFAULT '',
		probe_pool_size     INTEGER NOT NULL DEFAULT 150,
		updated_at          TEXT    NOT NULL DEFAULT ''
	);`,

	// 代理用户：连 SOCKS5 代理的身份（不能登录后台）。pwd 为明文连接密码（非哈希）。
	`CREATE TABLE IF NOT EXISTS proxy_user (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT    NOT NULL UNIQUE,
		pwd        TEXT    NOT NULL,
		remark     TEXT    NOT NULL DEFAULT '',
		all_groups INTEGER NOT NULL DEFAULT 0,
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

	// 上游代理：仅属于 Type B 分组；含权重、启停、健康状态。
	// user 即用户名（本身可含 {var} 占位，运行期由客户端尾段变量替换；不含占位时为定值）。
	// 设计决定：早期版本曾有独立 username_template 列，现已统一并入 user 字段。因本项目无存量旧库，
	// 故【不提供 ALTER TABLE DROP COLUMN 降级迁移】——建表语句直接采用新结构即可；
	// 若未来出现遗留旧库需清理该死列，再补幂等的 DROP COLUMN 迁移（SQLite 3.35+）。
	`CREATE TABLE IF NOT EXISTS upstream_proxy (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id          INTEGER NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
		host              TEXT    NOT NULL,
		port              INTEGER NOT NULL,
		user              TEXT    NOT NULL DEFAULT '',
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

	// 目标域名命中聚合桶（Top 目标域名特性）：与 traffic_stat 同构的分钟桶模型。
	//   - key = 完整主机名（含子域，www.x.com 与 mail.x.com 分开计数）；纯 IP 目标也作为 key 计入。
	//   - group_id 维度支撑「全局 Top」(不过滤) 与「分组 Top」(group_id 过滤) 两种查询。
	//   - (domain,group_id,bucket_time) 复合主键即唯一约束，支撑 upsert 累加与按保留期清理。
	`CREATE TABLE IF NOT EXISTS domain_hit (
		domain      TEXT    NOT NULL,
		group_id    INTEGER NOT NULL,
		bucket_time TEXT    NOT NULL,
		hit_count   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (domain, group_id, bucket_time)
	);`,
	// 按时间的辅助索引：保留期清理与时间窗口 Top 聚合查询走它（与 idx_stat_bucket 同理）。
	`CREATE INDEX IF NOT EXISTS idx_domain_hit_bucket ON domain_hit(bucket_time);`,
}

// seedSystemSetting 确保系统设置单行存在（首次建库时插入默认行；已存在则忽略）。
const seedSystemSetting = `INSERT OR IGNORE INTO system_setting (id) VALUES (1);`
