# Deep Interview Spec: deeproxy V2 批量修复与增强

## Metadata
- Interview ID: di-v2-batch-20260614
- Rounds: 4
- Final Ambiguity Score: 13%
- Type: brownfield
- Generated: 2026-06-14T06:27:45Z
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.88 | 0.35 | 0.31 |
| Constraint Clarity | 0.86 | 0.25 | 0.21 |
| Success Criteria | 0.84 | 0.25 | 0.21 |
| Context Clarity | 0.93 | 0.15 | 0.14 |
| **Total Clarity** | | | **0.87** |
| **Ambiguity** | | | **0.13** |

## Topology
本批次确认 6 个顶层组件，全部 active，无推迟。

| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| 1. 用户授权 (User Authz) | active | 独立授权按钮/弹窗、通配"全部组"标志、修复授权不生效、未授权日志 | AC-1.x |
| 2. 仪表盘与图表 (Dashboard & Charts) | active | 卡片对齐、暗色图表背景、饼图黑边、主题切换图表错乱、首页显示监听端口 | AC-2.x |
| 3. 代理池上游 (Pool Upstreams) | active | 批量添加多格式、跨页多选批量改权重/启用、服务端分页、性能评估 | AC-3.x |
| 4. 系统设置与连接提示 (Settings & Hints) | active | 服务器域名/IP 设置、首页连接示例、复制代理地址按钮({group}) | AC-4.x |
| 5. 转发/认证核心健壮性 (Core Robustness) | active | 配置事务化、relay 半开修复、健康检查协程池、域名规范化 | AC-5.x |
| 6. 次要修复 (Minor Fixes) | active | 实时速率内存计数、router 初始化判断、crypto/rand、feature-status 文档 | AC-6.x |

## Goal
对 deeproxy V2(已实现的 SOCKS5 中继 + Web 管理面板)进行一批以**功能缺陷修复 + 体验增强 + 代码健壮性加固**为主的迭代。核心目标:
1. 让"用户↔代理组授权"真正可用且语义清晰(独立设置入口 + 通配全部组 + 正确的未授权提示)。
2. 修复仪表盘/图表在明暗主题与导航切换下的多处视觉错乱,并在首页暴露监听端口与可复制的连接示例。
3. 使代理池能承载数千条上游(服务端分页 + 批量操作 + 健康检查并发化)。
4. 落实 codex 代码评审的 HIGH/MEDIUM/LOW 项,消除配置分裂、fd 泄漏、串行探测、规则大小写漏匹配等隐患。

## Key Decisions (本次访谈关键决策)
- **D1 (Round 1) — "全部代理组"= 通配标志**:`proxy_user` 增加 `all_groups` 布尔字段(或等价持久化)。勾选后该用户对**所有现存及未来新建分组**自动授权;`IsAuthorized` 命中该标志直接放行,无需枚举。精细逐组勾选方式仍保留并存(用户二选一或叠加,以通配优先)。
- **D2 (Round 2) — 上游列表服务端分页 + 跨页全选**:`GET /groups/:id/upstreams` 增加 `page/pageSize/keyword/healthState` 参数(SQL LIMIT/OFFSET),默认 `pageSize=100`。批量"设置权重/启用"的多选支持两种模式:**选中当前页** 与 **选中全部(跨页,按当前筛选条件)**。
- **D3 (Round 3) — 复制地址用完整 socks5:// URL**:复制按钮产出 `socks5://<user>-{group}:<pwd>@<server-addr>:<socks5-port>`。`{group}` 为字面占位符让用户自填;`<user>`/`<pwd>` 取真实代理用户;`<server-addr>` 取系统设置的"服务器域名/IP",`<socks5-port>` 取实际监听端口。
- **D4 (Round 4, Contrarian) — relay 半开:仅出错时关两端**:正常 EOF(干净单向结束)**无条件保留另一方向**(下载绝不被提前切断);仅当某方向 `io.Copy` 返回非 nil error 时,立即关闭两端并 cancel 另一个 copy 以即时回收 fd;真正无 EOF 无 error 的卡死由 `idle_timeout_sec`(默认 300s)兜底。必须补半开回归测试。

## 重要事实更正 (Authoritative Format Correction)
`CLAUDE.md` 记载的用户名格式 `base64("user:pwd@host:port")` 是**首版设计,已被 V2 实现取代**。
**当前权威格式(代码 `auth/authz.go` 实测)**:
- 客户端 **username** = `<user>-<group>` 或 `<user>-<group>-<tail>`(Type A 动态上游 / Type B 命名变量)
- 客户端 **password** = 该代理用户的密码(常量时间比较)
- 认证依次校验:用户存在 → 分组存在 → 用户已授权该分组 → 密码匹配
所有新文案/连接示例/复制模板必须使用该真实格式。**附带任务**:更新 `CLAUDE.md` 与 `.omc/specs/deep-interview-socks5-relay.md` 以反映 V2 实际格式,消除文档漂移(归入 AC-6.4)。

