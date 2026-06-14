# Deep Interview Spec: 仪表盘 Top 目标域名（埋点 + 仪表盘 + 分组展示）

## Metadata
- Interview ID: topdomain-2026-06-14
- Rounds: 4 (+ Round 0 拓扑门)
- Final Ambiguity Score: 6.2%
- Type: brownfield
- Generated: 2026-06-14
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.95 | 0.35 | 0.3325 |
| Constraint Clarity | 0.95 | 0.25 | 0.2375 |
| Success Criteria | 0.92 | 0.25 | 0.2300 |
| Context Clarity | 0.92 | 0.15 | 0.1380 |
| **Total Clarity** | | | **0.938** |
| **Ambiguity** | | | **0.062** |

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| ① 域名埋点与持久化（后端） | active | 在中继处记录目标主机名命中，落到新 `domain_hit` 聚合表（参照 traffic_stat 模式） | AC-1~AC-6 |
| ② 仪表盘 Top 域名展示（前端） | active | 填补 Dashboard.vue 占位，全局 Top 10 域名横向柱状图 | AC-7~AC-9 |
| ③ 分组维度 Top 域名展示（前端） | active | 在 ProxyGroups.vue 上游抽屉内展示该分组 Top 10 域名横向柱状图 | AC-10~AC-11 |

## Goal
为 deeproxy 实现"Top 目标域名"统计特性：在 SOCKS5 中继成功建连时，按**完整主机名（含子域）**对目标域名进行埋点计数；以**分钟桶聚合 + flush worker + 按保留期清理（复用 `stat_retention_days`，默认 30 天）**的方式持久化到新 `domain_hit` 表（含 `group_id` 维度）；在**仪表盘**填补现有"Top 目标域名"占位，用 **ECharts 横向柱状图**展示全局 **Top 10**（默认"今日"窗口）；并在 **ProxyGroups.vue 的上游抽屉内**、"分组流量(24h)"图下方，用同款横向柱状图展示**该分组的 Top 10** 目标域名。最后翻转 `feature_status.go` 中 `dashboard.top.domain` 为 implemented。

## Constraints
- **域名 Key 粒度**：完整主机名（`www.google.com` 与 `mail.google.com` 分开统计），不做注册域/PSL 聚合。
- **IP 目标处理**：纯 IP 目标（客户端本地 DNS 且未嗅探出域名）**也计入**，直接写 `d.host`（埋点逻辑无需判断是否为域名）。
- **时间维度/保留**：分钟桶聚合（`domain, group_id, bucket_time`）+ flush worker（复用现有 5~10s ticker）+ 按 `stat_retention_days`（默认 30 天）清理，与 `traffic_stat` 完全一致。
- **Top N 数量**：仪表盘 Top 10、分组抽屉 Top 10。
- **默认时间窗口**：今日（dayStart 起，与仪表盘现有 dayStart 逻辑一致）。
- **展示形态**：ECharts 横向柱状图（bar，yAxis=category 域名），复用现有 `deeproxy-dark` 暗色主题。
- **分组展示位置**：ProxyGroups.vue `openUpstreams` 上游抽屉内，"分组流量(24h)" EChart 下方。
- **埋点点位**：`server/server.go` `dialAndRelay`(≈line 257) 用 `d.host`；`handleSniff`(≈line 373) 用 `routeHost`（嗅探后域名更准）。与 `IncReq` 同位置。
- **复用范式**：严格镜像 `traffic_stat` 的 store/repo/flush/cleanup 与 `handleTop` 的现有 kind 分发；前端复用 `getTopN`、`EChart` 组件与现有暗色主题。
- 全部新代码中文注释；单文件职责单一；DRY（与 traffic_stat 公共逻辑尽量复用）。

## Non-Goals
- 不做注册域/PSL 聚合（只存完整主机名）。
- 不做 GeoIP / 地理分布。
- 不做 per-user 维度的 Top 域名（仅全局 + per-group）。
- 不做域名访问明细日志/时间序列趋势图（只做 Top N 计数排行）。
- 不改动现有 `traffic_stat` 表结构与现有热路径语义（埋点为纯增量旁路）。
- 不新增独立按钮/弹窗（分组复用现有上游抽屉）。

