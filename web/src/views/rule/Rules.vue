<script setup>
// 规则管理（AC-22/29/39）。已对齐 T7 实测契约（camelCase）：
// - RuleGroup: { id,name,scope,groupIds,groups:[{id,name}],ruleCount }；应用到分组用 setRuleGroupGroups。
// - Rule: { id,match,action,order }。
// - 测试器 /rule-groups/test {target,groupId} → {action,matchedRule,fromGroup,matched,sniffNote}。
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as ruleApi from '@/api/rule'
import * as groupApi from '@/api/group'

const loading = ref(false)
const ruleGroups = ref([])
const groups = ref([])

async function loadAll() {
  loading.value = true
  try {
    ruleGroups.value = (await ruleApi.listRuleGroups()) || []
  } catch {
    ruleGroups.value = []
  } finally {
    loading.value = false
  }
  try {
    groups.value = (await groupApi.listGroups()) || []
  } catch {
    groups.value = []
  }
}

// ===== 规则组编辑（name + scope + 应用分组）=====
const rgDialog = reactive({ visible: false, isEdit: false, form: null })
function emptyRg() {
  return { id: null, name: '', scope: 'global', groupIds: [] }
}
function openCreateRg() {
  rgDialog.isEdit = false
  rgDialog.form = emptyRg()
  rgDialog.visible = true
}
function openEditRg(row) {
  rgDialog.isEdit = true
  rgDialog.form = JSON.parse(JSON.stringify({ ...emptyRg(), ...row }))
  rgDialog.visible = true
}
async function saveRg() {
  const f = rgDialog.form
  if (!f.name) return ElMessage.warning('请输入规则组名称')
  try {
    let id = f.id
    if (rgDialog.isEdit) {
      await ruleApi.updateRuleGroup(id, { name: f.name, scope: f.scope })
    } else {
      const created = await ruleApi.createRuleGroup({ name: f.name, scope: f.scope })
      id = created?.id
    }
    // scope=group 时设置应用到的分组（覆盖式）。
    if (f.scope === 'group' && id) {
      await ruleApi.setRuleGroupGroups(id, f.groupIds || [])
    }
    ElMessage.success('保存成功')
    rgDialog.visible = false
    loadAll()
  } catch {
    /* ignore */
  }
}
async function removeRg(row) {
  await ElMessageBox.confirm(`确认删除规则组「${row.name}」？`, '提示', { type: 'warning' }).catch(() => 'cancel')
  try {
    await ruleApi.deleteRuleGroup(row.id)
    ElMessage.success('已删除')
    loadAll()
  } catch {
    /* ignore */
  }
}

// ===== 规则编辑（抽屉内）=====
const ruleDrawer = reactive({ visible: false, rg: null, list: [] })
async function openRules(rg) {
  ruleDrawer.rg = rg
  ruleDrawer.visible = true
  await loadRules(rg.id)
}
async function loadRules(rgId) {
  try {
    ruleDrawer.list = (await ruleApi.listRules(rgId)) || []
  } catch {
    ruleDrawer.list = []
  }
}
const ruleDialog = reactive({ visible: false, isEdit: false, form: null })
function emptyRule() {
  return { id: null, matchType: 'domain-suffix', matchValue: '', action: 'forward', order: 0 }
}
function openCreateRule() {
  ruleDialog.isEdit = false
  ruleDialog.form = emptyRule()
  ruleDialog.visible = true
}
function openEditRule(row) {
  const [matchType, ...rest] = (row.match || '').split(':')
  ruleDialog.isEdit = true
  ruleDialog.form = { ...emptyRule(), ...row, matchType: matchType || 'domain-suffix', matchValue: rest.join(':') }
  ruleDialog.visible = true
}
async function saveRule() {
  const f = ruleDialog.form
  if (!f.matchValue) return ElMessage.warning('请输入匹配值')
  const payload = { match: `${f.matchType}:${f.matchValue}`, action: f.action, order: f.order }
  try {
    if (ruleDialog.isEdit) await ruleApi.updateRule(ruleDrawer.rg.id, f.id, payload)
    else await ruleApi.createRule(ruleDrawer.rg.id, payload)
    ElMessage.success('保存成功')
    ruleDialog.visible = false
    loadRules(ruleDrawer.rg.id)
  } catch {
    /* ignore */
  }
}
async function removeRule(row) {
  await ElMessageBox.confirm('确认删除该规则？', '提示', { type: 'warning' }).catch(() => 'cancel')
  try {
    await ruleApi.deleteRule(ruleDrawer.rg.id, row.id)
    ElMessage.success('已删除')
    loadRules(ruleDrawer.rg.id)
  } catch {
    /* ignore */
  }
}

// ===== 规则测试器（AC-39，传 groupId）=====
const tester = reactive({ visible: false, target: '', groupId: null, result: null })
function openTester() {
  tester.result = null
  tester.visible = true
}
async function runTest() {
  if (!tester.target) return ElMessage.warning('请输入域名或 IP')
  if (!tester.groupId) return ElMessage.warning('请选择分组')
  try {
    tester.result = await ruleApi.testRule({ target: tester.target, groupId: tester.groupId })
  } catch {
    /* ignore */
  }
}

function actionTag(action) {
  return action === 'forward' ? 'success' : action === 'direct' ? 'primary' : 'danger'
}

onMounted(loadAll)
</script>

