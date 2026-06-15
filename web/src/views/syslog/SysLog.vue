<script setup>
// 系统日志（AC-33/34/35）：SSE 实时滚动 + 级别筛选 + 暗亮适配。
// 含「系统日志」与「连接审计」两个标签（AC-36）。日志仅内存环形缓冲，重启清空。
// 已对齐 T7：后端用默认(无名)SSE 事件，故用 onmessage 接收。
import { onMounted, onBeforeUnmount, onActivated, onDeactivated, ref, nextTick, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
// 虚拟滚动：日志行非定高（.log-msg 是 pre-wrap+break-all、.log-fields 是无界 JSON.stringify），
// 故用 vue-virtual-scroller 的 DynamicScroller（原生按内容测量缓存可变行高），只渲染可视区域，
// 避免 5000 行全量 DOM 卡顿；定高方案会因可变行高抖动，被排除。
import { DynamicScroller, DynamicScrollerItem } from 'vue-virtual-scroller'
import 'vue-virtual-scroller/dist/vue-virtual-scroller.css'
import * as syslogApi from '@/api/syslog'
import { formatTime, formatBytes } from '@/utils/format'

const { t } = useI18n()

const activeTab = ref('log')

// ===== 系统日志 =====
const level = ref('') // 空=全部
const logs = ref([])
const autoScroll = ref(true)
const scroller = ref(null) // DynamicScroller 组件实例引用，用于贴底滚动
const connected = ref(false)
let es = null

// 稳定 _id 自增序号：DynamicScroller 复用 recycler 需要稳定 key，
// 不能用 v-for 索引——MAX_RENDER splice 截断后索引会整体前移、破坏行复用。
let logSeq = 0
function withId(entry) {
  entry._id = ++logSeq
  return entry
}

const MAX_RENDER = 5000 // 与后端环形缓冲一致，前端也限制渲染条数防卡顿

function levelTag(l) {
  return { debug: 'info', info: 'success', warn: 'warning', error: 'danger' }[l] || 'info'
}

async function loadSnapshot() {
  try {
    const list = (await syslogApi.getLogs({ level: level.value || undefined })) || []
    // 为快照每条打稳定 _id（供 DynamicScroller 复用 key）
    logs.value = list.map(withId)
    scrollToBottom()
  } catch {
    logs.value = []
  }
}

function appendLog(entry) {
  // 级别筛选：前端再过滤一次（后端流可能已按级别过滤）。
  if (level.value && entry.level !== level.value) return
  logs.value.push(withId(entry))
  if (logs.value.length > MAX_RENDER) logs.value.splice(0, logs.value.length - MAX_RENDER)
  scrollToBottom()
}

function scrollToBottom() {
  if (!autoScroll.value) return
  // DynamicScroller 自管滚动容器，旧的 logBox.scrollTop 不再适用；
  // 用 scrollToBottom() API 在内容渲染后滚到末项，实现 SSE 新行贴底。
  nextTick(() => {
    if (scroller.value) scroller.value.scrollToBottom()
  })
}

function openStream() {
  closeStream()
  try {
    es = syslogApi.openLogStream(level.value || undefined)
    es.onopen = () => {
      connected.value = true
    }
    // 后端用默认(无名)SSE 事件，故用 onmessage 接收。
    es.onmessage = (ev) => {
      try {
        appendLog(JSON.parse(ev.data))
      } catch {
        /* 非 JSON 行忽略 */
      }
    }
    es.onerror = () => {
      connected.value = false
      // EventSource 会自动重连，无需手动处理。
    }
  } catch {
    connected.value = false
  }
}
function closeStream() {
  if (es) {
    es.close()
    es = null
  }
  connected.value = false
}

// 级别变化：重拉快照 + 重连流。
watch(level, () => {
  loadSnapshot()
  openStream()
})

function clearScreen() {
  logs.value = []
  ElMessage.success('已清屏（仅前端，不影响后端缓冲）')
}

// ===== 连接审计（AC-36）：服务端分页 + 四维筛选 =====
const audit = ref([]) // 当前页记录
const auditTotal = ref(0) // 后端返回的筛选后总条数（驱动分页器页数）
const auditPage = ref(1)
const auditPageSize = ref(50) // 默认 50，可选 50/100/200（上限 200 由后端钳制）
// 四维筛选条件：与后端 query 参数同名；空串=该维不筛。
const auditFilter = ref({ user: '', target: '', action: '', group: '' })

// 请求序号：快速切换筛选/翻页/页大小会并发多个请求，较慢的旧请求可能后到并覆盖新结果。
// 每次发起请求自增并记录序号，响应回来时若已非最新序号则丢弃，避免乱序响应污染当前页。
let auditReqSeq = 0

// loadAudit：按当前页码 + 页大小 + 四维筛选向后端请求一页审计记录。
// 为什么服务端分页：审计可能有大量记录，前端只拉一页避免全量渲染卡顿；total 用于分页器。
async function loadAudit() {
  // 捕获本次请求序号；await 后若已被更新的请求取代则丢弃本次结果（防乱序覆盖）。
  const seq = ++auditReqSeq
  try {
    const res = await syslogApi.getAuditLogs({
      page: auditPage.value,
      pageSize: auditPageSize.value,
      user: auditFilter.value.user || undefined,
      target: auditFilter.value.target || undefined,
      action: auditFilter.value.action || undefined,
      group: auditFilter.value.group || undefined,
    })
    if (seq !== auditReqSeq) return // 已有更新请求发出，丢弃这个过期响应
    audit.value = res?.items || []
    auditTotal.value = res?.total || 0
  } catch {
    if (seq !== auditReqSeq) return // 过期请求的失败也不应覆盖最新状态
    audit.value = []
    auditTotal.value = 0
  }
}
// 查询：筛选条件变化后回到第 1 页再拉（否则可能停在越界页码）。
function searchAudit() {
  auditPage.value = 1
  loadAudit()
}
// 重置：清空四维筛选并回到第 1 页重新拉取。
function resetAudit() {
  auditFilter.value = { user: '', target: '', action: '', group: '' }
  auditPage.value = 1
  loadAudit()
}
// 翻页：el-pagination 当前页变化时重拉对应页。
function onAuditPageChange(p) {
  auditPage.value = p
  loadAudit()
}
// 改每页条数：回到第 1 页重拉（条数变化后旧页码可能越界）。
function onAuditSizeChange(s) {
  auditPageSize.value = s
  auditPage.value = 1
  loadAudit()
}
watch(activeTab, (t) => {
  if (t === 'audit') {
    // 切到审计 tab 时拉第 1 页（带当前筛选）。
    auditPage.value = 1
    loadAudit()
  }
})

function actionTag(action) {
  return action === 'forward' ? 'success' : action === 'direct' ? 'primary' : 'danger'
}

onMounted(() => {
  loadSnapshot()
  openStream()
})
// keep-alive 缓存导致 onBeforeUnmount 不触发：本视图被 <keep-alive :max="6"> 缓存，
// 离开页面只会「停用(deactivate)」而非卸载，onBeforeUnmount 不会执行，SSE 会在后台持续
// 接收并 appendLog，造成 EventSource 泄漏。故在 onDeactivated 关流、onActivated 复订。
onDeactivated(closeStream)
onActivated(() => {
  // 重新激活时按与挂载/级别变更相同的次序：先拉快照再开流。
  // openStream 内部会先 closeStream，故即使残留旧流也不会泄漏出第二个 EventSource。
  loadSnapshot()
  openStream()
})
// 保留 onBeforeUnmount：当缓存条数超过 :max=6 触发 LRU 淘汰第 7 个页面时，组件会真正卸载，
// 此时仍需关闭 SSE 流。
onBeforeUnmount(closeStream)
</script>

<template>
  <div class="dp-page">
    <el-card>
      <template #header>
        <div class="flex-between">
          <el-tabs v-model="activeTab" class="log-tabs">
            <el-tab-pane label="系统日志" name="log" />
            <el-tab-pane label="连接审计" name="audit" />
          </el-tabs>
          <div v-if="activeTab === 'log'" class="flex-row toolbar">
            <el-tag :type="connected ? 'success' : 'info'" effect="plain" size="small">
              {{ connected ? '实时连接中' : '未连接' }}
            </el-tag>
            <el-select v-model="level" placeholder="级别" size="small" style="width: 110px" clearable>
              <el-option label="DEBUG" value="debug" />
              <el-option label="INFO" value="info" />
              <el-option label="WARN" value="warn" />
              <el-option label="ERROR" value="error" />
            </el-select>
            <el-checkbox v-model="autoScroll" size="small">自动滚动</el-checkbox>
            <el-button size="small" :icon="'Delete'" @click="clearScreen">清屏</el-button>
          </div>
          <el-button v-else size="small" :icon="'Refresh'" @click="loadAudit">刷新</el-button>
        </div>
      </template>

      <!-- 系统日志：终端风格滚动（DynamicScroller 虚拟滚动，仅渲染可视区域行） -->
      <!-- v-show 隐藏 audit tab 时仍保留 scroller 实例；key-field 用稳定 _id；
           min-item-size 给终端单行估值，size-dependencies 跟随 message/fields 变化重新测量可变行高 -->
      <DynamicScroller
        v-show="activeTab === 'log'"
        ref="scroller"
        :items="logs"
        :min-item-size="22"
        key-field="_id"
        class="log-box"
      >
        <template #default="{ item: l, index, active }">
          <DynamicScrollerItem
            :item="l"
            :active="active"
            :data-index="index"
            :size-dependencies="[l.message, l.fields]"
          >
            <div class="log-line">
              <span class="log-time">{{ formatTime(l.time) }}</span>
              <el-tag :type="levelTag(l.level)" size="small" effect="dark" class="log-level">
                {{ (l.level || '').toUpperCase() }}
              </el-tag>
              <span class="log-msg">{{ l.message }}</span>
              <span v-if="l.fields" class="log-fields">{{ JSON.stringify(l.fields) }}</span>
            </div>
          </DynamicScrollerItem>
        </template>
        <template #after>
          <div v-if="logs.length === 0" class="log-empty text-muted">暂无日志（重启后清空，无历史）</div>
        </template>
      </DynamicScroller>

      <!-- 连接审计 -->
      <div v-show="activeTab === 'audit'">
        <!-- 四维筛选条（user 精确 / target 子串 / action 精确 / group 精确），服务端筛选 -->
        <div class="audit-filter flex-row">
          <el-input
            v-model="auditFilter.user"
            :placeholder="t('syslog.filterUser')"
            size="small"
            clearable
            style="width: 150px"
            @keyup.enter="searchAudit"
          />
          <el-input
            v-model="auditFilter.target"
            :placeholder="t('syslog.filterTarget')"
            size="small"
            clearable
            style="width: 180px"
            @keyup.enter="searchAudit"
          />
          <el-select
            v-model="auditFilter.action"
            :placeholder="t('syslog.filterAction')"
            size="small"
            clearable
            style="width: 130px"
          >
            <el-option :label="t('action.forward')" value="forward" />
            <el-option :label="t('action.direct')" value="direct" />
            <el-option :label="t('action.reject')" value="reject" />
          </el-select>
          <el-input
            v-model="auditFilter.group"
            :placeholder="t('syslog.filterGroup')"
            size="small"
            clearable
            style="width: 150px"
            @keyup.enter="searchAudit"
          />
          <el-button type="primary" size="small" :icon="'Search'" @click="searchAudit">{{ t('common.search') }}</el-button>
          <el-button size="small" :icon="'RefreshLeft'" @click="resetAudit">{{ t('common.reset') }}</el-button>
        </div>

        <el-table :data="audit" border size="small" :empty-text="t('syslog.emptyAudit')">
          <el-table-column :label="t('syslog.colTime')" width="170">
            <template #default="{ row }">{{ formatTime(row.time) }}</template>
          </el-table-column>
          <el-table-column prop="user" :label="t('syslog.colUser')" width="120" />
          <el-table-column prop="group" :label="t('syslog.colGroup')" width="120" />
          <el-table-column prop="target" :label="t('syslog.colTarget')" min-width="180" show-overflow-tooltip />
          <el-table-column :label="t('syslog.colAction')" width="100">
            <template #default="{ row }">
              <el-tag size="small" :type="actionTag(row.action)" effect="plain">{{ row.action }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="upstream" :label="t('syslog.colUpstream')" min-width="150" show-overflow-tooltip />
          <el-table-column :label="t('syslog.colUp')" width="100">
            <template #default="{ row }">{{ formatBytes(row.upBytes) }}</template>
          </el-table-column>
          <el-table-column :label="t('syslog.colDown')" width="100">
            <template #default="{ row }">{{ formatBytes(row.downBytes) }}</template>
          </el-table-column>
        </el-table>

        <!-- 服务端分页：total 来自后端筛选后真实条数；50/100/200 三档 -->
        <el-pagination
          class="audit-pagination"
          :current-page="auditPage"
          :page-size="auditPageSize"
          :page-sizes="[50, 100, 200]"
          :total="auditTotal"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="onAuditPageChange"
          @size-change="onAuditSizeChange"
        />
      </div>
    </el-card>
  </div>
</template>

<style scoped lang="scss">
@use '@/styles/responsive.scss' as r;

.log-tabs {
  :deep(.el-tabs__header) {
    margin: 0;
  }
}
.toolbar {
  gap: 12px;
  // 手机端：工具条（实时标签/级别选择/自动滚动/清空）允许换行，避免与 tabs 同行挤爆
  @include r.mobile {
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 8px;
  }
}
.log-box {
  height: calc(100vh - 200px);
  // 滚动由 DynamicScroller（.vue-recycle-scroller）自身的 overflow-y:auto 接管，
  // 这里只给高度；虚拟滚动下每行绝对定位，旧的 overflow-x 横滚 + min-width:max-content 失效，
  // 故去掉横滚，依赖 .log-msg 的 pre-wrap+break-all 在宽度内换行（DynamicScroller 测量该可变行高）。
  background: var(--el-fill-color-darker);
  border-radius: 6px;
  padding: 10px 12px;
  font-family: 'SF Mono', Menlo, Consolas, monospace;
  font-size: 12.5px;
  line-height: 1.7;
}
.log-line {
  display: flex;
  align-items: baseline;
  gap: 8px;
  // 虚拟滚动下行宽固定为容器宽，超长内容靠 log-msg 换行；不再用 nowrap+max-content 横滚。
}
.log-time {
  color: var(--el-text-color-secondary);
  flex-shrink: 0;
}
.log-level {
  flex-shrink: 0;
  width: 56px;
  text-align: center;
}
.log-msg {
  color: var(--el-text-color-primary);
  white-space: pre-wrap;
  word-break: break-all;
}
.log-fields {
  color: var(--el-text-color-secondary);
}
.log-empty {
  text-align: center;
  padding: 40px 0;
}
</style>
