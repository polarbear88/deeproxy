// 路由配置 + 全局守卫。
// 守卫逻辑：
//   - 未初始化系统 → 强制跳首次设置页（AC-19/26）。
//   - 未登录访问受保护页 → 跳登录页（AC-20）。
//   - 已登录访问登录/设置页 → 跳仪表盘。
import { createRouter, createWebHashHistory } from 'vue-router'
import { useUserStore } from '@/stores/user'

// 业务页面均挂在 MainLayout 下；登录与首次设置为独立全屏页。
const routes = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/auth/Login.vue'),
    meta: { public: true, title: 'menu.login' },
  },
  {
    path: '/setup',
    name: 'setup',
    component: () => import('@/views/auth/Setup.vue'),
    meta: { public: true, title: 'menu.setup' },
  },
  {
    path: '/',
    component: () => import('@/layouts/MainLayout.vue'),
    redirect: '/dashboard',
    children: [
      {
        path: 'dashboard',
        name: 'dashboard',
        component: () => import('@/views/dashboard/Dashboard.vue'),
        meta: { title: 'menu.dashboard', icon: 'Odometer' },
      },
      {
        path: 'proxy',
        name: 'proxy',
        component: () => import('@/views/proxy/ProxyGroups.vue'),
        meta: { title: 'menu.proxy', icon: 'Connection' },
      },
      {
        path: 'rule',
        name: 'rule',
        component: () => import('@/views/rule/Rules.vue'),
        meta: { title: 'menu.rule', icon: 'Filter' },
      },
      {
        path: 'user',
        name: 'user',
        component: () => import('@/views/user/Users.vue'),
        meta: { title: 'menu.user', icon: 'User' },
      },
      {
        path: 'syslog',
        name: 'syslog',
        component: () => import('@/views/syslog/SysLog.vue'),
        meta: { title: 'menu.syslog', icon: 'Document' },
      },
      {
        path: 'system',
        name: 'system',
        component: () => import('@/views/system/Settings.vue'),
        meta: { title: 'menu.system', icon: 'Setting' },
      },
    ],
  },
  // 兜底：未知路径回仪表盘
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  // 用 hash 模式：单二进制 embed 静态托管下无需后端 history fallback 即可工作
  history: createWebHashHistory(),
  routes,
})

router.beforeEach(async (to) => {
  const userStore = useUserStore()

  // 首次设置页与登录页放行，但需先确认系统是否已初始化
  if (to.name === 'setup') {
    return true
  }

  // 检查系统初始化状态（仅在尚未向后端确认过时查一次，失败不阻塞放行交由接口 401 处理）。
  // 用 initChecked 而非 initialized 作判据：initialized 默认 true，健康系统下恒为 true，
  // 旧条件 if (userStore.initialized) 会导致每次路由切换都重复请求 init 接口。
  if (!userStore.initChecked) {
    try {
      const ok = await userStore.fetchInitStatus()
      if (!ok) {
        // 未初始化 → 强制去首次设置
        return { name: 'setup' }
      }
    } catch {
      // 接口暂不可用时不拦截，避免开发期白屏
    }
  }

  if (to.meta.public) {
    // 已登录还去登录页 → 回仪表盘
    if (to.name === 'login' && userStore.loggedIn) {
      return { name: 'dashboard' }
    }
    return true
  }

  // 受保护页：未登录跳登录
  if (!userStore.loggedIn) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