## Acceptance Criteria
- [ ] AC-1：新增 `domain_hit` 表（`domain TEXT, group_id INTEGER, bucket_time TEXT, hit_count INTEGER, PRIMARY KEY(domain, group_id, bucket_time)` + bucket 索引），通过 `store/schema.go:schemaStmts` 在启动时创建。
- [ ] AC-2：新增 `store/domain_hit_repo.go`，提供 `FlushDomainHits`（批量 upsert 累加）、`QueryTopDomains(start, end, limit, groupID)`（groupID=0 表示全局）、`CleanupBefore`（按保留期删除），镜像 `traffic_stat_repo.go`。
- [ ] AC-3：`stats` 包新增域名计数维度（如 `IncDomain(host string, groupID int64)` + `CollectDomainDeltas()`），线程安全（原子/锁），与现有 dimCounter 平行。
- [ ] AC-4：`server/server.go` 在 `dialAndRelay` 与 `handleSniff` 成功建连后调用 `IncDomain`，分别用 `d.host` / `routeHost`；纯 IP 目标也写入。
- [ ] AC-5：flush worker 在现有 ticker 内同时 flush 域名命中；cleanup worker 同时按 `stat_retention_days` 清理 `domain_hit`。
- [ ] AC-6：`handleTop` 的 `case "domain"` 调用 `QueryTopDomains(dayStart, now, 10, groupID)`，返回 `[]topItem{Name: domain, Count: hits}`；接受 `?groupId=` 查询参数（镜像 `handleTimeSeries`），groupId 缺省=全局；移除 `X-Feature-Status: not-implemented` 头。
- [ ] AC-7：`feature_status.go` 中 `dashboard.top.domain` 翻转为 `FeatureImplemented`。
- [ ] AC-8：`web/src/api/dashboard.js` 的 `getTopN` 支持 `{kind:'domain', limit, window, groupId}`。
- [ ] AC-9：Dashboard.vue 移除 `<el-empty>` 占位（≈line 278-288），新增 `topDomains` ref，`loadTop` 增加 `kind=domain` 拉取，用 ECharts 横向柱状图（复用暗色主题）展示全局 Top 10。
- [ ] AC-10：ProxyGroups.vue 上游抽屉内、"分组流量(24h)" 图下方，新增 `groupTopDomains` ref，`loadGroupChart(groupId)` 增加 `getTopN({kind:'domain', groupId})` 拉取，用同款横向柱状图展示该分组 Top 10。
- [ ] AC-11：端到端 —— 通过代理访问若干域名后，仪表盘与对应分组抽屉均能看到访问的目标域名按次数降序排列。
- [ ] AC-12：`go build ./...` 与 `go test ./...` 全绿；前端 `vite build` 通过；新增 store 测试覆盖 `domain_hit` upsert/query/cleanup（镜像现有 traffic_stat 测试）。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| "目标域名"指什么粒度 | 完整主机名 vs 注册域聚合 | 完整主机名（含子域），不做 PSL |
| 纯 IP 目标怎么办 | 计入 vs 跳过 | 计入，直接写 d.host |
| 是否需要时间维度 | 累计总数 vs 分钟桶 | 分钟桶 + 30天保留，与 traffic_stat 一致 |
| 展示用什么形态 | 表格(同现有) vs 柱状图 vs 饼图 | ECharts 横向柱状图 |
| 分组展示在哪 | 上游抽屉 vs 独立按钮弹窗 | 复用现有上游抽屉 |
| 显示多少个 | Top 10 vs 20 | 两边均 Top 10 |
| 默认时间窗口 | 今日 vs 24h vs 选择器 | 今日（dayStart） |

## Technical Context
- 现状空壳：`api/dashboard_handler.go:245` `handleTop` 的 `case "domain"` 返回 `[]` 并打 `X-Feature-Status: not-implemented`；`topItem` 已有 `Count` 字段。
- 未实现注册：`api/feature_status.go:27-31` `featureStatusTable["dashboard.top.domain"] = FeatureNotImplemented`。
- 埋点点位：`server/server.go` `dialAndRelay`(≈257, `d.host`)、`handleSniff`(≈373, `routeHost`)，与 `h.counter.IncReq(...)` 同位置。
- 存储范式：`store/schema.go:schemaStmts`（建表）、`store/traffic_stat_repo.go`（FlushTrafficStats/QueryTopGroups/CleanupBefore 范式）、`store/models.go:53` `StatRetentionDays` 默认 30。
- 前端范式：`web/src/api/dashboard.js` `getTopN`→`GET /dashboard/top`；Dashboard.vue 现有 Top 分组/用户为 el-table、动作分布为饼图（暗色主题）、`loadTop` 仅取 group/user；占位在 Dashboard.vue:278-288。ProxyGroups.vue `openUpstreams`/`loadGroupChart`/抽屉内 `<EChart>`(≈513)。`web/src/components/EChart.js` 注册 `deeproxy-dark` 主题。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| DomainHit | core domain | domain, group_id, bucket_time, hit_count | belongs to Group（group_id），按分钟桶聚合 |
| Group | core domain | id, name, type | has many DomainHit |
| TopItem (DTO) | supporting | name(domain), count | handleTop 返回 |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 2 (DomainHit, Group) | 2 | - | - | N/A |
| 2 | 2 | 0 | 0 | 2 | 100% |
| 3 | 2 | 0 | 0 | 2 | 100% |
| 4 | 3 (+TopItem DTO) | 1 | 0 | 2 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (Round 0 + 4 rounds)</summary>

### Round 0 — 拓扑确认
**Q:** 读成 3 个顶层组件（埋点持久化 / 仪表盘展示 / 分组展示），对吗？
**A:** 对，就这 3 个。

### Round 1
**Q:** 目标域名按什么粒度作为统计 key？
**A:** 完整主机名（含子域）。
**Ambiguity:** 41%

### Round 2
**Q1:** 纯 IP 目标要不要计入？ **A:** IP 也记录。
**Q2:** 时间维度和保留怎么做？ **A:** 分钟桶 + 保留期（与流量一致）。
**Ambiguity:** 29%

### Round 3
**Q1:** Top 域名用什么形态展示？ **A:** 横向柱状图。
**Q2:** 分组展示在哪个 UI 位置？ **A:** 上游抽屉内。
**Ambiguity:** 14.3%

### Round 4
**Q1:** 显示多少个？ **A:** 两边都 Top 10。
**Q2:** 默认时间窗口？ **A:** 今日。
**Ambiguity:** 6.2%
</details>
