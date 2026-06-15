// 系统日志 API（AC-33/34/35）。日志仅内存环形缓冲，SSE 实时推送，按级别筛选。
// 连接审计日志（AC-36）也走内存环形缓冲。已与 T7 实测契约对齐。
//
// SSE：后端用默认(无名)事件 sse.Encode(Data:entry)，故前端用 onmessage 接收。
import request from './request'

// 拉取当前缓冲区内的日志快照（首屏初始化用），支持级别筛选 → [{ time, level, message, fields }]
export function getLogs(params) {
  return request.get('/syslog', { params })
}

// 拉取连接审计日志（AC-36），服务端分页 + 四维筛选。
// params: { page, pageSize, user, target, action, group }（空参数=该维不筛）。
// 返回 { items: [{ time, user, group, target, action, upstream, upBytes, downBytes }], total, page, pageSize }，
// items 已按最新→最旧排序。分页放服务端：前端每次只拿一页，避免一次性渲染大量记录卡顿。
export function getAuditLogs(params) {
  return request.get('/syslog/audit', { params })
}

// 建立系统日志 SSE 实时流。返回 EventSource，调用方用 onmessage 接收、负责 close。
export function openLogStream(level) {
  const qs = level ? `?level=${encodeURIComponent(level)}` : ''
  return new EventSource(`/api/syslog/stream${qs}`, { withCredentials: true })
}
