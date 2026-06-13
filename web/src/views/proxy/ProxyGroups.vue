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
const upstreamDrawer = reactive({ visible: false, group: null, list: [] })
async function openUpstreams(group) {
  upstreamDrawer.group = group
  upstreamDrawer.visible = true
  loadGroupChart(group.id)
  await loadUpstreams(group.id)
}
async function loadUpstreams(groupId) {
  try {
    upstreamDrawer.list = (await groupApi.listUpstreams(groupId)) || []
  } catch {
    upstreamDrawer.list = []
  }
}

const upDialog = reactive({ visible: false, isEdit: false, form: null })
function emptyUpstreamForm() {
  return { id: null, host: '', port: 1080, user: '', usernameTemplate: '', pwd: '', weight: 1, enabled: true }
}
function openCreateUpstream() {
  upDialog.isEdit = false
  upDialog.form = emptyUpstreamForm()
  upDialog.visible = true
}
function openEditUpstream(row) {
  upDialog.isEdit = true
  upDialog.form = { ...emptyUpstreamForm(), ...row }
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
    loadUpstreams(gid)
  } catch {
    /* ignore */
  }
}
async function removeUpstream(row) {
  const gid = upstreamDrawer.group.id
  await ElMessageBox.confirm('确认删除该上游？', '提示', { type: 'warning' }).catch(() => 'cancel')
  try {
    await groupApi.deleteUpstream(gid, row.id)
    ElMessage.success('已删除')
    loadUpstreams(gid)
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
      <div class="flex-between drawer-toolbar">
        <span class="text-muted">命名变量模板：用户名里写 <code>{region}</code> 等占位，客户端尾段按名替换。</span>
        <el-button type="primary" size="small" :icon="'Plus'" @click="openCreateUpstream">添加上游</el-button>
      </div>

      <el-table :data="upstreamDrawer.list" border size="small" empty-text="暂无上游代理">
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

      <el-divider content-position="left">分组流量（24h）</el-divider>
      <EChart :option="groupChartOption" height="260px" />
    </el-drawer>

    <!-- 上游编辑对话框 -->
    <el-dialog v-model="upDialog.visible" :title="upDialog.isEdit ? '编辑上游' : '添加上游'" width="520px">
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
      <template #footer>
        <el-button @click="upDialog.visible = false">取消</el-button>
        <el-button type="primary" @click="saveUpstream">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.drawer-toolbar {
  margin-bottom: 12px;
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
