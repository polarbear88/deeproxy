<script setup>
// 代理组管理（AC-21/28，G2）。已对齐 T7 实测契约（camelCase DTO）：
// - Group: { id,name,remark,type,healthCheck:{enabled,mode,url,intervalSec,failThreshold,recoverThreshold},todayUp,todayDown,todayReq }
// - Upstream: { id,host,port,user,pwd,weight,enabled,healthState:'healthy'|'unhealthy'|'unknown',latencyMs }
// - Type A 隐藏代理池与健康检查 UI（G2）。
import { onMounted, reactive, ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import EChart from '@/components/EChart.js'
import * as groupApi from '@/api/group'
import * as dashApi from '@/api/dashboard'
import { formatBytes, formatNumber, formatBucketTime, formatAxisTime } from '@/utils/format'
import { useAppStore } from '@/stores/app'

// i18n：组件③类型改名、组件⑤「流量」按钮、组件⑦校验文案均经 t() 翻译
const { t } = useI18n()
// 应用 store：读 isMobile 控制抽屉在手机端的宽度（结构尺寸走 JS 真相源）
const appStore = useAppStore()

const loading = ref(false)
const groups = ref([])

// ===== 组编辑对话框 =====
const groupDialog = reactive({ visible: false, isEdit: false, form: null })
// 组件⑦：分组名输入校验。仅 ^[A-Za-z0-9]+$（与后端 ValidIdentifier 同规则），
// 编辑态名字仍可改，故新建/编辑都会触发该校验。
const groupFormRef = ref(null)
const groupRules = computed(() => ({
  name: [
    { required: true, message: t('validate.required'), trigger: 'blur' },
    { pattern: /^[A-Za-z0-9]+$/, message: t('validate.alnum'), trigger: 'blur' },
  ],
}))
function emptyGroupForm() {
  return {
    id: null,
    name: '',
    remark: '',
    type: 'B',
    healthCheck: {
      enabled: true,
      mode: 'url',
      url: 'https://www.bing.com/hp/api/v1/carousel?&format=json',
      intervalSec: 600,
      failThreshold: 3,
      recoverThreshold: 2,
    },
  }
}
function openCreateGroup() {
  groupDialog.isEdit = false
  groupDialog.form = emptyGroupForm()
  groupDialog.visible = true
}
function openEditGroup(row) {
  groupDialog.isEdit = true
  groupDialog.form = JSON.parse(JSON.stringify({ ...emptyGroupForm(), ...row }))
  groupDialog.visible = true
}
async function saveGroup() {
  const f = groupDialog.form
  // 组件⑦：提交前先跑表单校验（必填 + ^[A-Za-z0-9]+$），不通过则中止
  const ok = await groupFormRef.value?.validate().catch(() => false)
  if (!ok) return
  try {
    if (groupDialog.isEdit) await groupApi.updateGroup(f.id, f)
    else await groupApi.createGroup(f)
    ElMessage.success(t('common.saveSuccess'))
    groupDialog.visible = false
    loadGroups()
  } catch {
    /* ignore */
  }
}
async function removeGroup(row) {
  await ElMessageBox.confirm(t('proxyGroups.deleteConfirm', { name: row.name }), t('common.notice'), { type: 'warning' }).catch(() => 'cancel')
  try {
    await groupApi.deleteGroup(row.id)
    ElMessage.success(t('common.deleteSuccess'))
    loadGroups()
  } catch {
    /* ignore */
  }
}

async function loadGroups() {
  loading.value = true
  try {
    groups.value = (await groupApi.listGroups()) || []
  } catch {
    groups.value = []
  } finally {
    loading.value = false
  }
}

// ===== 上游代理抽屉（仅 Type B）=====
// 表格采用服务端分页 + 多选 + 批量操作（AC-3.1/3.3/3.4）。
const upstreamDrawer = reactive({ visible: false, group: null, list: [], total: 0, loading: false })
// 分页与筛选参数：默认每页 100。
const upQuery = reactive({ page: 1, pageSize: 100, keyword: '', healthState: '' })
const upTableRef = ref(null)
const selectedRows = ref([]) // 当前页勾选的行
const selectAllByFilter = ref(false) // 跨页全选（按当前筛选）开关

async function openUpstreams(group) {
  upstreamDrawer.group = group
  upstreamDrawer.visible = true
  // 重置分页/筛选/选择，避免上次状态串台
  upQuery.page = 1
  upQuery.keyword = ''
  upQuery.healthState = ''
  selectAllByFilter.value = false
  selectedRows.value = []
  loadGroupChart(group.id)
  await loadUpstreams()
}
// 按当前分页/筛选参数加载上游列表。兼容后端可能返回 {items,total} 或裸数组两种形态。
async function loadUpstreams() {
  if (!upstreamDrawer.group) return
  upstreamDrawer.loading = true
  try {
    const r = await groupApi.listUpstreams(upstreamDrawer.group.id, {
      page: upQuery.page,
      pageSize: upQuery.pageSize,
      keyword: upQuery.keyword || undefined,
      healthState: upQuery.healthState || undefined,
    })
    if (Array.isArray(r)) {
      // 后端尚未切到分页契约时的降级：当作单页全量
      upstreamDrawer.list = r
      upstreamDrawer.total = r.length
    } else {
      upstreamDrawer.list = r?.items || []
      upstreamDrawer.total = r?.total ?? upstreamDrawer.list.length
    }
  } catch {
    upstreamDrawer.list = []
    upstreamDrawer.total = 0
  } finally {
    upstreamDrawer.loading = false
  }
}
// 筛选变化：回到第 1 页并清空跨页全选。
function onFilterChange() {
  upQuery.page = 1
  selectAllByFilter.value = false
  loadUpstreams()
}
function onPageChange(p) {
  upQuery.page = p
  loadUpstreams()
}
function onSelectionChange(rows) {
  selectedRows.value = rows
  // 手动改动当前页选择时，跨页全选语义失效
  if (selectAllByFilter.value) selectAllByFilter.value = false
}
// 是否有可执行批量操作的选择（当前页勾选 或 跨页全选）。
const hasBulkSelection = computed(() => selectAllByFilter.value || selectedRows.value.length > 0)
const bulkSelectionLabel = computed(() => {
  if (selectAllByFilter.value) return t('proxyGroups.selectAllByFilterMsg', { total: upstreamDrawer.total })
  if (selectedRows.value.length) return t('proxyGroups.selectedCount', { n: selectedRows.value.length })
  return ''
})


const upDialog = reactive({ visible: false, isEdit: false, tab: 'single', form: null, batchText: '', submitting: false, result: null })
function emptyUpstreamForm() {
  return { id: null, host: '', port: 1080, user: '', pwd: '', weight: 1, enabled: true }
}
function openCreateUpstream() {
  upDialog.isEdit = false
  upDialog.tab = 'single'
  upDialog.form = emptyUpstreamForm()
  upDialog.batchText = ''
  upDialog.result = null
  upDialog.visible = true
}
function openEditUpstream(row) {
  upDialog.isEdit = true
  upDialog.tab = 'single'
  upDialog.form = { ...emptyUpstreamForm(), ...row }
  upDialog.result = null
  upDialog.visible = true
}
async function saveUpstream() {
  const f = upDialog.form
  const gid = upstreamDrawer.group.id
  if (!f.host) return ElMessage.warning(t('proxyGroups.enterHostWarn'))
  try {
    if (upDialog.isEdit) await groupApi.updateUpstream(gid, f.id, f)
    else await groupApi.createUpstream(gid, f)
    ElMessage.success(t('common.saveSuccess'))
    upDialog.visible = false
    loadUpstreams()
  } catch {
    /* ignore */
  }
}
// 批量添加：按行拆分，去空行后提交；展示成功数与失败行号/原因（AC-3.1/3.2）。
async function submitBatchAdd() {
  const gid = upstreamDrawer.group.id
  // 文本框按换行拆成数组：CRLF 规范化、逐行 trim、去空行；逐行容错由后端负责。
  const lines = upDialog.batchText
    .replace(/\r\n/g, '\n')
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l.length > 0)
  if (lines.length === 0) return ElMessage.warning(t('proxyGroups.pasteUpstreamWarn'))
  upDialog.submitting = true
  upDialog.result = null
  try {
    const r = await groupApi.batchAddUpstreams(gid, lines)
    upDialog.result = { ok: r?.ok ?? 0, failed: r?.failed || [] }
    if (upDialog.result.failed.length === 0) {
      ElMessage.success(t('proxyGroups.addOkCount', { ok: upDialog.result.ok }))
      upDialog.visible = false
    } else {
      ElMessage.warning(t('proxyGroups.addPartial', { ok: upDialog.result.ok, failed: upDialog.result.failed.length }))
    }
    loadUpstreams()
  } catch {
    /* ignore */
  } finally {
    upDialog.submitting = false
  }
}
async function removeUpstream(row) {
  const gid = upstreamDrawer.group.id
  await ElMessageBox.confirm(t('proxyGroups.deleteUpstreamConfirm'), t('common.notice'), { type: 'warning' }).catch(() => 'cancel')
  try {
    await groupApi.deleteUpstream(gid, row.id)
    ElMessage.success(t('common.deleteSuccess'))
    loadUpstreams()
  } catch {
    /* ignore */
  }
}
async function toggleUpstream(row) {
  try {
    await groupApi.toggleUpstream(upstreamDrawer.group.id, row.id, row.enabled)
    ElMessage.success(row.enabled ? t('common.enabled') : t('common.disabled'))
  } catch {
    row.enabled = !row.enabled
  }
}

