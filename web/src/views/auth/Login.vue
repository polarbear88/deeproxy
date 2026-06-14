<script setup>
// 登录页（AC-20/26）。校验通过后由 user store 签发会话（Cookie）。
import { reactive, ref, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useUserStore } from '@/stores/user'
import { useThemeStore } from '@/stores/theme'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const userStore = useUserStore()
const themeStore = useThemeStore()

const formRef = ref(null)
const loading = ref(false)
const form = reactive({ username: '', password: '' })
// rules 用 computed：使校验消息里的 t() 随语言切换响应式重算
//（普通对象只在 setup 时求值一次，切换语言后消息不会更新——这是表单错误提示
//  无多语言的根因之一）。
const rules = computed(() => ({
  username: [{ required: true, message: t('validate.usernameRequired'), trigger: 'blur' }],
  password: [{ required: true, message: t('validate.passwordRequired'), trigger: 'blur' }],
}))

async function onSubmit() {
  const ok = await formRef.value.validate().catch(() => false)
  if (!ok) return
  loading.value = true
  try {
    await userStore.login({ username: form.username, password: form.password })
    ElMessage.success(t('auth.loginOk'))
    const redirect = route.query.redirect || '/dashboard'
    router.replace(redirect)
  } catch {
    // 错误提示已由 axios 拦截器统一处理
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <el-icon class="theme-corner" @click="themeStore.toggle">
      <Moon v-if="!themeStore.isDark" />
      <Sunny v-else />
    </el-icon>
    <el-card class="auth-card">
      <div class="auth-brand">
        <img src="/favicon.svg" class="auth-logo" alt="logo" />
        <h2>{{ t('auth.loginBrandTitle') }}</h2>
        <p class="text-muted">{{ t('auth.loginBrandSub') }}</p>
      </div>
      <el-form ref="formRef" :model="form" :rules="rules" size="large" @keyup.enter="onSubmit">
        <el-form-item prop="username">
          <el-input v-model="form.username" :placeholder="t('auth.usernamePlaceholder')" :prefix-icon="'User'" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input
            v-model="form.password"
            type="password"
            show-password
            :placeholder="t('auth.passwordPlaceholder')"
            :prefix-icon="'Lock'"
          />
        </el-form-item>
        <el-button type="primary" class="auth-submit" :loading="loading" @click="onSubmit">
          {{ t('auth.loginBtn') }}
        </el-button>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped lang="scss">
.auth-page {
  position: relative;
  height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, var(--dp-content-bg), var(--el-bg-color));
}
.theme-corner {
  position: absolute;
  top: 20px;
  right: 24px;
  font-size: 22px;
  cursor: pointer;
  color: var(--el-text-color-regular);
}
.auth-card {
  /* 桌面 380px；手机端用 min(380px, 92vw) 自适应居中不溢出（AC-9） */
  width: min(380px, 92vw);
  padding: 12px 8px;
}
.auth-brand {
  text-align: center;
  margin-bottom: 24px;
  .auth-logo {
    width: 56px;
    height: 56px;
  }
  h2 {
    margin: 12px 0 4px;
  }
  p {
    margin: 0;
  }
}
.auth-submit {
  width: 100%;
}
</style>