## Constraints
- 转发热路径性能不得回退:快照采用内存加权选择,数千上游对选路无显著影响;分页只解决**管理 UI 渲染**与**健康探测**的瓶颈。
- 配置写入必须保证 DB 权威数据与转发快照一致:坏配置不得持久留库(D5,见 AC-5.1)。
- 健康探测协程池为**全局共享**,不分组,默认大小 **150**,可在系统设置中修改;所有分组的所有探测任务都走这一个池;每个探测带 per-probe context 超时(沿用 10s)。
- 主题只能在 ECharts `init` 时指定,切换主题需 dispose + 重建;必须处理 keep-alive 下 `onActivated` 重新 resize/重建,避免隐藏容器零尺寸 init。
- 全部新代码遵循项目规范:中文注释解释"为什么"、单文件职责单一、DRY、优先成熟库。
- 批量添加解析必须自动识别两种格式:`user:pass@host:port` 与 `user:pass:host:port`,每行一条,容错跳过非法行并回报失败行号/原因。

## Non-Goals
- 不实现 `dashboard/top?kind=domain`(结构性缺口:`traffic_stat` 无 domain 维度);本次仅**对齐文档/测试与代码现状**,显式标注"首版未实现"(AC-6.4)。
- 不改动 SOCKS5 协议范围(仍仅 CONNECT/TCP)。
- 不引入 UDP/BIND、GeoIP、远程规则订阅。
- 不把代理用户密码改为哈希存储(本次范围外,维持现状)。
- "服务器域名/IP"设置仅用于**提示文案/连接示例**,不参与任何实际监听绑定或转发逻辑。

## Acceptance Criteria

### Component 1 — 用户授权
- [ ] AC-1.1 用户管理列表/操作区有**独立的"设置授权分组"按钮**,打开独立弹窗,与"编辑用户"(用户名/密码/备注)完全分离。
- [ ] AC-1.2 授权弹窗内有"授权全部代理组"开关;开启后写入 `all_groups=true`,该用户对所有现存及**未来新建**分组自动可访问。
- [ ] AC-1.3 关闭通配开关时可逐组勾选,保存为 `group_user` 行;保存后重新打开弹窗能正确回显已授权状态(修复"设置后仍显示未授权")。
- [ ] AC-1.4 设置授权后实际 SOCKS5 连接对已授权分组放行、对未授权分组拒绝(端到端验证,修复"实际测试也无效")。
- [ ] AC-1.5 当用户名+密码正确但访问未授权分组时,**服务端日志打印明确的"用户 X 访问分组 Y 未授权"提示**,而非泛化的 `failed to authenticate: user authentication failed`。(`auth/authz.go` 已有 `AuthError{Reason:"用户未授权访问该分组"}`,需将其 reason 提升到服务端连接日志层。)

### Component 2 — 仪表盘与图表
- [ ] AC-2.1 "运行健康"与"连接用户名格式说明"两卡片同行**等高对齐**(el-row 加 flex stretch / el-card height:100%)。
- [ ] AC-2.2 暗色模式下仪表盘所有图表(时序图、动作分布饼图)背景与整体暗色一致,无突兀亮/暗块。
- [ ] AC-2.3 亮色模式下动作分布饼图无难看黑边(`itemStyle.borderColor` 用 ECharts 可解析的真实色值,而非 Canvas 无法解析的 `var(--el-bg-color)`)。
- [ ] AC-2.4 在非仪表盘页切换主题后再进入仪表盘,图表位置/尺寸正常,不错乱不变小(EChart 增加 `onActivated` resize/重建,处理 keep-alive 隐藏容器零尺寸)。
- [ ] AC-2.5 代理组"分组流量"图表(抽屉内)暗色模式背景同样正确。
- [ ] AC-2.6 首页(仪表盘)显示当前 SOCKS5 监听端口(及 Web 端口);需新增后端暴露监听地址/端口的来源。

### Component 3 — 代理池上游
- [ ] AC-3.1 上游添加弹窗同时支持"单条表单添加"与"批量文本框添加"。
- [ ] AC-3.2 批量添加每行一条,自动识别 `user:pass@host:port` 与 `user:pass:host:port`,非法行跳过并回报行号+原因,成功条数回报。
- [ ] AC-3.3 上游列表服务端分页,默认每页 100,带 `page/pageSize/keyword/healthState` 参数。
- [ ] AC-3.4 列表支持多选,可"选中当前页"或"跨页全选(按当前筛选)",对选中项批量设置权重与启用/禁用状态。
- [ ] AC-3.5 数千条上游下,管理列表加载与操作流畅;转发选路与健康检查不因数量退化(健康检查并发化见 AC-5.3)。性能评估结论写入交付说明。

