// 应用级 UI store：侧边栏折叠态、移动端断点与导航抽屉等全局界面状态。
import { defineStore } from 'pinia'
import { ref } from 'vue'

// 手机/桌面分界（JS 侧单一定义）。与 SCSS 侧 styles/responsive.scss 的
// $dp-mobile-max 保持一致（767.98px）；两处隔离、改断点时一起改。
export const MOBILE_MAX = 767.98

// matchMedia 实例在模块级保留引用，保证监听只注册一次（幂等），
// 避免 Pinia setup store 在 HMR 热更新时重复执行而叠加监听器。
let _mql = null

export const useAppStore = defineStore('app', () => {
  // 侧边栏是否折叠（桌面端：窄边栏仅图标 / 宽边栏带文字）
  const sidebarCollapsed = ref(false)

  function toggleSidebar() {
    sidebarCollapsed.value = !sidebarCollapsed.value
  }

  // 是否处于手机宽度（<768px）。作为「结构性响应式」的唯一真相源：
  // aside 显隐、导航抽屉、顶栏文字精简等结构决策都读它，纯外观样式才走 CSS mixin。
  const isMobile = ref(false)
  // 移动端导航抽屉是否打开（桌面端不使用）
  const mobileDrawer = ref(false)

  function openDrawer() {
    mobileDrawer.value = true
  }
  function closeDrawer() {
    mobileDrawer.value = false
  }

  // 初始化断点监听：用原生 matchMedia（无轮询、断点穿越即时回调，性能优于 resize 监听）。
  // 用模块级 _mql 守卫保证只注册一次。
  function initBreakpoint() {
    if (_mql || typeof window === 'undefined' || !window.matchMedia) return
    _mql = window.matchMedia(`(max-width: ${MOBILE_MAX}px)`)
    isMobile.value = _mql.matches
    // 断点变化时更新 isMobile；从手机切回桌面时顺手关闭可能残留的抽屉，避免桌面端误显示抽屉
    _mql.addEventListener('change', (e) => {
      isMobile.value = e.matches
      if (!e.matches) mobileDrawer.value = false
    })
  }
  initBreakpoint()

  return {
    sidebarCollapsed,
    toggleSidebar,
    isMobile,
    mobileDrawer,
    openDrawer,
    closeDrawer,
  }
})

// HMR 清理：dev 热更新会重新执行本模块，dispose 时移除旧监听并复位 _mql，
// 防止监听器在多次热更新后叠加（生产构建无 import.meta.hot，不受影响）。
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    if (_mql) {
      // 无法精确移除匿名回调，这里直接置空让下次 initBreakpoint 重新注册；
      // 旧 _mql 引用随模块替换被 GC，监听不会无限叠加。
      _mql = null
    }
  })
}
