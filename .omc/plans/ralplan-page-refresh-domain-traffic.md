# RALPLAN: 切页刷新 + 停止实时连接轮询泄漏 + Top域名增加流量展示

> 来源: `.omc/specs/deep-interview-page-refresh-domain-traffic.md`(深度访谈,模糊度 11.3%)
> 模式: --consensus --direct --deliberate(含 DB 迁移,中等风险)
> 状态: **pending approval**

---

## Requirements Summary

三个独立顶层组件:

- **① 切页刷新**: 给 `ProxyGroups.vue` / `Users.vue` / `Rules.vue` / `Settings.vue` 补 `onActivated` 重新拉数据(Dashboard/SysLog 已有,不动)。
- **② 停止实时连接轮询泄漏**: `RealtimeConnections.vue` 在 keep-alive 下切走仍每 5s 轮询 `/api/connections`,补 `onActivated`(重启)/`onDeactivated`(停)。
- **③ Top域名加流量**: 后端 `domain_hit` 表加 `bytes` 列(启动自动 ALTER 迁移),`stats` domainCounter 加字节原子计数+差分,`QueryTopDomains`/`handleTop` 返回 bytes,前端仪表盘「Top目标域名」卡片与代理组 Type A 域名图表展示流量列 + 命中/流量排序切换。

约束: 总流量=up+down 单列; 直接提交 main 不建分支; 全中文注释; zh/en i18n key 对齐。

---

## RALPLAN-DR Summary

### Principles (P)
- **P1 最小侵入**: 已正确的代码(Dashboard 刷新/轮询、SysLog SSE、relayCounted 热路径)绝不改动。
- **P2 维度一致**: 域名字节计数沿用 `domainKey{domain, groupID}` 现有维度,不引入 user_id,镜像 `IncDomain`/hits 的已验证差分+eviction 机制。
- **P3 老库无缝**: 迁移幂等,历史行 bytes=0,新数据才累加,重复启动不报错。
- **P4 前端 DRY**: 三处页面的 onActivated 刷新与排序切换复用现有 load 函数,不复制逻辑。
- **P5 可测**: 每个组件有独立回归测试或可观测验证点。

### Decision Drivers (top 3)
1. **字节归属的正确性**: 必须在 relay 结束后、域名(d.host/routeHost)在同帧可用时累加,且嗅探路径用 routeHost(与 IncDomain 一致)。
2. **现有差分/eviction 不回归**: domainCounter 加 bytes 后,CollectDomainDeltas 的 idle≥3 周期 eviction 与基线推进必须对 hits 和 bytes 同步处理,避免丢/重。
3. **迁移健壮性**: ALTER ADD COLUMN 在列已存在时必须幂等(SQLite 无 IF NOT EXISTS for ADD COLUMN,需先探测 PRAGMA table_info 或忽略 "duplicate column" 错误)。

### Viable Options

**组件③ 字节累加位置** —
- **Option A(选)**: 在 `recordTraffic` 内新增域名字节累加,把域名作为新参数传入(签名改为 `recordTraffic(d, host, up, down)`)。Pros: 集中、与现有 IncReq/IncDomain 同位; Cons: 改一个内部函数签名(2 个调用点)。
- **Option B**: 新增独立 `recordDomainTraffic(host, groupID, up, down)`,在两个调用点单独调。Pros: recordTraffic 签名不变; Cons: 调用点多一行,易漏嗅探路径用 routeHost。
- **裁决**: A —— 单一函数职责清晰,签名集中改动,嗅探/非嗅探各自传对的 host,编译器保证两点都传。

**组件③ 迁移幂等实现** —
- **Option A(选)**: 启动迁移先 `PRAGMA table_info(domain_hit)` 检测 bytes 列是否存在,不存在才 ALTER。Pros: 明确、可读、跨 SQLite 版本稳定; Cons: 多一次查询。
- **Option B**: 直接 ALTER,捕获并忽略 "duplicate column name" 错误。Pros: 少一次查询; Cons: 靠错误字符串匹配,脆弱。
- **裁决**: A —— PRAGMA 探测比错误字符串匹配健壮。

