# Consensus Plan: 仪表盘「Top 目标域名」（埋点 + 持久化 + 仪表盘 + 分组展示）

> **Status: pending approval**
> Plan target: `/Users/polarbear/code/deeproxy`
> Spec authority: `.omc/specs/deep-interview-top-target-domains.md`（模糊度 6.2%，PASSED）
> Consensus: Planner → Architect → Critic，2 轮迭代达成一致（Architect=ARCHITECTURALLY_SOUND，Critic=APPROVED）
> 范式：镜像 `traffic_stat`（counter → flush worker → repo upsert → cleanup）与 `handleTop` kind 分发——**唯内存层 eviction 策略不照抄**（domain 维度无界）。

---

## 1. Requirements Summary

在 SOCKS5 中继**成功建连**时，按**完整主机名（含子域，`www.google.com` 与 `mail.google.com` 分开）** 对目标域名计数；纯 IP 目标也计入（直接写 `d.host`）。以**分钟桶聚合 + flush worker（复用现 5s ticker）+ 按 `stat_retention_days`（默认 30 天）清理**持久化到**新 `domain_hit` 表（含 `group_id` 维度）**。前端：

- **仪表盘**：填补现有 `<el-empty>` 占位，ECharts **横向柱状图**展示**全局 Top 10**，默认**今日**窗口（dayStart）。
- **分组**：在 `ProxyGroups.vue` 上游抽屉内、「分组流量(24h)」图下方，同款横向柱状图展示**该分组 Top 10**。

翻转 `feature_status.go` 的 `dashboard.top.domain` 为 `implemented`，**并同步更新所有锁定旧行为的现有测试与契约文档**。

**Non-Goals**：不做 PSL/注册域聚合、不做 GeoIP、不做 per-user 域名 Top、不做域名时序趋势/明细日志、不改 `traffic_stat` 表结构与热路径语义、不新增独立按钮/弹窗。

---

## 2. RALPLAN-DR Summary (short mode)

### Principles
1. **镜像优先，但识别误植范式**：结构复刻 `traffic_stat`，唯独 **eviction 策略**例外——(group,user) 有界、`CollectDeltas` 从不删 key 是对的；domain 无界，必须加 eviction。
2. **热路径零成本 + 内存有界**：埋点只在已成功建连点做一次 `atomic` 累加；domain map 必须能在域名闲置后收缩。
3. **DRY**：复用 `TruncateToMinute`/`fmtTime`/`WriteTx`/`parseQueryID`/`EChart`/`deeproxy-dark`。
4. **契约稳定 + 单一真源**：沿用 `topItem{Name,Count}` DTO；翻转功能状态时，测试与文档作为契约一并更新。
5. **中文注释 + 单文件单职责**。

### Decision Drivers (top 3)
1. **不破坏现有绿测**：翻转 feature 状态会触发两处既有断言失败，必须同变更内修正。
2. **基数控制——内存与磁盘双层**：domain 基数远高于 (group,user)，两层都要有界。
3. **正确性**：global（groupId<=0 哨兵）与 per-group 查询语义、bucket_time 文本格式必须与 traffic_stat 完全一致。

### Viable Options
- **(a) per-group 取数**：**A1 复用 `handleTop?kind=domain&groupId=X`**（镜像 `handleTimeSeries` 的 `?groupId=`）。零新端点。A2 新端点否决（多路由/评审面，与 kind 分发冲突）。
- **(b) domain 计数维度位置**：**B1 现有 `Counter` 内新增独立 domain map + 独立 `domMu` + eviction**。理由：domain 维度无论放哪都需 eviction；独立 `domMu` 把 B2（独立结构）的锁隔离优点吸收进来，又省去多对象/多 wiring。B2 否决（多持对象+多 wiring，锁隔离已被 domMu 覆盖）。B3 复用 `dimKey/dimCounter` **Invalidated**（污染 traffic_stat 语义、破坏现有 flush）。
- **(c) 存储表形态**：**C1 `domain_hit(domain, group_id, bucket_time, hit_count, PK 三元组)` + bucket 索引**，镜像 traffic_stat。C2 仅累计总数 **Invalidated**（无法按今日过滤/按保留期清理，违反 spec）。

