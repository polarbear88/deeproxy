<script setup>
// 主布局：左侧菜单 + 顶栏（折叠按钮/标题/暗亮切换/管理员菜单）+ 内容区。
// 对应 AC-25（左侧菜单、暗/亮模式）。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessageBox } from 'element-plus'
import { useAppStore } from '@/stores/app'
import { useThemeStore } from '@/stores/theme'
import { useLangStore } from '@/stores/lang'
import { useUserStore } from '@/stores/user'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const themeStore = useThemeStore()
const langStore = useLangStore()
const userStore = useUserStore()

// 从路由表动态生成菜单项（带 meta.title/icon 的子路由）
const menuRoutes = computed(() => {
  const main = router.options.routes.find((r) => r.path === '/')
  return (main?.children || []).filter((c) => c.meta?.title)
})

// 当前激活菜单 = 当前路由 name
const activeMenu = computed(() => route.name)

function go(name) {
  if (name !== route.name) router.push({ name })
  // 移动端：选中菜单后关闭导航抽屉（桌面端 mobileDrawer 不使用，调用无副作用）
  appStore.closeDrawer()
}

// 顶栏汉堡按钮：桌面端切换边栏折叠（原行为）；移动端打开导航抽屉。
function onHamburger() {
  if (appStore.isMobile) appStore.openDrawer()
  else appStore.toggleSidebar()
}

