# 工作计划：「实时连接」(Realtime Connections) 全栈模块

> 状态：**pending approval**（RALPLAN-DR 共识已通过：Planner → Architect[SOUND-WITH-CHANGES] → Critic[ACCEPT]，2 轮迭代）
> 来源规格：`.omc/specs/deep-interview-realtime-connections.md`（deep-interview，模糊度 11.8%）
> 模式：RALPLAN-DR short。所有 Architect + Critic 要求与保留项已折入。

---

## 1. 需求摘要

在 deeproxy 管理后台新增只读「实时连接」菜单模块，以表格展示**此刻仍在中继的全部活跃 SOCKS5 连接**。全栈纵向切片：

- **后端**：新建**中性叶子包 `connreg/`**（`package connreg`，仅依赖 stdlib，`action` 存为 `string` → 零项目依赖，杜绝 `api → server` 环），承载活跃连接登记表。`cmd/deeproxy/main.go` 创建唯一实例，注入到 SOCKS5 服务端（`connectHandle` 写）与 API（handler 读）。这一模式复刻现有 `syslog.AuditBuffer`（中性叶子，server 写、api 读）。
- **API**：`GET /api/connections?limit=500&sort=start|duration` 返回有界 Top-N + 精确 `total` + `truncated`。无分页、无服务端过滤。
- **前端**：`web/src/views/connections/RealtimeConnections.vue`，Element Plus 表格、截断提示（"显示 X / 共 Y 条"）、排序切换、自动刷新开关 + 间隔选择（2s/5s/10s/关，默认开 5s，复用 Dashboard 轮询模式）。含路由、api 模块、zh/en i18n。

**响应形态（权威）**：
```
{ items: [{ id, target, action, upstream, user, group, client, start_ts, duration_sec }],
  total, limit, truncated }
```
- 列：目标主机(target) / 动作(action) / 连接时长(duration) / 上游代理地址(upstream) / 用户名+分组名(user+group) / 客户端来源(client) / 开始时间(start_ts)。
- **`action` 仅 `forward` | `direct`**：被拒绝连接在 Allow 阶段即关闭（`server/server.go:124-128`），从不进入 `connectHandle`，故结构上不出现在活跃列表。UI 加帮助行："拒绝记录请在系统日志/审计查看。"
- `start_ts` = unix 秒（int64，客户端算时长更简单）；`upstream` JSON 用空串 `""`，前端渲染 `"—"`。

**一号硬约束（load-bearing）**：register/deregister/setUpstream/setAction 均 O(1)，仅在连接开始/拨号完成/结束处发生，**绝不进入 `relayCounted` 的 `io.Copy` 循环**（server.go:267,407）。无逐字节记账、**不显示实时字节**——保留 Linux `splice(2)` 零拷贝快路径。这与现有 `recordTraffic`/`recordAudit`（server.go:425-453，仅在拷贝结束后一次性记账）纪律一致。

---

## 2. RALPLAN-DR 摘要（short 模式）

### 原则 (5)
1. **热路径不可侵犯** — 中继拷贝循环与 `splice` 快路径神圣不可碰；登记表只在 O(1) 生命周期点（开/拨号完成/关）触碰连接，紧挨现有 `counter.ConnOpened/ConnClosed`。
2. **中性叶子分层** — connreg 是零依赖叶子（仅 stdlib，`action` 用 string），server 写、api 读、main 装配，复刻 `syslog.AuditBuffer` 先例，杜绝 `api → server` 环。
3. **有今无史** — 纯活跃 map（connID→entry），关连接即删，map 永远是"此刻快照"，非环形历史。**权威 total = Snapshot 扫描时所见条目数**，与 `stats.ActiveConns()` 在同一生命周期点同步。
4. **单文件单职责** — 登记表独立包/文件，handler 独立文件，Vue 视图独立目录。全中文注释解释"为什么"。
5. **i18n 完备** — 所有用户可见串在 zh.js / en.js 双语同键集，由**键对齐自动测试**守护（历史缺键 bug）。

### 决策驱动 (Top 3)
1. **零热路径开销 + 拨号阶段可见** — 登记/回填不得给拷贝循环加任何代码，同时仍能呈现仍在拨号/嗅探（慢/故障转移）的连接——运维最想看的正是这些。
2. **有界后端成本（用户自述"数量过大卡死"）** — Snapshot 不得在每 5s 轮询时按 O(N) 分配/排序；必须 O(N) 时间 / O(K) 空间。
3. **单一、panic 安全的活跃计数真相源** — 不引入第三个计数器；即使连接 goroutine panic（recoverPool，lifecycle.go:43-48）计数也精确。