---

## 3. Acceptance Criteria（可测）

- **AC-1** `domain_hit` 表存在：`domain TEXT NOT NULL, group_id INTEGER NOT NULL, bucket_time TEXT NOT NULL, hit_count INTEGER NOT NULL DEFAULT 0`，`PRIMARY KEY(domain, group_id, bucket_time)`，含 `idx_domain_hit_bucket`。`PRAGMA table_info(domain_hit)` 可验证。
- **AC-2** `store/domain_hit_repo.go`（**package `store`**）提供 `FlushDomainHits([]DomainDelta) error`（内部 `if len(deltas)==0 { return nil }` 守卫，镜像 traffic_stat_repo.go:37-39；`WriteTx` + `ON CONFLICT(domain,group_id,bucket_time) DO UPDATE SET hit_count = hit_count + excluded.hit_count`；`fmtTime(TruncateToMinute(...))`）、`QueryTopDomains(start,end, limit, groupID) ([]TopDomainStat, error)`（`groupID<=0` 不过滤，`>0` 加 `AND group_id=?`；`GROUP BY domain ORDER BY SUM(hit_count) DESC LIMIT ?`）、`CleanupDomainHitsBefore(cutoff) (int64,error)`。
- **AC-3** `stats.Counter` 新增 `IncDomain(host string, groupID int64)`（空 host return；`atomic` 累加；独立 `domMu` 惰性建 key）与 `CollectDomainDeltas() []DomainSnapshot`（差分推进基线，零增量跳过）。现有 `dims`/`CollectDeltas`/`stats/flush/flush_test.go` 不受影响、仍绿。
- **AC-3b 内存有界**：`CollectDomainDeltas` 对连续 N 个周期零增量的 domain key 在 flush goroutine 内（持 `domMu` 写锁）`delete()`。测试证明：注入域名 → flush → 停止命中 → 经 N 个 collect 周期后 `len(domains)` 收缩到 0；下次命中经 double-check 重建，计数正确。
- **AC-4** `server/server.go`：`dialAndRelay`（`IncReq` 同位 ≈line 257）调 `h.counter.IncDomain(d.host, d.auth.GroupID)`；`handleSniff`（≈line 373）调 `h.counter.IncDomain(routeHost, d.auth.GroupID)`。纯 IP 也写入；拨号失败路径不埋点。
- **AC-5** `stats/flush/flush.go` `flushOnce()`：分别 `CollectDeltas()` 与 `CollectDomainDeltas()`；改早退为「两者皆空才 return」；用**同一** `bucket := TruncateToMinute(now)`，各自调 `FlushTrafficStats`/`FlushDomainHits`（两 repo 各有 len==0 守卫，互不依赖）。`cleanupOnce()` 用同一 `cutoff` 追加 `CleanupDomainHitsBefore`。
- **AC-6** `handleTop` `case "domain"`：解析可选 `?groupId=`（镜像 `handleTimeSeries`，用 `parseQueryID`），调 `QueryTopDomains(dayStart, end, 10, groupID)`，返回 `[]topItem{Name: domain, Count: hits}`；删除 `X-Feature-Status` 头。
- **AC-7** `api/feature_status.go`：`dashboard.top.domain` = `FeatureImplemented`。
- **AC-7b 更新既有断言**：`api/api_test.go:472-473`（`TestFeatureStatus`）改断言 `== "implemented"`，更新 :461 注释；`api/api_test.go:813-819`（`TestDashboardTop` domain 块）重写为断言 HTTP 200 + body 为 JSON 数组 + 无 `X-Feature-Status` 头，更新 :797/:813 注释（**不得遗留「占位」字样**）。
- **AC-7c 更新契约文档**：`api/CONTRACT.md:28`、`:160`、`api/AC_EVIDENCE_T4_T7.md:43` 改为「kind=domain 已落地，返回 `[{name,count}]`，无 X-Feature-Status」。
- **AC-8** `web/src/api/dashboard.js` `getTopN` 注释补 `{kind:'domain', limit, window?, groupId?}`（透传 params，无逻辑改动）。
- **AC-9** `Dashboard.vue`：移除 `<el-empty>` 占位与「首版暂不支持」tag；新增 `topDomains` ref；`loadTop()` 增 `getTopN({kind:'domain', limit:10})`；横向 bar（`yAxis.type='category', inverse:true`；`xAxis.type='value'`）。**series 数据绑定 `.count`**（非 `.bytes`/`.value`）。
- **AC-10** `ProxyGroups.vue`：抽屉「分组流量(24h)」`<EChart>` 下方新增「Top 目标域名」分隔 + `<EChart>`；新增 `groupTopDomains` ref；`loadGroupChart(groupId)` 增 `getTopN({kind:'domain', limit:10, groupId})`；同样 **绑定 `.count`**。
- **AC-11** 端到端：经代理访问 ≥3 域名后，仪表盘全局卡片与对应分组抽屉均按命中次数降序显示。
- **AC-12** `go build ./...`、`go test ./...`（含修订后的 `api/api_test.go` 全绿）、`web` 下 `vite build` 全绿；新增 `store/domain_hit_test.go` 覆盖 upsert 累加 / QueryTopDomains（global + per-group + limit） / cleanup。
- **AC-12b `-race`**：`go test -race ./stats/... ./store/...`（含 `IncDomain`/`CollectDomainDeltas`/eviction 并发用例）通过，无 data race。

