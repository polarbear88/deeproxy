<script setup>
// 仪表盘（AC-24/27）。已对齐 T7 实测契约：
// - GET /dashboard/overview → 扁平 { upRate,downRate,activeConns,todayUp,todayDown,todayReq,
//     todayRejectRule,todayRejectAuth,healthyProxies,totalProxies,uptimeSec }。
// - GET /dashboard/timeseries?window= → { times,up,down,req }。
// - GET /dashboard/action-dist → [{name,value}]。
// - GET /dashboard/top?kind=group|user|domain → group/user:[{name,bytes}]、domain:[{name,count}]。
// - GET /dashboard/runtime → { memMB,goroutines,groups:[{id,name,healthy,total,allDown}] }。
import { onMounted, onBeforeUnmount, onActivated, onDeactivated, ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import EChart from '@/components/EChart.js'
import StatCard from '@/components/StatCard.vue'
import { useThemeStore } from '@/stores/theme'
import * as dashApi from '@/api/dashboard'
import { getServerInfo } from '@/api/system'
import { formatBytes, formatRate, formatNumber, formatUptime, formatBucketTime, formatAxisTime } from '@/utils/format'

// i18n：仅展示层翻译，统计数据 name 字段（forward/direct/reject）保持原始值
const { t } = useI18n()

const overview = ref({
  upRate: 0,
  downRate: 0,
  activeConns: 0,
  todayUp: 0,
  todayDown: 0,
  todayReq: 0,
  todayRejectRule: 0,
  todayRejectAuth: 0,
  healthyProxies: 0,
  totalProxies: 0,
  uptimeSec: 0,
  version: '',
})
// 服务端连接信息（专用端点 GET /settings/server-info，camelCase）：
// SOCKS5 监听端口、Web 管理端口、对外地址，供首页端口展示与连接示例使用。
const serverInfo = ref({ serverAddr: '', socks5Port: 0, webPort: 0 })
const runtime = ref({ memMB: 0, goroutines: 0, groups: [] })
const timeWindow = ref('24h')
const tsData = ref({ times: [], up: [], down: [], req: [] })
const actionDist = ref([])
const topGroups = ref([])
const topUsers = ref([])
const topDomains = ref([])
// Top 目标域名卡片独立时间窗（24h/7d，默认 24h；与上方流量图 timeWindow 解耦）。
const domainWindow = ref('24h')

const themeStore = useThemeStore()

// 饼图扇区描边色：Canvas 渲染器无法解析 CSS 变量（var(--el-bg-color)），
// 必须在运行时读取计算后的真实色值，否则亮色下退化为黑边、暗色下不一致。
// 随主题切换重新解析（toggle 会改 isDark）。
const pieBorderColor = ref('#ffffff')
function resolvePieBorderColor() {
  // 从根元素读取 Element Plus 注入的 --el-bg-color 计算值（亮色白/暗色深灰）
  const v = getComputedStyle(document.documentElement)
    .getPropertyValue('--el-bg-color')
    .trim()
  pieBorderColor.value = v || (themeStore.isDark ? '#141414' : '#ffffff')
}
// 主题切换后等 .dark 类与 CSS 变量更新到 DOM，再重新解析（nexttick 后变量才生效）
watch(
  () => themeStore.isDark,
  () => {
    requestAnimationFrame(resolvePieBorderColor)
  },
)

let realtimeTimer = null

// startRealtimeTimer：启动 3s 实时轮询（仅刷新 overview/runtime）。幂等：
// 若已有定时器则先清掉再建，避免在 onMounted 与 onActivated 都调用时叠加出多个定时器。
function startRealtimeTimer() {
  if (realtimeTimer) clearInterval(realtimeTimer)
  realtimeTimer = setInterval(() => {
    loadOverview()
    loadRuntime()
  }, 3000)
}
// stopRealtimeTimer：停止轮询并置空，供 onDeactivated/onBeforeUnmount 复用。
function stopRealtimeTimer() {
  if (realtimeTimer) clearInterval(realtimeTimer)
  realtimeTimer = null
}

async function loadOverview() {
  try {
    const d = await dashApi.getOverview()
    if (d) Object.assign(overview.value, d)
  } catch {
    /* ignore */
  }
}
// 拉取服务端连接信息（专用端点，与概览解耦）。失败时保留默认零值，模板侧有兜底。
async function loadServerInfo() {
  try {
    const d = await getServerInfo()
    if (d) serverInfo.value = { serverAddr: d.serverAddr || '', socks5Port: d.socks5Port || 0, webPort: d.webPort || 0 }
  } catch {
    /* ignore */
  }
}
async function loadRuntime() {
  try {
    const d = await dashApi.getRuntimeHealth()
    if (d) runtime.value = { memMB: d.memMB, goroutines: d.goroutines, groups: d.groups || [] }
  } catch {
    /* ignore */
  }
}
async function loadTimeseries() {
  try {
    const d = await dashApi.getTimeseries({ window: timeWindow.value })
    if (d) tsData.value = d
  } catch {
    /* ignore */
  }
}
async function loadActionDist() {
  try {
    actionDist.value = (await dashApi.getActionDist({ window: timeWindow.value })) || []
  } catch {
    /* ignore */
  }
}
async function loadTop() {
  // Top 分组、Top 用户：固定「今日」窗口，随上方流量图时间窗刷新一并加载。
  try {
    const [g, u] = await Promise.all([
      dashApi.getTopN({ kind: 'group', limit: 5 }),
      dashApi.getTopN({ kind: 'user', limit: 5 }),
    ])
    topGroups.value = g || []
    topUsers.value = u || []
  } catch {
    topGroups.value = []
    topUsers.value = []
  }
}

// loadTopDomains 独立加载 Top 目标域名，使用卡片自己的 domainWindow（24h/7d）。
// 与 loadTop 拆开：切换该卡片时间窗时只刷新这一张卡片，不波及分组/用户表。
async function loadTopDomains() {
  try {
    // top50：limit 扩大到 50，后端 handleTop 已支持动态 limit，前端同步调大保证数据完整
    const d = await dashApi.getTopN({ kind: 'domain', limit: 50, window: domainWindow.value })
    topDomains.value = d || []
  } catch {
    topDomains.value = []
  }
}

function reloadByWindow() {
  loadTimeseries()
  loadActionDist()
  loadTop()
}

const todayReject = computed(() => overview.value.todayRejectRule + overview.value.todayRejectAuth)
const healthRatio = computed(() => {
  const t = overview.value.totalProxies || 0
  return t === 0 ? 0 : Math.round((overview.value.healthyProxies / t) * 100)
})

// 对外地址：后端 server-info 给出 serverAddr 时优先用，否则回退到当前页面 host。
const serverHost = computed(() => {
  const a = (serverInfo.value.serverAddr || '').trim()
  if (a) return a
  return (typeof window !== 'undefined' && window.location.hostname) || '127.0.0.1'
})

// 真实 V2 连接示例：socks5://<user>-<group>:<pwd>@<server-addr>:<socks5-port>
// 用占位 user/group/pwd，端口取真实监听端口，便于用户直接照抄替换。
// 计算属性内调用 t，密码占位随语言切换重算（R-9）。
const connectionExample = computed(() => {
  const port = serverInfo.value.socks5Port || 1080
  return `socks5://alice-default:<${t('dashboard.passwordPlaceholder')}>@${serverHost.value}:${port}`
})

const tsOption = computed(() => ({
  // tooltip 标题把后端桶时间格式化为 2000/01/01 00:00:00 样式；
  // 上行/下行按字节可读化，请求数按千分位，避免显示原始 RFC3339 字符串与裸数字。
  tooltip: {
    trigger: 'axis',
    formatter: (params) => {
      if (!params || !params.length) return ''
      const title = formatBucketTime(params[0].axisValue)
      const reqName = t('dashboard.legendReq')
      const lines = params.map((p) => {
        const val = p.seriesName === reqName ? formatNumber(p.value) : formatBytes(p.value)
        return `${p.marker}${p.seriesName}: ${val}`
      })
      return [title, ...lines].join('<br/>')
    },
  },
  legend: { data: [t('dashboard.legendUp'), t('dashboard.legendDown'), t('dashboard.legendReq')] },
  // containLabel 让 ECharts 按双侧刻度文字实际宽度预留空间，
  // 避免左轴 formatBytes 长文本（如 "1.23 MB"）被容器左边缘截断（右轴请求数同理）。
  grid: { left: 8, right: 8, top: 40, bottom: 30, containLabel: true },
  xAxis: { type: 'category', boundaryGap: false, data: tsData.value.times, axisLabel: { formatter: (v) => formatAxisTime(v) } },
  yAxis: [
    { type: 'value', name: t('dashboard.axisTraffic'), axisLabel: { formatter: (v) => formatBytes(v) } },
    { type: 'value', name: t('dashboard.axisReq'), position: 'right' },
  ],
  series: [
    { name: t('dashboard.legendUp'), type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: tsData.value.up },
    { name: t('dashboard.legendDown'), type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: tsData.value.down },
    { name: t('dashboard.legendReq'), type: 'line', yAxisIndex: 1, smooth: true, data: tsData.value.req },
  ],
}))

const actionOption = computed(() => {
  // 原始数据：name 为后端英文动作值（forward/direct/reject），value 为计数。
  // 展示层把 name 翻译为本地化标签（在 computed 内调用 t，随语言切换重算 — R-9），
  // value 始终保持原始计数不变。占位空数据同样翻译。
  const raw =
    actionDist.value.length > 0
      ? actionDist.value
      : [
          { name: 'forward', value: 0 },
          { name: 'direct', value: 0 },
          { name: 'reject', value: 0 },
        ]
  const data = raw.map((d) => ({ name: t('action.' + d.name), value: d.value }))
  return {
    tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
    legend: { bottom: 0 },
    series: [
      {
        name: t('dashboard.actionDist'),
        type: 'pie',
        radius: ['45%', '70%'],
        avoidLabelOverlap: true,
        itemStyle: { borderRadius: 6, borderColor: pieBorderColor.value, borderWidth: 2 },
        label: { show: false },
        data,
      },
    ],
  }
})

// topDomainOption 已删除：改为 el-table 列表显示，支持 top50 可滚动浏览，无需 ECharts 图表

onMounted(() => {
  resolvePieBorderColor()
  loadOverview()
  loadServerInfo()
  loadRuntime()
  reloadByWindow()
  loadTopDomains()
  // 注意：实时轮询定时器不在此启动。对 keep-alive 组件，onActivated 在首次挂载后也会触发，
  // 若这里再 setInterval 一次会与 onActivated 里的 startRealtimeTimer 叠加出双定时器。
  // 故统一交由 onActivated 启动（startRealtimeTimer 幂等，安全）。
})
// keep-alive 缓存导致 onBeforeUnmount 不触发：本视图被 <keep-alive :max="6"> 缓存，
// 离开页面只「停用」不卸载，若仅靠 onBeforeUnmount 清定时器，3s 轮询会在后台持续触发。
// 故在 onDeactivated 停表，onActivated 复启。
onDeactivated(stopRealtimeTimer)
// 保留 onBeforeUnmount：缓存超过 :max=6 触发 LRU 淘汰时组件真正卸载，此时仍需停掉定时器。
onBeforeUnmount(stopRealtimeTimer)
// keep-alive 下切回仪表盘不会重新 mount，故 onMounted 不再触发；
// 用 onActivated 钩子在每次激活时：① 重拉动作分布，使离开期间产生的新动作及时反映；
// ② 重启实时轮询定时器，恢复 overview/runtime 的 3s 刷新。
// 为什么单独拉动作分布：3s 轮询刻意只刷新 overview/runtime，不含动作分布。
onActivated(() => {
  loadActionDist()
  startRealtimeTimer()
})
</script>

<template>
  <div class="dp-page">
    <!-- 实时 + 今日指标 -->
    <el-row :gutter="16" class="dp-card-gap">
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard :title="t('dashboard.upRate')" :value="formatRate(overview.upRate)" icon="Top" color="#67c23a" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard :title="t('dashboard.downRate')" :value="formatRate(overview.downRate)" icon="Bottom" color="#409eff" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard :title="t('dashboard.activeConns')" :value="formatNumber(overview.activeConns)" icon="Connection" color="#e6a23c" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard :title="t('dashboard.todayTraffic')" :value="formatBytes(overview.todayUp + overview.todayDown)" icon="DataLine" color="#909399" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard :title="t('dashboard.todayReq')" :value="formatNumber(overview.todayReq)" icon="Histogram" color="#9c27b0" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard
          :title="t('dashboard.todayReject')"
          :value="formatNumber(todayReject)"
          :suffix="t('dashboard.todayRejectSuffix', { rule: overview.todayRejectRule, auth: overview.todayRejectAuth })"
          icon="CircleClose"
          color="#f56c6c"
        />
      </el-col>
    </el-row>

    <!-- 图表区：时序 + 饼图 -->
    <el-row :gutter="16" class="dp-card-gap">
      <el-col :xs="24" :lg="16">
        <el-card>
          <template #header>
            <div class="flex-between">
              <span>{{ t('dashboard.trafficReqTs') }}</span>
              <el-radio-group v-model="timeWindow" size="small" @change="reloadByWindow">
                <el-radio-button value="1h">1h</el-radio-button>
                <el-radio-button value="24h">24h</el-radio-button>
                <el-radio-button value="7d">7d</el-radio-button>
              </el-radio-group>
            </div>
          </template>
          <EChart :option="tsOption" height="320px" />
        </el-card>
      </el-col>
      <el-col :xs="24" :lg="8">
        <el-card>
          <template #header><span>{{ t('dashboard.actionDist') }}</span></template>
          <EChart :option="actionOption" height="320px" />
        </el-card>
      </el-col>
    </el-row>

    <!-- Top N 排行 -->
    <el-row :gutter="16" class="dp-card-gap topn-row">
      <el-col :xs="24" :md="8">
        <el-card>
          <template #header><span>{{ t('dashboard.topGroups') }}</span></template>
          <el-table :data="topGroups" size="small" :show-header="false" :empty-text="t('common.empty')">
            <el-table-column prop="name" />
            <el-table-column align="right" width="120">
              <template #default="{ row }">{{ formatBytes(row.bytes) }}</template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="8">
        <el-card>
          <template #header><span>{{ t('dashboard.topUsers') }}</span></template>
          <el-table :data="topUsers" size="small" :show-header="false" :empty-text="t('common.empty')">
            <el-table-column prop="name" />
            <el-table-column align="right" width="120">
              <template #default="{ row }">{{ formatBytes(row.bytes) }}</template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="8">
        <el-card>
          <template #header>
            <div class="flex-between">
              <span>{{ t('dashboard.topDomains') }}</span>
              <el-radio-group v-model="domainWindow" size="small" @change="loadTopDomains">
                <el-radio-button value="24h">24h</el-radio-button>
                <el-radio-button value="7d">7d</el-radio-button>
              </el-radio-group>
            </div>
          </template>
          <!-- 改为列表：top50 条目用 el-table 替代 EChart 横向柱状图，与 topGroups/topUsers 写法一致；
               el-table 自带 empty-text，无需额外 el-empty 分支 -->
          <el-table :data="topDomains" size="small" :show-header="false" :empty-text="t('common.empty')">
            <el-table-column prop="name" />
            <el-table-column align="right" width="100">
              <template #default="{ row }">{{ row.count }}</template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>

    <!-- 运行健康区 + 使用说明 -->
    <el-row :gutter="16" align="stretch">
      <el-col :xs="24" :lg="14">
        <el-card class="full-card">
          <template #header>
            <div class="flex-between">
              <span>{{ t('dashboard.runHealth') }}</span>
              <el-tag v-if="overview.version" type="info" effect="plain" size="small" round>{{ t('dashboard.version') }}: {{ overview.version }}</el-tag>
            </div>
          </template>
          <el-row :gutter="16">
            <el-col :span="8">
              <StatCard :title="t('dashboard.memMB')" :value="runtime.memMB" suffix="MB" icon="Cpu" color="#409eff" />
            </el-col>
            <el-col :span="8">
              <StatCard title="Goroutine" :value="formatNumber(runtime.goroutines)" icon="Operation" color="#67c23a" />
            </el-col>
            <el-col :span="8">
              <StatCard :title="t('dashboard.uptime')" :value="formatUptime(overview.uptimeSec)" icon="Timer" color="#e6a23c" />
            </el-col>
          </el-row>
          <el-divider />
          <div class="flex-between">
            <span class="text-muted">{{ t('dashboard.healthyProxies') }}</span>
            <span>{{ t('dashboard.healthyAvail', { healthy: overview.healthyProxies, total: overview.totalProxies }) }}</span>
          </div>
          <el-progress :percentage="healthRatio" :status="healthRatio < 50 ? 'exception' : 'success'" />
          <div class="group-health">
            <el-tag
              v-for="g in runtime.groups"
              :key="g.id"
              :type="g.allDown ? 'danger' : g.healthy < g.total ? 'warning' : 'success'"
              effect="plain"
              class="group-tag"
            >
              {{ g.name }}: {{ g.healthy }}/{{ g.total }}
            </el-tag>
            <el-text v-if="!runtime.groups || runtime.groups.length === 0" type="info">{{ t('dashboard.noPoolGroup') }}</el-text>
          </div>
        </el-card>
      </el-col>
      <el-col :xs="24" :lg="10">
        <el-card class="full-card">
          <template #header><span>{{ t('dashboard.connFormatTitle') }}</span></template>
          <!-- 服务端监听信息 + 真实连接示例（AC-2.6 / AC-4.2） -->
          <el-descriptions :column="1" border size="small" class="conn-ports">
            <el-descriptions-item :label="t('dashboard.socks5Port')">
              <code>{{ serverInfo.socks5Port || '—' }}</code>
              <span class="text-muted"> @ {{ serverHost }}</span>
            </el-descriptions-item>
            <el-descriptions-item :label="t('dashboard.webPort')">
              <code>{{ serverInfo.webPort || '—' }}</code>
            </el-descriptions-item>
            <el-descriptions-item :label="t('dashboard.connExample')">
              <code class="conn-example">{{ connectionExample }}</code>
            </el-descriptions-item>
          </el-descriptions>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item :label="t('dashboard.basicFormat')">
              <code>user-group</code>
              <span class="text-muted"> — {{ t('dashboard.basicFormatHint') }}</span>
            </el-descriptions-item>
            <el-descriptions-item :label="t('dashboard.upstreamFormatA')">
              <code>user-group-base64(u:p@host:port)</code>
            </el-descriptions-item>
            <el-descriptions-item :label="t('dashboard.namedVarB')">
              <code>user-group-region_us#session_abc</code>
              <span class="text-muted"> — {{ t('dashboard.namedVarBHint') }}</span>
            </el-descriptions-item>
          </el-descriptions>
          <el-alert
            class="usage-tip"
            type="info"
            :closable="false"
            show-icon
            :title="t('dashboard.connTip')"
          />
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped lang="scss">
@use '@/styles/responsive.scss' as r;

/* 手机端：el-col 换行/单列堆叠后，el-row 的 :gutter 只管水平间距、不含行间距，
 * 这里给行加 row-gap 让上下卡片之间有垂直间距，避免折行后挤在一起。 */
.el-row {
  @include r.mobile {
    row-gap: 12px;
  }
}
/* 等高卡片（AC-2.1）：el-row align=stretch 已让两列等高，
   卡片再填满列高，使「运行健康」与「连接说明」底边对齐 */
.full-card {
  height: 100%;
}
/* TopN 三卡片（流量Top分组 / 流量Top用户 / Top目标域名）等高：
 * 域名卡片改为 top50 列表，高度 280px + overflow-y:auto 让超出内容可滚动，
 * 不再固定截断——高度与左侧两个卡片视觉对齐即可。*/
.topn-row {
  :deep(.el-card__body) {
    height: 280px;
    overflow-y: auto;
  }
}
.el-progress {
  margin: 12px 0;
}
.group-health {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
}
.usage-tip {
  margin-top: 14px;
}
/* 端口/连接示例描述块与下方格式说明留出间距 */
.conn-ports {
  margin-bottom: 14px;
}
/* 连接示例可能较长，允许换行避免溢出卡片 */
.conn-example {
  word-break: break-all;
  white-space: normal;
}
code {
  background: var(--el-fill-color-light);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
}
</style>
