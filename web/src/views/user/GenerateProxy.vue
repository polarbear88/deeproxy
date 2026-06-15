<script setup>
// 生成代理页面（组件：Generate Proxy）。
// 目标：让运维选择一个已有代理用户 + 一个代理组，按代理组类型补充信息，
//   生成「连接本机 SOCKS5 服务」的代理连接串，并提供与用户管理页一致的两种复制格式。
//
// 最终用户名遵循 v2 契约 user-组名[-尾段]（见 auth/username.go）：
//   - Type A（动态上游）：尾段 = base64("u:p@host:port")（见 auth/upstream.go EncodeUpstream），
//     即 用户名 = `<user>-<groupName>-<base64>`。
//   - Type B（代理池）：尾段 = 命名变量串 name_value#name_value...（见 auth/variables.go），
//     无变量时无尾段 → 用户名 = `<user>-<groupName>`。
//
// 两种复制格式（与 Users.vue 完全同源，密码取用户明文 pwd，服务器地址/端口取 getServerInfo）：
//   - 格式1：socks5://<username>:<pwd>@<serverAddr>:<socks5Port>
//   - 格式2：<serverAddr>:<socks5Port>:<username>:<pwd>
import { onMounted, reactive, ref, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import * as userApi from '@/api/user'
import * as groupApi from '@/api/group'
import { getServerInfo } from '@/api/system'

const { t } = useI18n()

const loading = ref(false)
const users = ref([])
const groups = ref([])

// 复制连接串所需的全局上下文：服务器域名/IP 与 SOCKS5 监听端口（来自 /settings/server-info）。
// 缺失时沿用 Users.vue 的占位符策略，避免拼出报错。
const serverAddr = ref('')
const socks5Port = ref(0)

// 表单：选中的用户 id、代理组 id，以及 Type A 上游 4 字段、Type B 动态变量行。
const form = reactive({
  userId: null,
  groupId: null,
  // Type A 动态上游：host/port 必填，user/pwd 可空（上游免认证）。
  upstream: { host: '', port: null, user: '', pwd: '' },
  // Type B 命名变量：动态键值行，每行 { name, value }。
  variables: [{ name: '', value: '' }],
})

// 选中用户对象（用于取明文 pwd）。
const selectedUser = computed(() => users.value.find((u) => u.id === form.userId) || null)
// 选中代理组对象（用于取 name 与 type）。
const selectedGroup = computed(() => groups.value.find((g) => g.id === form.groupId) || null)
// 当前组类型：'A' 动态上游 / 'B' 代理池 / null 未选。
const groupType = computed(() => selectedGroup.value?.type || null)

async function loadAll() {
  loading.value = true
  try {
    users.value = (await userApi.listUsers()) || []
  } catch {
    users.value = []
  }
  try {
    groups.value = (await groupApi.listGroups()) || []
  } catch {
    groups.value = []
  } finally {
    loading.value = false
  }
}

// 拉取服务器地址/端口；失败静默降级为占位符（与 Users.vue 一致）。
async function loadProxyContext() {
  try {
    const info = await getServerInfo()
    if (info) {
      serverAddr.value = info.serverAddr || ''
      socks5Port.value = info.socks5Port || 0
    }
  } catch {
    /* ignore：降级占位符 */
  }
}

// ===== 尾段构造 =====
// Type A 尾段：把 4 字段拼成明文 "u:p@host:port" 后做标准 base64（对应 Go base64.StdEncoding）。
// 之所以用 unescape(encodeURIComponent(...)) 包一层：btoa 仅接受 Latin1，若上游凭据含中文/多字节
// 会抛异常；先转 UTF-8 字节再编码，保证与后端按字节解码一致。
function buildTailA() {
  const u = form.upstream
  const plain = `${u.user || ''}:${u.pwd || ''}@${u.host}:${u.port}`
  return btoa(unescape(encodeURIComponent(plain)))
}

// Type B 尾段：把非空变量行拼成 name_value#name_value...（'_' 连接名与值、'#' 连接多个）。
// 仅纳入「变量名非空」的行；全部为空则返回 ''（无尾段）。
function buildTailB() {
  const segs = form.variables
    .filter((v) => v.name && v.name.trim() !== '')
    .map((v) => `${v.name}_${v.value ?? ''}`)
  return segs.join('#')
}

// 生成最终用户名 user-组名[-尾段]；不满足前置条件时返回空串。
const username = computed(() => {
  const user = selectedUser.value
  const group = selectedGroup.value
  if (!user || !group) return ''

  let tail = ''
  if (group.type === 'A') {
    // Type A 需要 host 与 port 才能构造合法上游尾段。
    if (!form.upstream.host || !form.upstream.port) return ''
    tail = buildTailA()
  } else if (group.type === 'B') {
    tail = buildTailB()
  }

  // 无尾段时不追加结尾 '-'（契约：user-组名 即合法、无尾段）。
  return tail ? `${user.username}-${group.name}-${tail}` : `${user.username}-${group.name}`
})

// 当前用户明文密码，缺失用占位 <pwd>（与 Users.vue 一致）。
const pwdOrPlaceholder = computed(() => selectedUser.value?.pwd || selectedUser.value?.password || '<pwd>')
const addrOrPlaceholder = computed(() => serverAddr.value || '<server-addr>')
const portOrPlaceholder = computed(() => socks5Port.value || '<socks5-port>')

// 格式1：socks5://<username>:<pwd>@<addr>:<port>
const proxyFormat1 = computed(() => {
  if (!username.value) return ''
  return `socks5://${username.value}:${pwdOrPlaceholder.value}@${addrOrPlaceholder.value}:${portOrPlaceholder.value}`
})
// 格式2：<addr>:<port>:<username>:<pwd>
const proxyFormat2 = computed(() => {
  if (!username.value) return ''
  return `${addrOrPlaceholder.value}:${portOrPlaceholder.value}:${username.value}:${pwdOrPlaceholder.value}`
})

// ===== 动态变量行增删（Type B）=====
function addVariable() {
  form.variables.push({ name: '', value: '' })
}
function removeVariable(idx) {
  form.variables.splice(idx, 1)
  // 至少保留一行，便于继续输入。
  if (form.variables.length === 0) form.variables.push({ name: '', value: '' })
}

// ===== 复制（DRY：与 Users.vue 同款剪贴板工具，含非安全上下文降级）=====
async function copyText(text) {
  if (!text) return
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    ElMessage.success(t('users.copied'))
  } catch {
    ElMessage.error(t('users.copyFailed'))
  }
}

