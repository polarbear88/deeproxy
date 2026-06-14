<script setup>
// 代理组管理（AC-21/28，G2）。已对齐 T7 实测契约（camelCase DTO）：
// - Group: { id,name,remark,type,healthCheck:{enabled,mode,url,intervalSec,failThreshold,recoverThreshold},todayUp,todayDown,todayReq }
// - Upstream: { id,host,port,user,usernameTemplate,pwd,weight,enabled,healthState:'healthy'|'unhealthy'|'unknown',latencyMs }
// - Type A 隐藏代理池与健康检查 UI（G2）。
import { onMounted, reactive, ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import EChart from '@/components/EChart.js'
import * as groupApi from '@/api/group'
import * as dashApi from '@/api/dashboard'
import { formatBytes, formatNumber } from '@/utils/format'

const loading = ref(false)
const groups = ref([])

// ===== 组编辑对话框 =====
const groupDialog = reactive({ visible: false, isEdit: false, form: null })
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
  if (!f.name) return ElMessage.warning('请输入分组名称')
  try {
    if (groupDialog.isEdit) await groupApi.updateGroup(f.id, f)
    else await groupApi.createGroup(f)
    ElMessage.success('保存成功')
    groupDialog.visible = false
    loadGroups()
  } catch {
    /* ignore */
  }
}
async function removeGroup(row) {
  await ElMessageBox.confirm(`确认删除分组「${row.name}」？`, '提示', { type: 'warning' }).catch(() => 'cancel')
  try {
    await groupApi.deleteGroup(row.id)
    ElMessage.success('已删除')
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
  if (selectAllByFilter.value) return `已按当前筛选全选（共 ${upstreamDrawer.total} 条）`
  if (selectedRows.value.length) return `已选 ${selectedRows.value.length} 条`
  return ''
})


