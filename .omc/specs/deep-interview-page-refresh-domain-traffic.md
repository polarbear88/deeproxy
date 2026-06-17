# Deep Interview Spec: 切页刷新 + 停止后台轮询 + Top域名增加流量

## Metadata
- Interview ID: di-2026-06-17-page-refresh-domain-traffic
- Rounds: 3
- Final Ambiguity Score: 11.3%
- Type: brownfield
- Generated: 2026-06-17
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown (brownfield weights)
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.90 | 0.35 | 0.315 |
| Constraint Clarity | 0.85 | 0.25 | 0.2125 |
| Success Criteria | 0.90 | 0.25 | 0.225 |
| Context Clarity | 0.90 | 0.15 | 0.135 |
| **Total Clarity** | | | **0.8875** |
| **Ambiguity** | | | **0.1125 (11.3%)** |

## Topology
| Component | Status | Description | 覆盖 / 推迟说明 |
|-----------|--------|-------------|----------------|
| ① 切页刷新 | active | 每次切换到某页面刷新该页目标数据 | 给 ProxyGroups / Users / Rules / Settings 补 `onActivated` 刷新（Dashboard、SysLog 已有，无需改） |
| ② 停止后台轮询泄漏 | active | 不在某页时停止其后台轮询 | RealtimeConnections 补 `onActivated`/`onDeactivated`，切走停轮询、切回重启（Dashboard 轮询已正确停止，无需改） |
| ③ Top域名增加流量 | active | 仪表盘(总数据)+代理组(组数据) Top 域名增加「总流量」展示 | 后台 domain_hit 表加 bytes 列(自动迁移)；前端加流量列 + 排序切换按钮 |

## Goal
在 deeproxy 的 Vue3 + Element Plus Web 管理界面中：(1) 让所有业务页面在每次被导航进入时刷新自身目标数据，消除 keep-alive 缓存导致的「切回来是旧数据」问题；(2) 修复「实时连接」页切走后仍每 5 秒轮询 `/api/connections` 的后台泄漏；(3) 为「Top 目标域名」排行（仪表盘总数据 + 代理组 Type A 组数据）增加每域名的「总流量（up+down 合计字节数）」展示，并支持按命中次数 / 按流量切换排序。

## Constraints
- **不改已正确的部分**：Dashboard 已用 `onActivated` 刷新且轮询已正确在 `onDeactivated`/`onBeforeUnmount` 停止；SysLog 用 SSE 且 keep-alive 感知正确。这两页不动。
- **流量口径固定为总流量**：每域名流量 = up_bytes + down_bytes 合计，**单列存储**（不拆上行/下行）。
- **老库自动迁移**：domain_hit 表通过启动时 `ALTER TABLE ... ADD COLUMN bytes INTEGER NOT NULL DEFAULT 0`（或等价幂等迁移）兼容现有 SQLite 部署；历史行 bytes=0，仅新数据累加。
- **字节归属点已确认可行**：`server/server.go` 的 `recordTraffic` 调用处，域名（非嗅探路径 `d.host` / 嗅探路径 `routeHost`）与 up/down 字节在同一栈帧内可用，无需跨层穿透；嗅探路径须传 `routeHost`（与现有 `IncDomain` 一致），纯 IP 目标按 IP 字符串计入（沿用现有语义）。
- **关键库版本**：Vue 3.5.13、Element Plus 2.9.4、Pinia 2.3.1、Vue Router 4.5.0、keep-alive `:max=6`。
- 全部代码中文注释，解释「为什么」；DRY；单文件职责单一（继承项目 CLAUDE.md 规范）。
- 直接提交 main，不建分支（见项目记忆 no-branches-commit-to-main）。

## Non-Goals
- 不改 Dashboard 与 SysLog 的现有刷新/轮询/SSE 逻辑。
- 不做 per-user 域名维度（domain_hit 仅 (domain, group_id)，不加 user_id）。
- 不拆上行/下行字节（仅总流量一列）。
- 不引入新的全局轮询或全局 store。

