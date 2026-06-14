// 仪表盘聚合 API（AC-24/27）。已与 T7 实测契约对齐。
import request from './request'

// 仪表盘概览(扁平)：{ upRate,downRate,activeConns,todayUp,todayDown,todayReq,
//   todayRejectRule,todayRejectAuth,healthyProxies,totalProxies,uptimeSec }
export function getOverview() {
  return request.get('/dashboard/overview')
}

// 时间序列：{ times:[], up:[], down:[], req:[] }。window: '1h'|'24h'|'7d'；groupId 可选
export function getTimeseries(params) {
  return request.get('/dashboard/timeseries', { params })
}

// 动作分布饼图：[{ name:'forward', value }]。window 可选
export function getActionDist(params) {
  return request.get('/dashboard/action-dist', { params })
}

// Top N 排行：kind: 'group'|'user'|'domain'
export function getTopN(params) {
  // params: { kind, limit, window? }
  //   - group/user：返回 [{ name, bytes }]
  //   - domain：返回 [{ name, count }]，可选 groupId 过滤（缺省=全局，传 groupId 则仅该分组）
  return request.get('/dashboard/top', { params })
}

// 运行健康区：{ memMB, goroutines, groups:[{id,name,healthy,total,allDown}] }
export function getRuntimeHealth() {
  return request.get('/dashboard/runtime')
}
