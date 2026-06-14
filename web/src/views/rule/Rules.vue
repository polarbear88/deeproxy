<script setup>
// 规则管理（AC-22/29/39）。已对齐 T7 实测契约（camelCase）：
// - RuleGroup: { id,name,scope,groupIds,groups:[{id,name}],ruleCount }；应用到分组用 setRuleGroupGroups。
// - Rule: { id,match,action,order }。
// - 测试器 /rule-groups/test {target,groupId} → {action,matchedRule,fromGroup,matched,sniffNote}。
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as ruleApi from '@/api/rule'
import * as groupApi from '@/api/group'
import { useAppStore } from '@/stores/app'

// i18n：仅展示层翻译，数据层（match/action 等）始终保持原始英文值（API/DB 不污染）
const { t } = useI18n()
// 应用 store：读 isMobile 控制规则抽屉在手机端的宽度
const appStore = useAppStore()

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
  if (!f.name) return ElMessage.warning(t('rules.rgNameRequired'))
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
    ElMessage.success(t('common.saveSuccess'))
    rgDialog.visible = false
    loadAll()
  } catch {
    /* ignore */
  }
}
async function removeRg(row) {
  await ElMessageBox.confirm(t('rules.deleteRgConfirm', { name: row.name }), t('common.notice'), { type: 'warning' }).catch(() => 'cancel')
  try {
    await ruleApi.deleteRuleGroup(row.id)
    ElMessage.success(t('common.deleteSuccess'))
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
  if (!f.matchValue) return ElMessage.warning(t('rules.enterMatchValueWarn'))
  const payload = { match: `${f.matchType}:${f.matchValue}`, action: f.action, order: f.order }
  try {
    if (ruleDialog.isEdit) await ruleApi.updateRule(ruleDrawer.rg.id, f.id, payload)
    else await ruleApi.createRule(ruleDrawer.rg.id, payload)
    ElMessage.success(t('common.saveSuccess'))
    ruleDialog.visible = false
    loadRules(ruleDrawer.rg.id)
  } catch {
    /* ignore */
  }
}
async function removeRule(row) {
  await ElMessageBox.confirm(t('rules.deleteRuleConfirm'), t('common.notice'), { type: 'warning' }).catch(() => 'cancel')
  try {
    await ruleApi.deleteRule(ruleDrawer.rg.id, row.id)
    ElMessage.success(t('common.deleteSuccess'))
    loadRules(ruleDrawer.rg.id)
  } catch {
    /* ignore */
  }
}

// ===== 规则导入 / 导出（规则组维度，抽屉内操作）=====
// 导出：把当前规则组的规则列表（match/action/order，剔除 id 等内部字段）下载为 JSON 文件。
async function exportRules() {
  const rg = ruleDrawer.rg
  if (!rg) return
  // 仅导出与业务相关的字段，保持与导入格式对称；schemaVersion 便于未来兼容演进。
  const rules = (ruleDrawer.list || []).map((r) => ({ match: r.match, action: r.action, order: r.order }))
  const payload = { schemaVersion: 1, ruleGroup: rg.name, rules }
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `deeproxy-rules-${rg.name}-${Date.now()}.json`
  a.click()
  URL.revokeObjectURL(url)
  ElMessage.success(t('rules.exported'))
}

// 导入：读取 JSON 文件，逐条 createRule 到当前规则组，最后汇报成功/失败条数。
const importingRules = ref(false)
function onImportRules(uploadFile) {
  const rg = ruleDrawer.rg
  if (!rg) return false
  const file = uploadFile.raw || uploadFile
  const reader = new FileReader()
  reader.onload = async () => {
    let parsed
    try {
      parsed = JSON.parse(reader.result)
    } catch {
      ElMessage.error(t('rules.importInvalidJson'))
      return
    }
    // 兼容两种格式：{rules:[...]} 包装，或裸数组 [...]。
    const list = Array.isArray(parsed) ? parsed : parsed.rules
    if (!Array.isArray(list) || list.length === 0) {
      ElMessage.error(t('rules.importNoRules'))
      return
    }
    await ElMessageBox.confirm(t('rules.importConfirm', { count: list.length }), t('common.notice'), {
      type: 'warning',
    }).catch(() => Promise.reject())
    importingRules.value = true
    let ok = 0
    let failed = 0
    try {
      // 逐条创建：单条非法（缺 match/action）跳过并计入失败，不中断整体导入。
      for (const r of list) {
        if (!r || !r.match || !r.action) {
          failed++
          continue
        }
        try {
          await ruleApi.createRule(rg.id, { match: r.match, action: r.action, order: r.order ?? 0 })
          ok++
        } catch {
          failed++
        }
      }
      ElMessage.success(t('rules.importDone', { ok, failed }))
      loadRules(rg.id)
    } finally {
      importingRules.value = false
    }
  }
  reader.readAsText(file)
  return false
}

// ===== 规则测试器（AC-39，传 groupId）=====
const tester = reactive({ visible: false, target: '', groupId: null, result: null })
function openTester() {
  tester.result = null
  tester.visible = true
}
async function runTest() {
  if (!tester.target) return ElMessage.warning(t('rules.enterTargetWarn'))
  if (!tester.groupId) return ElMessage.warning(t('rules.selectGroupWarn'))
  try {
    tester.result = await ruleApi.testRule({ target: tester.target, groupId: tester.groupId })
  } catch {
    /* ignore */
  }
}

function actionTag(action) {
  return action === 'forward' ? 'success' : action === 'direct' ? 'primary' : 'danger'
}

// 匹配串展示：将 "type:value" 的类型前缀翻译为本地化标签，value 原样保留。
// 用 indexOf(':') 而非 split，避免 ip-cidr 的 IPv6/CIDR（含多个冒号）被错误切分。
function matchLabel(match) {
  if (!match) return ''
  const i = match.indexOf(':')
  if (i < 0) return match
  const type = match.slice(0, i)
  const value = match.slice(i + 1)
  return `${t('matchType.' + type)}: ${value}`
}

onMounted(loadAll)
</script>

<template>
  <div class="dp-page">
    <el-card>
      <template #header>
        <div class="flex-between">
          <span>{{ t('rules.title') }}</span>
          <div>
            <el-button :icon="'MagicStick'" @click="openTester">{{ t('rules.tester') }}</el-button>
            <el-button type="primary" :icon="'Plus'" @click="openCreateRg">{{ t('common.add') }}</el-button>
          </div>
        </div>
      </template>

      <el-table v-loading="loading" :data="ruleGroups" border :empty-text="t('rules.emptyRuleGroups')">
        <el-table-column prop="name" :label="t('rules.rgName')" min-width="160" />
        <el-table-column :label="t('rules.scope')" width="160">
          <template #default="{ row }">
            <el-tag :type="row.scope === 'global' ? 'danger' : 'primary'" effect="plain">
              {{ row.scope === 'global' ? t('rules.scopeGlobal') : t('rules.scopeGroup') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('rules.applyGroups')" min-width="200">
          <template #default="{ row }">
            <template v-if="row.scope === 'global'"><span class="text-muted">{{ t('rules.allGroups') }}</span></template>
            <template v-else>
              <el-tag v-for="g in row.groups || []" :key="g.id" class="mr4" size="small">{{ g.name }}</el-tag>
              <span v-if="!row.groups || row.groups.length === 0" class="text-muted">{{ t('rules.notApplied') }}</span>
            </template>
          </template>
        </el-table-column>
        <el-table-column :label="t('rules.ruleCount')" width="90" prop="ruleCount" />
        <el-table-column :label="t('common.actions')" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openRules(row)">{{ t('rules.btnRules') }}</el-button>
            <el-button link type="primary" @click="openEditRg(row)">{{ t('common.edit') }}</el-button>
            <el-button link type="danger" @click="removeRg(row)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 规则组编辑 -->
    <el-dialog v-model="rgDialog.visible" :title="rgDialog.isEdit ? t('rules.editRuleGroup') : t('rules.createRuleGroup')" width="520px">
      <el-form v-if="rgDialog.form" :model="rgDialog.form" label-width="100px">
        <el-form-item :label="t('common.name')" required>
          <el-input v-model="rgDialog.form.name" />
        </el-form-item>
        <el-form-item :label="t('rules.scope')">
          <el-radio-group v-model="rgDialog.form.scope">
            <el-radio value="global">{{ t('rules.scopeGlobalShort') }}</el-radio>
            <el-radio value="group">{{ t('rules.scopeGroup') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="rgDialog.form.scope === 'group'" :label="t('rules.applyGroups')">
          <el-select v-model="rgDialog.form.groupIds" multiple :placeholder="t('rules.selectGroups')" style="width: 100%">
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="rgDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="saveRg">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <!-- 规则抽屉 -->
    <el-drawer v-model="ruleDrawer.visible" :title="t('rules.drawerTitle', { name: ruleDrawer.rg?.name || '' })" :size="appStore.isMobile ? '90%' : '55%'">
      <div class="flex-between drawer-toolbar">
        <span class="text-muted">{{ t('rules.orderHint') }}</span>
        <div class="flex-row" style="gap: 8px">
          <el-button size="small" :icon="'Download'" @click="exportRules">{{ t('rules.exportRules') }}</el-button>
          <el-upload :show-file-list="false" :before-upload="onImportRules" accept=".json">
            <el-button size="small" :icon="'Upload'" :loading="importingRules">{{ t('rules.importRules') }}</el-button>
          </el-upload>
          <el-button type="primary" size="small" :icon="'Plus'" @click="openCreateRule">{{ t('rules.addRule') }}</el-button>
        </div>
      </div>
      <el-table :data="ruleDrawer.list" border size="small" :empty-text="t('rules.emptyRules')">
        <el-table-column prop="order" :label="t('rules.order')" width="70" />
        <el-table-column :label="t('rules.matchCol')" min-width="220">
          <template #default="{ row }">{{ matchLabel(row.match) }}</template>
        </el-table-column>
        <el-table-column :label="t('rules.actionCol')" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="actionTag(row.action)" effect="plain">{{ t('action.' + row.action) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="150" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openEditRule(row)">{{ t('common.edit') }}</el-button>
            <el-button link type="danger" @click="removeRule(row)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>

    <!-- 规则编辑 -->
    <el-dialog v-model="ruleDialog.visible" :title="ruleDialog.isEdit ? t('rules.editRule') : t('rules.createRule')" width="520px">
      <el-form v-if="ruleDialog.form" :model="ruleDialog.form" label-width="100px">
        <el-form-item :label="t('rules.matchType')">
          <el-select v-model="ruleDialog.form.matchType">
            <el-option :label="t('matchType.domain')" value="domain" />
            <el-option :label="t('matchType.domain-suffix')" value="domain-suffix" />
            <el-option :label="t('matchType.ip-cidr')" value="ip-cidr" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('rules.matchValue')" required>
          <el-input v-model="ruleDialog.form.matchValue" :placeholder="t('rules.matchValuePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('rules.actionCol')">
          <el-radio-group v-model="ruleDialog.form.action">
            <el-radio value="forward">{{ t('action.forward') }}</el-radio>
            <el-radio value="direct">{{ t('action.direct') }}</el-radio>
            <el-radio value="reject">{{ t('action.reject') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('rules.order')">
          <el-input-number v-model="ruleDialog.form.order" :min="0" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="ruleDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="saveRule">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <!-- 规则测试器 -->
    <el-dialog v-model="tester.visible" :title="t('rules.tester')" width="560px">
      <el-form label-width="100px">
        <el-form-item :label="t('rules.testTarget')">
          <el-input v-model="tester.target" :placeholder="t('rules.testTargetPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('rules.testGroup')">
          <el-select v-model="tester.groupId" :placeholder="t('rules.testGroupPlaceholder')" style="width: 100%">
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <div v-if="tester.result" class="test-result">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item :label="t('rules.hitRule')">
            {{ tester.result.matchedRule || t('rules.noHit') }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('rules.source')">{{ tester.result.fromGroup || '-' }}</el-descriptions-item>
          <el-descriptions-item :label="t('rules.testResult')">
            <el-tag :type="actionTag(tester.result.action)" effect="plain">{{ t('action.' + tester.result.action) }}</el-tag>
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
        <el-button @click="tester.visible = false">{{ t('common.close') }}</el-button>
        <el-button type="primary" @click="runTest">{{ t('common.test') }}</el-button>
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
