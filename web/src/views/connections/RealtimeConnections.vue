<script setup>
// 实时连接页面（US-005）。
// 轮询 /api/connections 展示当前所有活跃 SOCKS5 连接，支持按开始时间 / 连接时长排序。
// 被拒绝的连接在 server 层 Allow 阶段已关闭，结构上不进入活跃注册表，
// 因此本页动作列仅出现 forward / direct，无需处理 reject。
import { ref, watch, onMounted, onBeforeUnmount, onActivated, onDeactivated } from 'vue'
import { useI18n } from 'vue-i18n'
import { getActiveConnections } from '@/api/connections'

const { t } = useI18n()

// ── 响应式状态 ──────────────────────────────────────────────
const rows = ref([])           // 当前活跃连接列表
const total = ref(0)           // 后端返回的真实总数（可能超过 limit）
const truncated = ref(false)   // 是否因超出 limit=500 被截断
const sortBy = ref('start')    // 排序方式：'start'（开始时间）| 'duration'（连接时长）
const autoRefresh = ref(true)  // 是否开启自动刷新
const intervalSec = ref(5)     // 刷新间隔秒数（0 = 关闭）

// 定时器句柄，onBeforeUnmount 时清理，防止组件卸载后继续轮询
let timer = null

// ── 数据加载 ────────────────────────────────────────────────
// 请求当前活跃连接列表。
// axios 拦截器已 return resp.data，故直接解构 { items, total, truncated }。
async function load() {
  try {
    const r = await getActiveConnections({ limit: 500, sort: sortBy.value })
    rows.value = r.items ?? []
    total.value = r.total ?? 0
    truncated.value = r.truncated ?? false
  } catch {
    // 请求失败静默处理：axios 拦截器已弹 ElMessage，不重复提示
  }
}

// ── 定时器管理 ──────────────────────────────────────────────
// 先清旧定时器再按当前设置重建，保证切换排序/刷新开关/间隔时行为一致。
function restartTimer() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  if (autoRefresh.value && intervalSec.value > 0) {
    timer = setInterval(load, intervalSec.value * 1000)
  }
}

// 排序变更时立即重新加载（无需等下一个定时器周期）
watch(sortBy, load)

// 自动刷新开关或间隔变更时重建定时器
watch([autoRefresh, intervalSec], restartTimer)

// keep-alive 生命周期说明：
// 本页被包裹在 <keep-alive :max=6> 中，导航离开时组件不销毁（onBeforeUnmount 不触发），
// 而是进入"停用"状态（onDeactivated 触发）。若不在此处停定时器，轮询会在后台持续运行，
// 造成资源浪费和不必要的接口请求。
//
// 正确做法：
//   onActivated   — 首次挂载（onMounted 之后）以及每次从 keep-alive 缓存激活时均触发，
//                   在此刷新数据并启动定时器，保证回到本页时数据立即更新；
//   onDeactivated — 导航离开进入缓存时触发，在此停止定时器；
//   onBeforeUnmount — 组件真正销毁时（超出 keep-alive max 被淘汰）触发，兜底清理。
//
// 注意：onActivated 在首次挂载时也会触发（在 onMounted 之后），因此将 load()+restartTimer()
// 移入 onActivated 即可同时覆盖"首次进入"和"从缓存回来"两种场景，无需在 onMounted 里重复调用。
onMounted(() => {
  // 首次挂载时 onActivated 会紧随其后触发，无需在此重复 load()/restartTimer()
})

// 每次激活（首次挂载后 + 从 keep-alive 缓存恢复）时立即刷新并重启定时器
onActivated(() => {
  load()
  restartTimer()
})

// 导航离开、组件进入 keep-alive 缓存时停止轮询，避免后台空转
onDeactivated(() => {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
})

// 组件真正销毁时（被 keep-alive max 淘汰）兜底清理定时器
onBeforeUnmount(() => {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
})

// ── 工具函数 ────────────────────────────────────────────────
// 把 duration_sec 格式化为人可读字符串，如 "1m 23s" / "45s" / "2h 3m 4s"。
// 不复用 formatUptime（那个输出带天数，这里连接时长通常在小时以内，语义不同）。
function formatDuration(sec) {
  const s = Math.floor(Number(sec) || 0)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rs = s % 60
  if (m < 60) return rs > 0 ? `${m}m ${rs}s` : `${m}m`
  const h = Math.floor(m / 60)
  const rm = m % 60
  return rm > 0 ? `${h}h ${rm}m` : `${h}h`
}