// ===== 批量改权重 / 启用禁用（AC-3.4）=====
// 构造选择载荷（扁平结构）：跨页全选 → mode=filter + keyword/healthState；
// 否则 mode=ids + ids 列表。
function buildSelectionPayload() {
  if (selectAllByFilter.value) {
    return {
      mode: 'filter',
      keyword: upQuery.keyword || undefined,
      healthState: upQuery.healthState || undefined,
    }
  }
  return { mode: 'ids', ids: selectedRows.value.map((r) => r.id) }
}
// field: 'weight'|'enabled'；value: 对应新值。后端按 field 决定写哪个列。
async function bulkApply(field, value, successMsg) {
  if (!hasBulkSelection.value) return ElMessage.warning(t('proxyGroups.selectUpstreamWarn'))
  const gid = upstreamDrawer.group.id
  try {
    const r = await groupApi.bulkUpdateUpstreams(gid, {
      ...buildSelectionPayload(),
      field,
      [field]: value,
    })
    ElMessage.success(t('proxyGroups.bulkAffected', { msg: successMsg, n: r?.affected ?? 0 }))
    selectAllByFilter.value = false
    selectedRows.value = []
    upTableRef.value?.clearSelection?.()
    loadUpstreams()
  } catch {
    /* ignore */
  }
}
async function bulkSetWeight() {
  const { value } = await ElMessageBox.prompt(t('proxyGroups.setWeightPrompt'), t('proxyGroups.setWeightTitle'), {
    inputPattern: /^[1-9]\d*$/,
    inputErrorMessage: t('proxyGroups.positiveIntError'),
    inputValue: '1',
  }).catch(() => ({ value: null }))
  if (value == null) return
  bulkApply('weight', Number(value), t('proxyGroups.bulkWeightDone'))
}
function bulkEnable() {
  bulkApply('enabled', true, t('proxyGroups.bulkEnableDone'))
}
function bulkDisable() {
  bulkApply('enabled', false, t('proxyGroups.bulkDisableDone'))
}
// 批量删除：复用选择载荷（勾选→ids；跨页全选→filter）。删除不可逆，二次确认显示具体条数。
async function bulkDelete() {
  if (!hasBulkSelection.value) return ElMessage.warning(t('proxyGroups.selectUpstreamWarn'))
  // 条数来源：跨页全选 = 当前筛选下的总数；否则 = 勾选行数。
  const count = selectAllByFilter.value ? upstreamDrawer.total : selectedRows.value.length
  const ok = await ElMessageBox.confirm(
    t('proxyGroups.bulkDeleteConfirm', { n: count }),
    t('common.notice'),
    { type: 'warning' },
  ).catch(() => false)
  if (!ok) return
  const gid = upstreamDrawer.group.id
  // 删除接口契约：{ ids } 或 { filter:{keyword,healthState} }（与 bulk 改值的扁平结构不同）。
  const payload = selectAllByFilter.value
    ? { filter: { keyword: upQuery.keyword || undefined, healthState: upQuery.healthState || undefined } }
    : { ids: selectedRows.value.map((r) => r.id) }
  try {
    const r = await groupApi.bulkDeleteUpstreams(gid, payload)
    ElMessage.success(t('proxyGroups.bulkAffected', { msg: t('proxyGroups.bulkDeleteDone'), n: r?.affected ?? 0 }))
    selectAllByFilter.value = false
    selectedRows.value = []
    upTableRef.value?.clearSelection?.()
    loadUpstreams()
  } catch {
    /* ignore */
  }
}
const testing = ref({})
async function testUpstream(row) {
  testing.value[row.id] = true
  try {
    const r = await groupApi.testUpstream(upstreamDrawer.group.id, row.id)
    if (r?.ok) ElMessage.success(t('proxyGroups.testOk', { ms: r.latencyMs }))
    else ElMessage.error(t('proxyGroups.testFail', { err: r?.error || t('proxyGroups.unknownError') }))
    // 测试即一次探测：后端已写回延迟与健康状态，刷新列表使「延迟/健康」列即时更新。
    loadUpstreams()
  } catch {
    /* ignore */
  } finally {
    testing.value[row.id] = false
  }
}

