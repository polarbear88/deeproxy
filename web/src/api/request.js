// axios 实例封装：统一 baseURL、超时、错误处理与会话失效跳转。
// 后端会话用 HttpOnly Cookie（deeproxy_admin），故启用 withCredentials 让浏览器带上 Cookie。
//
// 后端契约（已与 T7 对齐）：
//   - 成功：直接返回数据体（或 {ok:true}），HTTP 200。
//   - 失败：HTTP 非 2xx + { msg: "中文说明" }。
//   - 无 {code,data} 包裹。
import axios from 'axios'
import { ElMessage } from 'element-plus'
import router from '@/router'
import { useUserStore } from '@/stores/user'

const request = axios.create({
  baseURL: '/api',
  timeout: 15000,
  withCredentials: true,
})

// 响应拦截：成功直接返回 data；失败统一提示并按 401 跳登录。
request.interceptors.response.use(
  (resp) => resp.data,
  (error) => {
    const status = error?.response?.status
    if (status === 401) {
      // 会话失效：清理本地标记并跳转登录。
      const userStore = useUserStore()
      userStore.clear()
      if (router.currentRoute.value.name !== 'login') {
        router.replace({ name: 'login' })
      }
      ElMessage.error('登录已失效，请重新登录')
    } else {
      // 后端错误体为 { msg: "..." }。
      const msg = error?.response?.data?.msg || error.message || '网络错误'
      ElMessage.error(msg)
    }
    return Promise.reject(error)
  },
)

export default request
