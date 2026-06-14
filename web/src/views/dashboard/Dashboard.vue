<script setup>
// 仪表盘（AC-24/27）。已对齐 T7 实测契约：
// - GET /dashboard/overview → 扁平 { upRate,downRate,activeConns,todayUp,todayDown,todayReq,
//     todayRejectRule,todayRejectAuth,healthyProxies,totalProxies,uptimeSec }。
// - GET /dashboard/timeseries?window= → { times,up,down,req }。
// - GET /dashboard/action-dist → [{name,value}]。
// - GET /dashboard/top?kind=group|user|domain → group/user:[{name,bytes}]、domain:[{name,count}]。
// - GET /dashboard/runtime → { memMB,goroutines,groups:[{id,name,healthy,total,allDown}] }。
import { onMounted, onBeforeUnmount, ref, computed, watch } from 'vue'
import EChart from '@/components/EChart.js'
import StatCard from '@/components/StatCard.vue'
import { useThemeStore } from '@/stores/theme'
import * as dashApi from '@/api/dashboard'
import { getServerInfo } from '@/api/system'
import { formatBytes, formatRate, formatNumber, formatUptime } from '@/utils/format'

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
  // Top 分组、Top 用户已落地真实数据；Top 域名仍占位（需 CONNECT 目标域名埋点）。
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
const connectionExample = computed(() => {
  const port = serverInfo.value.socks5Port || 1080
  return `socks5://alice-default:<密码>@${serverHost.value}:${port}`
})

const tsOption = computed(() => ({
  tooltip: { trigger: 'axis' },
  legend: { data: ['上行', '下行', '请求数'] },
  grid: { left: 50, right: 50, top: 40, bottom: 30 },
  xAxis: { type: 'category', boundaryGap: false, data: tsData.value.times },
  yAxis: [
    { type: 'value', name: '流量', axisLabel: { formatter: (v) => formatBytes(v) } },
    { type: 'value', name: '请求', position: 'right' },
  ],
  series: [
    { name: '上行', type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: tsData.value.up },
    { name: '下行', type: 'line', smooth: true, areaStyle: { opacity: 0.15 }, data: tsData.value.down },
    { name: '请求数', type: 'line', yAxisIndex: 1, smooth: true, data: tsData.value.req },
  ],
}))

const actionOption = computed(() => ({
  tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
  legend: { bottom: 0 },
  series: [
    {
      name: '动作分布',
      type: 'pie',
      radius: ['45%', '70%'],
      avoidLabelOverlap: true,
      itemStyle: { borderRadius: 6, borderColor: pieBorderColor.value, borderWidth: 2 },
      label: { show: false },
      data:
        actionDist.value.length > 0
          ? actionDist.value
          : [
              { name: 'forward', value: 0 },
              { name: 'direct', value: 0 },
              { name: 'reject', value: 0 },
            ],
    },
  ],
}))

onMounted(() => {
  resolvePieBorderColor()
  loadOverview()
  loadServerInfo()
  loadRuntime()
  reloadByWindow()
  realtimeTimer = setInterval(() => {
    loadOverview()
    loadRuntime()
  }, 3000)
})
onBeforeUnmount(() => {
  if (realtimeTimer) clearInterval(realtimeTimer)
})
</script>

