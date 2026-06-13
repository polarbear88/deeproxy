<script setup>
// 用户管理（AC-23/30）。已对齐 T7 实测契约（camelCase）：
// - 用户 { id, username, remark, groupIds }；创建/更新支持 groupIds（user→groups 授权方向）。
// 代理用户仅能连 SOCKS5 代理，不能登录后台。
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as userApi from '@/api/user'
import * as groupApi from '@/api/group'

const loading = ref(false)
const users = ref([])
const groups = ref([])

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

const dialog = reactive({ visible: false, isEdit: false, form: null })
function emptyForm() {
  return { id: null, username: '', password: '', remark: '', groupIds: [] }
}
function openCreate() {
  dialog.isEdit = false
  dialog.form = emptyForm()
  dialog.visible = true
}
function openEdit(row) {
  dialog.isEdit = true
  dialog.form = { ...emptyForm(), ...row, password: '' }
  dialog.visible = true
}
async function save() {
  const f = dialog.form
  if (!f.username) return ElMessage.warning('请输入用户名')
  if (!dialog.isEdit && !f.password) return ElMessage.warning('请设置密码')
  try {
    if (dialog.isEdit) {
      const payload = { username: f.username, remark: f.remark, groupIds: f.groupIds }
      if (f.password) payload.password = f.password
      await userApi.updateUser(f.id, payload)
    } else {
      await userApi.createUser({
        username: f.username,
        password: f.password,
        remark: f.remark,
        groupIds: f.groupIds,
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

function groupNames(ids) {
  if (!ids || ids.length === 0) return []
  return groups.value.filter((g) => ids.includes(g.id)).map((g) => g.name)
}

onMounted(loadAll)
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
            <el-tag v-for="n in groupNames(row.groupIds)" :key="n" size="small" class="mr4">{{ n }}</el-tag>
            <span v-if="groupNames(row.groupIds).length === 0" class="text-muted">未授权</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

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
        <el-form-item label="授权分组">
          <el-select v-model="dialog.form.groupIds" multiple placeholder="选择可访问分组" style="width: 100%">
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.mr4 {
  margin-right: 4px;
}
</style>
