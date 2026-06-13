<script setup>
// 系统设置（AC-31/37/40）。已对齐 T7 实测契约：
// - GET/PUT /settings：{ adminUser, statRetentionDays, hcDefaults:{mode,url,intervalSec,failThreshold,recoverThreshold} }。
// - 改密 /settings/admin-password {oldPassword,newPassword}（校验旧密码 → 清所有会话）。
// - 导出 /settings/export → { schemaVersion, data }；导入 /settings/import { schemaVersion, data, strategy }。
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useRouter } from 'vue-router'
import * as sysApi from '@/api/system'
import { useUserStore } from '@/stores/user'

const router = useRouter()
const userStore = useUserStore()
const loading = ref(false)

// ===== 基础设置 =====
const settings = reactive({
  adminUser: '',
  statRetentionDays: 30,
  hcDefaults: {
    mode: 'url',
    url: 'https://www.bing.com/hp/api/v1/carousel?&format=json',
    intervalSec: 600,
    failThreshold: 3,
    recoverThreshold: 2,
  },
  // 运行期动态设置（取消配置文件后迁入系统设置，可后台热改；log_level 立即生效，其余新连接生效）
  defaultAction: 'forward',
  logLevel: 'info',
  idleTimeoutSec: 300,
  sniffDomain: true,
  sniffTimeoutMs: 300,
})
async function loadSettings() {
  loading.value = true
  try {
    const d = await sysApi.getSettings()
    if (d) {
      settings.adminUser = d.adminUser || ''
      settings.statRetentionDays = d.statRetentionDays ?? 30
      if (d.hcDefaults) Object.assign(settings.hcDefaults, d.hcDefaults)
      // 运行期动态设置
      settings.defaultAction = d.defaultAction || 'forward'
      settings.logLevel = d.logLevel || 'info'
      settings.idleTimeoutSec = d.idleTimeoutSec ?? 300
      settings.sniffDomain = d.sniffDomain ?? true
      settings.sniffTimeoutMs = d.sniffTimeoutMs ?? 300
    }
  } catch {
    /* ignore */
  } finally {
    loading.value = false
  }
}
async function saveSettings() {
  try {
    await sysApi.updateSettings({
      statRetentionDays: settings.statRetentionDays,
      hcDefaults: settings.hcDefaults,
      // 运行期动态设置一并提交
      defaultAction: settings.defaultAction,
      logLevel: settings.logLevel,
      idleTimeoutSec: settings.idleTimeoutSec,
      sniffDomain: settings.sniffDomain,
      sniffTimeoutMs: settings.sniffTimeoutMs,
    })
    ElMessage.success('已保存')
  } catch {
    /* ignore */
  }
}

// ===== 管理员密码（校验旧密码；改密后清所有会话 → 跳登录）=====
const pwdForm = reactive({ oldPassword: '', newPassword: '', confirm: '' })
async function changePassword() {
  if (!pwdForm.oldPassword) return ElMessage.warning('请输入当前密码')
  if (!pwdForm.newPassword) return ElMessage.warning('请输入新密码')
  if (pwdForm.newPassword !== pwdForm.confirm) return ElMessage.warning('两次新密码不一致')
  if (pwdForm.newPassword.length < 6) return ElMessage.warning('新密码至少 6 位')
  try {
    await sysApi.changeAdminPassword({ oldPassword: pwdForm.oldPassword, newPassword: pwdForm.newPassword })
    ElMessage.success('密码已修改，请重新登录')
    pwdForm.oldPassword = pwdForm.newPassword = pwdForm.confirm = ''
    userStore.clear()
    router.replace({ name: 'login' })
  } catch {
    /* ignore */
  }
}

// ===== 配置导入导出 =====
async function exportConfig() {
  try {
    const data = await sysApi.exportConfig()
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `deeproxy-config-${Date.now()}.json`
    a.click()
    URL.revokeObjectURL(url)
    ElMessage.success('已导出')
  } catch {
    /* ignore */
  }
}
const importing = ref(false)
function onImportFile(uploadFile) {
  const file = uploadFile.raw || uploadFile
  const reader = new FileReader()
  reader.onload = async () => {
    let parsed
    try {
      parsed = JSON.parse(reader.result)
    } catch {
      return ElMessage.error('文件不是合法 JSON')
    }
    await ElMessageBox.confirm('导入将整体覆盖当前配置（后端导入前会自动备份），确认继续？', '提示', {
      type: 'warning',
    }).catch(() => Promise.reject())
    importing.value = true
    try {
      await sysApi.importConfig({
        schemaVersion: parsed.schemaVersion,
        data: parsed.data ?? parsed,
        strategy: 'overwrite',
      })
      ElMessage.success('导入成功')
    } catch {
      /* ignore */
    } finally {
      importing.value = false
    }
  }
  reader.readAsText(file)
  return false
}

onMounted(loadSettings)
</script>

