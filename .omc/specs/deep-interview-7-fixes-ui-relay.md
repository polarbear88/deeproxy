# Deep Interview Spec: deeproxy 7 项 UI/中继修复

## Metadata
- Interview ID: 7-fixes-ui-relay
- Rounds: 6 (+ Round 0 topology)
- Final Ambiguity Score: 6.25%
- Type: brownfield
- Generated: 2026-06-15
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.95 | 0.35 | 0.333 |
| Constraint Clarity | 0.92 | 0.25 | 0.230 |
| Success Criteria | 0.93 | 0.25 | 0.233 |
| Context Clarity | 0.95 | 0.15 | 0.143 |
| **Total Clarity** | | | **0.9375** |
| **Ambiguity** | | | **0.0625** |

## Topology
7 个相互独立的修复组件，全部纳入本次范围（用户确认「全对，7 项都做」）。

| # | Component | Status | Description | Coverage / 验收 |
|---|-----------|--------|-------------|------------------|
| 1 | 规则列表字体 | active | 规则管理列表字体放大 | 14px → 16px，仅规则列表 |
| 2 | 仪表盘图表刷新 | active | 动作分布图切回仪表盘时刷新 | 切换到仪表盘路由/激活即重新拉取 |
| 3 | 实时连接显示域名 | active | 直连判定的连接显示嗅探到的域名 | 后端回填域名，目标主机列直接显示域名 |
| 4 | 系统日志卡顿 | active | 大量日志时不卡 | 虚拟滚动，仅渲染可视区域 |
| 5 | 客户端断开未终止上游 | active | 客户端断电后及时断开上游 | TCP Keepalive 检测死连接并清理 |
| 6 | 复制地址新格式 | active | 用户管理新增可选复制格式 | 新增 addr:port:user-group:pwd 格式 |
| 7 | 连接审计分页+查询 | active | 审计列表分页并支持查询 | 服务端分页 + 4 维筛选 |

## Goal
修复 deeproxy 管理后台（Vue3 + Element Plus）与 SOCKS5 中继后端（Go）的 7 个独立问题：
3 个涉及后端改动（#3 域名回填、#5 断连检测、#7 审计分页），4 个为前端改动（#1 字体、#2 刷新、#4 虚拟滚动、#6 复制格式）。

### 各组件权威实现要点

**#1 规则列表字体（前端）**
- 文件：`web/src/views/rule/Rules.vue`（表格 L256–282），全局字号 `web/src/styles/index.scss:68`（body 14px）。
- 实现：在 `Rules.vue` 的 scoped 样式中将规则列表表格字号设为 **16px**，**仅作用于规则列表**，不改全局、不影响其他页面表格。
- 注意 [[deeproxy-ep-ondemand-css-specificity]]：覆盖 Element Plus 样式需提高特异度（如 `body .el-table`），否则被运行时注入的 chunk 盖掉。

**#2 仪表盘动作分布刷新（前端）**
- 文件：`web/src/views/dashboard/Dashboard.vue`，`loadActionDist()` L106；当前仅 `onMounted` + 时间窗 watcher 触发，**无 onActivated**；3s 轮询 L150 只调 `loadOverview/loadRuntime`，刻意不含动作分布。
- 实现：增加在「切回仪表盘」时刷新动作分布。优先 `onActivated()`（若 keep-alive）；否则在路由进入 dashboard 时调用 `loadActionDist()`。需确认 router 是否对 dashboard 启用 keep-alive 以决定用 `onActivated` 还是路由守卫。

**#3 实时连接显示域名（后端 + 前端展示不变）**
- 根因：`connreg/registry.go` 的 `ConnMeta.Target`（L29）只存原始 SOCKS5 目标（IP）；`server/server.go handleSniff()` L415 嗅探出的域名仅用于路由匹配，**未写回**注册表；`connreg` 无 `SetTarget` 方法。
- 实现：
  1. `connreg/registry.go` 新增 `SetTarget(id int64, target string)` 方法（参照现有 `SetAction`/`SetUpstream` 的 atomic 回填模式），用 `atomic.Pointer[string]` 或在持锁下更新 `meta.Target`；`Snapshot`/`ConnView.Target` 读取最新值。
  2. `server/server.go handleSniff()` 嗅探成功（`detect.Sniff` 还原出域名）后，调用 `h.conns.SetTarget(d.connID, host)` 写回域名。
  3. 前端 `RealtimeConnections.vue` 目标主机列无需改动（`prop="target"` 直接渲染回填后的域名）。
- 展示：探测到域名后目标主机列**直接显示域名**（覆盖原 IP）。