---

## 4. Implementation Steps（按依赖顺序）

### Step 1 — Schema：新增 `domain_hit` 表
**文件**：`store/schema.go`（`schemaStmts`，`idx_stat_bucket` 之后 ≈line 233）
```sql
CREATE TABLE IF NOT EXISTS domain_hit (
  domain      TEXT    NOT NULL,
  group_id    INTEGER NOT NULL,
  bucket_time TEXT    NOT NULL,
  hit_count   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (domain, group_id, bucket_time)
);
CREATE INDEX IF NOT EXISTS idx_domain_hit_bucket ON domain_hit(bucket_time);
```
中文注释：完整主机名作 key、含 group_id 维度、按 bucket 清理。`IF NOT EXISTS` 对旧库幂等。

### Step 2 — Repo：`store/domain_hit_repo.go`（package `store`）
新文件，与 traffic_stat_repo.go 同包（复用未导出 `fmtTime`/`TruncateToMinute`/`WriteTx`；`api→store` 已有依赖，无 import cycle）。
- `type DomainDelta struct { Domain string; GroupID int64; BucketTime time.Time; HitCount int64 }`
- `FlushDomainHits(deltas []DomainDelta) error`：`if len(deltas)==0 { return nil }` → `WriteTx` + prepared upsert（ON CONFLICT 累加）→ 每行 `fmtTime(TruncateToMinute(d.BucketTime))`。
- `type TopDomainStat struct { Domain string; HitCount int64 }`
- `QueryTopDomains(start, end time.Time, limit int, groupID int64) ([]TopDomainStat, error)`：`where := "bucket_time >= ? AND bucket_time < ?"`；`if groupID > 0 { where += " AND group_id = ?" }`（参数化）；`SELECT domain, COALESCE(SUM(hit_count),0) FROM domain_hit WHERE <where> GROUP BY domain ORDER BY SUM(hit_count) DESC LIMIT ?`。
- `CleanupDomainHitsBefore(cutoff time.Time) (int64, error)`：`DELETE FROM domain_hit WHERE bucket_time < ?`。
> **实现时务必确认** `bucket_time` 用与 traffic_stat 完全相同的 `fmtTime(TruncateToMinute)` 布局，使 `ON CONFLICT` 在同一分钟桶上正确碰撞（Critic Open Question）。