### 关键决策

#### 决策 A — 何时登记 & 如何回填 upstream/action
Type B 上游仅在 `dialWithFailover` 之后已知（server.go:245,388）；嗅探路径动作仅在对嗅探域名 `MatchRule` 后已知（server.go:368-387）。

**Option A1 — 登记后回填（采纳）。** 在 `connectHandle` 进入处、紧随 `h.counter.ConnOpened()`（server.go:180）登记全部前置元数据（id/target/action/user/group/client/start；upstream=""）。把 `connID` 写入 `decision.connID`（新字段，server/ctxkey.go），按值流向所有下游方法，**零签名改动**。`dialWithFailover` 成功后 `SetUpstream`；嗅探解析动作后 `SetAction`（仅 forward/direct 分支，见下）。
- 优点：拨号/嗅探连接可见（符合目标）；登记/注销集中在 `connectHandle` 单一咽喉点，"必注销"保证搭已正确的生命周期代码顺风车；回填 O(1) 且在拷贝循环外；connID-in-decision 免参数穿线。
- 缺点：每连接至多 3 次 O(1) 冷写（register + setUpstream + setAction）；拨号/嗅探瞬间行短暂显示 upstream="—"/pending 动作——**记为有意行为**。

**Option A2 — 拨号后登记（否决）。** 仅在上游已知后登记。拨号阶段（慢/卡）连接不可见，正是最该看的；注销需穿过两个调用点 + 失败早退，更易泄漏条目。

#### 决策 B — Snapshot 算法（用户"卡死"恐惧的后端侧）
**Option B1 — Range→全切片→全排序→截断（否决）。** O(N) 分配 + O(N log N) 排序 / 每 5s 轮询；N=50k 即把"卡死"风险搬到服务端。

**Option B2 — 单趟 Range 内有界 top-K 堆（采纳）。** 一次 `Range`，维护大小 ≤ `limit` 的堆，按排序键比较；`total` = Range 所见条目数（精确）。O(N) 时间、O(K) 空间、无全 N 切片。
- **堆方向按排序模式参数化（Critic 保留项 1，必须）**：
  - `sort=start`（要 K 个**最大** start_ts = 最新）→ **最小堆**（堆顶为所留 K 个里 start_ts 最小者）；堆满时新元素 > 堆顶则弹出最小者。即"驱逐最旧"。
  - `sort=duration`（时长最长 = start_ts **最小** = K 个最旧）→ **最大堆**（堆顶为所留里 start_ts 最大者）；堆满时新元素 < 堆顶则弹出最大者。即"驱逐最新"。
  - **比较器在 tie 边界含 `seq`(connID)**：start_ts 相等时按 seq 决定去留，使跨轮选择确定、无抖动。
  - 一趟结束后 `sort.SliceStable` 把 ≤K 内容排成展示序：`start`→start 降序后 seq 降序；`duration`→start 升序后 seq 升序（堆出队无序，必须再排）。

#### 决策 C — 登记表包归属
**connreg/ 中性叶子（采纳）**，而非 `server`（会逼出 `api → server` 环）或 `stats`（污染计数专注包）。先例：`syslog.AuditBuffer`。

---

## 3. 验收标准（可测试）

