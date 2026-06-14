# Deep Interview Spec: 实时连接（Realtime Connections）菜单模块

## Metadata
- Interview ID: realtime-connections-2026-06-15
- Rounds: 5
- Final Ambiguity Score: 11.8%
- Type: brownfield
- Generated: 2026-06-15
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.92 | 0.35 | 0.322 |
| Constraint Clarity | 0.85 | 0.25 | 0.213 |
| Success Criteria | 0.85 | 0.25 | 0.213 |
| Context Clarity | 0.90 | 0.15 | 0.135 |
| **Total Clarity** | | | **0.882** |
| **Ambiguity** | | | **0.118** |

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| 实时连接全栈功能 | active | 后端活跃连接登记表 → API 端点 → 前端菜单/页面，一个纵向切片 | 全部验收标准覆盖此单一组件 |

## Goal
在 deeproxy 管理后台新增一个「实时连接」菜单模块，展示**当前此刻仍在中继的全部活跃 SOCKS5 连接**。每条连接以表格行展示：目标主机、动作（直连/转发/拒绝）、连接时长、上游代理地址、代理用户名+分组名、客户端来源、开始时间。当活跃连接数量过大时，后端只返回 Top N 条并附总数提示，避免前端渲染过多数据卡死。前端表格支持可切换刷新间隔与暂停。

## Constraints
- **从零新建活跃连接登记表**：现有代码无逐连接表，仅 `stats/counter.go` 的聚合 `activeConns atomic.Int64`。连接元数据（目标/动作/分组/上游）当前只短暂存在于 context 的 `decision` 结构，用完即弃。
- **热路径零额外开销**：不得为本功能在 `relayCounted` 的 `io.Copy` 中继循环里增加按字节/按块的记账。**明确不显示实时已传字节数**，以保留 Linux `splice(2)` 零拷贝快路径（这是经评估后用户确认的取舍）。登记/注销只发生在连接开始与结束两个点，O(1)，不进数据拷贝循环。
- **截断策略**：后端固定 Top N（默认 N=500，可配置常量）。响应同时返回 `total`（当前真实活跃总数）供前端显示"显示 500 / 共 N 条"。
- **排序口径**：默认按连接开始时间倒序（最新优先）。前端可切换为按连接时长倒序。注意：连接时长 = now − 开始时间，与开始时间是同一排序键的反向，后端无需为时长单独埋点。
- **活跃边界**：表里只含**此刻仍开着**的连接。连接开始时登记，结束时（`connectHandle` 的 defer）立即从表中移除。纯活跃 map，不保留已关闭连接、不做历史 ring buffer。
- **并发安全且不阻塞热路径**：登记表用 `sync.Map` 或 mutex-guarded map，注册/注销为轻量 O(1) 操作，绝不在中继热路径上加锁等待。
- **刷新机制**：前端提供"自动刷新"开关 + 可选间隔（如 2s/5s/10s/关），默认开启（默认间隔 5s）。复用 Dashboard 现有 axios 轮询模式，**不需要 SSE**。
- **技术栈对齐**：后端 Gin（`api/` 包，handler 注册在 `api/server.go` 的 `Router()`，依赖经 `NewApp()` 注入）；前端 Vue 3 + Element Plus + vue-router（hash 模式）+ Pinia + i18n（zh/en）。

## Non-Goals
- **动作列只显示 forward/direct，不显示 reject**（共识评审结论 C1）：被拒绝的连接在规则判定 Allow 阶段即被关闭（`server/server.go:125-129`，`decision` 结构注释明确"reject 不会进入 ConnectHandle"），从不进入"活跃"状态，故活跃连接表结构上无法呈现 reject 行。页面加一句说明：拒绝记录请在「系统日志/审计」查看。若将来需要"被拒绝连接"可见，是另一个独立功能（拒绝事件流），单独立项。
- 不显示实时已传字节数（保留 splice 零拷贝；字节总数可在连接结束后从 `syslog.AuditBuffer` 审计查看）。
- 不保留已关闭连接 / 不做连接历史表（syslog 审计已部分覆盖历史视角）。
- 不做后端 limit/offset 分页（采用固定 Top N + 总数提示）。
- 不做服务端按主机/分组/动作的筛选查询（首版前端仅排序切换，不做后端过滤）。
- 不做主动断开/Kill 某条连接的操作（首版只读展示）。
- 不引入 SSE/WebSocket 推送（轮询即可）。
- 不改变 `relayCounted` 的中继语义与 DEC-D1 半关闭修复。