### Component 4 — 系统设置与连接提示
- [ ] AC-4.1 系统设置新增"服务器域名/IP"字段,默认值自动获取本机网络 IP(非回环);保存后持久化。
- [ ] AC-4.2 首页连接说明使用该地址 + 真实监听端口给出**具体可用的连接示例**(真实 V2 用户名格式)。
- [ ] AC-4.3 用户管理操作区新增"复制代理地址"按钮,复制 `socks5://<user>-{group}:<pwd>@<server-addr>:<socks5-port>`,`{group}` 为字面占位符。
- [ ] AC-4.4 系统设置新增"健康检查协程池大小"字段(默认 150),与 AC-5.3 联动。

### Component 5 — 转发/认证核心健壮性 (codex HIGH/MEDIUM)
- [ ] AC-5.1 (HIGH, server.go:185 / rule_handler.go:246) 配置写入改为**写前构建候选配置并编译校验通过后再发布**,或写入+校验+发布事务化;rebuild 失败不得留下坏数据(DB 与快照不分裂)。补失败回滚测试。
- [ ] AC-5.2 (HIGH, relay.go:103) 按 D4 实现:正常 EOF 保活另一方向,出错关两端并 cancel 另一 copy;补半开/出错回归测试,验证无 fd/goroutine 泄漏且下载不被提前切断。
- [ ] AC-5.3 (MEDIUM 必改, health.go:250) 健康检查改用**全局共享 goroutine 池**(默认 150,设置可调),所有分组所有探测共用;每探测带 per-probe context;单轮总耗时由串行 N×10s 降为受池大小限制的并发。
- [ ] AC-5.4 (MEDIUM, engine.go:101) 域名规则入库与匹配前统一 canonicalize(小写 + 去尾点),`Example.COM` / `example.com.` 能命中 `example.com`。
- [ ] AC-5.5 (MEDIUM, dashboard_handler.go:71 / flush.go:126) 实时速率改为从内存 Counter 暴露累计快照计算,SQLite 仅做历史持久化,不受 flush 周期/落库失败影响。

### Component 6 — 次要修复 (codex MEDIUM/LOW)
- [ ] AC-6.1 (MEDIUM, router/index.js:84) 修正初始化检查:仅未确认时查一次(反转条件或加 `initChecked` 状态),避免每次路由切换都请求。
- [ ] AC-6.2 (LOW, session.go:77) `crypto/rand.Read` 错误不再忽略;失败时返回错误,登录走 500,移除"全零仍唯一"的错误注释。
- [ ] AC-6.3 (LOW) `dashboard/top?kind=domain` 维持未实现但响应契约稳定(空数组 + `X-Feature-Status` 头);前端显示"首版暂不支持"占位。
- [ ] AC-6.4 (LOW) 建立/更新权威 feature-status 说明,并修正 `CLAUDE.md` 等文档中已漂移的用户名格式与未实现项描述。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| "全部代理组"= 把当前所有组写进 join 表 | 未来新建的组要不要自动授权? | 选通配标志 `all_groups`,覆盖未来新组 (D1) |
| 数千上游会拖垮转发 | 瓶颈到底在哪? | 热路径无碍;瓶颈在 UI 渲染 + 串行探测,用分页 + 协程池解决 (D2/AC-5.3) |
| 用户名格式是 base64("user:pwd@host:port") | 代码实测是什么? | 实测为 `user-group[-tail]`;CLAUDE.md 已漂移,以代码为准并修文档 (D3/AC-6.4) |
| relay 存在严重 goroutine 泄漏需激进修复 | 通道已 buffered,真问题是什么? | 真问题是出错侧的 idle fd 滞留;正常下载必须保活,仅出错关两端 (D4) |
| 健康检查池按分组划分 | 每组一个池还是全局? | 全局共享单池,默认 150 可调,所有分组共用 (用户明确要求) |