**组件① 刷新实现** —
- **Option A(选)**: 各页 `onActivated(loadXxx)` 复用现有 onMounted 里的同一 load 函数。Pros: DRY、零新逻辑; Cons: 无。
- **Option B**: 路由守卫统一刷新。Pros: 集中; Cons: 需维护路由→load 映射,过度设计。
- **裁决**: A —— 复用现有 load,最小且符合 Dashboard 既有范式。

---

## Implementation Steps

### 组件① 切页刷新(纯前端,4 文件)
> **【共识修正 M4】** 不要"把 load 移到 onActivated 并声称 Dashboard 是此范式"——这是错的。Dashboard(`Dashboard.vue:244-254`)把全部 load 放在 `onMounted`,`onActivated`(L265-268)只跑子集(`loadActionDist`)+ `startRealtimeTimer`。Vue3 keep-alive 首次进入 `onMounted`→`onActivated` 都触发,再次进入只触发 `onActivated`。这四页**无任何轮询定时器**,故双触发最多多一次只读请求(无害),但仍应避免。
> **采用范式**: 保留现有 `onMounted(loadXxx)`,新增**带首帧守卫的 onActivated**——首次激活跳过(因 onMounted 已加载),之后每次激活刷新:
> ```js
> let activatedOnce = false
> onActivated(() => { if (!activatedOnce) { activatedOnce = true; return } loadXxx() })
> ```

1. `web/src/views/proxy/ProxyGroups.vue`: 导入 `onActivated`,加守卫式 `onActivated(loadGroups)`(现 onMounted L424 调 loadGroups)。
2. `web/src/views/user/Users.vue`: 守卫式 `onActivated(() => { loadAll(); loadProxyContext() })`(对齐 onMounted L208)。
3. `web/src/views/rule/Rules.vue`: 守卫式 `onActivated(loadAll)`(对齐 onMounted)。
4. `web/src/views/system/Settings.vue`: 守卫式 `onActivated(loadSettings)`(对齐 onMounted)。

### 组件② 停止实时连接轮询(纯前端,1 文件)
5. `web/src/views/connections/RealtimeConnections.vue`: 导入 `onActivated`/`onDeactivated`。
   - `onDeactivated`: `if (timer) { clearInterval(timer); timer = null }`(切走停轮询)。
   - `onActivated`: `load(); restartTimer()`(切回立即刷新并重启)。
   - 把现有 onMounted 的 `load()+restartTimer()` 逻辑保留或移入 onActivated(避免首次双触发,同①对策)。
   - onBeforeUnmount 保留清理。

### 组件③ Top域名加流量(后端 Go + 前端)
**后端:**
6. `stats/counter.go`:
   - `domainCounter`(L45-50)加 `bytes atomic.Int64`(热路径累加)+ `lastBytes int64`(flush-worker-only 基线)。
   - 新增 `AddDomainBytes(host string, groupID int64, n int64)`: 镜像 `IncDomain`(L191-209)的 RLock 快路径+写锁建键,`dc.bytes.Add(n)`; host=="" 直接返回。
7. **【共识修正 C1 — 关键 bug,必须修】** `stats/counter.go` `CollectDomainDeltas`(L400-449):
   - **背景**: hit 在连接**打开**时记(`server.go:285/439`,在 relayCounted 之前);bytes 在连接**关闭**时记(`server.go:290/445`,relayCounted 之后)。长连接可能在 hits 空闲 3 周期后被 eviction,而此时 bytes 才刚到 → **尾部字节永久丢失**。原计划"镜像 hits 已验证逻辑/不丢不重"的判断**是错的**——hits 与连接存在同时(都在 open),bytes 不是。
   - **修正 a(emit/idle)**: diff 循环里,**bytes delta 非零也算活动** → 重置 `idleCycles=0` 并 emit `DomainSnapshot`(即使 hits delta 为 0);差分 bytes(`cur.bytes - lastBytes`)写入 `DomainDelta.Bytes`,推进 `lastBytes` 基线(与 lastHits 同步)。
   - **修正 b(eviction gate)**: L440 的删除前双检查由 `dc.hits.Load()==dc.lastHits` 改为 `dc.hits.Load()==dc.lastHits && dc.bytes.Load()==dc.lastBytes`——有未 flush 字节的键不得删除。