- [ ] **AC-1** 新包 `connreg/`（`package connreg`）定义 `Registry`，方法：`Register(meta ConnMeta) (connID int64)`、`SetUpstream(connID int64, upstream string)`、`SetAction(connID int64, action string)`、`Deregister(connID int64)`、`Snapshot(limit int, sortBy string) (items []ConnView, total int, truncated bool)`、`Len() int`。**仅 import stdlib**（`action` 存 string），无项目依赖、无环。
- [ ] **AC-2** connID 由 `atomic.Int64.Add(1)` 生成——单调、进程内唯一、存活期不复用。同一 `seq` 值用作 duration 排序 tiebreaker。
- [ ] **AC-3** `connectHandle`（server.go:~180）进入处调用一次 `Register`；立即将返回 id 写入 `d.connID`（新 `decision` 字段，server/ctxkey.go）；`defer registry.Deregister(connID)` 置于与 `defer h.counter.ConnClosed()`（server.go:181）**同一 defer 区**，保证 panic 也计数对齐。
- [ ] **AC-4（客户端地址）** 在 `connectHandle` 进入处经 `if c, ok := writer.(net.Conn); ok { client = c.RemoteAddr().String() }` 捕获，断言失败回退 `""`（复刻 server.go:482 已验证的 cast）。**禁止用 `req.RemoteAddr`**（错误来源）。AuditEntry 无 client 字段，此为净新增捕获。
- [ ] **AC-5（upstream 回填，双分支）** 抽取共享 helper `upstreamString(d decision, view *snapshot.UpstreamView) string`，**同时处理**：Type B = `net.JoinHostPort(view.Host, strconv.Itoa(view.Port))`；Type A forward = `d.auth.DynamicUpstream.Addr()`（auth/upstream.go:32）；direct/无 = `""`。`recordAudit`（server.go:437-443）重构为调用同一 helper（DRY）。`dialAndRelay`（server.go:245）与 `handleSniff`（server.go:388）成功后各调 `SetUpstream(d.connID, upstreamString(d, usedUpstream))`。
- [ ] **AC-6（嗅探动作回填，仅 forward/direct 分支）** 嗅探路径经 `MatchRule` 解析动作（server.go:368-387）后调 `SetAction(d.connID, string(action))`。**必须放在 `if action == rule.ActionReject { return errSniffReject }` 守卫之后**（即 `d.action = action` 处，server.go ~386），**绝不写入 `action="reject"`**（保留项 2）——维持 AC-11 的 action ∈ {forward,direct} 契约。
- [ ] **AC-7（热路径零触碰）** register/deregister/setUpstream/setAction 在 `relayCounted`/`io.Copy` 内**零新增语句**；`git diff server/relay.go` 显示拷贝循环无改动；`splice` 快路径保留。**本 AC 为代码评审 / git-diff 门，非自动断言**（如实标注）。
- [ ] **AC-8（必注销）** 每条退出路径都注销：成功、拨号失败（server.go:246-252）、嗅探拒绝（server.go:378-384）、panic（recoverPool 在 connectHandle **之上**的 goroutine 入口恢复，故 connectHandle 的 defer 先跑——lifecycle.go:43-48）。由覆盖其下全部 return 的单个 defer 保证。
- [ ] **AC-9（有界 Snapshot）** Snapshot 用**大小 ≤ limit 的有界堆**单趟 Range：O(N) 时间、O(K) 空间、无全 N 切片。`total` = Range 计数（精确）。`truncated = total > limit`。**堆方向按模式参数化：`start` 用最小堆（驱逐最小 start_ts），`duration` 用最大堆（驱逐最大 start_ts）；`seq` 参与 tie 边界比较器。**
- [ ] **AC-10（确定序）** `sort=start`→最新优先；`sort=duration`→最长优先（= 最旧 start_ts 优先）。start_ts 相等由 `seq`(connID) 破平局，**堆比较器与最终 `sort.SliceStable` 都含 seq**，跨轮序确定无抖动。
- [ ] **AC-11（响应形态）** 严格 `{ items: [{ id, target, action, upstream, user, group, client, start_ts, duration_sec }], total, limit, truncated }`。`start_ts` = unix 秒(int64)；`duration_sec` = `int64(now - start)` 快照；`upstream` 可为 `""`（前端渲染 "—"）；`action` ∈ {`forward`,`direct`}。
- [ ] **AC-12（单一活跃计数源）** `connreg.Registry` 扫描 total（及 `Len()`）为本视图权威活跃数；与 `stats.ActiveConns()`（stats/counter.go:212-218，surfaced api/dashboard_handler.go:23,85）追踪同一量（同在 connectHandle 开/关点）。测试断言**静止时两者完全相等（差异仅在开/关瞬间）**。**不创建第三计数器**；atomic `hint` 明确非权威。
- [ ] **AC-13（API）** 新 `api/connections_handler.go` 在 `auth` 组（server.go:134）注册受保护路由 `GET /api/connections`。Query：`limit`（默认常量 `connreg.DefaultLimit=500`，钳到上限）、`sort`（默认 `start`，接受 `start|duration`，未知→`start`，不返 400）。复用 `atoiDefault`（response.go:54）与 `respondOK`（response.go:23）。
- [ ] **AC-14（DI 命名，防冲突）** `App` 加字段 `connReg *connreg.Registry`（**勿与现有 `registry *pool.Registry` 混淆**，api/server.go:48）。`NewApp` 加**第 12 个位置参数** `connReg *connreg.Registry`，带 nil 兜底（`if connReg == nil { connReg = connreg.New() }`，复刻 server.go:83-86）。归档 `AppDeps` 结构体重构跟进项（本功能不做）。
- [ ] **AC-15（共享实例装配）** `cmd/deeproxy/main.go` 创建**唯一** `connReg := connreg.New()`（main.go:172-173 附近），将**同一实例**传入 `server.New(...)`（扩展签名 +`*connreg.Registry`）与 `api.NewApp(...)`。
- [ ] **AC-16（server 装配）** `server.New` 签名加 `*connreg.Registry`；handler 结构（server.go:155-161）加 `conns *connreg.Registry` 于 `New`（server.go:526-532）；`decision`（server/ctxkey.go:21）加 `connID int64`。
- [ ] **AC-17（登记表关停）** 记录：connreg 是纯内存单例，**无 goroutine、无 Shutdown 方法**。SOCKS 服务关停时在飞连接的 `defer Deregister` 随其关闭执行，map 自然排空。无需生命周期钩子。
- [ ] **AC-18（前端路由+菜单）** `web/src/router/index.js` 加 `/connections` 子路由，`meta: { title: 'menu.connections', icon: <Element Plus 图标名> }`；菜单经 `MainLayout.vue` 子路由过滤自动出现（layouts/MainLayout.vue:22-24,72-75）。
- [ ] **AC-19（前端视图）** `web/src/views/connections/RealtimeConnections.vue`：7 列；动作列用现有 `t('action.'+row.action)`（仅 forward/direct）；帮助行 "拒绝记录请在系统日志/审计查看"；`truncated` 时显示 `{shown}/{total}` 截断提示；排序切换（开始时间/连接时长）；自动刷新开关 + 间隔选择（2s/5s/10s/关）默认**开 5s**；`onBeforeUnmount` 清 `clearInterval`（Dashboard.vue:255-257）；`start_ts` 渲染为格式化绝对时间，时长用 `duration_sec`（可前端 `now - start_ts` 实时重算）；`upstream==""` 渲染 "—"。
- [ ] **AC-20（api 模块）** `web/src/api/connections.js` 导出 `getActiveConnections({ limit, sort })` → `request.get('/connections', { params })`。
- [ ] **AC-21（i18n）** `menu.connections` + `connections.*` 块（列头、`truncatedHint`、`empty`、`sortByStart`/`sortByDuration`、`autoRefresh`、`interval`/`off`/间隔选项标签、`rejectHelp`）加入 zh.js **与** en.js，双语同键集。
- [ ] **AC-22（大 N）** 注册 >N 条活跃连接时，接口恰返回 N 条、精确 `total`、`truncated=true`；前端不卡。
- [ ] **AC-23（构建/测试/race）** 全部新代码含中文注释解释"为什么"；`make build` 通过；`go test ./... -race` 通过（含登记表并发、never-reject、tied-start 双模式排序、BenchmarkSnapshot 分配上限）；前端构建通过且补回 `api/dist/.gitkeep`（提交 b7e34f6）。