## Acceptance Criteria
- [ ] ProxyGroups / Users / Rules / Settings：从其他页切回时（keep-alive 命中）通过 `onActivated` 重新拉取各自数据，显示最新值（不再是旧缓存）。
- [ ] RealtimeConnections：导航离开后停止 5 秒轮询（`onDeactivated` 清 timer），导航返回后重启轮询（`onActivated`），`onBeforeUnmount` 仍清理；网络面板确认切走后无 `/api/connections` 请求。
- [ ] domain_hit 表新增 `bytes` 列；启动时对已有老库自动 ALTER 加列（幂等，重复启动不报错），历史行 bytes=0。
- [ ] `stats.Counter` 的 domainCounter 增加 bytes 原子计数 + 差分基线；新增 `AddDomainBytes(host, groupID, n)`（或上下行各一），`recordTraffic` 在 relay 结束后用 `d.host`/`routeHost` 把本连接 up+down 总字节累加到对应域名。
- [ ] `DomainDelta` 增加 bytes 字段；`FlushDomainHits` upsert 时累加 bytes；`CollectDomainDeltas` 差分 bytes；eviction 行为不回归。
- [ ] `QueryTopDomains` 返回每域名的 bytes 合计；`TopDomainStat` 增加 bytes 字段。
- [ ] dashboard API `handleTop?kind=domain` 返回项含 `count`（命中次数）与 `bytes`（总流量）；支持排序参数（如 `?sort=count|bytes`）。
- [ ] 前端仪表盘「Top 目标域名」卡片新增「流量」列（`formatBytes`）+ 命中/流量排序切换；代理组 Type A 域名图/列表同样展示流量并支持切换。
- [ ] `make build` 通过；`make test`（含现有 stats/domain 测试）通过；新增 domain bytes 累加/差分/迁移的回归测试。
- [ ] zh/en i18n key 对齐（新增「流量」「按流量排序」「按次数排序」等文案，zh.js/en.js key 集合一致，见记忆 i18n-key-parity-gate）。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| 「新增 Top 域名」是从零做 | 探查发现仪表盘已有「Top目标域名」卡片、代理组已有域名柱状图 | 实为「在已有 Top 域名上增加流量列」，非新建 |
| 「不在仪表盘就停请求」指仪表盘泄漏 | 探查发现 Dashboard 轮询已正确停止；RealtimeConnections 才是泄漏源 | 真正要修的是实时连接页缺 onDeactivated |
| 「切页刷新」全部页面都没刷新 | 探查发现 Dashboard/SysLog 已用 onActivated 刷新 | 仅 ProxyGroups/Users/Rules/Settings 需补刷新 |
| 流量可能要拆上/下行 | 问口径 | 仅总流量 up+down 合计，单列 |
| 老库可能要删库重建 | 问迁移策略 | 启动时自动 ALTER 加列，无缝升级 |
| 排序是否改 | 问排序依据 | 加排序切换按钮（命中次数 / 流量），不强制改默认 |

## Technical Context (brownfield findings)
- **前端视图**：`web/src/views/` 下 Dashboard.vue、proxy/ProxyGroups.vue、user/Users.vue、rule/Rules.vue、connections/RealtimeConnections.vue、syslog/SysLog.vue、system/Settings.vue。MainLayout.vue 用 `<keep-alive :max="6">`。
- **轮询泄漏点**：`RealtimeConnections.vue` 仅有 `onMounted`+`onBeforeUnmount`，无 onActivated/onDeactivated，keep-alive 下切走不卸载 → 5s 轮询不停。
- **已正确**：`Dashboard.vue` L73 startRealtimeTimer + L258 onDeactivated(stopRealtimeTimer) + L265 onActivated(startRealtimeTimer)。
- **后端 Top 域名**：API `api/dashboard_handler.go:174` handleTop(kind=domain) → `store.QueryTopDomains`（`store/domain_hit_repo.go:82`），目前仅 `SUM(hit_count)`。
- **域名命中存储**：`store/domain_hit_repo.go` DomainDelta{Domain,GroupID,BucketTime,HitCount}；FlushDomainHits upsert 累加；CleanupDomainHitsBefore 清理。
- **内存计数**：`stats/counter.go` domainCounter{hits atomic, lastHits, idleCycles}；IncDomain(host,groupID) 建连时调用；CollectDomainDeltas 差分 + idle≥3 周期 eviction。
- **字节归属可行点**：`server/server.go` recordTraffic(L459) 在 dialAndRelay(L284)/handleSniff(L437) 调用，relay 结束后拿到 up/down；同帧有 `d.host`（非嗅探）/ `routeHost`（嗅探，已 SetTarget）。`relayCounted`(server/relay.go:114) 本身不需改。
- **flush worker**：`stats/flush/flush.go` 每 5s tick，traffic 与 domain 两路独立 flush。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| domain_hit 桶 | core domain (storage) | domain, group_id, bucket_time(minute), hit_count, **+bytes(new)** | 由 domainCounter flush 而来；被 QueryTopDomains 聚合 |
| domainCounter | core domain (in-memory) | hits(atomic), lastHits, **+bytes(atomic, new), +lastBytes(new)**, idleCycles | keyed by domainKey{domain, groupID}；diff → DomainDelta |
| decision | supporting (per-conn ctx) | action, host, auth.GroupID, auth.UserID, connID | recordTraffic 用其 host 归属域名字节 |
| View (页面) | supporting (frontend) | route, onMounted, onActivated, onDeactivated | keep-alive 缓存；切页触发 onActivated 刷新 |
| Timer (轮询) | supporting (frontend) | intervalSec, timerHandle | onActivated 启 / onDeactivated 停 |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 5 | 5 | - | - | N/A |
| 2 | 5 | 0 | 1 (domainCounter +bytes) | 4 | 100% |
| 3 | 5 | 0 | 0 | 5 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (3 rounds + Round 0)</summary>

### Round 0 — Topology
**Q:** 确认 3 个顶层组件：①切页刷新 ②停止后台轮询 ③流量Top域名展示。对吗?
**A:** 三个都做。现在仪表盘和分组的 top 域名只显示请求次数不显示流量，增加同时显示流量即可。（→ ③ 重定义为「在已有 Top 域名上加流量列」）

### Round 1 — ③ Constraints
**Q:** 「流量」怎么算、怎么显示?（单 bytes 列 vs up/down 两列 + 排序）
**A:** 只要总流量(up+down 合计)。
**Ambiguity:** 25.7%

### Round 2 — ③ Success Criteria
**Q（合并发问）:** 老 domain_hit 库怎么处理(迁移)? + 加流量列后排序按什么?
**A:** 自动迁移(ALTER 加列)；加排序切换按钮。
**Ambiguity:** 11.3% ✅
</details>