async function handleLogout() {
  await ElMessageBox.confirm(t('common.logoutConfirm.message'), t('common.logoutConfirm.title'), {
    type: 'warning',
    confirmButtonText: t('common.logoutConfirm.confirm'),
    cancelButtonText: t('common.logoutConfirm.cancel'),
  }).catch(() => 'cancel')
  await userStore.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <el-container class="layout-root">
    <!-- 左侧菜单：桌面端常驻；移动端隐藏（改用顶栏汉堡 + 抽屉），结构决策走 isMobile 真相源 -->
    <el-aside
      v-show="!appStore.isMobile"
      class="layout-aside"
      :width="appStore.sidebarCollapsed ? 'var(--dp-aside-width-collapsed)' : 'var(--dp-aside-width)'"
    >
      <div class="logo">
        <img src="/favicon.svg" alt="logo" class="logo-img" />
        <span v-show="!appStore.sidebarCollapsed" class="logo-text">deeproxy</span>
      </div>
      <el-menu
        :default-active="activeMenu"
        :collapse="appStore.sidebarCollapsed"
        :collapse-transition="false"
        class="layout-menu"
        @select="go"
      >
        <el-menu-item v-for="r in menuRoutes" :key="r.name" :index="r.name">
          <el-icon><component :is="r.meta.icon" /></el-icon>
          <template #title>{{ t(r.meta.title) }}</template>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <!-- 移动端导航抽屉：从左侧滑出，复用同一套菜单项；选中后自动关闭（见 go()）。
         菜单用 inline v-for 各写一份（与上方 aside 重复），刻意不抽子组件——
         避免 props/emit 契约带来的额外复杂度；两处 el-menu 各自独立维护 active，
         均绑定同一响应式 activeMenu，随路由同步高亮，无双激活冲突。 -->
    <el-drawer
      v-model="appStore.mobileDrawer"
      direction="ltr"
      :with-header="false"
      size="220px"
      class="mobile-nav-drawer"
    >
      <div class="logo">
        <img src="/favicon.svg" alt="logo" class="logo-img" />
        <span class="logo-text">deeproxy</span>
      </div>
      <el-menu :default-active="activeMenu" class="layout-menu" @select="go">
        <el-menu-item v-for="r in menuRoutes" :key="r.name" :index="r.name">
          <el-icon><component :is="r.meta.icon" /></el-icon>
          <template #title>{{ t(r.meta.title) }}</template>
        </el-menu-item>
      </el-menu>
    </el-drawer>

    <el-container>
      <!-- 顶栏 -->
      <el-header class="layout-header">
        <div class="flex-row">
          <el-icon class="collapse-btn" @click="onHamburger">
            <Fold v-if="!appStore.sidebarCollapsed" />
            <Expand v-else />
          </el-icon>
          <span class="header-title">{{ route.meta?.title ? t(route.meta.title) : '' }}</span>
        </div>
        <div class="flex-row header-right">
          <!-- 暗/亮切换 -->
          <el-tooltip :content="themeStore.isDark ? t('common.switchLight') : t('common.switchDark')" placement="bottom">
            <el-icon class="theme-btn" @click="themeStore.toggle">
              <Moon v-if="!themeStore.isDark" />
              <Sunny v-else />
            </el-icon>
          </el-tooltip>
          <!-- 中/英语言切换：放在主题按钮与管理员下拉之间，风格与下拉一致 -->
          <el-dropdown @command="(c) => langStore.setLocale(c)">
            <span class="lang-trigger">
              <!-- 地球图标：Element Plus 无地球图标，用内联 SVG（圆+经纬线地球），随 el-icon 继承尺寸/颜色 -->
              <el-icon>
                <svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" fill="none"
                     stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <circle cx="12" cy="12" r="10" />
                  <line x1="2" y1="12" x2="22" y2="12" />
                  <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
                </svg>
              </el-icon>
              <span v-show="!appStore.isMobile" class="lang-label">{{ langStore.locale === 'zh' ? '中文' : 'English' }}</span>
              <el-icon><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="zh" :disabled="langStore.locale === 'zh'">中文</el-dropdown-item>
                <el-dropdown-item command="en" :disabled="langStore.locale === 'en'">English</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <!-- 管理员下拉 -->
          <el-dropdown @command="(c) => c === 'logout' && handleLogout()">
            <span class="admin-trigger">
              <el-icon><Avatar /></el-icon>
              <span v-show="!appStore.isMobile" class="admin-name">{{ userStore.username || t('common.admin') }}</span>
              <el-icon><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item @click="go('system')">{{ t('menu.system') }}</el-dropdown-item>
                <el-dropdown-item divided command="logout">{{ t('common.logout') }}</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>

      <!-- 内容区 -->
      <el-main class="layout-main">
        <router-view v-slot="{ Component }">
          <keep-alive :max="6">
            <component :is="Component" />
          </keep-alive>
        </router-view>
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped lang="scss">
// 引入响应式断点 mixin（@use 别名解析的冒烟点：本仓库首个在 scoped block 用 @use 的组件）
@use '@/styles/responsive.scss' as r;

.layout-root {
  height: 100vh;
}

.layout-aside {
  background-color: var(--el-bg-color);
  border-right: 1px solid var(--el-border-color-light);
  transition: width 0.25s ease;
  overflow: hidden;
}

.logo {
  height: 56px;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 16px;
  border-bottom: 1px solid var(--el-border-color-light);
  .logo-img {
    width: 28px;
    height: 28px;
  }
  .logo-text {
    font-size: 18px;
    font-weight: 700;
    color: var(--dp-brand);
    white-space: nowrap;
  }
}

.layout-menu {
  border-right: none;
}

.layout-header {
  height: 56px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  background-color: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color-light);
  padding: 0 16px;
  // 手机端：减小左右内边距释放横向空间
  @include r.mobile {
    padding: 0 10px;
  }

  .collapse-btn,
  .theme-btn {
    font-size: 20px;
    cursor: pointer;
    color: var(--el-text-color-regular);
    &:hover {
      color: var(--dp-brand);
    }
  }
  .header-title {
    margin-left: 14px;
    font-size: 16px;
    font-weight: 600;
  }
  .header-right {
    gap: 18px;
    // 手机端：缩小间距，配合文字隐藏（v-show）让图标更紧凑、不溢出
    @include r.mobile {
      gap: 8px;
    }
  }
  .admin-trigger {
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    outline: none;
    .admin-name {
      font-size: 14px;
    }
  }
  // 语言切换控件：与管理员下拉同款样式，保持顶栏视觉一致
  .lang-trigger {
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    outline: none;
    color: var(--el-text-color-regular);
    &:hover {
      color: var(--dp-brand);
    }
    .lang-label {
      font-size: 14px;
    }
  }
}

.layout-main {
  background-color: var(--dp-content-bg);
  padding: 0;
  overflow-y: auto;
  // 手机端：内容区禁止横向溢出，把宽表格等内容的横向滚动收敛到各自容器内部，
  // 避免整个页面被撑出横向滚动条（AC-6 关键）。
  @include r.mobile {
    overflow-x: hidden;
  }
}

// 移动端导航抽屉内部：去掉 el-drawer 默认 padding，让 logo 与菜单贴合边缘（与桌面 aside 观感一致）
.mobile-nav-drawer {
  :deep(.el-drawer__body) {
    padding: 0;
  }
}
</style>