---

## 4. 实现步骤

### (a) 新包 `connreg/` — 登记表、connID、有界堆 Snapshot
建 `connreg/registry.go`（如有需要可 `connreg/heap.go`）。仅 stdlib；`action` 存 `string`（无 `rule` import → 零项目依赖 → 无环）。

```go
package connreg

// ConnMeta 是登记一条活跃连接时已知的元数据（连接进入即可填）。
// action 用 string（非 rule.Action）以保持 connreg 零项目依赖（中性叶子包）。
type ConnMeta struct {
    Target string    // 目标主机（server.targetHost(req)）
    Action string    // "forward"/"direct"（needsSniff 嗅探后由 SetAction 回填真值）
    User   string    // d.auth.User
    Group  string    // d.auth.Group
    Client string    // writer.(net.Conn).RemoteAddr().String()
    Start  time.Time // time.Now()
}

// activeConn 是内部存储单元；upstream/action 为可后填字段，
// 用 atomic.Pointer[string] 与 Snapshot 的并发读安全对齐（无需逐条 mutex）。
// 不可变字段（id/meta 的其余项）在 Store 入 map 前填好，发布后只读，无需同步。
type activeConn struct {
    id       int64
    meta     ConnMeta
    upstream atomic.Pointer[string]
    action   atomic.Pointer[string] // 后填，覆盖 meta.Action（嗅探路径）
}

// ConnView 是对外快照视图（喂给 api handler 序列化）。
type ConnView struct {
    ID, StartUnix, DurationSec int64
    Target, Action, Upstream, User, Group, Client string
}

// Registry 是活跃连接登记表：纯活跃 map（connID→activeConn），不保留历史。
// 注册/注销/回填 O(1)、不进中继热路径（一号硬约束）。
type Registry struct {
    seq    atomic.Int64 // 单调递增 connID（永不复用存活 ID；亦作 duration 排序稳定 tiebreaker）
    hint   atomic.Int64 // 非权威活跃数快速提示（权威 total 来自 Snapshot 扫描计数）
    active sync.Map     // map[int64]*activeConn —— connID 为不相交键，store/delete 近无锁
}

const DefaultLimit = 500 // 截断上限 N（可配置常量）

func New() *Registry
func (r *Registry) Register(m ConnMeta) int64        // seq.Add(1)→填好 entry→active.Store→hint.Add(1)
func (r *Registry) SetUpstream(id int64, up string)  // Load→upstream.Store(&up)
func (r *Registry) SetAction(id int64, a string)     // Load→action.Store(&a)
func (r *Registry) Deregister(id int64)              // Delete→hint.Add(-1)
func (r *Registry) Len() int                         // int(hint.Load())（非权威提示）
func (r *Registry) Snapshot(limit int, sortBy string) (items []ConnView, total int, truncated bool)
```

