// 实时连接列表 API（US-005）。
// 后端契约：GET /api/connections?limit=500&sort=start|duration
//   响应：{ items: [...], total, limit, truncated }
//   axios 拦截器已 return resp.data，故调用方直接拿 { items, total, truncated }。
import request from './request'

// 获取当前活跃连接列表。
// params: { limit: number, sort: 'start' | 'duration' }
// 返回：{ items: ConnView[], total: number, limit: number, truncated: boolean }
export function getActiveConnections(params) {
  return request.get('/connections', { params })
}
