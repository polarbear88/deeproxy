// 通用格式化工具（DRY：流量/速率/时间在多个页面复用）。

// 把字节数格式化为带单位的可读字符串，如 1536 → "1.50 KB"
export function formatBytes(bytes) {
  const n = Number(bytes) || 0
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let val = n / 1024
  let i = 0
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024
    i++
  }
  return `${val.toFixed(2)} ${units[i]}`
}

// 把字节/秒格式化为速率字符串，如 2048 → "2.00 KB/s"
export function formatRate(bytesPerSec) {
  return `${formatBytes(bytesPerSec)}/s`
}

// 把秒数格式化为运行时长，如 3661 → "1h 1m 1s"
export function formatUptime(seconds) {
  let s = Math.floor(Number(seconds) || 0)
  const d = Math.floor(s / 86400)
  s %= 86400
  const h = Math.floor(s / 3600)
  s %= 3600
  const m = Math.floor(s / 60)
  s %= 60
  const parts = []
  if (d) parts.push(`${d}d`)
  if (h || d) parts.push(`${h}h`)
  parts.push(`${m}m`)
  parts.push(`${s}s`)
  return parts.join(' ')
}

// 把时间戳（毫秒或 ISO）格式化为 "YYYY-MM-DD HH:mm:ss"
export function formatTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return String(ts)
  const pad = (x) => String(x).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  )
}

// 数字千分位
export function formatNumber(n) {
  return (Number(n) || 0).toLocaleString('en-US')
}

// 把时序图后端返回的「桶时间标签」格式化为 "YYYY/MM/DD HH:mm:ss"（供图表 tooltip 用）。
//
// 后端 times 有两种文本形态（见 store/traffic_stat_repo.go）：
//   - 1h/24h 原粒度：RFC3339，如 "2026-06-14T12:34:00Z"
//   - 7d 小时降采样：strftime 标签，如 "2026-06-14 12"（无分秒）
// 这里统一解析为本地时间并按 2000/01/01 00:00:00 的样式输出；
// 解析失败则原样返回，保证 tooltip 不至于显示空白。
export function formatBucketTime(label) {
  if (!label) return '-'
  const s = String(label)
  // 7d 小时桶 "YYYY-MM-DD HH"：缺分秒，补成完整时间再交给 Date 解析（按本地时区）。
  const hourBucket = /^(\d{4})-(\d{2})-(\d{2}) (\d{2})$/.exec(s)
  const d = hourBucket ? new Date(`${hourBucket[1]}-${hourBucket[2]}-${hourBucket[3]}T${hourBucket[4]}:00:00`) : new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const pad = (x) => String(x).padStart(2, '0')
  return (
    `${d.getFullYear()}/${pad(d.getMonth() + 1)}/${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  )
}

// 把桶时间标签格式化为 X 轴底部用的紧凑样式 "MM/DD HH:mm"。
// 与 formatBucketTime 复用同一套解析逻辑（RFC3339 / "YYYY-MM-DD HH" 两种形态），
// 但省去年份与秒，避免 X 轴标签过长重叠；解析失败原样返回，保证轴不至于空白。
export function formatAxisTime(label) {
  if (!label) return ''
  const s = String(label)
  const hourBucket = /^(\d{4})-(\d{2})-(\d{2}) (\d{2})$/.exec(s)
  const d = hourBucket ? new Date(`${hourBucket[1]}-${hourBucket[2]}-${hourBucket[3]}T${hourBucket[4]}:00:00`) : new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const pad = (x) => String(x).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
