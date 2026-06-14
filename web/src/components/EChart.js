// ECharts 封装组件：统一处理 init/resize/dispose 与暗亮主题切换（DRY）。
// 用法：<EChart :option="option" height="320px" />
import * as echarts from 'echarts'
import { useThemeStore } from '@/stores/theme'

// 自定义暗色主题名（区别于内置 'dark'，便于显式控制背景/文本/轴线配色）。
const DARK_THEME = 'deeproxy-dark'

// 注册暗色主题：背景设为透明（随 el-card 暗色背景走，避免图表出现突兀的纯黑块），
// 文本/轴线/分割线统一用暗色面板下可读的中性灰，保证与 Element Plus 暗色主题一致。
// 模块级只注册一次，避免每次创建实例重复注册（DRY）。
echarts.registerTheme(DARK_THEME, {
  // 关键：透明背景，让图表融入卡片暗色背景，而非内置 dark 的 #100C2A 深紫块
  backgroundColor: 'transparent',
  textStyle: { color: 'rgba(255,255,255,0.85)' },
  title: { textStyle: { color: 'rgba(255,255,255,0.92)' } },
  legend: { textStyle: { color: 'rgba(255,255,255,0.75)' } },
  // 折线/类目轴：轴线与刻度文本用中性灰，分割线降低对比避免刺眼
  categoryAxis: {
    axisLine: { lineStyle: { color: 'rgba(255,255,255,0.25)' } },
    axisTick: { lineStyle: { color: 'rgba(255,255,255,0.25)' } },
    axisLabel: { color: 'rgba(255,255,255,0.65)' },
    splitLine: { lineStyle: { color: 'rgba(255,255,255,0.08)' } },
  },
  valueAxis: {
    axisLine: { lineStyle: { color: 'rgba(255,255,255,0.25)' } },
    axisTick: { lineStyle: { color: 'rgba(255,255,255,0.25)' } },
    axisLabel: { color: 'rgba(255,255,255,0.65)' },
    splitLine: { lineStyle: { color: 'rgba(255,255,255,0.08)' } },
  },
})

export default {
  name: 'EChart',
  props: {
    // ECharts option 对象
    option: { type: Object, required: true },
    // 容器高度
    height: { type: String, default: '300px' },
  },
  setup(props) {
    const el = ref(null)
    let chart = null
    // 容器尺寸监听器：覆盖「视口不变但容器宽高变化」的场景，
    // 例如移动端导航抽屉开合、侧边栏显隐、内容抽屉(size 60%/90%)切换——
    // 这些都不触发 window.resize，仅靠 window 监听会让图表停留在旧/零尺寸。
    let ro = null
    const themeStore = useThemeStore()

    // 容器是否具备可见尺寸：keep-alive 隐藏页面时容器宽高为 0，
    // 此时 init/resize 会得到 0 尺寸的错乱画布，需跳过等返回页面再处理。
    function hasSize() {
      return !!el.value && el.value.clientWidth > 0 && el.value.clientHeight > 0
    }

    // 按当前主题创建实例（暗色用自定义 'deeproxy-dark' 主题）
    function createChart() {
      if (!el.value || !hasSize()) return
      if (chart) {
        chart.dispose()
        chart = null
      }
      chart = echarts.init(el.value, themeStore.echartsTheme() || undefined)
      chart.setOption(props.option || {})
    }

    function resize() {
      // 无尺寸时（隐藏态）不 resize，避免画布被压缩成 0 尺寸
      if (!hasSize()) return
      // 关键修复：容器从 0 尺寸变为可见时补建实例。
      // 抽屉/弹窗/标签页等场景下，组件 onMounted 时容器仍是 0 尺寸（抽屉滑入动画/display:none），
      // createChart 会被 hasSize 守卫跳过导致 chart 为 null；此后即使容器获得尺寸，
      // 若 resize 仅做 chart.resize()（chart 为 null 时是空操作），图表将永远不渲染。
      // 因此这里在 chart 缺失但容器已有尺寸时补建一次，由 ResizeObserver 触发，覆盖延迟可见场景。
      if (!chart) {
        createChart()
        return
      }
      chart.resize()
    }

    onMounted(() => {
      createChart()
      window.addEventListener('resize', resize)
      // 用 ResizeObserver 监听容器自身尺寸变化（浏览器原生全局，无需 import）。
      // 回调里走 resize()，其内部 hasSize 守卫会在隐藏(零尺寸)时跳过，安全。
      if (el.value && typeof ResizeObserver !== 'undefined') {
        ro = new ResizeObserver(() => resize())
        ro.observe(el.value)
      }
    })

    // keep-alive 复用：返回本页时 onMounted 不再触发。
    // 若期间在别的页面切过主题，图表实例仍是旧主题且可能在隐藏（零尺寸）时被重建错乱，
    // 这里在重新激活时重建实例 + resize，确保主题正确且尺寸贴合容器。
    onActivated(() => {
      createChart()
      resize()
    })

    onBeforeUnmount(() => {
      window.removeEventListener('resize', resize)
      // 断开容器尺寸监听，避免实例销毁后回调仍持有已 dispose 的 chart 引用
      if (ro) {
        ro.disconnect()
        ro = null
      }
      if (chart) {
        chart.dispose()
        chart = null
      }
    })

    // option 变化时增量更新
    watch(
      () => props.option,
      (val) => {
        chart && chart.setOption(val || {}, true)
      },
      { deep: true },
    )

    // 主题切换时重建实例（ECharts 主题只能在 init 时指定）。
    // 若当前页处于隐藏态（零尺寸），createChart 内部 hasSize 守卫会跳过，
    // 待 onActivated 返回本页时再以正确主题重建，避免隐藏容器被错误初始化。
    watch(
      () => themeStore.isDark,
      () => {
        createChart()
      },
    )

    return () =>
      h('div', {
        ref: el,
        style: { width: '100%', height: props.height },
      })
  },
}
