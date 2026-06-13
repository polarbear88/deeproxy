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