### Step 3 — Counter：domain 维度（独立锁 + eviction）
**文件**：`stats/counter.go`
- `type domainKey struct { domain string; groupID int64 }`；`type domainCounter struct { hits atomic.Int64; lastHits int64; idleCycles int }`（`lastHits`/`idleCycles` 仅 flush goroutine 访问）。
- `Counter` 加字段：`domMu sync.RWMutex`（独立于 `c.mu`）+ `domains map[domainKey]*domainCounter`。`NewCounter()` 初始化该 map。
- `IncDomain(host string, groupID int64)`：`if host == "" { return }`；`domMu.RLock` 快路径取；缺则 `domMu.Lock` double-check 建；`.hits.Add(1)`（atomic，锁外）。
- `type DomainSnapshot struct { Domain string; GroupID int64; HitCount int64 }`
- `CollectDomainDeltas() []DomainSnapshot`（仅 flush goroutine 调用）：
  1. `domMu.RLock` 复制键/指针快照，`RUnlock`。
  2. 遍历：`h := dc.hits.Load(); d := h - dc.lastHits; dc.lastHits = h`。`d>0` → 入输出并 `idleCycles=0`；`d==0` → `idleCycles++`。
  3. **eviction**：收集 `idleCycles >= evictAfterIdleCycles`（常量 `=3`，≈15s 闲置）的 key，升级 `domMu.Lock` 统一 `delete()`，删前 double-check `hits.Load()==lastHits`（不等则不删、归零 idleCycles）。
  4. 返回非零增量切片。
  中文注释：domain 无界故必须 eviction；下次命中经 double-check 重建（fresh struct ⇒ `lastHits=0` ⇒ 下个 delta 精确）。
> 不改动 `dimKey/dimCounter/CollectDeltas`，现有 flush 零回归。

### Step 4 — 埋点 wiring
**文件**：`server/server.go`
- `dialAndRelay` ≈line 257（`IncReq` 下一行）：`h.counter.IncDomain(d.host, d.auth.GroupID)` + 注释「目标域名命中埋点（纯 IP 也计入）」。
- `handleSniff` ≈line 373（`IncReq` 下一行）：`h.counter.IncDomain(routeHost, d.auth.GroupID)` + 注释「嗅探后 routeHost 更准」。

### Step 5 — Flush/Cleanup worker（flush 独立性）
**文件**：`stats/flush/flush.go`
- `flushOnce()`（≈line 105）：
  ```go
  dims := f.counter.CollectDeltas()
  domDeltas := f.counter.CollectDomainDeltas()
  if len(dims) == 0 && len(domDeltas) == 0 { return }
  bucket := store.TruncateToMinute(time.Now())
  if len(dims) > 0 { /* ... FlushTrafficStats(deltas) ... */ }
  if len(domDeltas) > 0 {
      dd := make([]store.DomainDelta, 0, len(domDeltas))
      for _, d := range domDeltas {
          dd = append(dd, store.DomainDelta{Domain: d.Domain, GroupID: d.GroupID, BucketTime: bucket, HitCount: d.HitCount})
      }
      if err := f.store.FlushDomainHits(dd); err != nil { f.logger.Warn("域名命中 flush 落库失败", "err", err, "n", len(dd)) }
  }
  ```
  两 repo 独立 flush、同一 bucket，各有 len==0 内部守卫。
- `cleanupOnce()`（≈line 134）：在 `CleanupBefore` 后用同一 `cutoff` 调 `f.store.CleanupDomainHitsBefore(cutoff)`，同样 Warn/Info。

### Step 6 — handleTop case "domain"
**文件**：`api/dashboard_handler.go`（`case "domain"` ≈line 245-251）
解析可选 groupId（镜像 :140-147），调 `a.store.QueryTopDomains(dayStart, end, limit, groupID)`，映射 `[]topItem{Name: t.Domain, Count: t.HitCount}`，`respondOK`。删除 `X-Feature-Status` 头。更新 `handleTop` doc 注释。

### Step 7 — feature_status 翻转
**文件**：`api/feature_status.go`（line 30）：`"dashboard.top.domain": FeatureImplemented`，注释更新。