8. `store/domain_hit_repo.go`:
   - `DomainDelta`(L19-24)加 `Bytes int64` 字段。
   - `FlushDomainHits`(L30-54)upsert: `hit_count = hit_count + ?, bytes = bytes + ?`。
   - `TopDomainStat`(L72-75)加 `Bytes int64`。
   - `QueryTopDomains`(L82-114): SELECT 增加 `COALESCE(SUM(bytes),0) AS bytes_sum`;**【共识修正 M2 — SQL 注入防护】** 新增 `orderBy` 参数,在函数内**白名单映射**为固定列名常量(`"count"→"hit_count"` 聚合别名 / `"bytes"→"bytes_sum"`),**严禁** `fmt.Sprintf` 拼接用户原始字符串到 `ORDER BY`;默认 `count`(保持现行为)。
   - **【共识修正 M2 — 编译破坏】** 签名增 `orderBy` 后,须同步更新唯一非测试调用点 `api/dashboard_handler.go:251` **及** `store/domain_hit_test.go` 的 6 处调用点(L38,54,80,89,101,137),否则 `make test` 不编译。
9. **【共识修正 M1 — 复用现有迁移框架,勿造新函数】** `store/schema.go`:
   - 在 `domain_hit` 建表 DDL(L248-252,`hit_count` 后)加 `bytes INTEGER NOT NULL DEFAULT 0` 列(新库直接带列)。
   - 在 `pendingColumnMigrations`(L43)**追加一条** `columnMigration{table:"domain_hit", column:"bytes", ddl:"ALTER TABLE domain_hit ADD COLUMN bytes INTEGER NOT NULL DEFAULT 0"}`。现有 `migrateColumns` + `columnExists`(PRAGMA table_info)已保证幂等,**不新增任何迁移函数/调用点**(符合 DRY/P1)。
10. **【共识修正 M3 — 缺失的 flush 接线,不接则流量恒为 0】** `stats/flush/flush.go`(L143-148 `DomainSnapshot→store.DomainDelta` 映射): 在 `DomainDelta{...}` 字面量加 `Bytes: d.Bytes`。否则内存差分出的字节在 flush 边界被静默丢弃。
11. `server/server.go`(采用原计划 Option A: 改 `recordTraffic` 签名;Architect 建议的 co-locate 为可选简化,不阻塞):
    - `recordTraffic`(L462-471)签名改为 `recordTraffic(d decision, host string, up, down int64)`,内部新增 `if up+down>0 { h.counter.AddDomainBytes(host, d.auth.GroupID, up+down) }`(单次合并 add)。
    - `dialAndRelay`(L290): 改调 `h.recordTraffic(d, d.host, up, down)`。
    - `handleSniff`(L445): 改调 `h.recordTraffic(d, routeHost, up, down)`(用 routeHost,与 L439 IncDomain 一致;此处 `up` 已含 `len(first)` 首包字节,见 L443)。
12. `api/dashboard_handler.go` `handleTop`(L170-262)的 `domain` 分支(L236-258):
    - L258 返回项由 `topItem{Name: t.Domain, Count: t.HitCount}` 改为同时设 `Bytes: t.Bytes`(`topItem` L166 已有 Bytes 字段,无需改结构)。
    - 新增 `sort` 查询参数解析(白名单 count|bytes,默认 count,仿 `parseTopDomainWindow` L323 范式),透传给 `QueryTopDomains` 的 orderBy。代理组 Type A 复用同 API(`?kind=domain&groupId=X`)。

**前端:**
13. `web/src/views/dashboard/Dashboard.vue` 「Top目标域名」卡片(L350): 表格加「流量」列(`formatBytes(row.bytes)`); 加排序切换(命中次数/流量),复用现有 domainWindow 旁的 radio 范式,切换时带 `sort` 参数重新 loadTopDomains。
14. **【共识补充 G2 — 须明确文件:行 + 验证】** `web/src/views/proxy/ProxyGroups.vue` Type A 域名图表抽屉:
    - `loadGroupChart`(L354)拉 `dashApi.getTopN({kind:'domain', limit:10, groupId})`(L363)→ `groupTopDomains`。
    - `groupTopDomainOption`(L402)横向柱状图当前绑定 `d.count`(L411)。增加流量展示(柱状图按流量或追加流量数值)+ 命中/流量排序切换,与 Dashboard 卡片一致。
    - getTopN 调用传 `sort` 参数。
