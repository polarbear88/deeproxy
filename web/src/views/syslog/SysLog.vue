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
// 按需引入图标组件（而非字符串 :icon="'Name'"），配合 unplugin-icons 自动按需打包
import { Delete, Refresh, Search, RefreshLeft } from '@element-plus/icons-vue'
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

// rAF 合批相关：高速流下逐条 push+滚动会频繁触发 Vue 响应式更新导致卡顿；
// 改为把每条日志先 push 进模块级 logBuffer，用 requestAnimationFrame 合并到下一帧统一
// 写入 logs.value，保证每帧最多渲染一次，浏览器绘制无积压。
let logBuffer = []
let rafHandle = null

// MAX_RENDER：前端渲染上限，与后端环形缓冲一致，防止全量 DOM 卡顿。
const MAX_RENDER = 5000

// 稳定 _id 自增序号：DynamicScroller 复用 recycler 需要稳定 key，
// 不能用 v-for 索引——MAX_RENDER splice 截断后索引会整体前移、破坏行复用。
let logSeq = 0
function withId(entry) {
  entry._id = ++logSeq
  return entry
}

function levelTag(l) {
  return { debug: 'info', info: 'success', warn: 'warning', error: 'danger' }[l] || 'info'
}

// ===== 滚动到底检测（R3：直接读 $el 原生属性）=====
// 为什么不用 @scroll / getScroll()：DynamicScroller 的滚动容器是其内部 .vue-recycle-scroller，
// 组件实例的 $el 即为该容器；直接监听 $el 的原生 scroll 事件并读 scrollTop/clientHeight/scrollHeight
// 最精确，不依赖 vue-virtual-scroller 内部 API（可能无 getScroll）。
const userAtBottom = ref(true)  // 用户是否处于底部，决定新日志是否自动跟随
const hasNew = ref(false)        // 是否有新日志未看（用于浮层提示）

// 判断是否已滚到底部（阈值 40px，避免像素精度问题误判）
function isAtBottom() {
  if (!scroller.value?.$el) return true
  const el = scroller.value.$el
  return el.scrollTop + el.clientHeight >= el.scrollHeight - 40
}

// scroll 事件回调：实时更新「是否在底部」状态
function onScrollerScroll() {
  userAtBottom.value = isAtBottom()
  // 用户手动滚到底说明已看到最新，清除「有新日志」提示
  if (userAtBottom.value) hasNew.value = false
}

// scroll 监听的 bind/unbind 封装，onMounted/onActivated 绑定、onDeactivated/onBeforeUnmount 解绑
function bindScrollListener() {
  nextTick(() => {
    scroller.value?.$el?.addEventListener('scroll', onScrollerScroll)
  })
}
function unbindScrollListener() {
  scroller.value?.$el?.removeEventListener('scroll', onScrollerScroll)
}

// visibilitychange 回调：标签页从后台切回前台时，若 buffer 有积压日志则立即排空
// 为什么需要：浏览器在标签页隐藏时会暂停/降频 rAF，但 SSE 仍在 appendLog 往 buffer 推数据；
// buffer 硬上限（MAX_RENDER）保证内存不无界增长；切回前台后此处一次性 flush 排空积压。
function onVisibilityChange() {
  if (!document.hidden && logBuffer.length > 0) {
    flushBuffer()
  }
}
function bindVisibilityListener() {
  document.addEventListener('visibilitychange', onVisibilityChange)
}
function unbindVisibilityListener() {
  document.removeEventListener('visibilitychange', onVisibilityChange)
}

// resetBuffer：清空 rAF + buffer，在清屏/切级别/加载快照前必须调用。
// 为什么：挂起的 rAF 携带旧级别/已清屏的 buffer 条目，若不取消，会在下一帧 flush
// 把旧数据灌进新列表，造成「清屏后旧日志重现」或「切级别后旧级别日志混入」的 bug（M2）。
function resetBuffer() {
  if (rafHandle !== null) {
    cancelAnimationFrame(rafHandle)
    rafHandle = null
  }
  logBuffer = []
}

// flushBuffer：rAF 回调，将 buffer 中积攒的日志一次性写入 logs.value，然后条件滚动。
// 合批逻辑：每帧只做一次 push + splice，大幅减少 Vue 响应式触发次数，解决高速流卡死问题。
function flushBuffer() {
  rafHandle = null
  if (logBuffer.length === 0) return
  // 一次性追加，触发一次 Vue 响应式更新（而非逐条触发）
  logs.value.push(...logBuffer.map(withId))
  logBuffer = []
  // 超出上限时从头部截断，保留最新 MAX_RENDER 条
  if (logs.value.length > MAX_RENDER) {
    logs.value.splice(0, logs.value.length - MAX_RENDER)
  }
  // 滚动策略：仅当用户在底部且自动滚动开启时跟随；否则出现「跳到最新」浮层
  if (autoScroll.value && userAtBottom.value) {
    scrollToBottom()
  } else if (autoScroll.value) {
    // 用户上滑看历史，出现「有新日志」提示，不打断阅读
    hasNew.value = true
  }
}