### Step 7b — 更新既有测试
**文件**：`api/api_test.go`
- `TestFeatureStatus`（:461 注释 + :472-473）：断言改 `!= "implemented"` 才 Fatalf。
- `TestDashboardTop`（:797/:813 注释 + :813-819 domain 块）：重写为
  ```go
  // kind=domain：已落地，返回 [{name,count}] 数组，且不带 X-Feature-Status。
  w := doJSON(t, app, "GET", "/api/dashboard/top?kind=domain", nil, cookies)
  if w.Code != http.StatusOK { t.Fatalf("top kind=domain 应 200, got %d", w.Code) }
  if w.Header().Get("X-Feature-Status") == "not-implemented" { t.Fatal("top kind=domain 已落地，不应标 not-implemented") }
  var items []map[string]any
  mustUnmarshal(t, w.Body.Bytes(), &items) // body 为 JSON 数组（空库可为空数组）
  ```
  注释不得遗留「占位」字样；非法 kind→400 用例保留不动。

### Step 7c — 更新契约文档
- `api/CONTRACT.md:28`：kind=group/user/domain 均已落地，domain 返回 `[{name,count}]`，无 X-Feature-Status。
- `api/CONTRACT.md:160`：依赖缺口「Top 域名」标记为已完成（domain_hit + IncDomain）。
- `api/AC_EVIDENCE_T4_T7.md:43`：从 kind 占位列表移除 `domain`。

### Step 8 — 前端 API 注释
**文件**：`web/src/api/dashboard.js`（`getTopN` ≈:21-24）：注释补 `groupId` 与 `kind:'domain'`。

### Step 9 — Dashboard.vue 全局 Top 10
**文件**：`web/src/views/dashboard/Dashboard.vue`
- `const topDomains = ref([])`（≈:38）。
- `loadTop()`（≈:103）：Promise.all 增 `dashApi.getTopN({ kind:'domain', limit:10 })` → `topDomains.value`；catch 置 `[]`。
- computed `topDomainOption`（横向 bar，绑定 `.count`，`yAxis.inverse:true`）：
```js
const topDomainOption = computed(() => ({
  tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
  grid: { left: 8, right: 24, top: 10, bottom: 10, containLabel: true },
  xAxis: { type: 'value' },
  yAxis: { type: 'category', inverse: true, data: topDomains.value.map(d => d.name) },
  series: [{ type: 'bar', data: topDomains.value.map(d => d.count),
    barMaxWidth: 18, itemStyle: { borderRadius: [0,4,4,0] } }],
}))
```
- 模板（≈:277-289）：移除 tag 与占位 `<el-empty>`；`<EChart v-if="topDomains.length" :option="topDomainOption" height="240px" />` 否则空态 `<el-empty description="暂无数据" :image-size="60" />`。

### Step 10 — ProxyGroups.vue 分组 Top 10
**文件**：`web/src/views/proxy/ProxyGroups.vue`
- `const groupTopDomains = ref([])`（≈:293）。
- `loadGroupChart(groupId)`（≈:294）：追加 `groupTopDomains.value = (await dashApi.getTopN({ kind:'domain', limit:10, groupId })) || []`（同一 try/catch，catch 置 `[]`）。
- computed `groupTopDomainOption`（同 Step 9 形态，源 `groupTopDomains`，绑定 `.count`）。
- 模板（≈:512-513，`groupChartOption` EChart 之后）：
```html
<el-divider content-position="left">Top 目标域名</el-divider>
<EChart v-if="groupTopDomains.length" :option="groupTopDomainOption" height="260px" />
<el-empty v-else description="暂无数据" :image-size="50" />
```

### Step 11 — 测试
**新文件 `store/domain_hit_test.go`**（package `store`，镜像 `store/top_users_test.go`）：upsert 累加 / QueryTopDomains（global+per-group+limit，含 `groupID=0` 合并多组）/ cleanup。
**`stats/stats_test.go`（或新增 `domain_test.go`）**：`IncDomain`+`CollectDomainDeltas` 差分；eviction 用例（AC-3b：闲置 N 周期后 `len(domains)` 收缩 → 重建计数正确）；`-race`（AC-12b）并发 `IncDomain` + flush goroutine collect。

---