**#4 系统日志虚拟滚动（前端）**
- 文件：`web/src/views/syslog/SysLog.vue`，日志区 L175–188，`v-for` 渲染全部行，`MAX_RENDER=5000` 仍产生 5000 DOM 节点导致卡顿。
- 实现：引入**虚拟滚动**，仅渲染可视区域行。优先复用成熟库（如 `vue-virtual-scroller` 的 `RecycleScroller`，或 Element Plus 的虚拟化列表）而非自造；保持现有 SSE 实时追加、level 过滤、终端样式不变。
- 注意：SSE 追加新行时虚拟列表需正确滚动到底/保持位置；保留 `MAX_RENDER` 兜底上限。

**#5 客户端断电检测 — TCP Keepalive（后端）**
- 根因：`server/relay.go relayCounted()` L110–167 用裸 `io.Copy` 双向中继；客户端 socket 握手超时在 relay 前已清除（`server/lifecycle.go:176`），relay 期间**无读超时、无 keepalive**；客户端断电（不发 FIN/RST）→ `io.Copy(target, clientR)` 永久阻塞 → `closeBoth()` 不触发 → `defer h.conns.Deregister()`（server.go ~L203）永不执行 → 上游与注册表常驻。
- 实现：对**客户端连接与上游连接**启用 **TCP Keepalive**（`net.TCPConn.SetKeepAlive(true)` + `SetKeepAlivePeriod(30 * time.Second)`，或经 `net.Dialer.KeepAlive`/`net.ListenConfig`）。死连接被 OS keepalive 探测发现后，`io.Copy` 返回错误，触发 `closeBoth()` 与 `Deregister`，上游随之终止。
- 约束：只杀真正死掉的连接，**不误伤长期空闲但仍存活的隧道**（这是选 keepalive 而非复用 idle 超时的原因）。Keepalive 周期默认 30s（可后续配置化，本次硬编码即可）。
- 验证：客户端关机后，实时连接列表与上游连接应在 keepalive 周期内（约 30–90s）被清理。

**#6 复制连接地址新格式（前端）**
- 文件：`web/src/views/user/Users.vue`，`buildProxyAddr()` L154–159 现仅产出 `socks5://{user}-{group}:{pwd}@{addr}:{port}`。
- 实现：**新增可选**复制格式 `{server-addr}:{socks5-port}:{user-group}:{pwd}`（即 `192.168.1.1:22:user-group:pass`），字段来源与现有 socks5:// 完全同源（仅写法不同）。用户可选择复制哪种格式（如下拉/双按钮/菜单）。保留原 socks5:// 格式。
- 边界：缺失字段沿用现有占位符策略（`<pwd>`/`<server-addr>`/`<socks5-port>`）。

**#7 连接审计分页 + 查询（后端 + 前端）**
- 根因：`api/syslog_handler.go handleAuditSnapshot()` L96–98 调 `a.audit.Snapshot()` 返回**全部** ring buffer（默认 5000 条）无分页；前端 `SysLog.vue` 审计 tab `loadAudit()` L91–98 一次性渲染全部行。
- 实现：
  1. 后端 `GET /api/syslog/audit` 增加 query 参数：`page`、`pageSize`、以及筛选 `user`/`target`/`action`/`group`；在 `handleAuditSnapshot` 中对 snapshot 先按筛选条件过滤，再切片分页，并返回 `{items, total, page, pageSize}`。注意 ring buffer 顺序一致性（建议 newest-first 稳定展示）。
  2. 前端审计 tab 增加 `el-pagination` + 4 个筛选输入（用户/目标主机/动作/分组），改为服务端分页拉取。
- 审计字段：Time/User/Group/Target/Action/Upstream/UpBytes/DownBytes。

## Constraints
- **热路径不可侵犯**：#5 的 keepalive 只在 socket 建立时设置一次，**不得**进入 `io.Copy` 热路径、不破坏 splice 零拷贝（参照既有约束 [[connreg 实时连接]]，`git diff server/relay.go` 的拷贝逻辑应保持零拷贝）。
- **#3 域名回填**同样不进 io.Copy 热路径，仅在嗅探解析点写一次。
- **全部中文注释**，解释「为什么」；遵循项目 DRY 与按功能分模块规范。
- **优先成熟库**：#4 虚拟滚动优先用成熟库而非自造。
- **i18n 键 zh/en 必须对等**：#6/#7 新增文案需同时加 zh.js 与 en.js。
- **不建分支**：所有代码直接提交 main（[[no-branches-commit-to-main]]）。
- CSS 覆盖 Element Plus 需提高特异度（[[deeproxy-ep-ondemand-css-specificity]]）。

