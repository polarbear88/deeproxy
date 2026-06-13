package store

import (
	"database/sql"
	"fmt"
	"time"
)

// traffic_stat_repo.go 提供分钟级流量聚合桶的批量 upsert（累加）、按保留期清理、
// 以及仪表盘时间窗口聚合查询（含 7d 查询期降采样）。
//
// 设计要点（M3 定稿）：
//   - 唯一存储粒度 = 分钟桶（bucketTime 截断到分钟），维度 = group_id + user_id。
//   - flush worker 把内存计数批量累加进桶（ON CONFLICT DO UPDATE 累加），经单写协程串行。
//   - 7d 等长窗口在查询期用 strftime 聚成小时，避免双写两套桶。

// TruncateToMinute 把时间截断到分钟（UTC），统一桶时间生成入口（DRY）。
func TruncateToMinute(t time.Time) time.Time {
	return t.UTC().Truncate(time.Minute)
}

// StatDelta 是一次 flush 中针对某 (group,user,minuteBucket) 的增量。
type StatDelta struct {
	GroupID    int64
	UserID     int64
	BucketTime time.Time // 已截断到分钟
	UpBytes    int64
	DownBytes  int64
	ReqCount   int64
}

// FlushTrafficStats 把一批增量批量累加进聚合桶（单事务，经单写协程串行 AC-12/14）。
//
// 用 ON CONFLICT(group_id,user_id,bucket_time) DO UPDATE 把字节/请求数累加到已有桶，
// 桶不存在则插入。整批放一个事务，减少 WAL 提交次数、保证原子。
func (s *Store) FlushTrafficStats(deltas []StatDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	return s.WriteTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`
			INSERT INTO traffic_stat (group_id, user_id, bucket_time, up_bytes, down_bytes, req_count)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(group_id, user_id, bucket_time) DO UPDATE SET
				up_bytes   = up_bytes   + excluded.up_bytes,
				down_bytes = down_bytes + excluded.down_bytes,
				req_count  = req_count  + excluded.req_count`)
		if err != nil {
			return fmt.Errorf("准备聚合桶 upsert 失败: %w", err)
		}
		defer stmt.Close()

		for _, d := range deltas {
			if _, err := stmt.Exec(
				d.GroupID, d.UserID, fmtTime(TruncateToMinute(d.BucketTime)),
				d.UpBytes, d.DownBytes, d.ReqCount,
			); err != nil {
				return fmt.Errorf("写入聚合桶失败: %w", err)
			}
		}
		return nil
	})
}

// CleanupBefore 删除 bucket_time 早于 cutoff 的全部聚合桶行（保留期清理 AC-13）。
// 返回删除行数，便于清理 worker 记日志。
func (s *Store) CleanupBefore(cutoff time.Time) (int64, error) {
	var affected int64
	err := s.Write(func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM traffic_stat WHERE bucket_time < ?`, fmtTime(cutoff.UTC()))
		if err != nil {
			return fmt.Errorf("清理过期聚合桶失败: %w", err)
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	return affected, err
}

// StatPoint 是聚合查询返回的一个时间点（用于仪表盘时序图）。
type StatPoint struct {
	Bucket    string // 时间标签（分钟桶 RFC3339 或降采样后的 "YYYY-MM-DD HH"）
	UpBytes   int64
	DownBytes int64
	ReqCount  int64
}

// QueryTimeSeries 查询 [start,end) 窗口内的时序聚合。
//
// downsampleHour=true 时按小时降采样（7d 窗口用，GROUP BY strftime 聚成小时）；
// false 时返回分钟桶原始粒度（1h/24h 窗口用）。
// groupID<=0 表示不限分组（全局汇总）；userID<=0 表示不限用户。
func (s *Store) QueryTimeSeries(start, end time.Time, groupID, userID int64, downsampleHour bool) ([]StatPoint, error) {
	// 选择时间分组表达式：小时降采样 vs 分钟原粒度。
	bucketExpr := "bucket_time"
	if downsampleHour {
		// strftime 把 RFC3339 文本聚成 "YYYY-MM-DD HH" 小时标签。
		bucketExpr = `strftime('%Y-%m-%d %H', bucket_time)`
	}

	// 动态拼维度过滤条件（参数化，避免注入）。
	where := "bucket_time >= ? AND bucket_time < ?"
	args := []any{fmtTime(start.UTC()), fmtTime(end.UTC())}
	if groupID > 0 {
		where += " AND group_id = ?"
		args = append(args, groupID)
	}
	if userID > 0 {
		where += " AND user_id = ?"
		args = append(args, userID)
	}

	query := fmt.Sprintf(`
		SELECT %s AS bucket,
		       SUM(up_bytes)   AS up_bytes,
		       SUM(down_bytes) AS down_bytes,
		       SUM(req_count)  AS req_count
		FROM traffic_stat
		WHERE %s
		GROUP BY bucket
		ORDER BY bucket`, bucketExpr, where)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询流量时序失败: %w", err)
	}
	defer rows.Close()

	var points []StatPoint
	for rows.Next() {
		var p StatPoint
		if err := rows.Scan(&p.Bucket, &p.UpBytes, &p.DownBytes, &p.ReqCount); err != nil {
			return nil, fmt.Errorf("扫描流量时序失败: %w", err)
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// StatTotals 是某时间窗口内的汇总值（用于仪表盘“今日总流量/请求数”等卡片）。
type StatTotals struct {
	UpBytes   int64
	DownBytes int64
	ReqCount  int64
}

// QueryTotals 汇总 [start,end) 窗口内的总流量与请求数。
// groupID<=0 / userID<=0 表示该维度不过滤。
func (s *Store) QueryTotals(start, end time.Time, groupID, userID int64) (StatTotals, error) {
	where := "bucket_time >= ? AND bucket_time < ?"
	args := []any{fmtTime(start.UTC()), fmtTime(end.UTC())}
	if groupID > 0 {
		where += " AND group_id = ?"
		args = append(args, groupID)
	}
	if userID > 0 {
		where += " AND user_id = ?"
		args = append(args, userID)
	}

	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(up_bytes),0), COALESCE(SUM(down_bytes),0), COALESCE(SUM(req_count),0)
		FROM traffic_stat WHERE %s`, where)

	var t StatTotals
	if err := s.db.QueryRow(query, args...).Scan(&t.UpBytes, &t.DownBytes, &t.ReqCount); err != nil {
		return t, fmt.Errorf("查询流量汇总失败: %w", err)
	}
	return t, nil
}