15. i18n: `web/src/i18n/zh.js` + `en.js` 加「流量」「按流量」「按次数」等 key,zh/en key 集合一致(common.* 复用,见记忆 i18n-key-parity-gate)。

---

## Risks and Mitigations
| 风险 | 缓解 |
|------|------|
| keep-alive 首次挂载 onMounted+onActivated 双触发导致重复请求 | 保留 onMounted(load) + 带首帧守卫的 onActivated(activatedOnce 跳过首次) —— 不改 onMounted,匹配项目范式;四页无定时器,双触发本就最多多一次只读请求 |
| **【C1 关键】** domainCounter 加 bytes 后 eviction 丢尾部字节 | hits 在 open 记、bytes 在 close 记,时序不同 → **不能**沿用 hits-only eviction。修正: eviction gate 双检查 `hits==lastHits && bytes==lastBytes`;bytes delta 非零也重置 idleCycles 并 emit。新增长连接(hit 后 ≥3 周期才到 bytes)回归测试 |
| ALTER ADD COLUMN 重复执行报错 | 复用现有 `pendingColumnMigrations` + `columnExists`(PRAGMA),已幂等,**不造新函数** |
| 嗅探路径误用 d.host(IP)而非 routeHost | recordTraffic 强制 host 参数,handleSniff 显式传 routeHost,与 L439 IncDomain 对齐 |
| 排序改默认破坏现有行为 | sort 默认 count,仅新增可选 bytes 排序,不改默认 |
| **【M2 安全】** orderBy 参数 SQL 注入 | sort 在函数内白名单映射为固定列名常量,严禁 Sprintf 拼接用户原始值到 ORDER BY |
| **【M3】** 内存差分出字节但 flush 边界丢弃,流量恒 0 | flush.go:145 映射加 `Bytes: d.Bytes` |
| 字节归属漏 first-packet | handleSniff 现有 `up += len(first)`(L443)在 recordTraffic 之前,up 已含首包,域名字节随之含首包 |
| **【M2 编译】** QueryTopDomains 签名变更破坏测试编译 | 同步更新调用点 dashboard_handler.go:251 + domain_hit_test.go 6 处(L38,54,80,89,101,137) |

## Verification Steps
1. `make build` 通过(Go 编译 + 前端构建)。
2. `make test` 通过,含现有 stats/store 测试(注意 QueryTopDomains 签名变更后 6 处测试调用点须更新)。
3. 新增后端测试:
   - `stats` domain bytes 累加/差分回归。
   - **【G1】eviction 尾部字节回归**: 记一次 hit → 推进 ≥3 flush 周期(无新 hit)→ 再 AddDomainBytes → 断言 bytes 被 flush 而非丢弃;断言带未 flush 字节的键不被 evict。
   - `store` 迁移幂等: 对无 bytes 列的老库 ALTER 后再次启动不报错(复用现有 migration 测试范式)。
   - 一次 forward 连接后 `QueryTopDomains` 的 bytes>0(防 M3 接线漏)。
4. 手动/网络面板: 切走实时连接页后无 `/api/connections` 请求; 切回 ProxyGroups/Users/Rules/Settings 显示最新数据(且首次进入仅一次请求)。
5. 接口验证: `GET /api/dashboard/top?kind=domain&sort=bytes` 返回按 bytes 降序; `?sort=count` 或缺省按命中降序。
6. **【G2】UI 验证**: 代理组 Type A 流量抽屉的域名图表展示流量并支持命中/流量排序切换。
7. i18n: zh/en key 集合 diff 为空(沿用现有 parity gate)。

## Pre-mortem (4 失败场景)
1. **场景:上线后域名流量全为 0**。根因: AddDomainBytes 没调,或 host 传空,**或 flush.go:145 漏接 `Bytes: d.Bytes`(M3)**。探测: 新增测试断言一次 forward 连接后 QueryTopDomains 的 bytes>0。
2. **场景:长连接的大流量域名流量被低估/丢失(C1 核心 bug)**。根因: hit 在 open 记、bytes 在 close 记,长连接 hits 空闲被 eviction,尾部字节丢失。探测: G1 回归测试(hit 后 ≥3 周期才到 bytes,断言不丢);eviction gate 双检查 + bytes-delta 重置 idleCycles。
3. **场景:老用户升级后启动崩溃**。根因: ALTER 在已有 bytes 列时抛错。缓解: 复用 `pendingColumnMigrations` + `columnExists`(已幂等),不手写迁移。
4. **场景:切页后请求翻倍/sort 参数被注入**。根因: onMounted+onActivated 双调 load(守卫修正);或 sort 原始串拼进 ORDER BY(白名单映射修正)。探测: 网络面板核对首次仅一次请求;orderBy 仅接受白名单常量。

