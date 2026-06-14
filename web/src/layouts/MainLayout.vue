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
    <!-- 左侧菜单 -->
    <el-aside
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

    <el-container>
      <!-- 顶栏 -->
      <el-header class="layout-header">
        <div class="flex-row">
          <el-icon class="collapse-btn" @click="appStore.toggleSidebar">
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
              <span class="lang-label">{{ langStore.locale === 'zh' ? '中文' : 'English' }}</span>
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
              <span class="admin-name">{{ userStore.username || t('common.admin') }}</span>
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
}
</style>
