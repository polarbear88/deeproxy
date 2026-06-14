<script setup>
// 系统设置（AC-31/37/40）。已对齐 T7 实测契约：
// - GET/PUT /settings：{ adminUser, statRetentionDays, hcDefaults:{mode,url,intervalSec,failThreshold,recoverThreshold} }。
// - 改密 /settings/admin-password {oldPassword,newPassword}（校验旧密码 → 清所有会话）。
// - 导出 /settings/export → { schemaVersion, data }；导入 /settings/import { schemaVersion, data, strategy }。
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import * as sysApi from '@/api/system'
import { useUserStore } from '@/stores/user'

// i18n：组件④设置卡片标题与字段标签经 t() 翻译
const { t } = useI18n()
const router = useRouter()
const userStore = useUserStore()
const loading = ref(false)

// ===== 基础设置 =====
const settings = reactive({
  adminUser: '',
  statRetentionDays: 30,
  // 服务器域名/IP：仅用于"复制代理地址"等连接提示文案；首次由后端探测本机非回环 IP，可手填覆盖。
  serverAddr: '',
  // 健康检查协程池大小：限制并发探测数（默认 150，可热调整）。
  probePoolSize: 150,
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
      settings.serverAddr = d.serverAddr || ''
      settings.probePoolSize = d.probePoolSize ?? 150
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
      serverAddr: settings.serverAddr,
      probePoolSize: settings.probePoolSize,
      hcDefaults: settings.hcDefaults,
      // 运行期动态设置一并提交
      defaultAction: settings.defaultAction,
      logLevel: settings.logLevel,
      idleTimeoutSec: settings.idleTimeoutSec,
      sniffDomain: settings.sniffDomain,
      sniffTimeoutMs: settings.sniffTimeoutMs,
    })
    ElMessage.success(t('settings.saved'))
  } catch {
    /* ignore */
  }
}

