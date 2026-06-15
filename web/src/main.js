// 应用入口：装配 Vue 实例、Pinia、路由，并挂载主题与语言。
// Element Plus 主体组件由 unplugin-auto-import + unplugin-vue-components（ElementPlusResolver）按需引入；
// 图标则全局注册（ElementPlusResolver 不解析图标，见下）；另保留 v-loading 指令与命令式服务的 CSS。
import { createApp } from 'vue'
import { createPinia } from 'pinia'
// 仅按需引入 ElLoading：其他 Element Plus 组件由 unplugin 自动按需注册，
// 但 v-loading 指令需要显式 app.use(ElLoading) 才能在模板中使用（指令不走组件自动注册路径）。
import { ElLoading } from 'element-plus'
// 图标全局注册（必须保留）：本项目模板大量使用裸标签图标（如 <el-icon><Plus/></el-icon>）
// 以及菜单/顶栏的动态图标 <component :is="r.meta.icon"（字符串名）>。ElementPlusResolver 只解析
// ^El 前缀组件，【不解析】@element-plus/icons-vue 的裸图标；若不全局注册，这些图标会退化为
// resolveComponent(名字) 运行时查找失败而渲染空白（MainLayout 菜单/顶栏 + 各 view 按钮图标全废）。
// 图标体积很小（非瘦身大头，大头是 ElementPlus 主体与 echarts，已分别按需/拆包），故全量注册。
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

// ── CSS 补全（R1）────────────────────────────────────────────────────────────
// unplugin-vue-components 会自动为 el-button/el-table 等组件注入 CSS，
// 但以下三个是"服务式调用"（JS 直接调用 ElMessage/ElMessageBox）或全局叠加层，
// 无组件实例可供 resolver 检测，必须手动引入：
import 'element-plus/theme-chalk/el-message.css'         // ElMessage.success/error/warning（api/request.js + 各 view）
import 'element-plus/theme-chalk/el-message-box.css'     // ElMessageBox.confirm（MainLayout.vue 退出确认 + ProxyGroups 删除确认等）
import 'element-plus/theme-chalk/el-overlay.css'         // 弹窗/抽屉/消息框背景遮罩层（el-dialog/el-drawer 依赖）
// el-loading.css 由 el-table / el-card 等携带 v-loading 的组件触发，
// unplugin 会自动注入其 CSS；此处无需重复引入。

import App from './App.vue'
import router from './router'
import i18n from './locales'
import { useThemeStore } from './stores/theme'
import { useLangStore } from './stores/lang'
// ⚠️ 注意：此行不可删除，index.scss:6 依赖 @use 'element-plus/theme-chalk/dark/css-vars.css'
// 暗色模式通过给 <html> 加 .dark 类切换，该 CSS 变量文件由 Element Plus 提供，
// 如果删除 index.scss 中的 @use，暗色主题下所有 EP 组件颜色将失效。(M4 护栏)
import './styles/index.scss'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)
app.use(i18n)
// 仅注册 ElLoading 以启用 v-loading 指令（v-loading 在 el-table / el-card 等组件上广泛使用）。
// 不再全量 app.use(ElementPlus)：全量注册会把所有 EP 组件注入全局，
// 与 unplugin 按需引入重复，导致 bundle 额外增大。
app.use(ElLoading)

// 全量注册 Element Plus 图标为全局组件：覆盖裸标签图标与 <component :is="字符串"> 动态图标，
// 这二者 ElementPlusResolver 不解析（见上方 import 注释）。仅注册图标，不注册 EP 主体组件，
// 故不与 unplugin 的组件按需引入冲突，也不会把 EP 全量组件打进 bundle。
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

// 在挂载前应用持久化的主题（暗/亮）与语言（中/英），避免首屏闪烁/语言抖动
useThemeStore(pinia).applyTheme()
useLangStore(pinia).applyLocale()

app.mount('#app')
