// 代理用户(ProxyUser) 管理 API（AC-23/30）。已与 T7 实测契约对齐。
//
// 后端 camelCase：ProxyUser { id, username, remark, groupIds }（授权 user→groups 方向支持）。
import request from './request'

export function listUsers() {
  return request.get('/proxy-users')
}

// 创建用户。payload: { username, password, remark?, groupIds? }
export function createUser(payload) {
  return request.post('/proxy-users', payload)
}

// 更新用户。payload: { username, password?, remark?, groupIds? }（password 空=不改）
export function updateUser(id, payload) {
  return request.put(`/proxy-users/${id}`, payload)
}

export function deleteUser(id) {
  return request.delete(`/proxy-users/${id}`)
}

// 设置某用户的授权分组（用户维度，覆盖式）AC-30
export function setUserGroups(id, groupIds) {
  return request.post(`/proxy-users/${id}/groups`, { groupIds })
}
