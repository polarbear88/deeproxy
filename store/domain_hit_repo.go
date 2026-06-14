package store

import (
	"database/sql"
	"fmt"
	"time"
)

// domain_hit_repo.go 提供「Top 目标域名」分钟级聚合桶的批量 upsert（累加）、
// 按保留期清理、以及按时间窗口的 Top N 聚合查询。
//
// 设计要点（镜像 traffic_stat_repo.go，但维度换为 domain + group_id）：
//   - 唯一存储粒度 = 分钟桶（bucketTime 截断到分钟），维度 = domain + group_id。
//   - flush worker 把内存命中计数批量累加进桶（ON CONFLICT DO UPDATE 累加），经单写协程串行。
//   - 复用未导出的 fmtTime / TruncateToMinute / WriteTx，保证 bucket_time 文本格式与
//     traffic_stat 完全一致（同一分钟桶才能正确 ON CONFLICT 碰撞）。

// DomainDelta 是一次 flush 中针对某 (domain,group,minuteBucket) 的命中增量。
type DomainDelta struct {
	Domain     string
	GroupID    int64
	BucketTime time.Time // 已截断到分钟
	HitCount   int64
}

// FlushDomainHits 把一批域名命中增量批量累加进聚合桶（单事务，经单写协程串行）。
//
// 用 ON CONFLICT(domain,group_id,bucket_time) DO UPDATE 把命中数累加到已有桶，
// 桶不存在则插入。空切片直接返回（防御性守卫，镜像 FlushTrafficStats）。
func (s *Store) FlushDomainHits(deltas []DomainDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	return s.WriteTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`
			INSERT INTO domain_hit (domain, group_id, bucket_time, hit_count)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(domain, group_id, bucket_time) DO UPDATE SET
				hit_count = hit_count + excluded.hit_count`)
		if err != nil {
			return fmt.Errorf("准备域名命中 upsert 失败: %w", err)
		}
		defer stmt.Close()

		for _, d := range deltas {
			if _, err := stmt.Exec(
				d.Domain, d.GroupID, fmtTime(TruncateToMinute(d.BucketTime)), d.HitCount,
			); err != nil {
				return fmt.Errorf("写入域名命中桶失败: %w", err)
			}
		}
		return nil
	})
}

// CleanupDomainHitsBefore 删除 bucket_time 早于 cutoff 的全部域名命中桶行（保留期清理）。
// 返回删除行数，便于清理 worker 记日志。DELETE 在无匹配行时天然 no-op。
func (s *Store) CleanupDomainHitsBefore(cutoff time.Time) (int64, error) {
	var affected int64
	err := s.Write(func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM domain_hit WHERE bucket_time < ?`, fmtTime(cutoff.UTC()))
		if err != nil {
			return fmt.Errorf("清理过期域名命中桶失败: %w", err)
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	return affected, err
}

// TopDomainStat 是 Top N 目标域名排行项（仪表盘/分组 kind=domain）。
type TopDomainStat struct {
	Domain   string
	HitCount int64
}

// QueryTopDomains 按命中次数降序返回 [start,end) 窗口内的 Top N 目标域名。
//
// groupID<=0 表示不过滤分组（全局合并所有分组的命中）——因真实 group_id 恒 ≥1
// （来自 group 表 AUTOINCREMENT、且鉴权阶段已拒绝未授权连接），故 0 是与任何真实
// 分组都不碰撞的「全局查询哨兵」；groupID>0 时仅统计该分组（参数化，避免注入）。
func (s *Store) QueryTopDomains(start, end time.Time, limit int, groupID int64) ([]TopDomainStat, error) {
	where := "bucket_time >= ? AND bucket_time < ?"
	args := []any{fmtTime(start.UTC()), fmtTime(end.UTC())}
	if groupID > 0 {
		where += " AND group_id = ?"
		args = append(args, groupID)
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT domain, COALESCE(SUM(hit_count),0) AS hits
		FROM domain_hit
		WHERE %s
		GROUP BY domain
		ORDER BY hits DESC
		LIMIT ?`, where)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 Top 域名失败: %w", err)
	}
	defer rows.Close()

	var list []TopDomainStat
	for rows.Next() {
		var t TopDomainStat
		if err := rows.Scan(&t.Domain, &t.HitCount); err != nil {
			return nil, fmt.Errorf("扫描 Top 域名失败: %w", err)
		}
		list = append(list, t)
	}
	return list, rows.Err()
}
