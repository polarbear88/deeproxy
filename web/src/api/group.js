// 代理组(Group) 与上游代理(UpstreamProxy) 管理 API（AC-21/28/38）。已与 T7 实测契约对齐。
//
// 后端为 camelCase DTO：
//   Group: { id,name,remark,type,healthCheck:{enabled,mode,url,intervalSec,failThreshold,recoverThreshold},todayUp,todayDown,todayReq }
//   Upstream: { id,host,port,user,usernameTemplate,pwd,weight,enabled,healthState:'healthy'|'unhealthy'|'unknown',latencyMs }
import request from './request'

export function listGroups() {
  return request.get('/groups')
}

export function createGroup(payload) {
  return request.post('/groups', payload)
}

export function updateGroup(id, payload) {
  return request.put(`/groups/${id}`, payload)
}

export function deleteGroup(id) {
  return request.delete(`/groups/${id}`)
}

// ===== Type B 组下的上游代理（嵌套路径）=====

export function listUpstreams(groupId) {
  return request.get(`/groups/${groupId}/upstreams`)
}

export function createUpstream(groupId, payload) {
  return request.post(`/groups/${groupId}/upstreams`, payload)
}

export function updateUpstream(groupId, upstreamId, payload) {
  return request.put(`/groups/${groupId}/upstreams/${upstreamId}`, payload)
}

export function deleteUpstream(groupId, upstreamId) {
  return request.delete(`/groups/${groupId}/upstreams/${upstreamId}`)
}

// 手动启用/禁用单条上游（AC-18）
export function toggleUpstream(groupId, upstreamId, enabled) {
  return request.post(`/groups/${groupId}/upstreams/${upstreamId}/toggle`, { enabled })
}

// 测试连接：立即探测单条上游 → { ok, latencyMs, error? }（AC-38）
export function testUpstream(groupId, upstreamId) {
  return request.post(`/groups/${groupId}/upstreams/${upstreamId}/test`)
}