## Non-Goals
- 不改 SOCKS5 协议范围（仍仅 CONNECT/TCP）。
- 不把 keepalive 周期做成完整可配置项（本次硬编码 30s 即可，后续可扩展）。
- 不改审计存储为数据库（仍为内存 ring buffer，仅加分页/筛选）。
- 不改动 #3 前端目标主机列结构（仅靠后端回填）。
- reject 连接不进活跃注册表（结构性如此，非本次新增）。

## Acceptance Criteria
- [ ] #1：规则管理列表字体为 16px，其他页面表格字体不变（14px）。
- [ ] #2：从其他页面切回仪表盘时，动作分布图自动重新拉取并刷新。
- [ ] #3：直连（域名规则命中）的实时连接，目标主机列显示嗅探到的域名而非 IP。
- [ ] #3：`connreg` 新增 `SetTarget`，`handleSniff` 嗅探成功后回填域名；`go build ./...` 通过。
- [ ] #4：系统日志大量日志（≥5000 行）滚动流畅，DOM 仅渲染可视区域行；SSE 实时追加正常。
- [ ] #5：客户端关机后，约 30–90s 内该连接从实时列表消失，上游连接被终止；正常活跃/空闲连接不被误杀。
- [ ] #5：`server/relay.go` 的双向拷贝逻辑（splice/零拷贝）git diff 无破坏。
- [ ] #6：用户管理可选择复制 `addr:port:user-group:pwd` 格式，原 socks5:// 格式仍可用。
- [ ] #7：`GET /api/syslog/audit` 支持 page/pageSize + user/target/action/group 筛选，返回分页结构。
- [ ] #7：审计 tab 有 el-pagination 与 4 个筛选控件，大量记录不卡。
- [ ] i18n：所有新增文案 zh/en 键对等。
- [ ] `go build ./...` 与前端 `pnpm build` 均通过。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| #5 用 idle 超时即可 | 会误杀空闲存活隧道 | 改用 TCP Keepalive，只杀真死连接 |
| #3 前端问题 | 实为后端未存储域名 | 后端加 SetTarget 回填 |
| #7「查询」泛指 | 需明确筛选维度 | user/target/action/group 四维 |
| #1「放大些」 | 无具体数值 | 14px → 16px |
| #6 替换原格式 | 用户说「新增可选择」 | 新增格式，原格式保留 |
| #4 已有 5000 上限够了 | 5000 DOM 仍卡 | 虚拟滚动 |

## Technical Context
- 后端 Go：`server/`（SOCKS5 服务端 + 中继）、`connreg/`（活跃连接注册表，stdlib-only 中性叶子包）、`detect/`（SNI/Host 嗅探）、`syslog/`（审计 ring buffer）、`api/`（Gin HTTP 管理接口）。
- 前端 Vue3 + Element Plus + ECharts，`web/src/views/{rule,dashboard,connections,syslog,user}/`，i18n `web/src/locales/{zh,en}.js`。
- 关键文件行号见各组件实现要点（基于本次 explore agent 实测）。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| ActiveConn (connreg) | core domain | id, Target(IP→域名), Action, Upstream, User, Group, Client, Start | Snapshot→ConnView 供实时连接 API |
| AuditEntry | core domain | Time, User, Group, Target, Action, Upstream, UpBytes, DownBytes | ring buffer(5000) → 审计 API |
| RelayConn | core domain | clientConn, upstreamConn | relayCounted 双向 io.Copy；keepalive 检活 |
| ProxyAddr (前端) | supporting | server-addr, socks5-port, user, group, pwd | 两种复制格式 |

## Interview Transcript
<details>
<summary>Full Q&A (Round 0 + 6 rounds)</summary>

### Round 0 — Topology
**Q:** 7 项读成 7 个独立修复组件，对吗？
**A:** 全对，7 项都做。

### Round 1 — #5 断连检测
**Q:** 客户端断电检测用哪种方式（keepalive / idle 超时 / 两者）？
**A:** TCP Keepalive（推荐）。

### Round 2 — #7 审计查询维度
**Q:** 审计「查询」支持哪些筛选维度？
**A:** 用户、目标主机、动作、分组（四维全选）。

### Round 3 — #3 域名展示
**Q:** 探测到域名后目标主机怎么展示？
**A:** 直接显示域名。

### Round 4 — #4 日志卡顿
**Q:** 系统日志卡顿怎么修？
**A:** 虚拟滚动（推荐）。

### Round 5 — #6 复制格式
**Q:** 192.168.1.1:22:user:pass 四字段映射什么？
**A:** addr:port:user-group:pwd（与 socks5:// 同源）。

### Round 6 — #1 字体
**Q:** 规则列表字体放大到多少？
**A:** 16px（推荐）。

</details>