## 5. Risks and Mitigations

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| **R1** | counter 并发：domain map 写入与差分/eviction 读竞争 | 数据错乱/race | 独立 `domMu`；`hits` 用 `atomic.Int64`；`lastHits`/`idleCycles` 仅 flush 单 goroutine；删前 double-check。`go test -race`（AC-12b）。 |
| **R2** | 基数增长（内存 map + SQLite 表行数） | 内存泄漏 + RLock 循环变长；DB 膨胀 | **内存层**：`CollectDomainDeltas` eviction（AC-3b）。**磁盘层**：`idx_domain_hit_bucket` + 按保留期 `CleanupDomainHitsBefore`。 |
| **R3** | IP-as-key 长尾基数（内存+磁盘） | 同 R2 | 内存层 eviction 回收闲置 IP key；磁盘层 Top 10 查询受 idx+GROUP BY 支撑。spec 明确 IP 计入。 |
| **R4** | bucket_time 文本格式不一致 | 窗口过滤/ON CONFLICT 失败 | 强制 `fmtTime(TruncateToMinute(...))`（与 traffic_stat 同入口）；store 测试端到端比对。 |
| **R5** | global 查询哨兵语义 | 误判 GroupID 来源 | **GroupID 恒 ≥1**（`auth/authz.go:80-88` 计数前已拒未授权连接；`GroupID = gi.ID` 来自 `group` AUTOINCREMENT，schema.go:152 从 1 起）。`groupID<=0` 是有意设计的全局查询哨兵，与任何真实 group_id 不碰撞，非匿名回退。 |
| **R6** | handleSniff 未嗅出域名时 routeHost 仍是 IP | 记 IP 而非域名 | 预期行为（IP 计入）；嗅出域名时 routeHost 更准。 |
| **R7** | flush 早退耦合漏 domain flush | 有命中无字节时丢域名计数 | Step 5「两者皆空才 return」+ 两 repo 独立 len 守卫。 |
| **R8** | 空 host 埋点 | 脏数据空域名行 | `IncDomain` 内 `if host=="" return`。 |
| **R9** | eviction 窗口 race：删 key 瞬间正好有新命中 | **最坏丢 ≤1 次计数**（非 double-count、非 race） | 删前写锁内 double-check `hits.Load()==lastHits`，不等则不删并归零 idleCycles。**这是一个有意接受的 ≤1-hit 欠计窗口**：当某次 `IncDomain` 已取得指针、尚未 `Add` 时与并发 eviction 竞争，该次自增落在被删的孤儿计数器上而丢失。结构上等价于 `flush.go:124-129` 已接受的「统计精度损失」，对 Top-N 展示无实质影响，`-race` 不会报（仍是合法指针上的 atomic）。**执行者不得为消除此窗口而给热路径加重锁**（会违反 Principle 2）。 |

---

## 6. Verification Steps

1. `cd /Users/polarbear/code/deeproxy && go build ./...`
2. `go test ./...` 全绿——特别确认 `api/api_test.go` 的 `TestFeatureStatus`、`TestDashboardTop`（Step 7b 修订后）通过；`stats/flush/flush_test.go`、`store/top_users_test.go` 不回归。
3. `go test -race ./stats/... ./store/...`（含 eviction 并发用例），无 data race。
4. Schema：`PRAGMA table_info(domain_hit);` 确认列/主键；确认 `idx_domain_hit_bucket`。
5. 内存有界（AC-3b）：单测断言闲置 N 周期后 `len(domains)` 收缩。
6. `cd web && npm run build`（vite build）通过。
7. 端到端（AC-11）：起服务 → 经 SOCKS5 访问 3+ 域名 → 等 ≥1 flush 周期 → 仪表盘横向柱状图降序；对应分组抽屉「分组流量(24h)」下方出现该分组 Top 10。
8. 契约一致性：`GET /feature-status` → `dashboard.top.domain: "implemented"`；`GET /dashboard/top?kind=domain`（及 `&groupId=X`）→ HTTP 200 + `[{name,count}]` + 无 `X-Feature-Status`；核对 `CONTRACT.md`/`AC_EVIDENCE_T4_T7.md` 与实际一致。
9. 回归：Top 分组/用户、动作分布、时序图、分组流量图行为不变。