- **`Snapshot`（有界 top-K，AC-9/10）：**
  1. 钳 `limit` 到 `[1, DefaultLimit]`。
  2. 初始化容量 `limit` 的堆；**比较器方向按 `sortBy` 选**：
     - `start`：最小堆（堆顶 = 最小 start_ts，相等则最小 seq）→ 驱逐最旧，留 K 新。
     - `duration`：最大堆（堆顶 = 最大 start_ts，相等则最大 seq）→ 驱逐最新，留 K 旧 = 最长。
  3. 单趟 `Range`：每条 `total++`；构建 `ConnView`（`upstream.Load()`→nil 则 ""，`action.Load()` 否则 `meta.Action`，`DurationSec=int64(now.Sub(Start).Seconds())`，`StartUnix=Start.Unix()`）；堆未满则压入，否则若优于堆顶则 pop+push。
  4. 一趟后排空堆到切片，`sort.SliceStable` 排成展示序：`start`→start 降序后 seq 降序；`duration`→start 升序后 seq 升序。
  5. `truncated = total > limit`。
- O(N) 时间、O(K) 空间、无全 N 切片。中文注释说明堆（有界成本）与 seq tiebreaker（无抖动）。

### (b) `server/server.go` + `server/ctxkey.go` handler 装配
1. **`decision.connID`**（server/ctxkey.go:21-40）：加 `connID int64` 字段 + 中文注释（按值携带登记 id 给下游，零签名改动）。
2. **handler 字段**（server.go:155-161）：加 `conns *connreg.Registry`；import `deeproxy/connreg`。
3. **`New` 签名**（server.go:519-525）：加 `conns *connreg.Registry`；接入 `h := &handler{...}`（server.go:526-532）。
4. **进入处登记**（server.go:~180，紧随 `h.counter.ConnOpened()`）：
   ```go
   client := ""
   if c, ok := writer.(net.Conn); ok { client = c.RemoteAddr().String() } // AC-4：正确来源；非 req.RemoteAddr
   id := h.conns.Register(connreg.ConnMeta{
       Target: d.host, Action: string(d.action),
       User: d.auth.User, Group: d.auth.Group, Client: client, Start: time.Now(),
   })
   d.connID = id
   defer h.conns.Deregister(id) // 与 defer h.counter.ConnClosed() 同一 defer 区，panic 也保证计数对齐
   ```
   在分派到 `handleSniff`/`dialAndRelay` 前设好 `d.connID`（server.go:187-190，它们按值收 `d`）。
5. **抽取 `upstreamString` helper**（AC-5，DRY）：双分支；重构 `recordAudit`（server.go:437-443）调用之。
6. **回填 upstream**：`dialAndRelay`（server.go:245）与 `handleSniff`（server.go:388）成功后：`h.conns.SetUpstream(d.connID, upstreamString(d, usedUpstream))`。
7. **回填 action（仅 forward/direct 分支）**：`handleSniff` 中 `MatchRule` 解析动作后、**在 `if action == rule.ActionReject { return errSniffReject }` 守卫之后**（server.go ~386，`d.action = action` 处）：`h.conns.SetAction(d.connID, string(action))`。绝不在守卫前调用（保留项 2）。
8. **确认零触碰 `relay.go`**（AC-7）。

### (c) `cmd/deeproxy/main.go` 共享实例装配（关键集成点）
1. main.go:172-173：`connReg := connreg.New()`（import `deeproxy/connreg`）。
2. `srv := server.New(holder, registry, counter, auditBuf, connReg, logger)`（main.go:206）。
3. `app := api.NewApp(st, holder, cfg, counter, logBuf, auditBuf, healthChecker, registry, connReg, logger, levelVar, version)`（main.go:196）——第 12 位参数，与 server 同一 `connReg` 指针。
4. 两侧共享一指针：server 写、API 读。

