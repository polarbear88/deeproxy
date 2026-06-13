// 主题 store：管理暗/亮模式，持久化到 localStorage，并同步给 ECharts 主题。
import { defineStore } from 'pinia'
import { ref } from 'vue'

const STORAGE_KEY = 'deeproxy-theme'

export const useThemeStore = defineStore('theme', () => {
  // 当前是否为暗色模式
  const isDark = ref(localStorage.getItem(STORAGE_KEY) === 'dark')

  // 把当前主题应用到 <html>：Element Plus 暗色方案约定加 .dark 类
  function applyTheme() {
    const root = document.documentElement
    if (isDark.value) {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
  }

  // 切换主题并持久化
  function toggle() {
    isDark.value = !isDark.value
    localStorage.setItem(STORAGE_KEY, isDark.value ? 'dark' : 'light')
    applyTheme()
  }

  // ECharts 主题名：暗色用 'dark'，亮色用 null（默认）
  function echartsTheme() {
    return isDark.value ? 'dark' : null
  }

  return { isDark, applyTheme, toggle, echartsTheme }
})