## Acceptance Criteria
- [ ] 新建后端活跃连接登记表（如 `server/connregistry.go` 或集成进 handler 结构），提供：`Register(meta) connID`、`Deregister(connID)`、`Snapshot(limit, sortBy) (entries []ConnView, total int)`。
- [ ] 在 `connectHandle`（`server/server.go`）连接进入时 `Register`、defer `Deregister`；登记字段包含 connID、目标主机（复用 `targetHost`）、动作（`decision.action`）、分组名、用户名、客户端来源地址、开始时间；forward 时在选定上游后补登记上游 host:port（Type A 动态上游 / Type B SWRR 选中节点）。
- [ ] 登记/注销为 O(1) 且不在 `io.Copy` 循环内；`relayCounted` 中继循环无任何新增按字节记账；splice 快路径保留（不显示实时字节）。
- [ ] 新建 `api/connections_handler.go`，注册受保护路由 `GET /api/connections`，支持 query：`?limit=`（默认 500，上限即 N）、`?sort=start|duration`（默认 start）。
- [ ] 响应形态：`{ items: [{ id, target, action, upstream, user, group, client, start_ts, duration_sec }], total, limit, truncated }`，其中 `truncated = total > limit`。
- [ ] `App` 结构注入登记表依赖，`NewApp()` 签名扩展，`cmd/deeproxy/main.go` 装配处传入。
- [ ] 前端新增路由 `/connections`（router/index.js，`meta.title: 'menu.connections'`，`meta.icon`），菜单项自动出现在 MainLayout 侧栏。
- [ ] 新建 `web/src/views/connections/RealtimeConnections.vue`：Element Plus 表格展示上述列；顶部显示"显示 X / 共 Y 条"截断提示；排序切换（开始时间/连接时长）；自动刷新开关 + 间隔选择 + 暂停；连接时长前端按 now−start_ts 实时渲染（或后端给 duration_sec）。
- [ ] 新建 `web/src/api/connections.js`（如有 api 模块目录）封装 `getActiveConnections({ limit, sort })`。
- [ ] i18n：zh.js / en.js 补 `menu.connections` 及页面内列名、提示、空态等键，中英齐全（遵循项目 i18n 缺键修复历史）。
- [ ] 大数量验证：模拟 >N 条活跃连接时，接口只返回 N 条且 `total` 正确、`truncated=true`，前端不卡死。
- [ ] 全部代码中文注释，解释"为什么"；遵循单文件职责单一、DRY、复用现有 ring buffer/handler/axios 模式。
- [ ] `make build` 通过；`go test ./...`（含 `-race`）通过；前端构建通过且补回 `api/dist/.gitkeep`（参照提交历史 b7e34f6）。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| 后端已有可用连接数据源 | 探针发现：无逐连接表，仅聚合计数 | 从零新建活跃连接登记表 |
| "实时"需展示实时字节速率 | 会破坏 io.Copy 的 splice 零拷贝、增热路径开销 | 用户确认不显示实时字节 |
| 截断用后端分页 | 分页 + 实时刷新会抖动且复杂 | 固定 Top N + total 提示 |
| Top N 按某固定键排序 | 用户想要灵活性 | 前端可切开始时间/时长，默认开始时间 |
| "活跃"可能含刚关闭的连接 | 决定纯 map vs 历史 ring buffer | 仅当前活跃，关连接立即移除（纯活跃 map） |
| 实时=自动高频刷新 | 列表抖动、后端压力 | 可切换间隔 + 可暂停，默认开 5s |