// ===== 分组独立流量图 =====
const groupTs = ref({ times: [], up: [], down: [], req: [] })
const groupTopDomains = ref([])
// 组件⑤：Type A 动态上游流量图表抽屉（仅含图表，不含上游池表格）。
// 独立于 upstreamDrawer，复用 loadGroupChart / groupChartOption / groupTopDomainOption。
const chartDrawer = reactive({ visible: false, group: null })
function openGroupChart(g) {
  chartDrawer.group = g
  chartDrawer.visible = true
  loadGroupChart(g.id)
}
async function loadGroupChart(groupId) {
  try {
    const d = await dashApi.getTimeseries({ window: '24h', groupId })
    if (d) groupTs.value = d
  } catch {
    groupTs.value = { times: [], up: [], down: [], req: [] }
  }
  // 该分组 Top 目标域名（独立 try/catch，互不影响时序图加载）。
  try {
    groupTopDomains.value = (await dashApi.getTopN({ kind: 'domain', limit: 10, groupId })) || []
  } catch {
    groupTopDomains.value = []
  }
}
const groupChartOption = computed(() => ({
  // tooltip 标题把后端桶时间格式化为 2000/01/01 00:00:00 样式（与首页流量图一致）；
  // 上行/下行按字节可读化，请求数按千分位，避免显示原始 RFC3339 字符串与裸数字。
  tooltip: {
    trigger: 'axis',
    formatter: (params) => {
      if (!params || !params.length) return ''
      const title = formatBucketTime(params[0].axisValue)
      const reqName = t('proxyGroups.legendReq')
      const lines = params.map((p) => {
        const val = p.seriesName === reqName ? formatNumber(p.value) : formatBytes(p.value)
        return `${p.marker}${p.seriesName}: ${val}`
      })
      return [title, ...lines].join('<br/>')
    },
  },
  legend: { data: [t('proxyGroups.legendUp'), t('proxyGroups.legendDown'), t('proxyGroups.legendReq')] },
  // containLabel 让 ECharts 按双侧刻度文字实际宽度预留空间，
  // 避免左轴 formatBytes 长文本（如 "1.23 MB"）被容器左边缘截断（右轴请求数同理）。
  grid: { left: 8, right: 8, top: 40, bottom: 30, containLabel: true },
  // X 轴底部时间标签格式化为紧凑 "MM/DD HH:mm"，避免显示原始 RFC3339 字符串。
  xAxis: { type: 'category', boundaryGap: false, data: groupTs.value.times, axisLabel: { formatter: (v) => formatAxisTime(v) } },
  // 双 Y 轴：左轴流量（字节可读化），右轴请求数（与首页仪表盘流量图同形态）。
  yAxis: [
    { type: 'value', name: t('proxyGroups.axisTraffic'), axisLabel: { formatter: (v) => formatBytes(v) } },
    { type: 'value', name: t('proxyGroups.axisReq'), position: 'right' },
  ],
  series: [
    { name: t('proxyGroups.legendUp'), type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: groupTs.value.up },
    { name: t('proxyGroups.legendDown'), type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: groupTs.value.down },
    { name: t('proxyGroups.legendReq'), type: 'line', yAxisIndex: 1, smooth: true, data: groupTs.value.req },
  ],
}))

