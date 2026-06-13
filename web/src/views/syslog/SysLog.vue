<script setup>
// 系统日志（AC-33/34/35）：SSE 实时滚动 + 级别筛选 + 暗亮适配。
// 含「系统日志」与「连接审计」两个标签（AC-36）。日志仅内存环形缓冲，重启清空。
// 已对齐 T7：后端用默认(无名)SSE 事件，故用 onmessage 接收。
import { onMounted, onBeforeUnmount, ref, nextTick, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as syslogApi from '@/api/syslog'
import { formatTime, formatBytes } from '@/utils/format'

const activeTab = ref('log')

// ===== 系统日志 =====
const level = ref('') // 空=全部
const logs = ref([])
const autoScroll = ref(true)
const logBox = ref(null)
const connected = ref(false)
let es = null

const MAX_RENDER = 5000 // 与后端环形缓冲一致，前端也限制渲染条数防卡顿

function levelTag(l) {
  return { debug: 'info', info: 'success', warn: 'warning', error: 'danger' }[l] || 'info'
}

async function loadSnapshot() {
  try {
    logs.value = (await syslogApi.getLogs({ level: level.value || undefined })) || []
    scrollToBottom()
  } catch {
    logs.value = []
  }
}

function appendLog(entry) {
  // 级别筛选：前端再过滤一次（后端流可能已按级别过滤）。
  if (level.value && entry.level !== level.value) return
  logs.value.push(entry)
  if (logs.value.length > MAX_RENDER) logs.value.splice(0, logs.value.length - MAX_RENDER)
  scrollToBottom()
}

function scrollToBottom() {
  if (!autoScroll.value) return
  nextTick(() => {
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
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

// ===== 连接审计（AC-36）=====
const audit = ref([])
async function loadAudit() {
  try {
    audit.value = (await syslogApi.getAuditLogs()) || []
  } catch {
    audit.value = []
  }
}
watch(activeTab, (t) => {
  if (t === 'audit') loadAudit()
})

function actionTag(action) {
  return action === 'forward' ? 'success' : action === 'direct' ? 'primary' : 'danger'
}

onMounted(() => {
  loadSnapshot()
  openStream()
})
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

      <!-- 系统日志：终端风格滚动 -->
      <div v-show="activeTab === 'log'" ref="logBox" class="log-box">
        <div v-for="(l, i) in logs" :key="i" class="log-line">
          <span class="log-time">{{ formatTime(l.time) }}</span>
          <el-tag :type="levelTag(l.level)" size="small" effect="dark" class="log-level">
            {{ (l.level || '').toUpperCase() }}
          </el-tag>
          <span class="log-msg">{{ l.message }}</span>
          <span v-if="l.fields" class="log-fields">{{ JSON.stringify(l.fields) }}</span>
        </div>
        <div v-if="logs.length === 0" class="log-empty text-muted">暂无日志（重启后清空，无历史）</div>
      </div>

      <!-- 连接审计 -->
      <el-table v-show="activeTab === 'audit'" :data="audit" border size="small" empty-text="暂无审计记录">
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatTime(row.time) }}</template>
        </el-table-column>
        <el-table-column prop="user" label="用户" width="120" />
        <el-table-column prop="group" label="分组" width="120" />
        <el-table-column prop="target" label="目标" min-width="180" show-overflow-tooltip />
        <el-table-column label="动作" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="actionTag(row.action)" effect="plain">{{ row.action }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="upstream" label="上游" min-width="150" show-overflow-tooltip />
        <el-table-column label="上行" width="100">
          <template #default="{ row }">{{ formatBytes(row.upBytes) }}</template>
        </el-table-column>
        <el-table-column label="下行" width="100">
          <template #default="{ row }">{{ formatBytes(row.downBytes) }}</template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped lang="scss">
.log-tabs {
  :deep(.el-tabs__header) {
    margin: 0;
  }
}
.toolbar {
  gap: 12px;
}
.log-box {
  height: calc(100vh - 200px);
  overflow-y: auto;
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
  white-space: nowrap;
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