// 复制前的前置校验提示：未选用户/组时给出明确提示，不复制非法串。
function copyFormat(text) {
  if (!selectedUser.value) return ElMessage.warning(t('generateProxy.selectUserWarn'))
  if (!selectedGroup.value) return ElMessage.warning(t('generateProxy.selectGroupWarn'))
  if (!text) return ElMessage.warning(t('generateProxy.incompleteWarn'))
  copyText(text)
}

onMounted(() => {
  loadAll()
  loadProxyContext()
})
</script>

<template>
  <div class="dp-page">
    <el-card v-loading="loading">
      <template #header>
        <div class="flex-between">
          <span>{{ t('generateProxy.title') }}</span>
        </div>
      </template>

      <el-form label-width="120px">
        <!-- 选择用户 -->
        <el-form-item :label="t('generateProxy.selectUser')">
          <el-select
            v-model="form.userId"
            filterable
            clearable
            :placeholder="t('generateProxy.selectUserPlaceholder')"
            style="width: 100%; max-width: 420px"
          >
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.id" />
          </el-select>
        </el-form-item>

        <!-- 选择代理组 -->
        <el-form-item :label="t('generateProxy.selectGroup')">
          <el-select
            v-model="form.groupId"
            filterable
            clearable
            :placeholder="t('generateProxy.selectGroupPlaceholder')"
            style="width: 100%; max-width: 420px"
          >
            <el-option v-for="g in groups" :key="g.id" :value="g.id" :label="`${g.name}（${t('groupType.' + g.type)}）`" />
          </el-select>
        </el-form-item>

        <!-- Type A：动态上游 4 字段 -->
        <template v-if="groupType === 'A'">
          <el-divider content-position="left">{{ t('generateProxy.upstreamTitle') }}</el-divider>
          <el-form-item :label="t('common.host')">
            <el-input v-model="form.upstream.host" :placeholder="t('generateProxy.upstreamHostPlaceholder')" style="max-width: 420px" />
          </el-form-item>
          <el-form-item :label="t('common.port')">
            <el-input-number v-model="form.upstream.port" :min="1" :max="65535" :controls="false" :placeholder="t('common.port')" style="width: 200px" />
          </el-form-item>
          <el-form-item :label="t('generateProxy.upstreamUser')">
            <el-input v-model="form.upstream.user" :placeholder="t('generateProxy.optionalAuth')" style="max-width: 420px" />
          </el-form-item>
          <el-form-item :label="t('generateProxy.upstreamPwd')">
            <el-input v-model="form.upstream.pwd" :placeholder="t('generateProxy.optionalAuth')" style="max-width: 420px" />
          </el-form-item>
        </template>

        <!-- Type B：动态命名变量 -->
        <template v-else-if="groupType === 'B'">
          <el-divider content-position="left">{{ t('generateProxy.variablesTitle') }}</el-divider>
          <el-form-item v-for="(v, idx) in form.variables" :key="idx" :label="idx === 0 ? t('generateProxy.variables') : ''">
            <div class="var-row">
              <el-input v-model="v.name" :placeholder="t('generateProxy.varNamePlaceholder')" style="width: 200px" />
              <span class="var-sep">_</span>
              <el-input v-model="v.value" :placeholder="t('generateProxy.varValuePlaceholder')" style="width: 200px" />
              <el-button link type="danger" :icon="'Delete'" @click="removeVariable(idx)" />
            </div>
          </el-form-item>
          <el-form-item label=" ">
            <el-button :icon="'Plus'" @click="addVariable">{{ t('generateProxy.addVariable') }}</el-button>
          </el-form-item>
        </template>
      </el-form>

      <!-- 生成结果：两种格式各一个只读框 + 复制按钮 -->
      <el-divider content-position="left">{{ t('generateProxy.resultTitle') }}</el-divider>
      <el-form label-width="120px">
        <el-form-item :label="t('generateProxy.addr1')">
          <div class="result-row">
            <el-input :model-value="proxyFormat1" readonly :placeholder="t('generateProxy.resultPlaceholder')" />
            <el-button type="primary" :icon="'CopyDocument'" @click="copyFormat(proxyFormat1)">{{ t('common.copy') }}</el-button>
          </div>
        </el-form-item>
        <el-form-item :label="t('generateProxy.addr2')">
          <div class="result-row">
            <el-input :model-value="proxyFormat2" readonly :placeholder="t('generateProxy.resultPlaceholder')" />
            <el-button type="primary" :icon="'CopyDocument'" @click="copyFormat(proxyFormat2)">{{ t('common.copy') }}</el-button>
          </div>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped lang="scss">
.flex-between {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.var-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.var-sep {
  color: var(--el-text-color-secondary);
  font-weight: 600;
}
.result-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  max-width: 640px;
}
</style>
