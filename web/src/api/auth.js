// 认证相关 API（首次设置 / 登录 / 登出 / 初始化状态）。已与 T7 实测契约对齐。
import request from './request'

// 查询系统是否已初始化 → { initialized: boolean }
export function getInitStatus() {
  return request.get('/auth/init-status')
}

// 首次设置管理员账号密码（AC-19）→ { ok:true }
export function setup(payload) {
  // payload: { username, password }
  return request.post('/auth/setup', payload)
}

// 管理员登录（AC-20），成功后后端 set-cookie 签发会话 → { ok:true }
export function login(payload) {
  // payload: { username, password }
  return request.post('/auth/login', payload)
}

// 登出，使会话失效
export function logout() {
  return request.post('/auth/logout')
}
