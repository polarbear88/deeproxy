<script setup>
// 用户管理（AC-23/30 + AC-1.x + AC-4.3）。已对齐 T7 实测契约（camelCase）：
// - 用户 { id, username, remark, allGroups, groupIds }。
// - 授权与编辑彻底分离：编辑弹窗只管 用户名/密码/备注；授权由独立的"设置授权分组"按钮+弹窗承担。
// - allGroups（授权全部代理组）是独立布尔标志，与 groupIds 精细授权并存，互不清空（后端语义）。
// 代理用户仅能连 SOCKS5 代理，不能登录后台。
import { onMounted, reactive, ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import * as userApi from '@/api/user'
import * as groupApi from '@/api/group'
import { getServerInfo } from '@/api/system'

// i18n：组件⑦用户名校验错误文案经 t() 翻译
const { t } = useI18n()

const loading = ref(false)
const users = ref([])
const groups = ref([])

// 复制代理地址所需的全局上下文：服务器域名/IP 与 SOCKS5 监听端口（来自专用端点 /settings/server-info）。
const serverAddr = ref('')
const socks5Port = ref(0)

async function loadAll() {
  loading.value = true
  try {
    users.value = (await userApi.listUsers()) || []
  } catch {
    users.value = []
  } finally {
    loading.value = false
  }
  try {
    groups.value = (await groupApi.listGroups()) || []
  } catch {
    groups.value = []
  }
}

// 拉取"复制代理地址"所需的服务器地址与端口（专用端点）；失败时静默降级为占位符。
async function loadProxyContext() {
  try {
    const info = await getServerInfo()
    if (info) {
      serverAddr.value = info.serverAddr || ''
      socks5Port.value = info.socks5Port || 0
    }
  } catch {
    /* ignore */
  }
}

// ===== 编辑/新建用户弹窗（仅 用户名/密码/备注，不含授权）=====
const dialog = reactive({ visible: false, isEdit: false, form: null })
// 组件⑦：用户名输入校验。仅 ^[A-Za-z0-9]+$（与后端 ValidIdentifier 同规则）。
// 编辑态 username 只读（:disabled），故该规则实际只在新建态触发。
const userFormRef = ref(null)
const userRules = computed(() => ({
  username: [
    { required: true, message: t('validate.required'), trigger: 'blur' },
    { pattern: /^[A-Za-z0-9]+$/, message: t('validate.alnum'), trigger: 'blur' },
  ],
}))
function emptyForm() {
  return { id: null, username: '', password: '', remark: '' }
}
function openCreate() {
  dialog.isEdit = false
  dialog.form = emptyForm()
  dialog.visible = true
}
function openEdit(row) {
  dialog.isEdit = true
  dialog.form = { ...emptyForm(), id: row.id, username: row.username, remark: row.remark, password: '' }
  dialog.visible = true
}
async function save() {
  const f = dialog.form
  // 组件⑦：提交前先跑表单校验（用户名必填 + ^[A-Za-z0-9]+$），不通过则中止
  const ok = await userFormRef.value?.validate().catch(() => false)
  if (!ok) return
  if (!dialog.isEdit && !f.password) return ElMessage.warning(t('users.setPasswordWarn'))
  try {
    if (dialog.isEdit) {
      // 编辑不再下发 groupIds，授权交由独立弹窗，避免误清空授权。
      const payload = { username: f.username, remark: f.remark }
      if (f.password) payload.password = f.password
      await userApi.updateUser(f.id, payload)
    } else {
      await userApi.createUser({
        username: f.username,
        password: f.password,
        remark: f.remark,
      })
    }
    ElMessage.success(t('common.saveSuccess'))
    dialog.visible = false
    loadAll()
  } catch {
    /* ignore */
  }
}
async function remove(row) {
  // 二次确认：取消即返回，避免未点确认就删除用户。
  const ok = await ElMessageBox.confirm(t('users.deleteConfirm', { name: row.username }), t('common.notice'), { type: 'warning' }).catch(() => false)
  if (!ok) return
  try {
    await userApi.deleteUser(row.id)
    ElMessage.success(t('common.deleteSuccess'))
    loadAll()
  } catch {
    /* ignore */
  }
}

// ===== 独立"设置授权分组"弹窗（AC-1.1/1.2/1.3）=====
// 与编辑用户完全分离：含"授权全部代理组"开关 + 关闭时逐组多选。
const authDialog = reactive({ visible: false, saving: false, userId: null, username: '', allGroups: false, groupIds: [] })
// 打开授权弹窗：从当前行回显 allGroups 与 groupIds（修复"设置后仍显示未授权"）。
function openAuth(row) {
  authDialog.userId = row.id
  authDialog.username = row.username
  authDialog.allGroups = !!row.allGroups
  // 深拷贝，避免直接引用行内数组导致取消后仍变更原数据
  authDialog.groupIds = Array.isArray(row.groupIds) ? [...row.groupIds] : []
  authDialog.visible = true
}
async function saveAuth() {
  authDialog.saving = true
  try {
    await userApi.setUserGroups(authDialog.userId, {
      allGroups: authDialog.allGroups,
      // 即便开启"全部"，仍按语义并存保留逐组精细授权
      groupIds: authDialog.groupIds,
    })
    ElMessage.success(t('users.authSaved'))
    authDialog.visible = false
    loadAll()
  } catch {
    /* ignore */
  } finally {
    authDialog.saving = false
  }
}

function groupNames(ids) {
  if (!ids || ids.length === 0) return []
  return groups.value.filter((g) => ids.includes(g.id)).map((g) => g.name)
}

// ===== 复制代理地址（AC-4.3）=====
// 复制 socks5://<user>-{group}:<pwd>@<server-addr>:<socks5-port>。
// {group} 为字面占位符（提示用户把它替换成目标代理组名）；<pwd> 取真实代理密码，
// 若后端列表未回明文密码则用占位 <pwd> 提示用户自行替换。
function buildProxyAddr(row) {
  const addr = serverAddr.value || '<server-addr>'
  const port = socks5Port.value || '<socks5-port>'
  const pwd = row.pwd || row.password || '<pwd>'
  return `socks5://${row.username}-{group}:${pwd}@${addr}:${port}`
}
// 新增格式 addr:port:user-group:pwd（如 192.168.1.1:1080:alice-{group}:pass）。
// 字段来源与 socks5:// 完全同源、沿用相同缺失占位（<server-addr>/<socks5-port>/<pwd>），
// {group} 仍为字面占位提示替换为目标代理组名；新增「可选」格式，原 socks5:// 保留不变。
function buildProxyAddr2(row) {
  const addr = serverAddr.value || '<server-addr>'
  const port = socks5Port.value || '<socks5-port>'
  const pwd = row.pwd || row.password || '<pwd>'
  return `${addr}:${port}:${row.username}-{group}:${pwd}`
}
// copyText：DRY 抽取的剪贴板写入工具，两种复制格式共用，避免重复实现复制逻辑。
// 优先 Clipboard API；非安全上下文（http）降级到 execCommand。
async function copyText(text) {
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
function copyProxyAddr(row) {
  copyText(buildProxyAddr(row))
}
function copyProxyAddr2(row) {
  copyText(buildProxyAddr2(row))
}

// 操作列下拉菜单的命令分发：把原先并排的多个按钮收敛为单一下拉入口。
function onAction(cmd, row) {
  if (cmd === 'auth') openAuth(row)
  else if (cmd === 'copy') copyProxyAddr(row)
  else if (cmd === 'copy2') copyProxyAddr2(row)
  else if (cmd === 'edit') openEdit(row)
  else if (cmd === 'delete') remove(row)
}

onMounted(() => {
  loadAll()
  loadProxyContext()
})
</script>

<template>
  <div class="dp-page">
    <el-card>
      <template #header>
        <div class="flex-between">
          <span>{{ t('users.title') }}</span>
          <el-button type="primary" @click="openCreate"><el-icon><Plus /></el-icon>{{ t('users.create') }}</el-button>
        </div>
      </template>

      <el-table v-loading="loading" :data="users" border :empty-text="t('users.emptyUsers')">
        <el-table-column prop="username" :label="t('users.username')" min-width="160" />
        <el-table-column :label="t('users.authedGroups')" min-width="260">
          <template #default="{ row }">
            <el-tag v-if="row.allGroups" type="success" size="small" class="mr4">{{ t('users.allGroups') }}</el-tag>
            <el-tag v-for="n in groupNames(row.groupIds)" :key="n" size="small" class="mr4">{{ n }}</el-tag>
            <span v-if="!row.allGroups && groupNames(row.groupIds).length === 0" class="text-muted">{{ t('users.notAuthed') }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="120" fixed="right">
          <template #default="{ row }">
            <!-- 操作按钮收进下拉菜单，避免操作列过于杂乱 -->
            <el-dropdown @command="(cmd) => onAction(cmd, row)">
              <el-button link type="primary">
                {{ t('common.actions') }}<el-icon class="el-icon--right"><ArrowDown /></el-icon>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="auth">{{ t('users.setAuth') }}</el-dropdown-item>
                  <el-dropdown-item command="copy">{{ t('users.copyProxyAddr') }}</el-dropdown-item>
                  <el-dropdown-item command="copy2">{{ t('users.copyProxyAddr2') }}</el-dropdown-item>
                  <el-dropdown-item command="edit">{{ t('common.edit') }}</el-dropdown-item>
                  <el-dropdown-item command="delete" divided>
                    <span style="color: var(--el-color-danger)">{{ t('common.delete') }}</span>
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 编辑/新建用户弹窗：仅 用户名/密码/备注 -->
    <el-dialog v-model="dialog.visible" :title="dialog.isEdit ? t('users.editUser') : t('users.createUser')" width="520px">
      <el-form v-if="dialog.form" ref="userFormRef" :model="dialog.form" :rules="userRules" label-width="100px">
        <el-form-item :label="t('users.username')" prop="username">
          <el-input v-model="dialog.form.username" :disabled="dialog.isEdit" :placeholder="t('users.usernamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="dialog.isEdit ? t('users.resetPassword') : t('users.password')" :required="!dialog.isEdit">
          <el-input
            v-model="dialog.form.password"
            type="password"
            show-password
            :placeholder="dialog.isEdit ? t('users.pwdEditPlaceholder') : t('users.pwdCreatePlaceholder')"
          />
        </el-form-item>
        <el-form-item :label="t('users.remark')">
          <el-input v-model="dialog.form.remark" :placeholder="t('users.optionalRemark')" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="save">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <!-- 独立授权弹窗（与编辑用户分离）-->
    <el-dialog v-model="authDialog.visible" :title="t('users.authDialogTitle', { name: authDialog.username })" width="520px">
      <el-form label-width="120px">
        <el-form-item :label="t('users.authAllGroups')">
          <el-switch v-model="authDialog.allGroups" />
          <span class="text-muted hint">{{ t('users.authAllHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('users.authPerGroup')">
          <el-select
            v-model="authDialog.groupIds"
            multiple
            clearable
            :placeholder="t('users.selectAccessibleGroups')"
            style="width: 100%"
          >
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="authDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="authDialog.saving" @click="saveAuth">{{ t('users.saveAuth') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.mr4 {
  margin-right: 4px;
}
.hint {
  margin-left: 10px;
  font-size: 12px;
}
</style>