### (d) API handler + 路由 + NewApp 签名
1. **`App` 字段**（api/server.go:48 附近）：加 `connReg *connreg.Registry`（与 `registry *pool.Registry` 区分）；import `deeproxy/connreg`（叶子，无环）。
2. **`NewApp`**（api/server.go:59-71）：加第 12 位 `connReg *connreg.Registry` + nil 兜底；接入 `&App{...}`。
3. **路由**（server.go:143 附近）：`auth.GET("/connections", a.handleListConnections)`。
4. **`api/connections_handler.go`**（仿 dashboard_handler.go 风格）：
   ```go
   type connItemResp struct {
       ID int64 `json:"id"`; Target string `json:"target"`; Action string `json:"action"`
       Upstream string `json:"upstream"`; User string `json:"user"`; Group string `json:"group"`
       Client string `json:"client"`; StartTs int64 `json:"start_ts"`; DurationSec int64 `json:"duration_sec"`
   }
   type connListResp struct {
       Items []connItemResp `json:"items"`; Total int `json:"total"`
       Limit int `json:"limit"`; Truncated bool `json:"truncated"`
   }
   func (a *App) handleListConnections(c *gin.Context) {
       limit := atoiDefault(c.Query("limit"), connreg.DefaultLimit)
       sort := c.DefaultQuery("sort", "start")
       views, total, truncated := a.connReg.Snapshot(limit, sort)
       // views→items 映射，respondOK(c, connListResp{Items, Total: total, Limit: limit, Truncated: truncated})
   }
   ```

### (e) 前端：视图 + 路由 + api 模块 + i18n
1. **`web/src/api/connections.js`** — `getActiveConnections({ limit, sort })` → `request.get('/connections', { params })`。
2. **`web/src/router/index.js`** — dashboard 后加子路由（router/index.js:33）：`{ path:'connections', name:'connections', component:()=>import('@/views/connections/RealtimeConnections.vue'), meta:{ title:'menu.connections', icon:'Histogram' } }`（用现有 Element Plus 图标串）。
3. **`web/src/views/connections/RealtimeConnections.vue`**（骨架）：
   ```
   <script setup>
   import { ref, onMounted, onBeforeUnmount, watch } from 'vue'
   import { useI18n } from 'vue-i18n'
   import { getActiveConnections } from '@/api/connections'
   const { t } = useI18n()
   const rows = ref([]), total = ref(0), truncated = ref(false)
   const sortBy = ref('start')                 // 'start' | 'duration'
   const autoRefresh = ref(true), intervalSec = ref(5)
   let timer = null
   async function load() {
     const r = await getActiveConnections({ limit: 500, sort: sortBy.value })
     rows.value = r.items; total.value = r.total; truncated.value = r.truncated
   }
   function restartTimer() {
     if (timer) clearInterval(timer)
     if (autoRefresh.value && intervalSec.value > 0)
       timer = setInterval(load, intervalSec.value * 1000)
   }
   onMounted(() => { load(); restartTimer() })
   onBeforeUnmount(() => { if (timer) clearInterval(timer) })
   watch(sortBy, load)
   watch([autoRefresh, intervalSec], restartTimer)
   </script>
   <template>
     <!-- 工具条：el-radio-group(sortBy) + el-switch(autoRefresh) + el-select(2/5/10/关)
          + 截断提示 t('connections.truncatedHint',{shown:rows.length,total}) when truncated
          + 帮助行 t('connections.rejectHelp') -->
     <!-- el-table :data="rows" 7 列：
          action → t('action.'+row.action)（仅 forward/direct）
          upstream → row.upstream || '—'
          duration → row.duration_sec（或前端 now-start_ts 实时计）
          start_ts → 格式化 row.start_ts(unix秒) -->
   </template>
   ```
   - 复用现有 `action.*` 标签（DRY）。`onBeforeUnmount` 清定时器（Dashboard 模式）。
4. **i18n**（zh.js & en.js，同键集）：`menu.connections`；`connections: { colTarget, colAction, colDuration, colUpstream, colUserGroup, colClient, colStartTs, truncatedHint, empty, sortByStart, sortByDuration, autoRefresh, interval, off, rejectHelp, ... }`。`truncatedHint` 用 `{shown}`/`{total}` 插值。
5. **补回 `api/dist/.gitkeep`**（提交 b7e34f6 先例）。

---

## 5. 风险与缓解

