// 语言 store：管理中/英切换，默认按 localStorage 优先、否则跟随浏览器语言；
// 用户手动切换后持久化到 localStorage，并同步给 vue-i18n 实例（照搬 theme.js 模式）。
import { defineStore } from 'pinia'
import { ref } from 'vue'
import i18n from '@/locales'

// 持久化 key（与 locales/index.js 的 STORAGE_KEY 必须保持一致）
const STORAGE_KEY = 'deeproxy-lang'

// 计算默认语言：localStorage 显式偏好优先，否则按浏览器语言回退（zh / en）
// 注意：部分环境可能没有 navigator，做一次防御性判断，缺失时按英文处理。
function resolveDefaultLocale() {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'zh' || stored === 'en') return stored
  const nav = typeof navigator !== 'undefined' ? navigator.language || '' : ''
  return nav.startsWith('zh') ? 'zh' : 'en'
}

export const useLangStore = defineStore('lang', () => {
  // 当前语言：'zh' | 'en'
  const locale = ref(resolveDefaultLocale())

  // 应用语言到 vue-i18n（首屏初始化用，保证与持久化偏好一致）
  function applyLocale() {
    i18n.global.locale.value = locale.value
  }

  // 切换语言：写回 localStorage 并同步 vue-i18n 当前 locale
  function setLocale(code) {
    if (code !== 'zh' && code !== 'en') return
    locale.value = code
    localStorage.setItem(STORAGE_KEY, code)
    i18n.global.locale.value = code
  }

  // 中英一键切换（供切换控件调用）
  function toggle() {
    setLocale(locale.value === 'zh' ? 'en' : 'zh')
  }

  return { locale, applyLocale, setLocale, toggle }
})