## Technical Context
关键代码锚点（brownfield 探针 + 直接读取确认）：
- **连接入口**：`server/server.go` `connectHandle()`（约 L167-191）——登记/注销 hook 点；`targetHost()`（约 L142-152）取目标主机；`decision` 结构（`server/ctxkey.go:21`）已携带 action/group/user/auth。
- **上游解析**：`server/server.go` `dialWithFailover()` / `resolveUpstream()`（约 L203-271）——forward 选定上游后可拿到 `UpstreamView`（Type B）或 `DynamicUpstream`（Type A）补登记。
- **审计先例**：`server/server.go` `recordAudit()`（约 L437）写入 `syslog.AuditBuffer`（内存环形缓冲，`syslog/` 的泛型 `ringBuffer[T]`）——登记表可借鉴并发环形缓冲/快照模式（但登记表是活跃 map，非历史 ring）。
- **聚合计数**：`stats/counter.go` `ConnOpened()`/`ConnClosed()`（L212/215）、`ActiveConns()`（L218）——总数可与登记表 `total` 交叉校验。
- **API 装配**：`api/server.go` `NewApp(...)`（L59）、`Router()`（L111+），受保护路由挂在 `auth := api.Group("")` 下（L134+）。
- **前端**：`web/src/router/index.js`（路由 + meta.title/icon，菜单自侧栏 MainLayout 派生）；`web/src/views/dashboard/Dashboard.vue`（轮询 + Element Plus 表格/卡片范式）；`web/src/layouts/MainLayout.vue`（侧栏菜单）；locales zh.js/en.js。
- **登记表容量默认 N=500**：作为可配置常量；超过即截断，`truncated` 标志驱动前端提示。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| ActiveConnection (新建) | core domain | id, target, action, upstream, user, group, client, start_ts | 由 decision + targetHost + upstream 视图组装；存于 ConnRegistry |
| ConnRegistry (新建) | core domain | map[connID]*ActiveConnection, capN | Register/Deregister/Snapshot；被 handler 持有、被 api 读取 |
| decision | supporting (existing) | action, host, auth(group/user), group | connectHandle 从 context 取出，喂给登记表 |
| UpstreamView / DynamicUpstream | supporting (existing) | host, port (上游代理地址) | forward 时补登记 upstream 字段 |
| AuditBuffer | external/reference (existing) | ringBuffer[AuditEntry] | 历史视角参照，非本功能数据源 |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 (拓扑+截断) | 2 | 2 | - | - | N/A |
| 2 (排序) | 3 | 1 | 0 | 2 | 67% |
| 3 (列集合) | 4 | 1 | 0 | 3 | 75% |
| 4 (活跃边界) | 5 | 1 | 0 | 4 | 80% |
| 5 (刷新) | 5 | 0 | 0 | 5 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (5 rounds + Round 0)</summary>

### Round 0 — Topology
**Q:** 现状无逐连接表，仅聚合计数；这是一个全栈纵向功能吗？
**A:** 对，一个全栈功能。

### Round 1 — 截断策略 (Constraints)
**Q:** 后端怎么限制返回量？
**A:** 固定 Top N + 总数提示。
**Ambiguity:** 34.3%

### Round 2 — 排序口径 (Constraints)
**Q:** Top N 按什么排序取前 N？
**A:** 前端可选开始时间或连接时长倒序，默认开始时间。
**Ambiguity:** 29.2%

### Round 3 — 显示列 (Success Criteria)
**Q:** 除目标主机/动作/时长外还要哪些列？（含"实时字节是否影响性能"澄清）
**A:** 上游代理地址、用户名+分组名、客户端来源+开始时间；**不加实时字节**。
**Ambiguity:** 24.3%

### Round 4 — 活跃边界 (Success Criteria / Contrarian)
**Q:** 表里只显示此刻开着的，还是也含刚关闭的？
**A:** 只显示当前活跃连接。
**Ambiguity:** 17.9%

### Round 5 — 刷新机制 (Success Criteria / Contrarian)
**Q:** 前端怎么刷新这个连接表？
**A:** 可切换间隔 + 可暂停。
**Ambiguity:** 11.8%

</details>