---

## 7. ADR（Architecture Decision Record）

- **Decision**：新增独立 `domain_hit` 表（含 group_id 维度）+ `Counter` 内独立 domain map（配独立 `domMu` 与 idle-cycle eviction）+ flush/cleanup worker 同周期同 bucket 旁路落库；`handleTop?kind=domain[&groupId=]` 取数；前端复用 `EChart` 横向柱状图与 `deeproxy-dark` 主题。
- **Drivers**：不破坏现有绿测（feature 翻转牵连既有断言与契约文档）；内存+磁盘双层基数控制；与 traffic_stat 的 bucket/哨兵/格式严格一致以保证正确性。
- **Alternatives considered**：
  - per-group 新端点（否决：多路由/评审面）。
  - 独立 `DomainCounter` 结构（否决：多对象+多 wiring，锁隔离已由 domMu 覆盖）。
  - 复用 `dimKey/dimCounter`（Invalidated：污染 traffic_stat 语义、破坏现有 flush）。
  - 仅累计总数表（Invalidated：无法按今日过滤/按保留期清理）。
  - 复用 `c.mu` 守 domain map（否决：eviction 写锁会拖累流量热路径，故改用独立 domMu）。
- **Why chosen**：最小新增面 + 最大复用既有成熟范式，同时为「无界 domain 维度」补上 traffic_stat 没有的两项必需保护（内存 eviction + 独立锁），并把功能翻转的契约连带项（测试/文档）纳入同一变更。
- **Consequences**：
  - 正向：仪表盘与分组均获得 Top 域名图表；表/内存均有界；现有功能零回归；契约文档与实现一致。
  - 代价/接受项：eviction 引入 ≤1-hit 欠计窗口（R9，有意接受，等价于既有统计精度损失）；domain 表/内存仍随活跃域名集合增长（受保留期与 eviction 约束）。
- **Follow-ups**（非本次范围）：
  - OQ-1：`evictAfterIdleCycles` 取值可观测后调整（默认 3）。
  - OQ-2：是否为 domain map 设硬上限（二级保护，防瞬时爆发）——本版依赖 eviction 自然收敛，列为观测项。
  - 可选：per-user 维度 Top 域名、域名时序趋势（spec 明确为 Non-Goal）。

---

## 8. Consensus Changelog（已应用的评审改进）

**Iteration 1 → 2（Architect NEEDS_REVISION / Critic REJECTED → 全部解决）：**
- **BLOCKING #1**：新增 Step 7b（更新 `api_test.go:472-473`、`:813-819` 及 :461/:797/:813 注释）+ Step 7c（更新 `CONTRACT.md:28,160`、`AC_EVIDENCE_T4_T7.md:43`）+ AC-7b/AC-7c；AC-12 明确含 api_test.go 通过，消除 AC-12 自相矛盾。
- **BLOCKING #2**：Step 3 改用独立 `domMu` + `CollectDomainDeltas` idle-cycle eviction（删前 double-check）；新增 AC-3b；R2/R3 改写覆盖内存+磁盘双层；Option (b) 以 eviction/锁隔离 tradeoff 论证（非仅代码复用）。
- **MINOR #3**：R5 改写为「GroupID 恒 ≥1 post-auth；`<=0` 为有意全局哨兵」。
- **补充项**：AC-12b 显式 `-race`；Step 5 明确 flush 独立性 + 两 repo len==0 守卫；确认 `domain_hit_repo.go` 在 package `store` 无 import cycle；AC-9/AC-10 显式绑定 `.count`。

**Iteration 2 终审（Architect ARCHITECTURALLY_SOUND / Critic APPROVED）：**
- 折叠 Architect/Critic 一致的非阻断改进：R9 显式记录 ≤1-hit eviction 窗口为「有意接受属性」，并加「执行者不得为此给热路径加重锁」的护栏。
- Critic Open Question 提升为 Step 2 实现注记：务必确认 `bucket_time` 用与 traffic_stat 完全相同的 `fmtTime(TruncateToMinute)` 布局，使 ON CONFLICT 正确碰撞。
