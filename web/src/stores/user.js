// 会话 store：管理后台管理员登录态。
// 后端会话用 HttpOnly Cookie（计划 D2-A），前端无法读 Cookie，
// 故这里只保存「是否已登录」与「管理员名」的轻量标记，权威以接口 401 为准。
import { defineStore } from 'pinia'
import { ref } from 'vue'
import * as authApi from '@/api/auth'

const FLAG_KEY = 'deeproxy-logged-in'

export const useUserStore = defineStore('user', () => {
  // 是否已登录（轻量标记，仅用于前端路由守卫的快速判断）
  const loggedIn = ref(localStorage.getItem(FLAG_KEY) === '1')
  // 管理员用户名
  const username = ref('')
  // 系统是否已完成首次初始化（设置过管理员账密）
  const initialized = ref(true)

  // 标记登录成功
  function setLoggedIn(name) {
    loggedIn.value = true
    username.value = name || ''
    localStorage.setItem(FLAG_KEY, '1')
  }

  // 清除登录态（登出或会话失效）
  function clear() {
    loggedIn.value = false
    username.value = ''
    localStorage.removeItem(FLAG_KEY)
  }

  // 查询系统初始化状态（是否需要跳首次设置页）。后端返回 { initialized }。
  async function fetchInitStatus() {
    const data = await authApi.getInitStatus()
    initialized.value = !!data.initialized
    return initialized.value
  }

  // 登录
  async function login(payload) {
    const data = await authApi.login(payload)
    setLoggedIn(payload.username)
    return data
  }

  // 首次设置管理员账密
  async function setup(payload) {
    const data = await authApi.setup(payload)
    initialized.value = true
    return data
  }

  // 登出
  async function logout() {
    try {
      await authApi.logout()
    } finally {
      clear()
    }
  }

  return {
    loggedIn,
    username,
    initialized,
    setLoggedIn,
    clear,
    fetchInitStatus,
    login,
    setup,
    logout,
  }
})