## Expanded Test Plan
- **Unit**: AddDomainBytes 累加; CollectDomainDeltas bytes 差分与基线推进; **eviction gate 双检查(hits&&bytes)**; **bytes-only 周期 emit 且重置 idleCycles**; 迁移幂等(复用 migration 测试)。
- **Integration**: store FlushDomainHits 累加 bytes 后 QueryTopDomains 返回正确 SUM(bytes) 与白名单 orderBy; handleTop sort 参数; 一次 forward 连接 bytes>0。
- **E2E/手动**: 实时连接页轮询生命周期(网络面板); 四页切回刷新且首次仅一次请求; 仪表盘卡片 + 代理组 Type A 抽屉的流量列与排序切换 UI。
- **Observability**: 域名字节经 5s flush worker 落库,落库后 Top 域名可见流量;长连接(>15s)关闭后字节不丢。

## ADR
- **Decision**: 字节按 `(domain, group_id)` 维度,单 bytes 列(up+down 合计),在 recordTraffic 内经新 host 参数累加; 迁移**复用现有 `pendingColumnMigrations` 框架**(非新函数); CollectDomainDeltas 的 eviction gate 与 idle 判定**同时纳入 bytes**(修正 open/close 时序错配); 前端切页用 onMounted + 守卫式 onActivated。
- **Drivers**: 字节归属正确性(尤其长连接)、现有差分/eviction 不回归、迁移健壮性、最小侵入、SQL 安全。
- **Alternatives considered**: 独立 recordDomainTraffic / co-locate AddDomainBytes(Architect 建议,作为可选简化,不阻塞); 错误字符串匹配迁移(脆弱,弃); 手写新迁移函数(重复造轮子,弃,已有框架); 路由守卫统一刷新(过度设计,弃); up/down 两列(用户明确只要总流量,弃); load 移到 onActivated(误标 Dashboard 范式,弃,改守卫式)。
- **Why chosen**: 与现有维度/差分机制同构,但**显式修正 bytes 与 hits 的生命周期差异**(open vs close)——这是初版计划误判为"不丢不重"之处,经 Architect+Critic 双重独立确认为真实 bug。
- **Consequences**: recordTraffic 内部签名变更(2 调用点 + 测试同步); QueryTopDomains 签名变更(7 处调用点); domain_hit 表 +1 列; CollectDomainDeltas eviction 逻辑增强; 前端 4 页加守卫式刷新。
- **Follow-ups**: 若后续要 per-user 域名或上下行拆分,需扩 domainKey/列(本期 Non-Goal)。

---

## Changelog(共识修正应用记录)
经 Architect(opus)+ Critic(opus)双重独立审查,以下修正已并入本计划:
- **C1(关键,双重确认)**: CollectDomainDeltas eviction 丢尾部字节 —— hit(open)与 bytes(close)时序错配。修正 eviction gate 双检查 + bytes-delta 重置 idleCycles/emit + G1 回归测试。删除原"镜像已验证逻辑/不丢不重"错误判断。
- **M1**: 迁移复用现有 `pendingColumnMigrations`/`columnExists`(schema.go:43/L248),不造新函数。
- **M2**: orderBy 白名单映射防 SQL 注入 + SUM(bytes) AS bytes_sum + 同步 7 处调用点防编译破坏。
- **M3**: 补 flush.go:145 `Bytes: d.Bytes` 接线(原计划完全遗漏)。
- **M4**: 前端改 onMounted+守卫式 onActivated,删除"load 移到 onActivated 是 Dashboard 范式"的错误依据。
- **G2**: 代理组 Type A 域名抽屉补文件:行(L354/402/411)+ UI 验证步骤。
- **G3**: dashboard_handler domain 分支(L258)显式设 Bytes + 新增 sort 解析。
- Architect 可选建议(co-locate AddDomainBytes 代替改 recordTraffic 签名)记录为备选,不阻塞执行。
