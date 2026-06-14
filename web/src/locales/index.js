// vue-i18n 实例：Composition API 模式（legacy:false），全局注入 $t/t。
// 这里独立计算「初始 locale」，与 stores/lang.js 的默认逻辑保持一致：
// 先看 localStorage('deeproxy-lang')，缺省按浏览器语言回退（zh / en）。
// 之所以在本文件内联一份初始判定，是为了避免 store ←→ i18n 的循环 import：
// store 需要 import 本实例去改 locale，故本实例不能反过来 import store。
import { createI18n } from 'vue-i18n'
import zh from './zh.js'
import en from './en.js'

// 语言持久化 key（与 stores/lang.js 的 STORAGE_KEY 必须一致）
const STORAGE_KEY = 'deeproxy-lang'

// 计算首屏初始语言：localStorage 优先，否则按 navigator.language 回退
function resolveInitialLocale() {
  const stored = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null
  if (stored === 'zh' || stored === 'en') return stored
  const nav = typeof navigator !== 'undefined' ? navigator.language || '' : ''
  return nav.startsWith('zh') ? 'zh' : 'en'
}

// fallbackLocale:'zh' —— 缺 key 时回退中文而非暴露原始 key（R-3/R-8）
const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: resolveInitialLocale(),
  fallbackLocale: 'zh',
  messages: { zh, en },
})

export default i18n
