// 应用入口：装配 Vue 实例、Pinia、路由、Element Plus 图标，并挂载主题。
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

import App from './App.vue'
import router from './router'
import { useThemeStore } from './stores/theme'
import './styles/index.scss'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)
// Element Plus 组件由 unplugin 自动按需引入，这里仅显式 use 一次以注册指令/全局配置
app.use(ElementPlus)

// 全量注册 Element Plus 图标为全局组件（图标体积小，便于各页面直接用 <el-icon><XXX/></el-icon>）
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

// 在挂载前应用持久化的主题（暗/亮），避免首屏闪烁
useThemeStore(pinia).applyTheme()

app.mount('#app')
