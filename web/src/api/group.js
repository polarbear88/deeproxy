// 代理组(Group) 与上游代理(UpstreamProxy) 管理 API（AC-21/28/38）。已与 T7 实测契约对齐。
//
// 后端为 camelCase DTO：
//   Group: { id,name,remark,type,healthCheck:{enabled,mode,url,intervalSec,failThreshold,recoverThreshold},todayUp,todayDown,todayReq }
//   Upstream: { id,host,port,user,pwd,weight,enabled,healthState:'healthy'|'unhealthy'|'unknown',latencyMs }
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

// 分页列出某组上游（AC-3.3）。params: { page, pageSize, keyword?, healthState? }
// 返回 { items:[Upstream], total:number }。后端按 SQL LIMIT/OFFSET + 筛选。
export function listUpstreams(groupId, params) {
  return request.get(`/groups/${groupId}/upstreams`, { params })
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

// ===== 批量操作（AC-3.1/3.4）=====

// 批量添加上游（AC-3.1/3.2）。后端真实契约：请求 { lines:[string,...] }（每行一条，
// 逐行容错）；亦兼容 { text:"多行" }，但 lines 优先。
// 返回 { ok:int, failed:[{ line:int, reason:string }] }（ok=成功数，failed=失败明细）。
export function batchAddUpstreams(groupId, lines) {
  return request.post(`/groups/${groupId}/upstreams/batch`, { lines })
}

// 批量设置权重/启用状态（AC-3.4）。后端契约为扁平结构：
//   mode: "filter" | "ids"
//   field: "weight" | "enabled"  指定本次修改的字段
//   ids?: [int64]                 mode=ids 时的目标 id 列表
//   keyword?/healthState?         mode=filter 时的筛选条件（跨页全选）
//   weight?/enabled?              对应 field 的新值
// 返回 { affected }。
export function bulkUpdateUpstreams(groupId, payload) {
  return request.post(`/groups/${groupId}/upstreams/bulk`, payload)
}

// 批量删除上游。后端契约：请求 { ids:[int64] } 或 { filter:{ keyword?, healthState? } }
//   ids 非空 → 按 id 列表删除；否则按 filter 删除当前分组匹配项（跨页全选）。返回 { affected }。
export function bulkDeleteUpstreams(groupId, payload) {
  return request.post(`/groups/${groupId}/upstreams/bulk-delete`, payload)
}