// TopUserStat 是 Top N 用户流量排行项（仪表盘 Top 排行榜 kind=user）。
type TopUserStat struct {
	UserID    int64
	UpBytes   int64
	DownBytes int64
	ReqCount  int64
}

// QueryTopUsers 按总流量（上+下行）降序返回 Top N 代理用户。
// 与 QueryTopGroups 同构，仅聚合维度换为 user_id（traffic_stat 已含 user_id 维度）。
func (s *Store) QueryTopUsers(start, end time.Time, limit int) ([]TopUserStat, error) {
	rows, err := s.db.Query(`
		SELECT user_id, COALESCE(SUM(up_bytes),0), COALESCE(SUM(down_bytes),0), COALESCE(SUM(req_count),0)
		FROM traffic_stat
		WHERE bucket_time >= ? AND bucket_time < ?
		GROUP BY user_id
		ORDER BY (SUM(up_bytes)+SUM(down_bytes)) DESC
		LIMIT ?`, fmtTime(start.UTC()), fmtTime(end.UTC()), limit)
	if err != nil {
		return nil, fmt.Errorf("查询 Top 用户失败: %w", err)
	}
	defer rows.Close()

	var list []TopUserStat
	for rows.Next() {
		var t TopUserStat
		if err := rows.Scan(&t.UserID, &t.UpBytes, &t.DownBytes, &t.ReqCount); err != nil {
			return nil, fmt.Errorf("扫描 Top 用户失败: %w", err)
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// TopGroupStat 是 Top N 分组流量排行项（仪表盘 Top 排行榜 kind=group）。
type TopGroupStat struct {
	GroupID   int64
	UpBytes   int64
	DownBytes int64
	ReqCount  int64
}

// QueryTopGroups 按总流量（上+下行）降序返回 Top N 分组。
func (s *Store) QueryTopGroups(start, end time.Time, limit int) ([]TopGroupStat, error) {
	rows, err := s.db.Query(`
		SELECT group_id, COALESCE(SUM(up_bytes),0), COALESCE(SUM(down_bytes),0), COALESCE(SUM(req_count),0)
		FROM traffic_stat
		WHERE bucket_time >= ? AND bucket_time < ?
		GROUP BY group_id
		ORDER BY (SUM(up_bytes)+SUM(down_bytes)) DESC
		LIMIT ?`, fmtTime(start.UTC()), fmtTime(end.UTC()), limit)
	if err != nil {
		return nil, fmt.Errorf("查询 Top 分组失败: %w", err)
	}
	defer rows.Close()

	var list []TopGroupStat
	for rows.Next() {
		var t TopGroupStat
		if err := rows.Scan(&t.GroupID, &t.UpBytes, &t.DownBytes, &t.ReqCount); err != nil {
			return nil, fmt.Errorf("扫描 Top 分组失败: %w", err)
		}
		list = append(list, t)
	}
	return list, rows.Err()
}