async function loadSnapshot() {
  // 加载快照前必须 resetBuffer，防止旧 rAF 把切级别前的 buffer 数据污染新快照
  resetBuffer()
  try {
    const list = (await syslogApi.getLogs({ level: level.value || undefined })) || []
    // 为快照每条打稳定 _id（供 DynamicScroller 复用 key）
    logs.value = list.map(withId)
    // 快照初始加载直接贴底，不受「流式暂停」逻辑影响
    scrollToBottom()
  } catch {
    logs.value = []
  }
}

function appendLog(entry) {
  // 级别筛选：前端再过滤一次（后端流可能已按级别过滤）
  if (level.value && entry.level !== level.value) return

  // 把新条目推入 buffer（不直接写 logs.value，避免逐条触发响应式）
  logBuffer.push(entry)

  // buffer 硬上限（M1）：必须在 appendLog 内做，独立于 flush 之外。
  // 原因：后台标签页 rAF 被浏览器暂停时 flush 不执行，若只在 flushBuffer 里截断，
  // buffer 会在后台无界增长耗尽内存；在 appendLog 内立即截断保证内存始终有界。
  if (logBuffer.length > MAX_RENDER) {
    logBuffer.splice(0, logBuffer.length - MAX_RENDER)
  }

  // 只在没有待执行的 rAF 时注册一个（避免同一帧注册多次）
  if (rafHandle === null) {
    rafHandle = requestAnimationFrame(flushBuffer)
  }
}

function scrollToBottom() {
  if (!autoScroll.value) return
  // DynamicScroller 自管滚动容器，旧的 logBox.scrollTop 不再适用；
  // 用 scrollToBottom() API 在内容渲染后滚到末项，实现 SSE 新行贴底。
  nextTick(() => {
    if (scroller.value) scroller.value.scrollToBottom()
    // 滚到底后同步 userAtBottom 状态
    userAtBottom.value = true
    hasNew.value = false
  })
}