<template>
  <div class="dp-page">
    <el-row :gutter="16">
      <el-col :xs="24" :lg="12">
        <el-card v-loading="loading" class="dp-card-gap">
          <template #header><span>运行期与默认值设置</span></template>
          <el-form label-width="130px">
            <el-form-item label="管理员账号">
              <el-input v-model="settings.adminUser" disabled />
            </el-form-item>

            <el-divider content-position="left">运行期设置</el-divider>
            <el-form-item label="默认动作">
              <el-select v-model="settings.defaultAction" style="width: 160px">
                <el-option label="转发(forward)" value="forward" />
                <el-option label="直连(direct)" value="direct" />
                <el-option label="拒绝(reject)" value="reject" />
              </el-select>
              <span class="text-muted hint">规则全不命中时的兜底动作</span>
            </el-form-item>
            <el-form-item label="日志级别">
              <el-select v-model="settings.logLevel" style="width: 160px">
                <el-option label="debug" value="debug" />
                <el-option label="info" value="info" />
                <el-option label="warn" value="warn" />
                <el-option label="error" value="error" />
              </el-select>
              <span class="text-muted hint">保存后立即热生效（无需重启）</span>
            </el-form-item>
            <el-form-item label="空闲超时(秒)">
              <el-input-number v-model="settings.idleTimeoutSec" :min="1" :max="86400" />
              <span class="text-muted hint">连接双向空闲回收（新连接生效，默认 300）</span>
            </el-form-item>
            <el-form-item label="域名嗅探">
              <el-switch v-model="settings.sniffDomain" />
              <span class="text-muted hint">IP 未命中 ip-cidr 时嗅探 SNI/Host 还原域名</span>
            </el-form-item>
            <el-form-item label="嗅探超时(毫秒)">
              <el-input-number v-model="settings.sniffTimeoutMs" :min="1" :max="60000" :step="50" />
              <span class="text-muted hint">嗅探首包等待上限（新连接生效，默认 300）</span>
            </el-form-item>

            <el-divider content-position="left">统计</el-divider>
            <el-form-item label="统计保留期(天)">
              <el-input-number v-model="settings.statRetentionDays" :min="1" :max="3650" />
              <span class="text-muted hint">过期聚合桶自动清理（默认 30）</span>
            </el-form-item>
            <el-divider content-position="left">健康检查默认值</el-divider>
            <el-form-item label="探测方式">
              <el-radio-group v-model="settings.hcDefaults.mode">
                <el-radio value="ping">Ping</el-radio>
                <el-radio value="url">请求 URL</el-radio>
              </el-radio-group>
            </el-form-item>
            <el-form-item v-if="settings.hcDefaults.mode === 'url'" label="探测 URL">
              <el-input v-model="settings.hcDefaults.url" />
            </el-form-item>
            <el-form-item label="间隔(秒)">
              <el-input-number v-model="settings.hcDefaults.intervalSec" :min="10" :step="10" />
            </el-form-item>
            <el-form-item label="失败阈值">
              <el-input-number v-model="settings.hcDefaults.failThreshold" :min="1" />
            </el-form-item>
            <el-form-item label="恢复阈值">
              <el-input-number v-model="settings.hcDefaults.recoverThreshold" :min="1" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="saveSettings">保存设置</el-button>
            </el-form-item>
          </el-form>
        </el-card>
      </el-col>

      <el-col :xs="24" :lg="12">
        <el-card class="dp-card-gap">
          <template #header><span>修改管理员密码</span></template>
          <el-form label-width="110px">
            <el-form-item label="当前密码">
              <el-input v-model="pwdForm.oldPassword" type="password" show-password />
            </el-form-item>
            <el-form-item label="新密码">
              <el-input v-model="pwdForm.newPassword" type="password" show-password placeholder="至少 6 位" />
            </el-form-item>
            <el-form-item label="确认新密码">
              <el-input v-model="pwdForm.confirm" type="password" show-password />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="changePassword">修改密码</el-button>
              <span class="text-muted hint">改密后所有会话失效，需重新登录</span>
            </el-form-item>
          </el-form>
        </el-card>

        <el-card>
          <template #header><span>配置导入 / 导出</span></template>
          <div class="ie-row">
            <el-button :icon="'Download'" @click="exportConfig">导出配置 JSON</el-button>
            <el-upload :show-file-list="false" :before-upload="onImportFile" accept=".json">
              <el-button :icon="'Upload'" :loading="importing">导入配置 JSON</el-button>
            </el-upload>
          </div>
          <el-alert
            class="ie-tip"
            type="info"
            :closable="false"
            show-icon
            title="导出含分组/规则/用户/授权（带 schemaVersion）；导入为整体覆盖，后端导入前会自动备份。"
          />
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped lang="scss">
.hint {
  margin-left: 10px;
  font-size: 12px;
}
.ie-row {
  display: flex;
  gap: 12px;
  align-items: center;
}
.ie-tip {
  margin-top: 14px;
}
</style>