const upDialog = reactive({ visible: false, isEdit: false, tab: 'single', form: null, batchText: '', submitting: false, result: null })
function emptyUpstreamForm() {
  return { id: null, host: '', port: 1080, user: '', usernameTemplate: '', pwd: '', weight: 1, enabled: true }
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
  if (!f.host) return ElMessage.warning('请输入上游主机')
  try {
    if (upDialog.isEdit) await groupApi.updateUpstream(gid, f.id, f)
    else await groupApi.createUpstream(gid, f)
    ElMessage.success('保存成功')
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
  if (lines.length === 0) return ElMessage.warning('请粘贴至少一行上游')
  upDialog.submitting = true
  upDialog.result = null
  try {
    const r = await groupApi.batchAddUpstreams(gid, lines)
    upDialog.result = { ok: r?.ok ?? 0, failed: r?.failed || [] }
    if (upDialog.result.failed.length === 0) {
      ElMessage.success(`成功添加 ${upDialog.result.ok} 条`)
      upDialog.visible = false
    } else {
      ElMessage.warning(`成功 ${upDialog.result.ok} 条，失败 ${upDialog.result.failed.length} 条`)
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
  await ElMessageBox.confirm('确认删除该上游？', '提示', { type: 'warning' }).catch(() => 'cancel')
  try {
    await groupApi.deleteUpstream(gid, row.id)
    ElMessage.success('已删除')
    loadUpstreams()
  } catch {
    /* ignore */
  }
}
async function toggleUpstream(row) {
  try {
    await groupApi.toggleUpstream(upstreamDrawer.group.id, row.id, row.enabled)
    ElMessage.success(row.enabled ? '已启用' : '已禁用')
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
  if (!hasBulkSelection.value) return ElMessage.warning('请先选择上游')
  const gid = upstreamDrawer.group.id
  try {
    const r = await groupApi.bulkUpdateUpstreams(gid, {
      ...buildSelectionPayload(),
      field,
      [field]: value,
    })
    ElMessage.success(`${successMsg}（影响 ${r?.affected ?? 0} 条）`)
    selectAllByFilter.value = false
    selectedRows.value = []
    upTableRef.value?.clearSelection?.()
    loadUpstreams()
  } catch {
    /* ignore */
  }
}
async function bulkSetWeight() {
  const { value } = await ElMessageBox.prompt('为选中上游设置统一权重', '批量设置权重', {
    inputPattern: /^[1-9]\d*$/,
    inputErrorMessage: '请输入正整数',
    inputValue: '1',
  }).catch(() => ({ value: null }))
  if (value == null) return
  bulkApply('weight', Number(value), '已批量设置权重')
}
function bulkEnable() {
  bulkApply('enabled', true, '已批量启用')
}
function bulkDisable() {
  bulkApply('enabled', false, '已批量禁用')
}
const testing = ref({})
async function testUpstream(row) {
  testing.value[row.id] = true
  try {
    const r = await groupApi.testUpstream(upstreamDrawer.group.id, row.id)
    if (r?.ok) ElMessage.success(`连通，延迟 ${r.latencyMs}ms`)
    else ElMessage.error(`不通：${r?.error || '未知错误'}`)
  } catch {
    /* ignore */
  } finally {
    testing.value[row.id] = false
  }
}

// ===== 分组独立流量图 =====
const groupTs = ref({ times: [], up: [], down: [] })
async function loadGroupChart(groupId) {
  try {
    const d = await dashApi.getTimeseries({ window: '24h', groupId })
    if (d) groupTs.value = d
  } catch {
    groupTs.value = { times: [], up: [], down: [] }
  }
}
const groupChartOption = computed(() => ({
  tooltip: { trigger: 'axis' },
  legend: { data: ['上行', '下行'] },
  grid: { left: 50, right: 20, top: 30, bottom: 30 },
  xAxis: { type: 'category', boundaryGap: false, data: groupTs.value.times },
  yAxis: { type: 'value', axisLabel: { formatter: (v) => formatBytes(v) } },
  series: [
    { name: '上行', type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: groupTs.value.up },
    { name: '下行', type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: groupTs.value.down },
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
          <span>代理组管理</span>
          <el-button type="primary" :icon="'Plus'" @click="openCreateGroup">新建代理组</el-button>
        </div>
      </template>

      <el-table v-loading="loading" :data="groups" border empty-text="暂无代理组">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="remark" label="备注" min-width="160" show-overflow-tooltip />
        <el-table-column label="类型" width="150">
          <template #default="{ row }">
            <el-tag :type="row.type === 'A' ? 'warning' : 'success'" effect="plain">
              {{ row.type === 'A' ? 'Type A 动态上游' : 'Type B 代理池' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="今日流量" width="130">
          <template #default="{ row }">{{ formatBytes((row.todayUp || 0) + (row.todayDown || 0)) }}</template>
        </el-table-column>
        <el-table-column label="今日请求" width="110">
          <template #default="{ row }">{{ formatNumber(row.todayReq || 0) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <el-button v-if="row.type === 'B'" link type="primary" @click="openUpstreams(row)">代理池</el-button>
            <el-button link type="primary" @click="openEditGroup(row)">编辑</el-button>
            <el-button link type="danger" @click="removeGroup(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 组编辑对话框 -->
    <el-dialog v-model="groupDialog.visible" :title="groupDialog.isEdit ? '编辑代理组' : '新建代理组'" width="560px">
      <el-form v-if="groupDialog.form" :model="groupDialog.form" label-width="110px">
        <el-form-item label="名称" required>
          <el-input v-model="groupDialog.form.name" placeholder="分组名称" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="groupDialog.form.remark" placeholder="可选备注" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="groupDialog.form.type" :disabled="groupDialog.isEdit">
            <el-radio value="A">Type A 动态上游</el-radio>
            <el-radio value="B">Type B 代理池</el-radio>
          </el-radio-group>
        </el-form-item>

        <!-- G2：仅 Type B 显示健康检查配置 -->
        <template v-if="groupDialog.form.type === 'B'">
          <el-divider content-position="left">健康检查</el-divider>
          <el-form-item label="启用">
            <el-switch v-model="groupDialog.form.healthCheck.enabled" />
          </el-form-item>
          <template v-if="groupDialog.form.healthCheck.enabled">
            <el-form-item label="探测方式">
              <el-radio-group v-model="groupDialog.form.healthCheck.mode">
                <el-radio value="ping">Ping</el-radio>
                <el-radio value="url">请求 URL</el-radio>
              </el-radio-group>
            </el-form-item>
            <el-form-item v-if="groupDialog.form.healthCheck.mode === 'url'" label="探测 URL">
              <el-input v-model="groupDialog.form.healthCheck.url" />
            </el-form-item>
            <el-form-item label="间隔(秒)">
              <el-input-number v-model="groupDialog.form.healthCheck.intervalSec" :min="10" :step="10" />
            </el-form-item>
            <el-form-item label="失败阈值">
              <el-input-number v-model="groupDialog.form.healthCheck.failThreshold" :min="1" />
              <span class="text-muted form-hint">连续失败次数标记不可用</span>
            </el-form-item>
            <el-form-item label="恢复阈值">
              <el-input-number v-model="groupDialog.form.healthCheck.recoverThreshold" :min="1" />
              <span class="text-muted form-hint">连续成功次数恢复</span>
            </el-form-item>
          </template>
        </template>
      </el-form>
      <template #footer>
        <el-button @click="groupDialog.visible = false">取消</el-button>
        <el-button type="primary" @click="saveGroup">保存</el-button>
      </template>
    </el-dialog>

    <!-- 上游代理抽屉（仅 Type B）-->
    <el-drawer v-model="upstreamDrawer.visible" :title="`代理池 - ${upstreamDrawer.group?.name || ''}`" size="60%">
      <div class="drawer-toolbar">
        <span class="text-muted">命名变量模板：用户名里写 <code>{region}</code> 等占位，客户端尾段按名替换。</span>
        <el-button type="primary" size="small" :icon="'Plus'" @click="openCreateUpstream">添加上游</el-button>
      </div>

      <!-- 筛选栏 -->
      <div class="filter-bar">
        <el-input
          v-model="upQuery.keyword"
          placeholder="按主机/用户名搜索"
          size="small"
          clearable
          style="width: 220px"
          @keyup.enter="onFilterChange"
          @clear="onFilterChange"
        />
        <el-select
          v-model="upQuery.healthState"
          placeholder="健康状态"
          size="small"
          clearable
          style="width: 130px"
          @change="onFilterChange"
        >
          <el-option label="可用" value="healthy" />
          <el-option label="不可用" value="unhealthy" />
          <el-option label="未知" value="unknown" />
        </el-select>
        <el-button size="small" @click="onFilterChange">搜索</el-button>
      </div>

      <!-- 批量操作工具栏 -->
      <div class="bulk-bar">
        <el-checkbox
          v-model="selectAllByFilter"
          :disabled="upstreamDrawer.total === 0"
          @change="selectedRows = []"
        >
          按当前筛选跨页全选
        </el-checkbox>
        <span v-if="bulkSelectionLabel" class="text-muted bulk-count">{{ bulkSelectionLabel }}</span>
        <div class="bulk-actions">
          <el-button size="small" :disabled="!hasBulkSelection" @click="bulkSetWeight">批量设权重</el-button>
          <el-button size="small" type="success" :disabled="!hasBulkSelection" @click="bulkEnable">批量启用</el-button>
          <el-button size="small" type="warning" :disabled="!hasBulkSelection" @click="bulkDisable">批量禁用</el-button>
        </div>
      </div>

      <el-table
        ref="upTableRef"
        v-loading="upstreamDrawer.loading"
        :data="upstreamDrawer.list"
        border
        size="small"
        empty-text="暂无上游代理"
        @selection-change="onSelectionChange"
      >
        <el-table-column type="selection" width="44" />
        <el-table-column label="地址" min-width="160">
          <template #default="{ row }">{{ row.host }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column prop="usernameTemplate" label="用户名模板" min-width="160" show-overflow-tooltip />
        <el-table-column prop="weight" label="权重" width="80" />
        <el-table-column label="健康" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="healthTagType(row.healthState)" effect="plain">
              {{ row.healthState === 'healthy' ? '可用' : row.healthState === 'unhealthy' ? '不可用' : '未知' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="延迟" width="90">
          <template #default="{ row }">{{ row.latencyMs != null ? row.latencyMs + 'ms' : '-' }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" size="small" @change="toggleUpstream(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="190" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :loading="testing[row.id]" @click="testUpstream(row)">测试</el-button>
            <el-button link type="primary" @click="openEditUpstream(row)">编辑</el-button>
            <el-button link type="danger" @click="removeUpstream(row)">删除</el-button>
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

      <el-divider content-position="left">分组流量（24h）</el-divider>
      <EChart :option="groupChartOption" height="260px" />
    </el-drawer>

    <!-- 上游编辑/添加对话框（添加时支持 单条 / 批量 两 tab）-->
    <el-dialog v-model="upDialog.visible" :title="upDialog.isEdit ? '编辑上游' : '添加上游'" width="560px">
      <!-- 编辑模式：仅单条表单 -->
      <el-form v-if="upDialog.isEdit && upDialog.form" :model="upDialog.form" label-width="110px">
        <el-form-item label="主机" required>
          <el-input v-model="upDialog.form.host" placeholder="域名或 IP" />
        </el-form-item>
        <el-form-item label="端口" required>
          <el-input-number v-model="upDialog.form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="用户名模板">
          <el-input v-model="upDialog.form.usernameTemplate" placeholder="如 acct-{region}-{session}" />
        </el-form-item>
        <el-form-item label="上游密码">
          <el-input v-model="upDialog.form.pwd" type="password" show-password placeholder="上游 SOCKS5 密码" />
        </el-form-item>
        <el-form-item label="权重">
          <el-input-number v-model="upDialog.form.weight" :min="1" />
        </el-form-item>
      </el-form>

      <!-- 添加模式：单条 / 批量 两 tab -->
      <el-tabs v-else v-model="upDialog.tab">
        <el-tab-pane label="单条添加" name="single">
          <el-form v-if="upDialog.form" :model="upDialog.form" label-width="110px">
            <el-form-item label="主机" required>
              <el-input v-model="upDialog.form.host" placeholder="域名或 IP" />
            </el-form-item>
            <el-form-item label="端口" required>
              <el-input-number v-model="upDialog.form.port" :min="1" :max="65535" />
            </el-form-item>
            <el-form-item label="用户名模板">
              <el-input v-model="upDialog.form.usernameTemplate" placeholder="如 acct-{region}-{session}" />
            </el-form-item>
            <el-form-item label="上游密码">
              <el-input v-model="upDialog.form.pwd" type="password" show-password placeholder="上游 SOCKS5 密码" />
            </el-form-item>
            <el-form-item label="权重">
              <el-input-number v-model="upDialog.form.weight" :min="1" />
            </el-form-item>
          </el-form>
        </el-tab-pane>
        <el-tab-pane label="批量添加" name="batch">
          <p class="text-muted batch-hint">
            每行一条，支持 <code>user:pass@host:port</code> 或 <code>user:pass:host:port</code>；IPv6 请用
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
              :title="`成功 ${upDialog.result.ok} 条，失败 ${upDialog.result.failed.length} 条`"
            />
            <ul v-if="upDialog.result.failed.length" class="fail-list">
              <li v-for="(f, i) in upDialog.result.failed" :key="i">第 {{ f.line }} 行：{{ f.reason }}</li>
            </ul>
          </div>
        </el-tab-pane>
      </el-tabs>

      <template #footer>
        <el-button @click="upDialog.visible = false">取消</el-button>
        <el-button
          v-if="!upDialog.isEdit && upDialog.tab === 'batch'"
          type="primary"
          :loading="upDialog.submitting"
          @click="submitBatchAdd"
        >
          提交批量
        </el-button>
        <el-button v-else type="primary" @click="saveUpstream">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
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