// 该分组 Top 目标域名横向柱状图（绑定后端 domain 的 .count，与仪表盘全局图同形态）。
const groupTopDomainOption = computed(() => ({
  tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
  grid: { left: 8, right: 24, top: 10, bottom: 10, containLabel: true },
  xAxis: { type: 'value' },
  yAxis: { type: 'category', inverse: true, data: groupTopDomains.value.map((d) => d.name) },
  series: [
    {
      type: 'bar',
      data: groupTopDomains.value.map((d) => d.count),
      barMaxWidth: 18,
      itemStyle: { borderRadius: [0, 4, 4, 0] },
    },
  ],
}))

function healthTagType(state) {
  if (state === 'healthy') return 'success'
  if (state === 'unhealthy') return 'danger'
  return 'info'
}

onMounted(loadGroups)
</script>

<template>
  <div class="dp-page">
    <el-card>
      <template #header>
        <div class="flex-between">
          <span>{{ t('proxyGroups.title') }}</span>
          <el-button type="primary" :icon="'Plus'" @click="openCreateGroup">{{ t('proxyGroups.create') }}</el-button>
        </div>
      </template>

      <el-table v-loading="loading" :data="groups" border :empty-text="t('proxyGroups.emptyGroups')">
        <el-table-column prop="name" :label="t('proxyGroups.name')" min-width="140" />
        <el-table-column prop="remark" :label="t('proxyGroups.remark')" min-width="160" show-overflow-tooltip />
        <el-table-column :label="t('proxyGroups.type')" width="150">
          <template #default="{ row }">
            <el-tag :type="row.type === 'A' ? 'warning' : 'success'" effect="plain">
              {{ t('groupType.' + row.type) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('proxyGroups.todayTraffic')" width="130">
          <template #default="{ row }">{{ formatBytes((row.todayUp || 0) + (row.todayDown || 0)) }}</template>
        </el-table-column>
        <el-table-column :label="t('proxyGroups.todayReq')" width="110">
          <template #default="{ row }">{{ formatNumber(row.todayReq || 0) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="240" fixed="right">
          <template #default="{ row }">
            <el-button v-if="row.type === 'B'" link type="primary" @click="openUpstreams(row)">{{ t('proxyGroups.pool') }}</el-button>
            <el-button v-if="row.type === 'A'" link type="primary" @click="openGroupChart(row)">{{ t('group.traffic') }}</el-button>
            <el-button link type="primary" @click="openEditGroup(row)">{{ t('common.edit') }}</el-button>
            <el-button link type="danger" @click="removeGroup(row)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 组编辑对话框 -->
    <el-dialog v-model="groupDialog.visible" :title="groupDialog.isEdit ? t('proxyGroups.editGroup') : t('proxyGroups.createGroup')" width="560px">
      <el-form v-if="groupDialog.form" ref="groupFormRef" :model="groupDialog.form" :rules="groupRules" label-width="110px">
        <el-form-item :label="t('proxyGroups.name')" prop="name">
          <el-input v-model="groupDialog.form.name" :placeholder="t('proxyGroups.namePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('proxyGroups.remark')">
          <el-input v-model="groupDialog.form.remark" :placeholder="t('proxyGroups.optionalRemark')" />
        </el-form-item>
        <el-form-item :label="t('proxyGroups.type')">
          <el-radio-group v-model="groupDialog.form.type" :disabled="groupDialog.isEdit">
            <el-radio value="A">{{ t('groupType.A') }}</el-radio>
            <el-radio value="B">{{ t('groupType.B') }}</el-radio>
          </el-radio-group>
        </el-form-item>

        <!-- G2：仅 Type B 显示健康检查配置 -->
        <template v-if="groupDialog.form.type === 'B'">
          <el-divider content-position="left">{{ t('proxyGroups.healthCheck') }}</el-divider>
          <el-form-item :label="t('proxyGroups.hcEnable')">
            <el-switch v-model="groupDialog.form.healthCheck.enabled" />
          </el-form-item>
          <template v-if="groupDialog.form.healthCheck.enabled">
            <el-form-item :label="t('proxyGroups.hcMode')">
              <el-radio-group v-model="groupDialog.form.healthCheck.mode">
                <el-radio value="ping">{{ t('settings.hcModePing') }}</el-radio>
                <el-radio value="url">{{ t('proxyGroups.hcModeUrl') }}</el-radio>
              </el-radio-group>
            </el-form-item>
            <el-form-item v-if="groupDialog.form.healthCheck.mode === 'url'" :label="t('proxyGroups.hcUrl')">
              <el-input v-model="groupDialog.form.healthCheck.url" />
            </el-form-item>
            <el-form-item :label="t('proxyGroups.hcInterval')">
              <el-input-number v-model="groupDialog.form.healthCheck.intervalSec" :min="10" :step="10" />
            </el-form-item>
            <el-form-item :label="t('proxyGroups.hcFailThreshold')">
              <el-input-number v-model="groupDialog.form.healthCheck.failThreshold" :min="1" />
              <span class="text-muted form-hint">{{ t('proxyGroups.hcFailHint') }}</span>
            </el-form-item>
            <el-form-item :label="t('proxyGroups.hcRecoverThreshold')">
              <el-input-number v-model="groupDialog.form.healthCheck.recoverThreshold" :min="1" />
              <span class="text-muted form-hint">{{ t('proxyGroups.hcRecoverHint') }}</span>
            </el-form-item>
          </template>
        </template>
      </el-form>
      <template #footer>
        <el-button @click="groupDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="saveGroup">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <!-- 上游代理抽屉（仅 Type B）-->
    <el-drawer v-model="upstreamDrawer.visible" :title="t('proxyGroups.poolDrawerTitle', { name: upstreamDrawer.group?.name || '' })" :size="appStore.isMobile ? '90%' : '60%'">
      <div class="drawer-toolbar">
        <span class="text-muted">{{ t('proxyGroups.poolTemplateHint') }}</span>
        <el-button type="primary" size="small" :icon="'Plus'" @click="openCreateUpstream">{{ t('proxyGroups.addUpstream') }}</el-button>
      </div>

      <!-- 筛选栏 -->
      <div class="filter-bar">
        <el-input
          v-model="upQuery.keyword"
          :placeholder="t('proxyGroups.searchPlaceholder')"
          size="small"
          clearable
          style="width: 220px"
          @keyup.enter="onFilterChange"
          @clear="onFilterChange"
        />
        <el-select
          v-model="upQuery.healthState"
          :placeholder="t('proxyGroups.healthStatePlaceholder')"
          size="small"
          clearable
          style="width: 130px"
          @change="onFilterChange"
        >
          <el-option :label="t('proxyGroups.stateHealthy')" value="healthy" />
          <el-option :label="t('proxyGroups.stateUnhealthy')" value="unhealthy" />
          <el-option :label="t('proxyGroups.stateUnknown')" value="unknown" />
        </el-select>
        <el-button size="small" @click="onFilterChange">{{ t('common.search') }}</el-button>
      </div>

      <!-- 批量操作工具栏 -->
      <div class="bulk-bar">
        <el-checkbox
          v-model="selectAllByFilter"
          :disabled="upstreamDrawer.total === 0"
          @change="selectedRows = []"
        >
          {{ t('proxyGroups.selectAllByFilter') }}
        </el-checkbox>
        <span v-if="bulkSelectionLabel" class="text-muted bulk-count">{{ bulkSelectionLabel }}</span>
        <div class="bulk-actions">
          <el-button size="small" :disabled="!hasBulkSelection" @click="bulkSetWeight">{{ t('proxyGroups.bulkSetWeight') }}</el-button>
          <el-button size="small" type="success" :disabled="!hasBulkSelection" @click="bulkEnable">{{ t('proxyGroups.bulkEnable') }}</el-button>
          <el-button size="small" type="warning" :disabled="!hasBulkSelection" @click="bulkDisable">{{ t('proxyGroups.bulkDisable') }}</el-button>
          <el-button size="small" type="danger" :disabled="!hasBulkSelection" @click="bulkDelete">{{ t('proxyGroups.bulkDelete') }}</el-button>
        </div>
      </div>

      <el-table
        ref="upTableRef"
        v-loading="upstreamDrawer.loading"
        :data="upstreamDrawer.list"
        border
        size="small"
        :empty-text="t('proxyGroups.emptyUpstreams')"
        @selection-change="onSelectionChange"
      >
        <el-table-column type="selection" width="44" />
        <el-table-column :label="t('proxyGroups.address')" min-width="160">
          <template #default="{ row }">{{ row.host }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column :label="t('proxyGroups.usernamePwd')" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">{{ row.user }}:{{ row.pwd }}</template>
        </el-table-column>
        <el-table-column prop="weight" :label="t('common.weight')" width="80" />
        <el-table-column :label="t('proxyGroups.health')" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="healthTagType(row.healthState)" effect="plain">
              {{ row.healthState === 'healthy' ? t('proxyGroups.stateHealthy') : row.healthState === 'unhealthy' ? t('proxyGroups.stateUnhealthy') : t('proxyGroups.stateUnknown') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('proxyGroups.latency')" width="90">
          <template #default="{ row }">{{ row.latencyMs != null ? row.latencyMs + 'ms' : '-' }}</template>
        </el-table-column>
        <el-table-column :label="t('common.enable')" width="80">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" size="small" @change="toggleUpstream(row)" />
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="190" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :loading="testing[row.id]" @click="testUpstream(row)">{{ t('common.test') }}</el-button>
            <el-button link type="primary" @click="openEditUpstream(row)">{{ t('common.edit') }}</el-button>
            <el-button link type="danger" @click="removeUpstream(row)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>

      <!-- 服务端分页：默认每页 100 -->
      <div class="pager">
        <el-pagination
          :current-page="upQuery.page"
          :page-size="upQuery.pageSize"
          :total="upstreamDrawer.total"
          layout="total, prev, pager, next, jumper"
          background
          @current-change="onPageChange"
        />
      </div>

      <el-divider content-position="left">{{ t('proxyGroups.groupTraffic24h') }}</el-divider>
      <EChart :option="groupChartOption" height="260px" />

      <el-divider content-position="left">{{ t('proxyGroups.topDomains') }}</el-divider>
      <EChart v-if="groupTopDomains.length" :option="groupTopDomainOption" height="260px" />
      <el-empty v-else :description="t('common.empty')" :image-size="50" />
    </el-drawer>

    <!-- 组件⑤：动态上游（Type A）流量图表抽屉。独立于上游池抽屉，仅含图表，无上游表格/分页/批量 -->
    <el-drawer
      v-model="chartDrawer.visible"
      :title="`${t('group.trafficDrawerTitle')} - ${chartDrawer.group?.name || ''}`"
      :size="appStore.isMobile ? '90%' : '60%'"
    >
      <el-divider content-position="left">{{ t('group.trafficDrawerTitle') }}（24h）</el-divider>
      <EChart :option="groupChartOption" height="260px" />

      <el-divider content-position="left">{{ t('proxyGroups.topDomains') }}</el-divider>
      <EChart v-if="groupTopDomains.length" :option="groupTopDomainOption" height="260px" />
      <el-empty v-else :description="t('common.empty')" :image-size="50" />
    </el-drawer>

    <!-- 上游编辑/添加对话框（添加时支持 单条 / 批量 两 tab）-->
    <el-dialog v-model="upDialog.visible" :title="upDialog.isEdit ? t('proxyGroups.editUpstream') : t('proxyGroups.addUpstream')" width="560px">
      <!-- 编辑模式：仅单条表单 -->
      <el-form v-if="upDialog.isEdit && upDialog.form" :model="upDialog.form" label-width="110px">
        <el-form-item :label="t('common.host')" required>
          <el-input v-model="upDialog.form.host" :placeholder="t('proxyGroups.upstreamHostPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('common.port')" required>
          <el-input-number v-model="upDialog.form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item :label="t('proxyGroups.username')">
          <el-input v-model="upDialog.form.user" :placeholder="t('proxyGroups.usernameTplPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('proxyGroups.upstreamPwd')">
          <el-input v-model="upDialog.form.pwd" type="password" show-password :placeholder="t('proxyGroups.upstreamPwdPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('common.weight')">
          <el-input-number v-model="upDialog.form.weight" :min="1" />
        </el-form-item>
      </el-form>

      <!-- 添加模式：单条 / 批量 两 tab -->
      <el-tabs v-else v-model="upDialog.tab">
        <el-tab-pane :label="t('proxyGroups.tabSingle')" name="single">
          <el-form v-if="upDialog.form" :model="upDialog.form" label-width="110px">
            <el-form-item :label="t('common.host')" required>
              <el-input v-model="upDialog.form.host" :placeholder="t('proxyGroups.upstreamHostPlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('common.port')" required>
              <el-input-number v-model="upDialog.form.port" :min="1" :max="65535" />
            </el-form-item>
            <el-form-item :label="t('proxyGroups.username')">
              <el-input v-model="upDialog.form.user" :placeholder="t('proxyGroups.usernameTplPlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('proxyGroups.upstreamPwd')">
              <el-input v-model="upDialog.form.pwd" type="password" show-password :placeholder="t('proxyGroups.upstreamPwdPlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('common.weight')">
              <el-input-number v-model="upDialog.form.weight" :min="1" />
            </el-form-item>
          </el-form>
        </el-tab-pane>
        <el-tab-pane :label="t('proxyGroups.tabBatch')" name="batch">
          <p class="text-muted batch-hint">
            {{ t('proxyGroups.batchHint', { fmt1: 'user:pass@host:port', fmt2: 'user:pass:host:port' }) }}
            <code>user:pass@[::1]:port</code>。
          </p>
          <el-input
            v-model="upDialog.batchText"
            type="textarea"
            :rows="8"
            placeholder="user:pass@host1:1080&#10;user:pass@host2:1080"
          />
          <!-- 失败行号/原因回显 -->
          <div v-if="upDialog.result" class="batch-result">
            <el-alert
              :type="upDialog.result.failed.length === 0 ? 'success' : 'warning'"
              :closable="false"
              show-icon
              :title="t('proxyGroups.failedSummary', { ok: upDialog.result.ok, failed: upDialog.result.failed.length })"
            />
            <ul v-if="upDialog.result.failed.length" class="fail-list">
              <li v-for="(f, i) in upDialog.result.failed" :key="i">{{ t('proxyGroups.failedLine', { line: f.line, reason: f.reason }) }}</li>
            </ul>
          </div>
        </el-tab-pane>
      </el-tabs>

      <template #footer>
        <el-button @click="upDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button
          v-if="!upDialog.isEdit && upDialog.tab === 'batch'"
          type="primary"
          :loading="upDialog.submitting"
          @click="submitBatchAdd"
        >
          {{ t('proxyGroups.submitBatch') }}
        </el-button>
        <el-button v-else type="primary" @click="saveUpstream">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
@use '@/styles/responsive.scss' as r;

.drawer-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.filter-bar {
  display: flex;
  gap: 10px;
  align-items: center;
  margin-bottom: 10px;
  // 手机端：筛选条允许换行，避免输入框/按钮一行挤爆溢出
  @include r.mobile {
    flex-wrap: wrap;
    gap: 8px;
  }
}
.bulk-bar {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.bulk-count {
  font-size: 12px;
}
.bulk-actions {
  margin-left: auto;
  display: flex;
  gap: 8px;
}
.pager {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
.batch-hint {
  font-size: 12px;
  margin: 0 0 8px;
}
.batch-result {
  margin-top: 12px;
}
.fail-list {
  margin: 8px 0 0;
  padding-left: 18px;
  font-size: 12px;
  color: var(--el-color-warning);
  max-height: 160px;
  overflow: auto;
}
.form-hint {
  margin-left: 10px;
  font-size: 12px;
}
code {
  background: var(--el-fill-color-light);
  padding: 1px 5px;
  border-radius: 4px;
}
</style>