// ===== 管理员密码（校验旧密码；改密后清所有会话 → 跳登录）=====
const pwdForm = reactive({ oldPassword: '', newPassword: '', confirm: '' })
async function changePassword() {
  if (!pwdForm.oldPassword) return ElMessage.warning(t('settings.pwdEnterCurrent'))
  if (!pwdForm.newPassword) return ElMessage.warning(t('settings.pwdEnterNew'))
  if (pwdForm.newPassword !== pwdForm.confirm) return ElMessage.warning(t('settings.pwdMismatch'))
  if (pwdForm.newPassword.length < 6) return ElMessage.warning(t('settings.pwdTooShort'))
  try {
    await sysApi.changeAdminPassword({ oldPassword: pwdForm.oldPassword, newPassword: pwdForm.newPassword })
    ElMessage.success(t('settings.pwdChanged'))
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
    ElMessage.success(t('settings.exported'))
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
      return ElMessage.error(t('settings.importInvalidJson'))
    }
    await ElMessageBox.confirm(t('settings.importConfirm'), t('common.notice'), {
      type: 'warning',
    }).catch(() => Promise.reject())
    importing.value = true
    try {
      await sysApi.importConfig({
        schemaVersion: parsed.schemaVersion,
        data: parsed.data ?? parsed,
        strategy: 'overwrite',
      })
      ElMessage.success(t('settings.importSuccess'))
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
    <!-- 卡片①：系统设置（全宽一整张）。内部用 el-divider 做分区：服务器与连接 / 运行期 / 统计 / 健康检查默认值。
         字段与 saveSettings() 载荷一一对应，未删任何字段；script 不动，仅模板由「4 张并排小卡」改为「单卡分区」。 -->
    <el-card v-loading="loading" class="dp-card-gap">
      <template #header><span>{{ t('menu.system') }}</span></template>
      <el-form label-width="150px">
        <!-- 分区：服务器与连接 -->
        <el-divider content-position="left">{{ t('settings.serverConn') }}</el-divider>
        <el-form-item :label="t('settings.adminAccount')">
          <el-input v-model="settings.adminUser" disabled style="max-width: 320px" />
        </el-form-item>
        <el-form-item :label="t('settings.serverAddr')">
          <el-input v-model="settings.serverAddr" :placeholder="t('settings.serverAddrPlaceholder')" style="max-width: 320px" />
          <span class="text-muted hint">{{ t('settings.serverAddrHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('settings.probePoolSize')">
          <el-input-number v-model="settings.probePoolSize" :min="1" :max="10000" />
          <span class="text-muted hint">{{ t('settings.probePoolSizeHint') }}</span>
        </el-form-item>

        <!-- 分区：运行期设置 -->
        <el-divider content-position="left">{{ t('settings.runtime') }}</el-divider>
        <el-form-item :label="t('settings.defaultAction')">
          <el-select v-model="settings.defaultAction" style="width: 160px">
            <el-option :label="t('settings.actionForwardOpt')" value="forward" />
            <el-option :label="t('settings.actionDirectOpt')" value="direct" />
            <el-option :label="t('settings.actionRejectOpt')" value="reject" />
          </el-select>
          <span class="text-muted hint">{{ t('settings.defaultActionHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('settings.logLevel')">
          <el-select v-model="settings.logLevel" style="width: 160px">
            <el-option label="debug" value="debug" />
            <el-option label="info" value="info" />
            <el-option label="warn" value="warn" />
            <el-option label="error" value="error" />
          </el-select>
          <span class="text-muted hint">{{ t('settings.logLevelHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('settings.idleTimeout')">
          <el-input-number v-model="settings.idleTimeoutSec" :min="1" :max="86400" />
          <span class="text-muted hint">{{ t('settings.idleTimeoutHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('settings.sniffDomain')">
          <el-switch v-model="settings.sniffDomain" />
          <span class="text-muted hint">{{ t('settings.sniffDomainHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('settings.sniffTimeout')">
          <el-input-number v-model="settings.sniffTimeoutMs" :min="1" :max="60000" :step="50" />
          <span class="text-muted hint">{{ t('settings.sniffTimeoutHint') }}</span>
        </el-form-item>

        <!-- 分区：统计 -->
        <el-divider content-position="left">{{ t('settings.stat') }}</el-divider>
        <el-form-item :label="t('settings.statRetention')">
          <el-input-number v-model="settings.statRetentionDays" :min="1" :max="3650" />
          <span class="text-muted hint">{{ t('settings.statRetentionHint') }}</span>
        </el-form-item>

        <!-- 分区：健康检查默认值 -->
        <el-divider content-position="left">{{ t('settings.hcDefaults') }}</el-divider>
        <el-form-item :label="t('settings.hcMode')">
          <el-radio-group v-model="settings.hcDefaults.mode">
            <el-radio value="ping">{{ t('settings.hcModePing') }}</el-radio>
            <el-radio value="url">{{ t('settings.hcModeUrl') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="settings.hcDefaults.mode === 'url'" :label="t('settings.hcUrl')">
          <el-input v-model="settings.hcDefaults.url" style="max-width: 420px" />
        </el-form-item>
        <el-form-item :label="t('settings.hcInterval')">
          <el-input-number v-model="settings.hcDefaults.intervalSec" :min="10" :step="10" />
        </el-form-item>
        <el-form-item :label="t('settings.hcFailThreshold')">
          <el-input-number v-model="settings.hcDefaults.failThreshold" :min="1" />
        </el-form-item>
        <el-form-item :label="t('settings.hcRecoverThreshold')">
          <el-input-number v-model="settings.hcDefaults.recoverThreshold" :min="1" />
        </el-form-item>

        <el-divider />
        <el-form-item>
          <el-button type="primary" @click="saveSettings">{{ t('settings.saveSettings') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 下方一行两列：左「修改密码」，右「配置导入导出」 -->
    <el-row :gutter="16" align="stretch">
      <el-col :xs="24" :lg="12">
        <el-card class="dp-card-gap full-card">
          <template #header><span>{{ t('settings.adminPassword') }}</span></template>
          <el-form label-width="110px">
            <el-form-item :label="t('settings.currentPassword')">
              <el-input v-model="pwdForm.oldPassword" type="password" show-password />
            </el-form-item>
            <el-form-item :label="t('settings.newPassword')">
              <el-input v-model="pwdForm.newPassword" type="password" show-password :placeholder="t('settings.newPasswordPlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('settings.confirmPassword')">
              <el-input v-model="pwdForm.confirm" type="password" show-password />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="changePassword">{{ t('settings.changePasswordBtn') }}</el-button>
              <span class="text-muted hint">{{ t('settings.pwdHint') }}</span>
            </el-form-item>
          </el-form>
        </el-card>
      </el-col>

      <el-col :xs="24" :lg="12">
        <el-card class="dp-card-gap full-card">
          <template #header><span>{{ t('settings.importExport') }}</span></template>
          <div class="ie-row">
            <el-button :icon="'Download'" @click="exportConfig">{{ t('settings.exportConfig') }}</el-button>
            <el-upload :show-file-list="false" :before-upload="onImportFile" accept=".json">
              <el-button :icon="'Upload'" :loading="importing">{{ t('settings.importConfig') }}</el-button>
            </el-upload>
          </div>
          <el-alert
            class="ie-tip"
            type="info"
            :closable="false"
            show-icon
            :title="t('settings.importExportTip')"
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
/* 分区分割线与上方内容拉开距离，使各分组在视觉上更清晰（④）。
 * 第一个分割线在卡片顶部，不需要额外上间距。 */
:deep(.el-divider--horizontal) {
  margin-top: 36px;
}
:deep(.el-form > .el-divider--horizontal:first-child) {
  margin-top: 8px;
}
.ie-row {
  display: flex;
  gap: 12px;
  align-items: center;
}
.ie-tip {
  margin-top: 14px;
}
/* 两卡片等高（修改管理员密码 / 配置导入导出）：el-row align=stretch 让两列等高，
 * 卡片再 height:100% 填满列高，使两卡片底边对齐。*/
.full-card {
  height: 100%;
}
</style>
