// ECharts 封装组件：统一处理 init/resize/dispose 与暗亮主题切换（DRY）。
// 用法：<EChart :option="option" height="320px" />
import * as echarts from 'echarts'
import { useThemeStore } from '@/stores/theme'

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
    const themeStore = useThemeStore()

    // 按当前主题创建实例（暗色用内置 'dark' 主题）
    function createChart() {
      if (!el.value) return
      if (chart) {
        chart.dispose()
        chart = null
      }
      chart = echarts.init(el.value, themeStore.echartsTheme() || undefined)
      chart.setOption(props.option || {})
    }

    function resize() {
      chart && chart.resize()
    }

    onMounted(() => {
      createChart()
      window.addEventListener('resize', resize)
    })

    onBeforeUnmount(() => {
      window.removeEventListener('resize', resize)
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

    // 主题切换时重建实例（ECharts 主题只能在 init 时指定）
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