| # | 风险 | 缓解 |
|---|------|-----------|
| R1 | 热路径回归（拷贝循环加代码）。 | 所有登记调用在 `connectHandle`/`dialAndRelay`/`handleSniff` 内、`relayCounted` **外**。AC-7 `git diff server/relay.go` 门 + 评审确认。 |
| R2 | connID 冲突/复用。 | `atomic.Int64.Add(1)` 单调 int64；现实寿命不溢出；仅关连接时删。 |
| R3 | map 增长 / 泄漏条目 → total 漂移、内存。 | 单个 `defer Deregister` 与 `defer ConnClosed` 同 defer 区，覆盖全部 return（成功/失败/嗅探拒绝）**及 panic**（recoverPool 在 connectHandle 之上恢复，其 defer 先跑——lifecycle.go:43-48）。map 受真实并发数界定，非历史。测试断言扫描 total 追踪 `counter.ActiveConns()`。 |
| R4 | 与 idleConn 的 goroutine 泄漏交互。 | 与已受信的 `counter.ActiveConns` 同寿命；由 `WrapIdle`（server.go:303,337）+ 嗅探泄漏修复（server.go:479-506）界定。无新泄漏面。 |
| R5 | 嗅探路径动作显示错/空。 | **必须** `SetAction` 于嗅探解析后、reject 守卫之后（server.go:386，AC-6）。拨号/嗅探瞬间占位记为有意。 |
| R6 | 客户端地址来源错。 | AC-4：cast `writer.(net.Conn)`（server.go:482 已证），回退 ""。**禁用 `req.RemoteAddr`。** SetTrustedProxies（api HTTP）与此无关——已剔除。 |
| R7 | Snapshot O(N) / 后端"卡死"。 | AC-9 有界堆 top-K：O(N) 时间、O(K) 空间、无全 N 切片。`BenchmarkSnapshot` N=50k + `testing.AllocsPerOp` 上限证 O(K)。 |
| R8 | start_ts 相等 → 行抖动。 | AC-10 seq tiebreaker（堆比较器 + `sort.SliceStable` 双处）；双模式 tied-start 确定序测试。 |
| R9 | `NewApp`/`server.New` 签名波及测试。 | nil 兜底先例（server.go:83-86）。grep 调用方：`cmd/deeproxy/main.go` + `api/*_test.go` + `server/*_test.go`；逐一更新；不测连接的传 `nil`。AC-14/16。 |
| R10 | import 环。 | connreg 仅 stdlib（action 用 string）→ 无项目依赖；`server`→connreg、`api`→connreg 均无环。先例 `syslog.AuditBuffer`。 |
| R11 | i18n 键漂移（历史）。 | AC-21 + **自动** zh/en 键对齐测试（M4a）。 |
| R12 | 两个分歧活跃计数。 | AC-12：登记表扫描 total 权威；同生命周期点追踪 `stats.ActiveConns()`；无第三计数器。`hint` 明确非权威。 |
| R13 | 登记表关停含糊。 | AC-17：纯内存单例，无 goroutine、无 Shutdown；经各连接 `defer Deregister` 随 SOCKS 服务关闭排空。 |

---

## 6. 验证步骤

1. **后端构建 & race：** `make build`；`go test ./... -race`。新测试：
   - `connreg/registry_test.go`：(a) 并发——N goroutine Register/SetUpstream/SetAction/Deregister 于 `-race`，终态 `Len()==0`；(b) 截断/序——注册 M>limit，`Snapshot(limit,"start")` 恰返 limit 条最新优先、`total==M`、`truncated`；`sort=duration` 最长优先；(c) **tied-start 确定性——双模式**（start 与 duration 均测）相等 Start 下断言稳定 seq 序、跨多次 Snapshot 不变；(d) **`BenchmarkSnapshot` N=50k** + `testing.AllocsPerOp` 上限证 O(K)；(e) **边界 N=limit+1**（如 501/limit=500）断言 `truncated=true` 且被驱逐的恰是各模式应淘汰者。
   - `server` 测试（M4b）：**reject 决策从不登记**——分别驱动 (i) Allow 阶段 reject 与 (ii) 嗅探阶段 reject（errSniffReject），断言活跃快照中均无该连接、且**绝无 action="reject" 条目**（保留项 2 全覆盖）。
   - `server`/集成：批量开/关后，登记表扫描 total **静止时 == `counter.ActiveConns()`**（差异仅在开/关瞬间，AC-12）。
   - `api/connections_handler_test.go`（httptest）：注入登记表，`GET /api/connections?limit=2&sort=duration`，断言 `{items,total,limit,truncated}` 形态 + 序。
