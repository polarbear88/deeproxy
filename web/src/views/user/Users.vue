<script setup>
// 用户管理（AC-23/30 + AC-1.x + AC-4.3）。已对齐 T7 实测契约（camelCase）：
// - 用户 { id, username, remark, allGroups, groupIds }。
// - 授权与编辑彻底分离：编辑弹窗只管 用户名/密码/备注；授权由独立的"设置授权分组"按钮+弹窗承担。
// - allGroups（授权全部代理组）是独立布尔标志，与 groupIds 精细授权并存，互不清空（后端语义）。
// 代理用户仅能连 SOCKS5 代理，不能登录后台。
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as userApi from '@/api/user'
import * as groupApi from '@/api/group'
import { getServerInfo } from '@/api/system'

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
  if (!f.username) return ElMessage.warning('请输入用户名')
  if (!dialog.isEdit && !f.password) return ElMessage.warning('请设置密码')
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
    ElMessage.success('保存成功')
    dialog.visible = false
    loadAll()
  } catch {
    /* ignore */
  }
}
async function remove(row) {
  await ElMessageBox.confirm(`确认删除用户「${row.username}」？`, '提示', { type: 'warning' }).catch(() => 'cancel')
  try {
    await userApi.deleteUser(row.id)
    ElMessage.success('已删除')
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
    ElMessage.success('授权已保存')
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
async function copyProxyAddr(row) {
  const text = buildProxyAddr(row)
  try {
    // 优先 Clipboard API；非安全上下文（http）降级到 execCommand。
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
    ElMessage.success('代理地址已复制')
  } catch {
    ElMessage.error('复制失败，请手动复制')
  }
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
          <span>用户管理（代理用户）</span>
          <el-button type="primary" :icon="'Plus'" @click="openCreate">新建用户</el-button>
        </div>
      </template>

      <el-table v-loading="loading" :data="users" border empty-text="暂无代理用户">
        <el-table-column prop="username" label="用户名" min-width="160" />
        <el-table-column label="已授权分组" min-width="260">
          <template #default="{ row }">
            <el-tag v-if="row.allGroups" type="success" size="small" class="mr4">全部代理组</el-tag>
            <el-tag v-for="n in groupNames(row.groupIds)" :key="n" size="small" class="mr4">{{ n }}</el-tag>
            <span v-if="!row.allGroups && groupNames(row.groupIds).length === 0" class="text-muted">未授权</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="300" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openAuth(row)">设置授权分组</el-button>
            <el-button link type="primary" @click="copyProxyAddr(row)">复制代理地址</el-button>
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 编辑/新建用户弹窗：仅 用户名/密码/备注 -->
    <el-dialog v-model="dialog.visible" :title="dialog.isEdit ? '编辑用户' : '新建用户'" width="520px">
      <el-form v-if="dialog.form" :model="dialog.form" label-width="100px">
        <el-form-item label="用户名" required>
          <el-input v-model="dialog.form.username" :disabled="dialog.isEdit" placeholder="代理用户名" />
        </el-form-item>
        <el-form-item :label="dialog.isEdit ? '重置密码' : '密码'" :required="!dialog.isEdit">
          <el-input
            v-model="dialog.form.password"
            type="password"
            show-password
            :placeholder="dialog.isEdit ? '留空表示不修改' : '设置密码'"
          />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="dialog.form.remark" placeholder="可选备注" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- 独立授权弹窗（与编辑用户分离）-->
    <el-dialog v-model="authDialog.visible" :title="`设置授权分组 - ${authDialog.username}`" width="520px">
      <el-form label-width="120px">
        <el-form-item label="授权全部代理组">
          <el-switch v-model="authDialog.allGroups" />
          <span class="text-muted hint">开启后该用户可访问所有代理组（仍保留下方逐组授权）</span>
        </el-form-item>
        <el-form-item label="逐组授权">
          <el-select
            v-model="authDialog.groupIds"
            multiple
            clearable
            placeholder="选择可访问分组"
            style="width: 100%"
          >
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="authDialog.visible = false">取消</el-button>
        <el-button type="primary" :loading="authDialog.saving" @click="saveAuth">保存授权</el-button>
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