<template>
  <div class="dp-page">
    <!-- 实时 + 今日指标 -->
    <el-row :gutter="16" class="dp-card-gap">
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard title="上行速率" :value="formatRate(overview.upRate)" icon="Top" color="#67c23a" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard title="下行速率" :value="formatRate(overview.downRate)" icon="Bottom" color="#409eff" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard title="活跃连接" :value="formatNumber(overview.activeConns)" icon="Connection" color="#e6a23c" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard title="今日流量" :value="formatBytes(overview.todayUp + overview.todayDown)" icon="DataLine" color="#909399" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard title="今日请求" :value="formatNumber(overview.todayReq)" icon="Histogram" color="#9c27b0" />
      </el-col>
      <el-col :xs="12" :sm="8" :md="6" :lg="4">
        <StatCard
          title="今日拒连"
          :value="formatNumber(todayReject)"
          :suffix="`规则${overview.todayRejectRule}/鉴权${overview.todayRejectAuth}`"
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
              <span>总流量 / 请求数时序</span>
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
          <template #header><span>动作分布</span></template>
          <EChart :option="actionOption" height="320px" />
        </el-card>
      </el-col>
    </el-row>

    <!-- Top N 排行 -->
    <el-row :gutter="16" class="dp-card-gap">
      <el-col :xs="24" :md="8">
        <el-card>
          <template #header><span>流量 Top 分组</span></template>
          <el-table :data="topGroups" size="small" :show-header="false" empty-text="暂无数据">
            <el-table-column prop="name" />
            <el-table-column align="right" width="120">
              <template #default="{ row }">{{ formatBytes(row.bytes) }}</template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="8">
        <el-card>
          <template #header><span>流量 Top 用户</span></template>
          <el-table :data="topUsers" size="small" :show-header="false" empty-text="暂无数据">
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
              <span>Top 目标域名</span>
              <el-tag size="small" type="info" effect="plain">首版暂不支持</el-tag>
            </div>
          </template>
          <el-empty description="需目标域名埋点，后续提供" :image-size="60" />
        </el-card>
      </el-col>
    </el-row>

    <!-- 运行健康区 + 使用说明 -->
    <el-row :gutter="16" align="stretch">
      <el-col :xs="24" :lg="14">
        <el-card class="full-card">
          <template #header><span>运行健康</span></template>
          <el-row :gutter="16">
            <el-col :span="8">
              <StatCard title="进程内存" :value="runtime.memMB" suffix="MB" icon="Cpu" color="#409eff" />
            </el-col>
            <el-col :span="8">
              <StatCard title="Goroutine" :value="formatNumber(runtime.goroutines)" icon="Operation" color="#67c23a" />
            </el-col>
            <el-col :span="8">
              <StatCard title="运行时长" :value="formatUptime(overview.uptimeSec)" icon="Timer" color="#e6a23c" />
            </el-col>
          </el-row>
          <el-divider />
          <div class="flex-between">
            <span class="text-muted">健康代理概览</span>
            <span>{{ overview.healthyProxies }} / {{ overview.totalProxies }} 可用</span>
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
            <el-text v-if="!runtime.groups || runtime.groups.length === 0" type="info">暂无 Type B 分组</el-text>
          </div>
        </el-card>
      </el-col>
      <el-col :xs="24" :lg="10">
        <el-card class="full-card">
          <template #header><span>连接用户名格式说明</span></template>
          <!-- 服务端监听信息 + 真实连接示例（AC-2.6 / AC-4.2） -->
          <el-descriptions :column="1" border size="small" class="conn-ports">
            <el-descriptions-item label="SOCKS5 监听端口">
              <code>{{ serverInfo.socks5Port || '—' }}</code>
              <span class="text-muted"> @ {{ serverHost }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="Web 管理端口">
              <code>{{ serverInfo.webPort || '—' }}</code>
            </el-descriptions-item>
            <el-descriptions-item label="连接示例">
              <code class="conn-example">{{ connectionExample }}</code>
            </el-descriptions-item>
          </el-descriptions>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="基本格式">
              <code>user-group</code>
              <span class="text-muted"> — 首段=用户名，次段=代理组。</span>
            </el-descriptions-item>
            <el-descriptions-item label="Type A 动态上游">
              <code>user-group-base64(u:p@host:port)</code>
            </el-descriptions-item>
            <el-descriptions-item label="Type B 命名变量">
              <code>user-group-region_us#session_abc</code>
              <span class="text-muted"> — _ 分隔名/值，# 分隔变量；替换上游模板 {region}/{session}。</span>
            </el-descriptions-item>
          </el-descriptions>
          <el-alert
            class="usage-tip"
            type="info"
            :closable="false"
            show-icon
            title="SOCKS5 密码字段需与代理用户密码匹配，并需该用户被授权访问目标分组。"
          />
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped lang="scss">
/* 等高卡片（AC-2.1）：el-row align=stretch 已让两列等高，
   卡片再填满列高，使「运行健康」与「连接说明」底边对齐 */
.full-card {
  height: 100%;
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