<template>
  <div class="dp-page">
    <el-card>
      <template #header>
        <div class="flex-between">
          <span>规则管理</span>
          <div>
            <el-button :icon="'MagicStick'" @click="openTester">规则测试器</el-button>
            <el-button type="primary" :icon="'Plus'" @click="openCreateRg">新建规则组</el-button>
          </div>
        </div>
      </template>

      <el-table v-loading="loading" :data="ruleGroups" border empty-text="暂无规则组">
        <el-table-column prop="name" label="规则组名称" min-width="160" />
        <el-table-column label="作用域" width="160">
          <template #default="{ row }">
            <el-tag :type="row.scope === 'global' ? 'danger' : 'primary'" effect="plain">
              {{ row.scope === 'global' ? '全局（优先）' : '分组' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="应用分组" min-width="200">
          <template #default="{ row }">
            <template v-if="row.scope === 'global'"><span class="text-muted">全部分组</span></template>
            <template v-else>
              <el-tag v-for="g in row.groups || []" :key="g.id" class="mr4" size="small">{{ g.name }}</el-tag>
              <span v-if="!row.groups || row.groups.length === 0" class="text-muted">未应用</span>
            </template>
          </template>
        </el-table-column>
        <el-table-column label="规则数" width="90" prop="ruleCount" />
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openRules(row)">规则</el-button>
            <el-button link type="primary" @click="openEditRg(row)">编辑</el-button>
            <el-button link type="danger" @click="removeRg(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 规则组编辑 -->
    <el-dialog v-model="rgDialog.visible" :title="rgDialog.isEdit ? '编辑规则组' : '新建规则组'" width="520px">
      <el-form v-if="rgDialog.form" :model="rgDialog.form" label-width="100px">
        <el-form-item label="名称" required>
          <el-input v-model="rgDialog.form.name" />
        </el-form-item>
        <el-form-item label="作用域">
          <el-radio-group v-model="rgDialog.form.scope">
            <el-radio value="global">全局</el-radio>
            <el-radio value="group">分组</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="rgDialog.form.scope === 'group'" label="应用分组">
          <el-select v-model="rgDialog.form.groupIds" multiple placeholder="选择分组" style="width: 100%">
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="rgDialog.visible = false">取消</el-button>
        <el-button type="primary" @click="saveRg">保存</el-button>
      </template>
    </el-dialog>

    <!-- 规则抽屉 -->
    <el-drawer v-model="ruleDrawer.visible" :title="`规则 - ${ruleDrawer.rg?.name || ''}`" size="55%">
      <div class="flex-between drawer-toolbar">
        <span class="text-muted">组内按顺序(order)首匹配；全局组优先于分组组。</span>
        <el-button type="primary" size="small" :icon="'Plus'" @click="openCreateRule">添加规则</el-button>
      </div>
      <el-table :data="ruleDrawer.list" border size="small" empty-text="暂无规则">
        <el-table-column prop="order" label="顺序" width="70" />
        <el-table-column prop="match" label="匹配" min-width="220" />
        <el-table-column label="动作" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="actionTag(row.action)" effect="plain">{{ row.action }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openEditRule(row)">编辑</el-button>
            <el-button link type="danger" @click="removeRule(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>

    <!-- 规则编辑 -->
    <el-dialog v-model="ruleDialog.visible" :title="ruleDialog.isEdit ? '编辑规则' : '添加规则'" width="520px">
      <el-form v-if="ruleDialog.form" :model="ruleDialog.form" label-width="100px">
        <el-form-item label="匹配类型">
          <el-select v-model="ruleDialog.form.matchType">
            <el-option label="domain（精确域名）" value="domain" />
            <el-option label="domain-suffix（域名后缀）" value="domain-suffix" />
            <el-option label="ip-cidr（IP/CIDR）" value="ip-cidr" />
          </el-select>
        </el-form-item>
        <el-form-item label="匹配值" required>
          <el-input v-model="ruleDialog.form.matchValue" placeholder="如 google.com 或 192.168.0.0/16" />
        </el-form-item>
        <el-form-item label="动作">
          <el-radio-group v-model="ruleDialog.form.action">
            <el-radio value="forward">forward</el-radio>
            <el-radio value="direct">direct</el-radio>
            <el-radio value="reject">reject</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="顺序">
          <el-input-number v-model="ruleDialog.form.order" :min="0" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="ruleDialog.visible = false">取消</el-button>
        <el-button type="primary" @click="saveRule">保存</el-button>
      </template>
    </el-dialog>

    <!-- 规则测试器 -->
    <el-dialog v-model="tester.visible" title="规则测试器" width="560px">
      <el-form label-width="100px">
        <el-form-item label="目标">
          <el-input v-model="tester.target" placeholder="域名或 IP" />
        </el-form-item>
        <el-form-item label="分组">
          <el-select v-model="tester.groupId" placeholder="选择分组" style="width: 100%">
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <div v-if="tester.result" class="test-result">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item label="命中规则">
            {{ tester.result.matchedRule || '未命中（走默认动作）' }}
          </el-descriptions-item>
          <el-descriptions-item label="来源">{{ tester.result.fromGroup || '-' }}</el-descriptions-item>
          <el-descriptions-item label="最终动作">
            <el-tag :type="actionTag(tester.result.action)" effect="plain">{{ tester.result.action }}</el-tag>
          </el-descriptions-item>
        </el-descriptions>
        <el-alert
          v-if="tester.result.sniffNote"
          class="test-note"
          type="warning"
          :closable="false"
          show-icon
          :title="tester.result.sniffNote"
        />
      </div>
      <template #footer>
        <el-button @click="tester.visible = false">关闭</el-button>
        <el-button type="primary" @click="runTest">测试</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.drawer-toolbar {
  margin-bottom: 12px;
}
.mr4 {
  margin-right: 4px;
}
.test-result {
  margin-top: 14px;
}
.test-note {
  margin-top: 12px;
}
</style>