## Technical Context (codebase findings)
- **授权模型**:`store.ProxyUser`(无 groups 字段)+ `group_user` join 表 + 快照 `map[AuthzKey]struct{}`;`IsAuthorized` O(1)(`snapshot/snapshot.go:161`)。未授权 reason 已在 `auth/authz.go:85` 产生但未冒泡到连接日志。
- **设置入口已分离**:`POST /api/proxy-users/:id/groups` → `handleSetUserGroups`(全量替换),与 `handleUpdateUser` 独立 —— 后端已就绪,缺前端独立按钮/回显。
- **错误串来源**:`failed to authenticate: %w` 来自 go-socks5 库 `server.go:152`,内层 `ErrUserAuthFailed`;内部 `AuthError.Reason` 不发给客户端,需在 deeproxy 服务端日志层打印。
- **主题/图表**:`web/src/stores/theme.js` 已支持跟随系统;无 `registerTheme`,用内置 `'dark'`;`EChart.js` 在 `isDark` watch 里 dispose+init,但**缺 `onActivated`**;keep-alive(`MainLayout.vue` `<keep-alive :max="6">`)导致隐藏容器零尺寸 init → 错乱。饼图 `itemStyle.borderColor:'var(--el-bg-color)'` Canvas 无法解析。
- **卡片对齐**:`Dashboard.vue:241-301` el-row 无 align,el-card 无 height:100%。
- **上游 API**:`api/group_handler.go` `handleCreateUpstream` 仅单条,无批量;`ProxyGroups.vue` 抽屉表格无分页/无多选;前端表单缺 `user` 静态字段。模型字段 host/port/user/username_template/pwd/weight/enabled/health_state。
- **健康检查**:`pool/health/health.go` `probeGroup` 串行 `for ups`,`defaultProbeTimeout=10s`,无协程池;base tick 30s。
- **relay**:`server/relay.go` `relayCounted` 两通道 buffered(1),`<-upc` 后 `<-downc`,无出错即时取消。
- **配置发布**:`api/server.go:185 rebuildAndSwap` DB 先写,`RebuildAndSwap` 失败保旧快照但**不回滚 DB**;`rebuildMu` 串行化。
- **速率**:`api/dashboard_handler.go` `rateSampler` 基于 `QueryTotals`(SQLite 当日累计)两次采样差分;内存 `stats/counter.go` Counter 已存在(atomic),仅用于 active/reject/action 分布,未用于速率。
- **路由**:`web/src/router/index.js:85` 条件 `if (userStore.initialized)` 与注释意图相反(`initialized` 默认 true),导致每次路由都查。
- **session**:`api/session.go:77` `_, _ = rand.Read(b)` 忽略错误。
- **设置字段**:`system_setting` 14 字段,无 server-addr,无 probe-pool-size;`PUT /settings` 合并非零字段;LogLevel 直接 `levelVar.Set` 热更新。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| ProxyUser | core domain | id, username, pwd, remark, **all_groups(new)** | has many Group via group_user 或 all_groups 通配 |
| Group (代理组) | core domain | id, name, type(A/B), hc_* | has many UpstreamProxy; authorized to many ProxyUser |
| group_user | supporting | group_id, user_id | join ProxyUser↔Group |
| UpstreamProxy | core domain | id, group_id, host, port, user, username_template, pwd, weight, enabled, health_state | belongs to Group |
| SystemSetting | supporting | ...14 fields, **server_addr(new)**, **probe_pool_size(new)** | singleton |
| Snapshot | supporting | rules, authz map, upstreams, settings | rebuilt on write, atomically swapped |
| HealthChecker | external/runtime | global goroutine pool(new), per-probe ctx | probes UpstreamProxy |
| Counter (stats) | supporting | atomic up/down/req per (group,user), action dist | feeds realtime rate(new) |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 4 | 4 | - | - | N/A |
| 2 | 6 | 2 | 0 | 4 | 100% |
| 3 | 7 | 1 | 1(SystemSetting+server_addr) | 5 | 100% |
| 4 | 8 | 1 | 1(UpstreamProxy relay touch) | 6 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (4 rounds + Round 0)</summary>

### Round 0 — Topology
**Q:** 6 顶层组件划分对吗?
**A:** 划分正确,全部要做。

### Round 1 — 用户授权 / Goal
**Q:** "允许授权全部代理组"怎么理解?
**A:** 通配标志(含未来新组)。
**Ambiguity:** 26% (Goal 0.78, Constraints 0.62, Criteria 0.70)

### Round 2 — 代理池 / Constraints
**Q:** 上游列表分页采用哪种架构?
**A:** 服务端分页 + 跨页全选。
**Ambiguity:** 19% (Goal 0.82, Constraints 0.74, Criteria 0.74)

### Round 3 — 设置与提示 / Goal
**Q:** "复制代理地址"采用哪种格式?
**A:** 完整 socks5:// URL。
**Ambiguity:** 16% (Goal 0.86, Constraints 0.78, Criteria 0.80)

### Round 4 (Contrarian) — 核心健壮性 / Constraints
**Q:** relay 半开连接修复策略?
**A:** (澄清后)仅出错时关两端、正常下载无条件保活(保半双工)。
**Ambiguity:** 13% (Goal 0.88, Constraints 0.86, Criteria 0.84)

</details>
