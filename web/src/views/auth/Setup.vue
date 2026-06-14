<script setup>
// 首次设置页（AC-19/26）：系统无管理员时设置账号密码。
import { reactive, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useUserStore } from '@/stores/user'
import { useThemeStore } from '@/stores/theme'

const { t } = useI18n()
const router = useRouter()
const userStore = useUserStore()
const themeStore = useThemeStore()

const formRef = ref(null)
const loading = ref(false)
const form = reactive({ username: '', password: '', confirm: '' })

// 两次密码一致性校验（错误消息走 i18n）
const validateConfirm = (_rule, value, cb) => {
  if (value !== form.password) cb(new Error(t('validate.passwordMismatch')))
  else cb()
}
// rules 用 computed：使校验消息里的 t() 随语言切换响应式重算
//（普通对象只在 setup 时求值一次，切换语言后消息不会更新）。
const rules = computed(() => ({
  username: [{ required: true, message: t('validate.setupUsernameRequired'), trigger: 'blur' }],
  password: [
    { required: true, message: t('validate.setupPasswordRequired'), trigger: 'blur' },
    { min: 6, message: t('validate.passwordMin'), trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: t('validate.confirmRequired'), trigger: 'blur' },
    { validator: validateConfirm, trigger: 'blur' },
  ],
}))

async function onSubmit() {
  const ok = await formRef.value.validate().catch(() => false)
  if (!ok) return
  loading.value = true
  try {
    await userStore.setup({ username: form.username, password: form.password })
    ElMessage.success(t('auth.setupOk'))
    router.replace({ name: 'login' })
  } catch {
    // 错误已由拦截器处理
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
        <h2>{{ t('auth.setupTitle') }}</h2>
        <p class="text-muted">{{ t('auth.setupSub') }}</p>
      </div>
      <el-form ref="formRef" :model="form" :rules="rules" size="large" label-position="top">
        <el-form-item :label="t('auth.setupUsernameLabel')" prop="username">
          <el-input v-model="form.username" :placeholder="t('auth.setupUsernamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('auth.setupPasswordLabel')" prop="password">
          <el-input v-model="form.password" type="password" show-password :placeholder="t('auth.setupPasswordPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('auth.setupConfirmLabel')" prop="confirm">
          <el-input v-model="form.confirm" type="password" show-password :placeholder="t('auth.setupConfirmPlaceholder')" />
        </el-form-item>
        <el-button type="primary" class="auth-submit" :loading="loading" @click="onSubmit">
          {{ t('auth.setupBtn') }}
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
  width: 400px;
  padding: 12px 8px;
}
.auth-brand {
  text-align: center;
  margin-bottom: 16px;
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
