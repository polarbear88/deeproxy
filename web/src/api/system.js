// 系统设置 API（AC-31/37/40）。
//
// 后端 settings 字段：
//   { adminUser, statRetentionDays, serverAddr, probePoolSize,
//     hcDefaults:{mode,url,intervalSec,failThreshold,recoverThreshold},
//     defaultAction, logLevel, idleTimeoutSec, sniffDomain, sniffTimeoutMs, socks5Port? }
// 其中后 5 项为运行期动态设置（取消配置文件后迁入系统设置，可后台热改）。
// serverAddr：服务器域名/IP（连接提示文案用）；probePoolSize：健康检查协程池大小（默认150）。
import request from './request'

export function getSettings() {
  return request.get('/settings')
}

// 服务器连接信息（专用端点，供"复制代理地址"等连接提示使用）。
// 返回 { serverAddr, socks5Port, webPort }（camelCase）。
export function getServerInfo() {
  return request.get('/settings/server-info')
}

// 更新系统设置。payload 可含：statRetentionDays、serverAddr、probePoolSize、hcDefaults:{...}、
// 以及运行期动态项 defaultAction/logLevel/idleTimeoutSec/sniffDomain/sniffTimeoutMs。
export function updateSettings(payload) {
  return request.put('/settings', payload)
}

// 修改管理员密码（AC-40，校验旧密码）。payload: { oldPassword, newPassword }
export function changeAdminPassword(payload) {
  return request.post('/settings/admin-password', payload)
}

// 导出配置 JSON（AC-37），带 schemaVersion
export function exportConfig() {
  return request.get('/settings/export')
}

// 导入配置 JSON（AC-37）。payload: { schemaVersion, data, strategy:'overwrite' }
export function importConfig(payload) {
  return request.post('/settings/import', payload)
}
