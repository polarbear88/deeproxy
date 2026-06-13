<script setup>
// 首次设置页（AC-19/26）：系统无管理员时设置账号密码。
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useUserStore } from '@/stores/user'
import { useThemeStore } from '@/stores/theme'

const router = useRouter()
const userStore = useUserStore()
const themeStore = useThemeStore()

const formRef = ref(null)
const loading = ref(false)
const form = reactive({ username: '', password: '', confirm: '' })

// 两次密码一致性校验
const validateConfirm = (_rule, value, cb) => {
  if (value !== form.password) cb(new Error('两次输入的密码不一致'))
  else cb()
}
const rules = {
  username: [{ required: true, message: '请设置管理员账号', trigger: 'blur' }],
  password: [
    { required: true, message: '请设置密码', trigger: 'blur' },
    { min: 6, message: '密码至少 6 位', trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: '请再次输入密码', trigger: 'blur' },
    { validator: validateConfirm, trigger: 'blur' },
  ],
}

async function onSubmit() {
  const ok = await formRef.value.validate().catch(() => false)
  if (!ok) return
  loading.value = true
  try {
    await userStore.setup({ username: form.username, password: form.password })
    ElMessage.success('设置成功，请登录')
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
        <h2>首次设置</h2>
        <p class="text-muted">请设置后台管理员账号与密码</p>
      </div>
      <el-form ref="formRef" :model="form" :rules="rules" size="large" label-position="top">
        <el-form-item label="管理员账号" prop="username">
          <el-input v-model="form.username" placeholder="设置管理员账号" />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input v-model="form.password" type="password" show-password placeholder="设置密码（至少6位）" />
        </el-form-item>
        <el-form-item label="确认密码" prop="confirm">
          <el-input v-model="form.confirm" type="password" show-password placeholder="再次输入密码" />
        </el-form-item>
        <el-button type="primary" class="auth-submit" :loading="loading" @click="onSubmit">
          完成设置
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