2. **前端：** `cd web && npm run build` 通过；**i18n 键对齐测试**（M4a——小 vitest 或构建期 zh/en 键 diff 脚本）通过；补回 `api/dist/.gitkeep`。
3. **手动大 N 截断：** 跑二进制，驱动 >500 条保持的 SOCKS 连接，开 实时连接 → 500 行、提示"显示 500 / 共 N 条"、响应顺畅；排序切换 + 间隔选择 + 暂停可用；连接关闭后行消失；帮助行可见；被拒目标从不出现。
4. **splice 路径确认（代码评审/git-diff 门，AC-7）：** `git diff server/relay.go` 无改动；确认全部登记调用在 `relayCounted` 外。可选 Linux `strace -e trace=splice`。
5. **交叉核对 total：** API `total` vs Dashboard `activeConns`（`counter.ActiveConns()`）负载下匹配（仅采样抖动）。

---

## 7. ADR — 实时连接模块

**状态：** Accepted（RALPLAN-DR 共识 short，Planner→Architect[SOUND-WITH-CHANGES]→Critic[ACCEPT]，2 轮）。

**决策。**
1. 登记表落在新**中性叶子包 `connreg/`**（`package connreg`），非 `server`、非 `stats`；`action` 存 `string` 使 connreg 仅依赖 stdlib。
2. **A1 登记后回填**，`connID` 经新 `decision.connID` 字段携带——进入处登记，拨号后回填 upstream，嗅探后（reject 守卫之后）回填 action。
3. **有界 top-K 堆 Snapshot**（O(N)/O(K)），权威 `total` 来自扫描计数，`seq` tiebreaker 确定序；堆方向按排序模式参数化。
4. **`action` 列仅 forward/direct**；被拒绝连接结构上不在场（Allow 阶段关闭）。UI 显示"拒绝记录请在系统日志/审计查看"。
5. **单一活跃计数源**：登记表扫描 total，追踪 `stats.ActiveConns()`；无第三计数器。

**驱动。** 热路径零开销（`splice` 保留）；用户自述"数量过大卡死"应用于后端轮询；panic 安全单一真相源；避免 `api → server` 环；i18n 对齐。

**考虑过的替代。**
- 登记表在 `server`（否决：逼出 `api → server` 环）或 `stats`（否决：污染计数包）。选 `connreg`，循 `syslog.AuditBuffer` 先例。
- A2 拨号后登记（否决：隐藏拨号/故障转移连接——最具诊断价值者；调用点更易泄漏）。
- 朴素 Range→全排序→截断（否决：O(N) 分配 + O(N log N) 每轮 = 把"卡死"搬到服务端）。
- 独立 `atomic` 作权威 total（否决：panic 下漂移；扫描计数精确）。
- 可显 reject 的动作列（否决，已由用户确认 C1：reject 从不进入活跃列表）。

**后果。**
- (+) `splice` 快路径不动；大 N 下有界内存/CPU；无抖动序；分层清晰；i18n/never-reject/bench 自动守护。
- (−) 每连接至多 3 次 O(1) 写；拨号/嗅探瞬间有意的 "—"/pending 窗口；`NewApp` 现 12 位参数（DI 异味，已延后）。

**跟进。**
1. `AppDeps` 结构体重构替代 12 位参数 `NewApp`（独立任务，本功能不做）。
2. 未来可选"被拒绝连接"事件视图（源自 syslog/audit，补活跃列表结构上看不到的部分）。

---

## 变更日志（共识迭代）
- **迭代 1（Architect SOUND-WITH-CHANGES）**：登记表从 `server` 迁至中性叶子包 `connreg/`（消除 `api→server` 环，循 AuditBuffer 先例）；total 改由扫描计数（消除 panic 下 atomic 漂移）；R5 `SetAction` 升为必须；明确关停语义；connID 经 `decision.connID` 携带（零签名改动）。
- **迭代 2（Critic ACCEPT-WITH-RESERVATIONS → 已折入）**：C1 reject 列由用户裁定为 forward/direct-only + UI 帮助行 + never-reject 测试；C2 客户端地址 `writer.(net.Conn)` 捕获（禁用 req.RemoteAddr，剔除 SetTrustedProxies 红鲱鱼）；C3 有界 top-K 堆 + BenchmarkSnapshot；M1 单一计数源；M2 seq tiebreaker；M3 `connReg` 命名防冲突；M4 自动化 i18n 对齐/never-reject/tied-start/bench 测试。
- **保留项折入**：(1) 堆方向按排序模式参数化（start 最小堆 / duration 最大堆，seq 入比较器），tied-start 测试双模式；(2) `SetAction` 仅在 forward/direct 分支、reject 守卫之后调用，never-reject 测试覆盖 Allow + 嗅探两条拒绝路径；AC-12 容差收紧为"静止时相等"；新增 N=limit+1 边界测试。
