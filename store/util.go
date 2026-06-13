package store

import "time"

// 时间存取约定（DRY）：
// SQLite 列以 TEXT 存 RFC3339Nano（UTC），repo 层统一用以下两个函数格式化/解析，
// 避免 modernc 驱动对 time.Time 的隐式处理差异，也便于跨平台一致排序。

// fmtTime 把 time.Time 格式化为入库字符串（UTC + RFC3339Nano）。
func fmtTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTime 解析入库字符串为 time.Time；空串或解析失败返回零值。
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// now 返回当前 UTC 时间，集中一处便于测试替换与一致性。
func now() time.Time {
	return time.Now().UTC()
}

// boolToInt 把布尔转 0/1（SQLite 无原生 bool）。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
