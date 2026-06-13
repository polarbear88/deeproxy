// 主题 store：管理暗/亮模式，默认「跟随系统」并随系统外观实时切换；
// 用户手动切换后记为显式偏好并持久化到 localStorage，同时同步给 ECharts 主题。
import { defineStore } from 'pinia'
import { ref } from 'vue'

const STORAGE_KEY = 'deeproxy-theme'

// 系统是否处于暗色：基于 prefers-color-scheme 媒体查询
// 注意：部分老旧环境可能没有 matchMedia，做一次防御性判断，缺失时按亮色处理
const darkMediaQuery =
  typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia('(prefers-color-scheme: dark)')
    : null

function systemPrefersDark() {
  return darkMediaQuery ? darkMediaQuery.matches : false
}

export const useThemeStore = defineStore('theme', () => {
  // 持久化的显式偏好：'dark' | 'light'，为 null 表示「跟随系统」（默认）
  const stored = localStorage.getItem(STORAGE_KEY)
  const preference = ref(stored === 'dark' || stored === 'light' ? stored : null)

  // 当前实际是否为暗色：有显式偏好时按偏好，否则跟随系统
  const isDark = ref(preference.value ? preference.value === 'dark' : systemPrefersDark())

  // 把当前主题应用到 <html>：Element Plus 暗色方案约定加 .dark 类
  function applyTheme() {
    const root = document.documentElement
    if (isDark.value) {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
  }

  // 系统外观变化时，仅在「跟随系统」模式下实时切换主题
  if (darkMediaQuery) {
    darkMediaQuery.addEventListener('change', (e) => {
      if (preference.value === null) {
        isDark.value = e.matches
        applyTheme()
      }
    })
  }

  // 切换主题：手动切换记为显式偏好并持久化，从此不再自动跟随系统
  function toggle() {
    isDark.value = !isDark.value
    preference.value = isDark.value ? 'dark' : 'light'
    localStorage.setItem(STORAGE_KEY, preference.value)
    applyTheme()
  }

  // 清除显式偏好，恢复「跟随系统」并立即按当前系统外观刷新
  function followSystem() {
    preference.value = null
    localStorage.removeItem(STORAGE_KEY)
    isDark.value = systemPrefersDark()
    applyTheme()
  }

  // ECharts 主题名：暗色用 'dark'，亮色用 null（默认）
  function echartsTheme() {
    return isDark.value ? 'dark' : null
  }

  return { isDark, preference, applyTheme, toggle, followSystem, echartsTheme }
})