// 点击「跳到最新」按钮：滚到底部并清除提示
function jumpToLatest() {
  scrollToBottom()
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

// teardown：关闭 SSE + 取消 rAF + 清空 buffer + 解绑所有监听（防泄漏）
function teardown() {
  closeStream()
  resetBuffer()
  unbindScrollListener()
  unbindVisibilityListener()
}

// 级别变化：重置 buffer 再拉快照 + 重连流（防旧 rAF 污染新级别列表，M2）
watch(level, () => {
  resetBuffer()
  loadSnapshot()
  openStream()
})

function clearScreen() {
  // 清屏前必须 resetBuffer，防止挂起的 rAF 在清屏后把旧条目 flush 回来（M2）
  resetBuffer()
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
  // 绑定滚动监听（nextTick 保证 scroller.$el 已挂载）
  bindScrollListener()
  // 绑定后台标签页 buffer 兜底监听
  bindVisibilityListener()
})
// keep-alive 缓存导致 onBeforeUnmount 不触发：本视图被 <keep-alive :max="6"> 缓存，
// 离开页面只会「停用(deactivate)」而非卸载，onBeforeUnmount 不会执行，SSE 会在后台持续
// 接收并 appendLog，造成 EventSource 泄漏。故在 onDeactivated 关流、onActivated 复订。
onDeactivated(() => {
  // 停用时完整 teardown：关 SSE + 取消 rAF + 清空 buffer + 解绑监听
  teardown()
})
onActivated(() => {
  // 重新激活时按与挂载/级别变更相同的次序：先拉快照再开流。
  // openStream 内部会先 closeStream，故即使残留旧流也不会泄漏出第二个 EventSource。
  loadSnapshot()
  openStream()
  // 重新激活时重新绑定监听（nextTick 保证 scroller.$el 已可用）
  bindScrollListener()
  bindVisibilityListener()
})
// 保留 onBeforeUnmount：当缓存条数超过 :max=6 触发 LRU 淘汰第 7 个页面时，组件会真正卸载，
// 此时仍需完整清理所有资源。
onBeforeUnmount(() => {
  teardown()
})
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
            <!-- 清屏按钮：使用标签形式图标（已迁移，配合按需打包） -->
            <el-button size="small" @click="clearScreen">
              <el-icon><Delete /></el-icon>
              清屏
            </el-button>
          </div>
          <!-- 刷新按钮：使用标签形式图标（已迁移） -->
          <el-button v-else size="small" @click="loadAudit">
            <el-icon><Refresh /></el-icon>
            {{ t('common.refresh') }}
          </el-button>
        </div>
      </template>

      <!-- 系统日志：终端风格滚动（DynamicScroller 虚拟滚动，仅渲染可视区域行） -->
      <!-- v-show 隐藏 audit tab 时仍保留 scroller 实例；key-field 用稳定 _id；
           min-item-size 给终端单行估值，size-dependencies 跟随 message/fields 变化重新测量可变行高 -->
      <!-- log-scroller-wrap 为相对定位容器，让「跳到最新」浮层按钮可用 position:absolute 定位 -->
      <div v-show="activeTab === 'log'" class="log-scroller-wrap">
        <DynamicScroller
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

        <!-- 「跳到最新」浮层：仅当有新日志且用户不在底部时显示。
             为什么用浮层而非强制滚动：用户上滑看历史时不应强行打断，给一个显眼的跳转入口。
             交互真值表：
               autoScroll=true  & userAtBottom=true  → 自动跟随，无浮层
               autoScroll=true  & userAtBottom=false → 暂停跟随，有新日志时出现浮层
               autoScroll=false                      → 完全手动，不显示浮层（用户主动关了自动滚动）-->
        <div
          v-show="hasNew && !userAtBottom && autoScroll"
          class="jump-to-latest"
          @click="jumpToLatest"
        >
          {{ t('common.jumpToLatest') }}
        </div>
      </div>

      <!-- 连接审计 -->
      <div v-show="activeTab === 'audit'">
        <!-- 四维筛选条（user 精确 / target 子串 / action 精确 / group 精确），服务端筛选 -->
        <div class="audit-filter flex-row">
          <el-input
            v-model="auditFilter.user"
            :placeholder="t('syslog.filterUser')"
            clearable
            style="width: 160px"
            @keyup.enter="searchAudit"
          />
          <el-input
            v-model="auditFilter.target"
            :placeholder="t('syslog.filterTarget')"
            clearable
            style="width: 200px"
            @keyup.enter="searchAudit"
          />
          <el-select
            v-model="auditFilter.action"
            :placeholder="t('syslog.filterAction')"
            clearable
            style="width: 150px"
          >
            <el-option :label="t('action.forward')" value="forward" />
            <el-option :label="t('action.direct')" value="direct" />
            <el-option :label="t('action.reject')" value="reject" />
          </el-select>
          <el-input
            v-model="auditFilter.group"
            :placeholder="t('syslog.filterGroup')"
            clearable
            style="width: 160px"
            @keyup.enter="searchAudit"
          />
          <!-- 搜索/重置按钮：使用标签形式图标（已迁移） -->
          <el-button type="primary" @click="searchAudit">
            <el-icon><Search /></el-icon>
            {{ t('common.search') }}
          </el-button>
          <el-button @click="resetAudit">
            <el-icon><RefreshLeft /></el-icon>
            {{ t('common.reset') }}
          </el-button>
        </div>

        <el-table class="audit-table" :data="audit" border :empty-text="t('syslog.emptyAudit')">
          <el-table-column :label="t('syslog.colTime')" width="180">
            <template #default="{ row }">{{ formatTime(row.time) }}</template>
          </el-table-column>
          <el-table-column prop="user" :label="t('syslog.colUser')" width="130" />
          <el-table-column prop="group" :label="t('syslog.colGroup')" width="130" />
          <el-table-column prop="target" :label="t('syslog.colTarget')" min-width="180" show-overflow-tooltip />
          <el-table-column :label="t('syslog.colAction')" width="110">
            <template #default="{ row }">
              <el-tag :type="actionTag(row.action)" effect="plain">{{ row.action }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="upstream" :label="t('syslog.colUpstream')" min-width="150" show-overflow-tooltip />
          <el-table-column :label="t('syslog.colUp')" width="110">
            <template #default="{ row }">{{ formatBytes(row.upBytes) }}</template>
          </el-table-column>
          <el-table-column :label="t('syslog.colDown')" width="110">
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

// 相对定位包裹容器，让「跳到最新」按钮可以 position:absolute 定位在 log-box 右下角
.log-scroller-wrap {
  position: relative;
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

// 「跳到最新」浮层按钮：固定在 log-box 右下角，用于高速流下用户上滑看历史时快速回到底部。
// 为什么用 position:absolute 而非 fixed：避免影响其他页面布局；relative 父容器限定其范围。
.jump-to-latest {
  position: absolute;
  bottom: 16px;
  right: 20px;
  background: var(--el-color-primary);
  color: #fff;
  padding: 6px 14px;
  border-radius: 16px;
  font-size: 12px;
  cursor: pointer;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
  user-select: none;
  transition: opacity 0.2s;
  &:hover {
    opacity: 0.85;
  }
}

// ===== 连接审计：筛选条与表格放大 + 间隔 + 分页右对齐 =====
// 筛选条各控件之间留间距，并与下方表格拉开 16px 间隔（原先紧贴无间隔）。
.audit-filter {
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
// 审计表格整体放大字号（去掉 size="small" 后默认偏小，这里再提一档到 14px）。
.audit-table {
  font-size: 14px;
  :deep(.el-table__header) th {
    font-size: 14px;
  }
}
// 分页器靠右对齐，并与表格留出上间距。
.audit-pagination {
  margin-top: 16px;
  justify-content: flex-end;
}
</style>
