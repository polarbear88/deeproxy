// 代理用户(ProxyUser) 管理 API（AC-23/30）。已与 T7 实测契约对齐。
//
// 后端 camelCase：ProxyUser { id, username, remark, allGroups, groupIds }（授权 user→groups 方向支持）。
// allGroups：是否授权全部代理组（独立布尔标志，与 groupIds 精细授权并存，互不清空）。
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

// 设置某用户的授权分组（用户维度，覆盖式）AC-30 / AC-1.1。
// payload: { allGroups: bool, groupIds: [int64] }。
//   - allGroups=true 表示授权全部代理组（独立标志，永不清空精细 groupIds 行）；
//   - groupIds 为逐组精细授权集合；二者并存，鉴权时任一命中即放行。
export function setUserGroups(id, { allGroups = false, groupIds = [] } = {}) {
  return request.post(`/proxy-users/${id}/groups`, { allGroups, groupIds })
}