// 动作 el-tag 颜色：forward 用默认蓝，direct 用绿色 success
function actionTagType(action) {
  if (action === 'direct') return 'success'
  return ''  // forward → 默认蓝
}
</script>

<template>
  <div class="connections-page">
    <!-- 页面标题 -->
    <div class="page-header">
      <h2 class="page-title">{{ t('menu.connections') }}</h2>
    </div>

    <!-- 工具栏：排序、自动刷新开关、刷新间隔 -->
    <el-card class="toolbar-card" shadow="never">
      <div class="toolbar">
        <!-- 排序方式 -->
        <div class="toolbar-item">
          <el-radio-group v-model="sortBy" size="small">
            <el-radio-button value="start">{{ t('connections.sortByStart') }}</el-radio-button>
            <el-radio-button value="duration">{{ t('connections.sortByDuration') }}</el-radio-button>
          </el-radio-group>
        </div>

        <!-- 自动刷新开关 -->
        <div class="toolbar-item">
          <el-switch v-model="autoRefresh" :active-text="t('connections.autoRefresh')" />
        </div>

        <!-- 刷新间隔选择 -->
        <div class="toolbar-item">
          <span class="toolbar-label">{{ t('connections.intervalLabel') }}：</span>
          <el-select v-model="intervalSec" size="small" style="width: 90px" :disabled="!autoRefresh">
            <el-option :label="t('connections.off')" :value="0" />
            <el-option label="2s" :value="2" />
            <el-option label="5s" :value="5" />
            <el-option label="10s" :value="10" />
          </el-select>
        </div>

        <!-- 手动刷新按钮 -->
        <div class="toolbar-item">
          <el-button size="small" @click="load">{{ t('common.refresh') }}</el-button>
        </div>
      </div>
    </el-card>

    <!-- 截断提示：超过 500 条时告知用户 -->
    <el-alert
      v-if="truncated"
      :title="t('connections.truncatedHint', { shown: rows.length, total })"
      type="warning"
      show-icon
      :closable="false"
      class="truncated-alert"
    />

    <!-- 拒绝连接说明：引导用户到系统日志查看审计记录 -->
    <div class="reject-help">
      <!-- 常驻上限说明：无论是否截断都显示，告知用户最多展示 500 条 -->
      <el-text type="info" size="small">{{ t('connections.limitHint') }}</el-text>
    </div>

    <!-- 活跃连接表格 -->
    <el-card shadow="never">
      <el-table
        :data="rows"
        stripe
        size="small"
        :empty-text="t('connections.empty')"
        style="width: 100%"
      >
        <!-- 目标主机 -->
        <el-table-column
          prop="target"
          :label="t('connections.colTarget')"
          min-width="180"
          show-overflow-tooltip
        />

        <!-- 动作：仅 forward / direct，用 el-tag 着色区分 -->
        <el-table-column :label="t('connections.colAction')" width="90">
          <template #default="{ row }">
            <el-tag :type="actionTagType(row.action)" size="small" disable-transitions>
              {{ t('action.' + row.action) }}
            </el-tag>
          </template>
        </el-table-column>

        <!-- 连接时长 -->
        <el-table-column :label="t('connections.colDuration')" width="100">
          <template #default="{ row }">
            {{ formatDuration(row.duration_sec) }}
          </template>
        </el-table-column>

        <!-- 上游代理地址：空串渲染 "—" -->
        <el-table-column :label="t('connections.colUpstream')" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.upstream || '—' }}
          </template>
        </el-table-column>

        <!-- 用户名 / 分组名 -->
        <el-table-column :label="t('connections.colUserGroup')" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.user }} / {{ row.group }}
          </template>
        </el-table-column>

        <!-- 客户端来源 IP:port -->
        <el-table-column
          prop="client"
          :label="t('connections.colClient')"
          min-width="140"
          show-overflow-tooltip
        />
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.connections-page {
  padding: 16px;
}

.page-header {
  margin-bottom: 16px;
}

.page-title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.toolbar-card {
  margin-bottom: 12px;
}

/* 工具栏横向排列，间距统一 */
.toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 16px;
}

.toolbar-item {
  display: flex;
  align-items: center;
  gap: 6px;
}

.toolbar-label {
  font-size: 13px;
  color: var(--el-text-color-regular);
  white-space: nowrap;
}

.truncated-alert {
  margin-bottom: 12px;
}

/* 拒绝连接说明：小字灰色，放在表格上方 */
.reject-help {
  margin-bottom: 8px;
  padding: 0 2px;
  /* 两条说明文案（rejectHelp / limitHint）竖排，避免行内挤在一起 */
  display: flex;
  flex-direction: column;
  gap: 2px;
}
</style>
