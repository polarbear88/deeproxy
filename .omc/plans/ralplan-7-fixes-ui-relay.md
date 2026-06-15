# 工作计划：deeproxy 7 项独立修复（UI + 中继）

> **状态：pending approval**（评审 2 轮共识达成 — Architect: SOUND / Critic: APPROVE；待用户最终批准后进入实施）
> 来源规格（权威）：`.omc/specs/deep-interview-7-fixes-ui-relay.md`（深度访谈 6 轮，最终模糊度 6.25%，PASSED）。
> 本计划由 planner 在 explore 实测各文件后落地，**仅规划，不改源码**。
> 全局约束：不建分支、直接提交 main；全部中文注释解释「为什么」；优先成熟库；i18n zh/en 键对等；
> 热路径（io.Copy / splice 零拷贝）不可侵犯（#3/#5）。

---

## 一、需求摘要（7 项，互相独立）

| # | 项目 | 端 | 一句话 |
|---|------|----|--------|
| 1 | 规则列表字体 | 前端 | 规则管理列表表格字号 14px → 16px，仅作用于规则列表 |
| 2 | 仪表盘动作分布刷新 | 前端 | 切回仪表盘（keep-alive 激活）时重新拉取动作分布图 |
| 3 | 实时连接显示域名 | 后端 | 嗅探还原的域名回填注册表，目标主机列显示域名而非 IP |
| 4 | 系统日志虚拟滚动 | 前端 | 大量日志只渲染可视区域，消除 5000 DOM 节点卡顿 |
| 5 | 客户端断电检测 | 后端 | 客户端/上游 TCP 连接启用 keepalive，死连接被探测后清理上游 |
| 6 | 复制地址新格式 | 前端 | 用户管理新增可选复制格式 `addr:port:user-group:pwd`，原 socks5:// 保留 |
| 7 | 连接审计分页+查询 | 后端+前端 | 审计接口服务端分页 + user/target/action/group 四维筛选 |

---

## 二、RALPLAN-DR 决策记录（SHORT 模式）

### Principles（原则）
1. **最小侵入、单一职责**：每项改动只触及其根因点，不顺手重构无关代码（7 项独立，互不耦合）。
2. **热路径神圣不可侵犯**：#3/#5 的写入只在「socket 建立 / 嗅探解析」一次性发生，绝不进入 `io.Copy` / splice 零拷贝循环。
3. **沿用既有模式 + 单一事实源（DRY）**：回填走 `atomic.Pointer` 既有套路（#3 仿 `SetAction`）；keepalive 走 stdlib `ListenConfig`/`net.Dialer`，且 30/15/3 参数集中为 `dialer` 包导出的单一 `KeepAliveConfig`，监听侧与上游 dialer 共用（#5，杜绝周期漂移、无 import cycle）。
4. **成熟库优先**：能用成熟、活跃维护的库就不自造（spec Constraint + 全局规范 §1 硬约束）。#4 用 `vue-virtual-scroller` 的 `DynamicScroller`（原生可变行高），不自研窗口化；新增单一依赖是满足该原则的必要代价。
5. **国际化对等**：所有新增可见文案同时落 `zh.js` 与 `en.js`，保持键集一致。

### Decision Drivers（决策驱动，Top 3）
1. **正确性与并发安全**：#3 `ConnMeta.Target` 当前是普通字段，被 `Snapshot` 无锁并发读（`activeConn.meta` 文档声明为「Store 后不可变」）；若改为可写普通字段则触发 data race → 必须用 atomic。
2. **可达性（reachability）**：#5 上游可能是 `x/net/proxy` 包装的 SOCKS5 连接，底层 `*net.TCPConn` 不可直接断言取到 → 需要从「构造拨号器」层面注入 keepalive，而非事后断言。
3. **成熟库优先 + 行高不定的现实**：#4 日志行**非定高**——`SysLog.vue:229-230` 的 `.log-msg{white-space:pre-wrap;word-break:break-all}` 与 `.log-fields`（无界 `JSON.stringify`，含 `\n`）产生可变行高 → 必须用支持动态行高的成熟库，自研定高窗口化会抖动。这与 Principle 4 / spec Constraint / 全局规范 §1「优先成熟库」一致。

### Viable Options（对真正有争议的决策给 ≥2 方案；琐碎项给单方案 + 反证）

#### 决策 A（#5）：客户端连接 keepalive 的接线方式与探测时窗
- **关键：用 `net.KeepAliveConfig` 而非裸 `KeepAlive` duration**。裸 `KeepAlive:30s` 只设 Idle=30s，Interval/Count 走 Go 默认（Interval=15s、Count=9）→ 探测耗时 30+15×9 = **165s**，超出 AC「约 30–90s」。必须显式设 `KeepAliveConfig{Enable:true, Idle:30*time.Second, Interval:15*time.Second, Count:3}` → 30+15×3 = **75s**，落在窗口内。go.mod 为 `go 1.26.4`，`KeepAliveConfig` 可用。
- **Option A1 — `net.ListenConfig{KeepAliveConfig: {...}}` 替换 `net.Listen`（推荐）**
  - 文件：`server/lifecycle.go:84` `Listen()` 内 `net.Listen("tcp", addr)` → 改 `(&net.ListenConfig{KeepAlive: -1, KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 30*time.Second, Interval: 15*time.Second, Count: 3}}).Listen(ctx, "tcp", addr)`。
  - 注意：`KeepAliveConfig` 优先级高于裸 `KeepAlive`；显式设 `KeepAlive: -1`（或仅靠 `Config.Enable=true`）以免歧义。
  - 优点：stdlib 自动对每个 accept 的 TCP 连接开 keepalive，无需逐连接类型断言；改动 1 处；不触碰 `deadlineListener.Accept` 热前路径。
  - 缺点：需要一个 `context`（用 `context.Background()`，装配期，监听器生命周期=进程级，无需取消语义）。
- **Option A2 — 在 `deadlineListener.Accept()` 内 `c.(*net.TCPConn).SetKeepAliveConfig(cfg)`**
  - 优点：与现有「Accept 时设握手 deadline」对称，集中在一处。
  - 缺点：每次 Accept 多一次类型断言（虽非字节热路径，但属连接建立路径）；断言失败需兜底；比 A1 啰嗦。
- **选定：A1**（更简洁、更 stdlib-idiomatic、改动面更小）。A2 作为回退。

#### 决策 B（#5）：上游连接 keepalive 的接线方式
> **定位更正（B4）**：本次泄漏的根因在**客户端侧**——`io.Copy(target, clientR)` 在客户端断电（无 FIN/RST）时永久阻塞；**客户端 keepalive（决策 A）才是真正的修复**。上游 keepalive **不是修复主体**，它与既有 `dialer.idleConn` 的 300s 读超时（`dialer/idleconn.go:23-24`，在 `server/server.go:325`(direct)/`:359`(forward) 经 `WrapIdle` 应用）**大体冗余**——**除了** `idle_timeout_sec=0` 这一情形：`WrapIdle` 在 `idle<=0` 时返回**裸 conn**（`idleconn.go:24`），无任何读超时。故上游 keepalive 的价值仅为：(a) 覆盖 `idle=0` 配置下上游死连接无人探测的空档；(b) 提供 <300s 的更快上游探测。dialer 接线本身正确，保留。
- **Option B1 — 给两个 dialer 注入 KeepAliveConfig（推荐）**
  - **DRY**：30/15/3 这组参数与客户端监听侧共用，统一为 `dialer` 包导出的单一 `KeepAliveConfig` var（`server` 已 import `dialer`、无 import cycle），监听侧与两个 dialer 均引用之，杜绝周期漂移。
  - `dialer/dialer.go:26` `DialDirect`：`net.Dialer{Timeout: dialTimeout}` → 加 `KeepAlive: -1, KeepAliveConfig: net.KeepAliveConfig{Enable:true, Idle:30*time.Second, Interval:15*time.Second, Count:3}`。
  - `dialer/dialer.go:43` `DialUpstream`：把 `proxy.SOCKS5(..., proxy.Direct)` 的 `forward` 参数从 `proxy.Direct` 换成 `&net.Dialer{Timeout: dialTimeout, KeepAlive: -1, KeepAliveConfig: net.KeepAliveConfig{Enable:true, Idle:30*time.Second, Interval:15*time.Second, Count:3}}`（已验证 `*net.Dialer` 满足 `proxy.Dialer`/`ContextDialer`，由它建立「本机→上游 SOCKS5 代理」的 TCP 连接并带 keepalive）。
  - 优点：从构造层注入，无需对返回的包装 conn 做不可靠断言；direct 与 forward 两条上游路径都覆盖；纯 stdlib + 既有库能力；探测时窗与客户端一致（75s）。
  - 缺点：探测的是「本机↔上游代理」这一跳（符合职责边界，非「上游代理↔最终目标」）。
- **Option B2 — 拨号后对返回 `net.Conn` 断言 `*net.TCPConn` 再 `SetKeepAliveConfig`**
  - 缺点：`DialUpstream` 返回 `x/net/proxy` 的 SOCKS5 包装 conn，**断言 `*net.TCPConn` 必失败** → 上游侧 keepalive 形同虚设。**否决**。
- **选定：B1**。它正面覆盖 `idle=0` 空档并提供 <300s 探测，无需降级。

#### 决策 C（#4）：虚拟滚动库选型
> **前提订正（B2）**：日志行**非定高**——`SysLog.vue:229-230` `.log-msg{white-space:pre-wrap;word-break:break-all}` + `.log-fields`（无界 `JSON.stringify`）→ `\n` 与超长 fields 产生可变行高。故「定高窗口化」前提为假，**自研定高方案被排除**（会抖动）。spec Constraint、Principle 4、全局规范 §1 均要求「优先成熟库」。
- **Option C1 — `vue-virtual-scroller` 的 `DynamicScroller`（推荐 / PRIMARY）**：原生支持**可变行高**（按内容自动测量缓存），契合非定高日志行；成熟库（社区活跃、文档完善）。需新增依赖。
  - 优点：直接解决可变行高；只渲染可视区域；API 友好（`DynamicScroller` + `DynamicScrollerItem` 包住现有 `.log-line` 终端 DOM，样式保留）；满足「成熟库优先」硬约束。
  - 缺点：新增一个依赖（`web/package.json` + `pnpm-lock.yaml`）。
- **Option C2 — Element Plus `el-table-v2`（虚拟化表格）**：项目已装 EP，但日志区是「终端风格 div 行」非表格，套 table 破坏终端样式与自由布局，且 table-v2 对**完全自由的多行可变高**支持不如 DynamicScroller 自然。**否决**。
- **Option C3 — 自研定高窗口化**：前提（定高）为假，可变行高下抖动；且违反「成熟库优先」。**否决（降为非目标）**。
- **选定：C1（`vue-virtual-scroller` `DynamicScroller`）作为唯一主选**。新增依赖是必要代价：成熟库优先是硬约束，且可变行高排除了自研。
  - **反证 C2/C3**：C2 破坏终端 div 语义且自由多行支持弱；C3 前提为假 + 违反库优先原则。

#### 决策 D（#3）：Target 回填的并发安全方式（单方案 + 反证）
- **选定：`activeConn` 新增 `target atomic.Pointer[string]`，`SetTarget` Store，`toView` 读取覆盖 `meta.Target`**（完全仿 `SetAction`/`SetUpstream`，registry.go:47-48/99-103/166-185）。
- **反证「在持锁下写 `meta.Target`」**：`Registry` 用 `sync.Map` 无逐条 mutex，且 `meta` 文档明确「Store 后不可变、无同步读」；引入锁会破坏既有「O(1) 无锁回填」一号硬约束，并与 `upstream/action` 的 atomic 模式不一致。故否决，采用 atomic。

#### 决策 E（#7）：审计快照展示顺序（单方案 + 反证）
- 实测 `syslog/buffer.go:80 snapshot()` 返回**最旧→最新**（环形按时间序）。
- **选定：handler 内过滤后 reverse 成「最新→最旧」再分页切片**（spec 推荐 newest-first 稳定展示）。
- **反证「前端 reverse」**：分页是服务端的，前端只拿到一页，无法对全集 reverse；必须在服务端过滤+排序+切片后返回。故在 handler 完成。

#### 决策 F（#1/#2/#6）：琐碎项单方案
- **#1**：给规则**组列表表**显式加 class（如 `.rule-group-table`），scoped 样式写 `body .rule-group-table :deep(.el-table__cell)` 设 `font-size:16px`（cell 级以压过 EP 特异度）。为什么提特异度：EP 样式由运行期按需注入 chunk 提供，特异度不足会被覆盖（项目记忆 `deeproxy-ep-ondemand-css-specificity`）。为什么必须用显式 class 而非 `.dp-page :deep(.el-table)`：抽屉明细表（`Rules.vue:321`）位于 `<el-drawer>`（L310）内，**el-drawer 会 teleport 到 body**，scoped 的 `.dp-page` 选择器**根本够不到它**——故只能正向选中规则组表，而非「收窄以避开明细表」。反证「直接 `.el-table{font-size:16px}`」：被 EP chunk 覆盖。
- **#2**：`MainLayout.vue:162` 已用 `<keep-alive :max="6">` → **`onActivated()` 调 `loadActionDist()`**。反证「路由守卫」：keep-alive 下组件不重新 mount，`onMounted` 不再触发，路由守卫不必要且更绕；`onActivated` 是 keep-alive 标准激活钩子。
- **#6**：在现有 `el-dropdown` 动作菜单（`Users.vue:218-232`）新增第二个复制项（command `copy2`），`onAction`（L185）分发到 `copyProxyAddr2(row)`，复用现有 clipboard 逻辑。反证「替换原格式」：访谈明确「新增可选择」，原 socks5:// 必须保留。

---

## 三、验收标准（可测试，对齐 spec AC）

- [ ] **#1**：规则管理列表（`Rules.vue` 规则组表）字体 16px；其他页面表格仍 14px（对比 `web/src/styles/index.scss:68` 全局值）。
- [ ] **#2**：从其他页面切回仪表盘（keep-alive 激活）时，动作分布图自动重新拉取刷新；时间窗 watcher / 首次 onMounted 行为不回归。
- [ ] **#3**：直连（域名规则命中、走嗅探路径）的实时连接，目标主机列显示嗅探到的域名（覆盖原 IP）；`connreg` 新增 `SetTarget`，`handleSniff` 嗅探成功后回填；`go build ./...` 通过。
- [ ] **#3 不变量**：`target atomic.Pointer[string]` 在 `SetTarget` 调用前**恒为 nil**（`Register` 不初始化、**绝不**初始化为 `&""`）；`toView` 在指针为 nil 时回退 `ac.meta.Target`（即嗅探前实时列表显示 `Register` 时的原始 IP/host，嗅探后才被域名覆盖）。
- [ ] **#3 竞争**：**新增** `go test -race ./connreg/...` 用例，并发跑 `SetTarget` 与 `Snapshot`，race detector 无告警（当前无此测试，须新增）。
- [ ] **#4**：≥5000 行日志滚动流畅，DOM 仅渲染可视区域行（DevTools 可见节点数远小于 5000）；**用 `vue-virtual-scroller` `DynamicScroller` 正确处理可变行高**（含 `\n` 与超长 fields 不抖动）；SSE 实时追加、贴底滚动、level 过滤、终端样式不变；保留 `MAX_RENDER` 兜底。
- [ ] **#5**：客户端关机后，在 **Idle 30s + Interval 15s × Count 3 = 75s（≤90s）** 内该连接从实时列表消失、上游连接被终止；正常活跃/空闲存活隧道不被误杀；keepalive 用 `net.KeepAliveConfig`（非裸 `KeepAlive` duration，后者 165s 超窗）。
- [ ] **#5 跨平台行为基准（权威 AC）**：上条 75s 行为测试是**跨平台**达标线（Windows/macOS/Linux 三平台均以「死连接 ≤90s 清理」为准）。`KeepAliveConfig` 由 Go runtime 在各平台映射到对应 socket 选项，行为一致。
- [ ] **#5 socket 机检（仅 linux，辅助证据，非跨平台 AC）**：**linux-only**——用 `SyscallConn` + `GetsockoptInt` 读 `TCP_KEEPIDLE/TCP_KEEPINTVL/TCP_KEEPCNT` 断言为 30/15/3。**Darwin 的 sockopt 名不同**（无 `TCP_KEEPIDLE`，对应 `TCP_KEEPALIVE` 等，语义/单位有别），故该机检**用 `//go:build linux` 门控**，仅作 linux 下的额外证据；macOS/Windows 不跑此检查、改以 75s 行为测试为准。
- [ ] **#5 零拷贝守门**：`server/relay.go relayCounted` 双向拷贝逻辑 `git diff` 为空（splice/零拷贝无改）。
- [ ] **#6**：用户管理可选择复制 `addr:port:user-group:pwd`（如 `192.168.1.1:1080:alice-prod:pass`），原 socks5:// 仍可复制；缺字段沿用 `<server-addr>`/`<socks5-port>`/`<pwd>` 占位策略。
- [ ] **#7**：`GET /api/syslog/audit` 支持 `page`/`pageSize` + `user`/`target`/`action`/`group` 筛选，返回 `{items,total,page,pageSize}`；最新→最旧稳定排序；前端审计 tab 有 `el-pagination` + 4 个筛选输入，大量记录不卡。
- [ ] **#7 分页边界（防越界 panic）**：`page` 下限 1；`pageSize` 默认 50、上限钳到 200；`start = min((page-1)*pageSize, total)`；`end = min(start+pageSize, total)`；`items = filtered[start:end]`（`start≥total` 时返回**空切片不 panic**）；`total` 为筛选后真实条数。给定越界 page（如 page=99999）返回空 items + 正确 total，不报错。
- [ ] **i18n（强制步骤）**：#6/#7 新增键后，`zh.js` 与 `en.js` 的**键集 diff 必须为空**（逐键路径比对，无单边缺失）。
- [ ] **构建**：`go build ./...`、`go vet ./...`、前端 `pnpm build` 均通过。

---

## 四、实施步骤（按「保持 go build ./... 每步可绿」分组排序）

> 排序原则：先做**纯新增、无调用方依赖**的后端叶子改动（#3a、#5 dialer、#7 backend），它们各自独立可编译；再接前端。后端三项彼此独立，但建议 #3 先于 #5（都在 server 包，#3 改动更小、回归面更窄）。

### 阶段 1 — 后端 #3：实时连接域名回填

1. **`connreg/registry.go`**
   - `activeConn` 结构（L44-49）新增字段：`target atomic.Pointer[string] // 后填：嗅探还原的域名，覆盖 meta.Target（仿 upstream/action 的 atomic 回填，保证与 Snapshot 并发读安全）`。
   - 新增方法（紧随 `SetAction`，L99-103 之后）：
     ```go
     // SetTarget 回填嗅探还原出的真实域名（IP 目标经首包嗅探得到域名后调用）。O(1)。
     // 为什么用 atomic 而非改 meta.Target：meta 在 Store 后被 Snapshot 无锁并发读，
     // 直接写普通字段会触发 data race；故与 upstream/action 一致用 atomic.Pointer。
     func (r *Registry) SetTarget(id int64, target string) {
         if v, ok := r.active.Load(id); ok {
             v.(*activeConn).target.Store(&target)
         }
     }
     ```
   - `toView`（L166-185）：在 `Target: ac.meta.Target` 之前加覆盖逻辑：
     ```go
     target := ac.meta.Target
     if p := ac.target.Load(); p != nil {
         target = *p // 嗅探回填的域名覆盖登记时的原始 IP
     }
     ```
     并把 `Target: ac.meta.Target` 改为 `Target: target`。
2. **`server/server.go handleSniff()`**（L374-432）
   - 在嗅探成功分支（L398-401，`if host, ok := detect.Sniff(first); ok {` 内、`routeHost = host` 旁）新增一行：
     `h.conns.SetTarget(d.connID, host) // 嗅探还原域名后回填注册表，使实时连接目标列显示域名而非 IP（非热路径，仅一次）`
   - 不动拨号目标（仍用原始 `target`），不动 io.Copy。
3. **校验**：`go build ./...`、`go vet ./...`；**新增** `connreg/registry_test.go`（或现有测试文件）中一条 `-race` 用例：并发 `SetTarget(id, ...)` 与 `Snapshot(...)`，确认无数据竞争（当前无此测试）。`go test -race ./connreg/...`。**不变量**：`target` 指针在 `SetTarget` 前恒 nil（`Register` 不触碰它，绝不初始化 `&""`），`toView` nil 时回退 `ac.meta.Target`。前端 `RealtimeConnections.vue` **不改**（`prop="target"` 自动渲染回填值）。

### 阶段 2 — 后端 #5：TCP Keepalive 检活

4. **共享 keepalive 配置（DRY，先做）**：30/15/3 这组参数被 `dialer` 与 `server/lifecycle.go` 两处使用，**禁止各写一份**。在 **`dialer` 包**导出单一来源（如 `var KeepAliveConfig = net.KeepAliveConfig{Enable:true, Idle:30*time.Second, Interval:15*time.Second, Count:3}`），两处都引用它。
   - **为什么放 `dialer` 包（无 import cycle）**：实测 `server` 已 import `deeproxy/dialer`（`server/server.go:42`），而 `dialer` 仅 import `deeproxy/auth` + stdlib、**不 import `server`**；故 `server/lifecycle.go` 引用 `dialer.KeepAliveConfig` 不会成环。无需新建包（避免无谓的叶子包）。
   - 注释说明「为什么集中」：keepalive 时窗是「检活语义」的单一事实源，分散会导致客户端/上游探测周期漂移。
5. **`server/lifecycle.go Listen()`**（L82-89）
   - 把 `net.Listen("tcp", addr)` 改为用 `net.ListenConfig` 并显式设 `KeepAliveConfig`（决策 A1），复用步骤 4 的共享配置：
     ```go
     lc := net.ListenConfig{
         KeepAlive:       -1,                     // 显式禁用裸 duration 路径，完全交由 KeepAliveConfig 决定
         KeepAliveConfig: dialer.KeepAliveConfig, // 单一事实源（步骤 4），与上游 dialer 周期一致
     }
     l, err := lc.Listen(context.Background(), network, addr)
     ```
   - 新增 import `context` 与 `deeproxy/dialer`。注释说明「为什么」：客户端断电不发 FIN/RST，relay 期无读超时；OS keepalive 探测死连接 → io.Copy 返错 → closeBoth + Deregister 清理上游与注册表。**为什么用 KeepAliveConfig 而非裸 `KeepAlive:30s`**：后者 Idle=30s 但 Interval/Count 走默认（15s×9）=165s，超 AC 窗；显式 30/15/3=75s。周期硬编码（非目标：不做完整可配置）。
   - **不动** `deadlineListener`、`clearHandshakeDeadline`、握手 deadline 逻辑。
6. **`dialer/dialer.go`**（决策 B1；定位见决策 B 的 B4 更正：上游 keepalive 仅补 `idle=0` 空档 + <300s 更快探测，非泄漏主修复）
   - `DialDirect`（L25-27）：`net.Dialer{Timeout: dialTimeout}` → `net.Dialer{Timeout: dialTimeout, KeepAlive: -1, KeepAliveConfig: KeepAliveConfig}`（复用步骤 4 的共享 var）。
   - `DialUpstream`（L35-54）：`proxy.SOCKS5("tcp", up.Addr(), pAuth, proxy.Direct)` 末参 `proxy.Direct` → `&net.Dialer{Timeout: dialTimeout, KeepAlive: -1, KeepAliveConfig: KeepAliveConfig}`（已验证 `*net.Dialer` 满足 `proxy.Dialer`/`ContextDialer`；它建立「本机→上游 SOCKS5 代理」TCP 连接并带 keepalive，解决 spec 预警的上游不可达）。注释说明「为什么换 forward dialer」。
7. **校验**：`go build ./...`、`go vet ./...`；`git diff server/relay.go` 必须为空（零拷贝未动）。

### 阶段 3 — 后端 #7：审计分页 + 四维筛选

7. **`api/syslog_handler.go handleAuditSnapshot()`**（L96-98）
   - 读取 query：`page`（默认 1，**下限钳到 1**）、`pageSize`（默认 50，**上限钳到 200**，`<=0` 归默认）、`user`/`target`/`action`/`group`（空=不筛）。
   - 流程：取 `a.audit.Snapshot()`（最旧→最新）→ 按四维筛选（`target` 子串包含；`user`/`group` 子串包含或相等；`action` 精确匹配 forward/direct/reject）→ **reverse 成最新→最旧**（决策 E）→ **安全切片**：
     ```
     total := len(filtered)
     start := min((page-1)*pageSize, total)   // start 永不越界
     end   := min(start+pageSize, total)
     items := filtered[start:end]              // start>=total 时为空切片，绝不 panic
     ```
     → 返回 `{items, total, page, pageSize}`（`total` 为筛选后真实条数）。
   - 抽出小工具（DRY）：query 解析+钳制、筛选谓词、安全分页切片，置于本 handler 文件或 `api/` 内合适处；中文注释。
   - **不动** ring buffer 存储（非目标：不改数据库）。
8. **校验**：`go build ./...`、`go vet ./...`；手测 `curl '/api/syslog/audit?page=1&pageSize=10&action=forward'` 返回分页结构。

### 阶段 4 — 前端 #1：规则列表字体

9. **`web/src/views/rule/Rules.vue`**
   - 给规则**组列表表**（L256 的 `<el-table>`）加 class：`class="rule-group-table"`。
   - 在 `<style scoped>` 增加（cell 级提特异度）：
     ```scss
     // 规则管理列表字号放大至 16px（访谈定值）。
     // 为什么用显式 class 而非 .dp-page :deep(.el-table)：抽屉明细表(L321)在 el-drawer(L310)内，
     // el-drawer teleport 到 body，scoped 的 .dp-page 选择器根本够不到它；故正向选中规则组表。
     // 为什么 body + cell 级：EP 样式由运行期注入 chunk 提供，特异度不足会被覆盖（记忆 ep-ondemand-css-specificity）。
     body .rule-group-table :deep(.el-table__cell) {
       font-size: 16px;
     }
     ```
   - 仅作用规则组列表，不改全局 `index.scss:68`，不波及抽屉明细表与他页表格。
10. **校验**：`pnpm build`；目视规则列表 16px、其他页表格不变。

### 阶段 5 — 前端 #2：仪表盘动作分布刷新

11. **`web/src/views/dashboard/Dashboard.vue`**
    - import 增加 `onActivated`；新增：
      ```js
      // keep-alive 下切回仪表盘不会重新 mount，故用 onActivated 钩子重拉动作分布，
      // 使离开期间产生的新动作及时反映（3s 轮询刻意不含动作分布，见 L150）。
      onActivated(() => { loadActionDist() })
      ```
    - 不改 3s 轮询、不改时间窗 watcher、不改 `onMounted`（首屏仍由 reloadByWindow/onMounted 拉一次）。
12. **校验**：`pnpm build`；切走再切回仪表盘，动作分布刷新。

### 阶段 6 — 前端 #6：复制地址新格式

13. **`web/src/views/user/Users.vue`**
    - 新增 `buildProxyAddr2(row)`（仿 `buildProxyAddr` L154-159，同源字段）：
      返回 `${addr}:${port}:${row.username}-{group}:${pwd}`，缺字段沿用 `<server-addr>`/`<socks5-port>`/`<pwd>` 占位。
    - 新增 `copyProxyAddr2(row)`：复用现有 clipboard 写入逻辑（抽公共 `copyText(text)` 工具，DRY，原 `copyProxyAddr` 也改调它）。
    - `el-dropdown-menu`（L223-230）在 `copy` 项旁加 `<el-dropdown-item command="copy2">{{ t('users.copyProxyAddr2') }}</el-dropdown-item>`；`onAction`（L185）增 `else if (cmd === 'copy2') copyProxyAddr2(row)`。
14. **i18n**：`zh.js`/`en.js` 的 `users` 段新增 `copyProxyAddr2`（zh：如「复制地址(IP:端口:用户-组:密码)」；en：如 "Copy Addr (ip:port:user-group:pwd)"）。键对等。
15. **校验**：`pnpm build`；两种格式都能复制，原格式不回归。

### 阶段 7 — 前端 #4：系统日志虚拟滚动

16. **新增依赖 + `web/src/views/syslog/SysLog.vue`**（决策 C1：`vue-virtual-scroller` `DynamicScroller`）
    - **依赖**：`cd web && pnpm add vue-virtual-scroller`（写入 `web/package.json` dependencies 与 `pnpm-lock.yaml`）；在组件内 `import { DynamicScroller, DynamicScrollerItem } from 'vue-virtual-scroller'` + `import 'vue-virtual-scroller/dist/vue-virtual-scroller.css'`（或全局 main.js 引一次样式）。
    - **改造日志区**（L145-156）：把 `.log-box` 内的 `v-for` 行替换为 `DynamicScroller`（`:items="logs"` `key-field` 用稳定键，`:min-item-size` 给一个终端行估值），每行用 `DynamicScrollerItem`（`:item`/`:active`/`:size-dependencies="[l.message, l.fields]"`）包住现有 `.log-line` 终端 DOM（`log-time/log-level/log-msg/log-fields` 样式与结构**保留**）。`DynamicScroller` 原生测量缓存**可变行高**，解决 `pre-wrap`/超长 fields 抖动。
    - **保留**：SSE `appendLog`（L36-40）追加；`autoScroll` 贴底（改用 scroller 的 `scrollToBottom()`/滚到末项 API，追加后若 autoScroll 则滚到底）；level 过滤；`MAX_RENDER=5000` 兜底（L19/39）；终端样式。
    - key 字段：现用 `:key="i"`（索引）在 `splice` 截断后会错位，改用稳定 key（如递增序号字段，appendLog 时给每条打 `_id`），保证 DynamicScroller 复用正确。
17. **校验**：`pnpm build`；灌 ≥5000 行，DevTools 看渲染节点数远小于 5000，滚动与 SSE 追加流畅。

### 阶段 8 — 前端 #7：审计 tab 分页 + 筛选

18. **`web/src/api/syslog.js getAuditLogs`**（L14-16）
    - 改签名为 `getAuditLogs(params)`：`request.get('/syslog/audit', { params })`，注释新增 page/pageSize/user/target/action/group + 返回 `{items,total,page,pageSize}`。
19. **`web/src/views/syslog/SysLog.vue` 审计 tab**（L93-102 逻辑 + L156-175 模板）
    - `loadAudit()` 改为带 `page/pageSize` + 4 筛选参数的服务端拉取，存 `audit.items`/`audit.total`/`page`/`pageSize`。
    - 模板：审计 tab 顶部加 4 个筛选输入（user/target/action/group，action 可用 `el-select` 选 forward/direct/reject）+ 查询/重置按钮；表格下加 `el-pagination`（`current-page`/`page-size`/`total`，change 时重拉）。
    - 切到 audit tab（watch L100-102）时拉第 1 页。
20. **i18n**：审计相关新增文案（4 个筛选 label、分页/查询/重置按钮文案）落 `zh.js`/`en.js`（建议放 `syslog` 段）。注意：SysLog.vue 现有审计区为**硬编码中文**（非 t()）；本次**新增**控件文案用 i18n 键（保持新代码合规），既有硬编码字符串本次不强制改造（避免扩大范围，可在注释标注 TODO）。
21. **校验**：`pnpm build`；筛选 + 翻页服务端生效，大量记录不卡。

---

## 五、风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| #3 Target 改普通字段写 → data race | race 崩溃/读到撕裂值 | 用 `atomic.Pointer[string]`（决策 D）；**新增** `-race` 测试覆盖 SetTarget vs Snapshot；指针不初始化为 `&""`（保 nil 回退语义） |
| #5 keepalive 时窗超 AC（裸 KeepAlive=165s） | 死连接 90s 内清不掉，AC 不达 | 用 `net.KeepAliveConfig{Idle:30,Interval:15,Count:3}`=75s（B1），设 `KeepAlive:-1` 避免裸 duration 干扰 |
| #5 keepalive 误碰零拷贝 | 性能/正确性回归 | 只在 `Listen`/`dialer` 构造点设一次；`git diff server/relay.go` 必须为空（AC 守门） |
| #5 上游 SOCKS5 conn keepalive 不可达 | 上游死连接不被探测 | 用 `&net.Dialer{KeepAliveConfig}` 作 `proxy.SOCKS5` 的 forward（决策 B1，已验证接口满足），从构造层注入而非事后断言 |
| #5 上游 keepalive 定位被误读为主修复 | 评审/维护误判 | 文档明确：客户端 keepalive 才是主修复；上游侧仅补 `idle=0` 空档 + <300s 探测（B4，引 idleconn.go:23-24 / server.go:325,359） |
| #5 ListenConfig 需 context | 装配期改动 | 用 `context.Background()`（监听器生命周期 = 进程级，无需取消语义） |
| #1 EP 样式被运行期 chunk 覆盖 | 16px 不生效 | 提特异度到 cell 级 `body .rule-group-table :deep(.el-table__cell)`（项目记忆已记此坑） |
| #1 选择器够不到抽屉明细表（teleport） | 误以为能用 .dp-page 收窄 | 抽屉 el-drawer teleport 到 body，scoped .dp-page 根本到不了；正向用显式 `.rule-group-table` 选中规则组表（B5） |
| #4 日志行非定高致虚拟滚动抖动 | 滚动跳动/复用错位 | 用 `vue-virtual-scroller` `DynamicScroller`（原生可变行高 + 测量缓存）；稳定 key（_id）替代索引 key |
| #4 新增依赖 | 打包体积/维护面 | 成熟库优先是硬约束（spec/全局规范），可变行高排除自研；仅引一个活跃维护库，`pnpm-lock.yaml` 同步 |
| #4 SSE 追加时贴底/位置错乱 | 体验回归 | 追加后按 autoScroll 调 scroller 滚到末项；非贴底时保持当前位置 |
| #7 分页越界 panic | 接口崩溃 | 安全切片 `start=min((page-1)*pageSize,total)`/`end=min(start+pageSize,total)`，start≥total 返空切片（B3，AC 守门） |
| #7 分页排序与 ring 顺序不一致 | 翻页错乱/抖动 | 服务端统一「过滤→reverse 最新优先→切片」，total 为筛选后数（决策 E） |
| i18n 漏键导致 en 缺失 | 英文界面显示 key | 收尾跑「键集对等」检查（见验证） |
| 误建分支 | 违反项目铁律 | 全程直接提交 main（项目记忆 no-branches） |

---

## 六、验证步骤（收尾统一执行）

1. **后端构建/静态检查**：`go build ./...`、`go vet ./...`。
2. **#3 竞争检测（须新增测试）**：在 `connreg/` **新增** `-race` 用例，多 goroutine 并发 `SetTarget` 与 `Snapshot`；跑 `go test -race ./connreg/...` 无告警。验证不变量：未调 `SetTarget` 时 `Snapshot().Target == meta.Target`（nil 回退）。
3. **#5 零拷贝守门**：`git diff server/relay.go` 输出为空（relayCounted 拷贝逻辑未改）。
4. **#5 断电手测（客观达标线）**：客户端经代理建连后**物理/虚拟断电**（不发 FIN/RST），死连接应在 **Idle 30 + Interval 15 × Count 3 = 75s（≤90s）** 内被探测：观察实时连接列表该行消失 + 上游 fd 释放（`lsof`/`ss` 不再见该上游连接）。另起一条**长空闲但存活**的隧道，确认不被误杀。
5. **#5 socket 机检（仅 linux，辅助）**：**linux-only**，用 `//go:build linux` 门控。对客户端与上游 socket 用 `SyscallConn().Control` + `unix.GetsockoptInt(fd, IPPROTO_TCP, TCP_KEEPIDLE/TCP_KEEPINTVL/TCP_KEEPCNT)` 断言实际为 30/15/3，证明 `KeepAliveConfig` 已落到内核 socket。**Darwin 注意**：sockopt 名不同（`TCP_KEEPIDLE` 在 macOS 不存在，约对应 `TCP_KEEPALIVE`），故 macOS/Windows **不跑此机检**，其跨平台正确性由本节步骤 4 的 75s 断电行为测试保证（权威 AC），机检仅为 linux 下的额外证据。
6. **#7 接口手测（含越界）**：`curl '/api/syslog/audit?page=1&pageSize=10&action=forward&user=alice'` 返回 `{items,total,page,pageSize}` 顺序最新→最旧；再测 `page=99999` 返回 `items:[]` + 正确 `total`，**不 panic / 不报错**。
7. **前端构建**：`cd web && pnpm build`（含新依赖 `vue-virtual-scroller`，确认打包通过）。
8. **前端目视**：#1 规则组列表 16px（他页 / 抽屉明细表 14px）；#2 切回仪表盘动作分布刷新；#4 ≥5000 行渲染节点数 ≪ 5000、可变行高（多行/超长 fields）不抖动、滚动/SSE 流畅；#6 两种复制格式可用、原格式不回归；#7 审计分页+四维筛选服务端生效。
9. **i18n 对等检查（强制，diff 必空）**：脚本逐键路径比对 `zh.js` 与 `en.js`（#6/#7 新增键后），两文件键集 **diff 为空**，无单边缺失。

---

## 七、文件与行号速查（实测）

- #1 `web/src/views/rule/Rules.vue`（规则组表 L256-282；抽屉明细表 L321+；根 `.dp-page` L244）；全局字号 `web/src/styles/index.scss:68`。
- #2 `web/src/views/dashboard/Dashboard.vue`（`loadActionDist` L106、3s 轮询 reloadByWindow 不含动作分布）；keep-alive 实证 `web/src/layouts/MainLayout.vue:162`（`<keep-alive :max="6">`）。
- #3 `connreg/registry.go`（`ConnMeta.Target` L28、`activeConn` L44-49、`SetUpstream` L91、`SetAction` L99、`Snapshot` L130、`toView` L166-185）；`server/server.go handleSniff` L374-432（嗅探成功 L398-401，回填点）。
- #4 `web/src/views/syslog/SysLog.vue`（`MAX_RENDER` L19、`appendLog` L36-40、`scrollToBottom` L42-48、`.log-box` 模板 L145-156、可变高样式 `.log-msg{pre-wrap;break-all}` L229-230 / `.log-fields` L232、样式 L180+）；`web/package.json`（已装 element-plus@^2.9.4；**需新增** `vue-virtual-scroller` 依赖 + `pnpm-lock.yaml` 同步）。
- #5 `server/lifecycle.go Listen()` L82-89（`net.Listen`→`ListenConfig.KeepAliveConfig`）；`server/relay.go relayCounted` L114-167（**勿动**）；`dialer/dialer.go DialDirect` L25-27 / `DialUpstream` L35-54（`proxy.Direct` 在 L43）；上游冗余依据 `dialer/idleconn.go:23-24`（`idle<=0` 返裸 conn）+ `server/server.go:325`(direct WrapIdle)/`:359`(forward WrapIdle)；go.mod `go 1.26.4`（`KeepAliveConfig` 可用）。
- #6 `web/src/views/user/Users.vue`（`buildProxyAddr` L154-159、`copyProxyAddr` L160、`onAction` L185、`el-dropdown-menu` L223-230）；i18n `web/src/locales/{zh,en}.js` `users` 段（zh.js:416）。
- #7 `api/syslog_handler.go handleAuditSnapshot` L96-98；`syslog/buffer.go snapshot()` L79-92（最旧→最新）；`syslog/audit.go`（`AuditEntry` 字段 L14-18、`Snapshot` L42-43）；前端 `web/src/views/syslog/SysLog.vue loadAudit` L93-102 + 审计表 L156-175；`web/src/api/syslog.js getAuditLogs` L13-16。

---

## 八、非目标（重申，防范围蔓延）

- 不改 SOCKS5 协议范围（仍仅 CONNECT/TCP）。
- keepalive 周期不做完整可配置（硬编码 30s）。
- 审计不改为数据库（仍内存 ring buffer）。
- #3 不改前端目标列结构（纯后端回填）。
- #4 不自研定高窗口化（日志行非定高，且 spec/全局规范要求成熟库优先）→ 用 `vue-virtual-scroller`。
- 不顺手把 SysLog.vue 既有硬编码中文全量改 i18n（仅新增文案用 i18n）。

---

## ADR 摘要（SHORT 模式核心决策）

- **Decision**：7 项按「最小侵入 + 沿用既有模式 + 成熟库优先」落地；#5 用 `net.KeepAliveConfig{Idle:30,Interval:15,Count:3}`（=75s，≤90s）于 `ListenConfig` 与 `net.Dialer`，#3 用 `atomic.Pointer[string]`，#4 用 `vue-virtual-scroller` 的 `DynamicScroller`（可变行高）。
- **Drivers**：并发正确性（#3 race）、keepalive 探测时窗达标（#5 KeepAliveConfig 而非裸 duration）、成熟库优先 × 日志行非定高的现实（#4）。
- **Alternatives considered**：#5 裸 `KeepAlive:30s`（165s 超窗，否决）/ TCPConn 事后断言（上游不可达，否决）；#4 EP table-v2（破坏终端样式、自由多行支持弱）/ 自研定高窗口化（行非定高、违库优先，降为非目标）；#3 持锁写普通字段（破坏无锁约束，否决）。
- **Why chosen**：#5 KeepAliveConfig 是唯一能精确控时窗落入 AC 的方式；#4 DynamicScroller 是唯一同时满足「成熟库 + 原生可变行高 + 保留终端 DOM」的方案；#3 atomic 与既有 SetAction/SetUpstream 一致且 race-clean。
- **#5 定位澄清**：客户端 keepalive 是泄漏主修复；上游 keepalive 仅补 `idle_timeout_sec=0` 空档（`WrapIdle` 返裸 conn）+ 提供 <300s 上游探测，非主修复（B4）。
- **Consequences**：#4 新增一个依赖（成熟库优先的必要代价）；#5 上游 keepalive 探测「本机↔上游代理」一跳（符合职责边界）；#5 keepalive 参数为 `dialer` 包单一 `KeepAliveConfig`（DRY，监听侧 + 两 dialer 共用，无 import cycle：`server`→`dialer` 已存在、反向不存在）；#5 linux socket 机检以 `//go:build linux` 门控，**Darwin/Windows 不跑该机检**（sockopt 名异，如 macOS 无 `TCP_KEEPIDLE`），跨平台正确性由 75s 行为测试这一权威 AC 保证。
- **Follow-ups**：keepalive 周期配置化、审计持久化、SysLog 既有硬编码文案 i18n 化（均为本次非目标）。

---

## Consensus Changelog

> 本计划经 2 轮 Architect + Critic 评审达成共识（Architect: SOUND / Critic: APPROVE）。

### Round 1 — REJECT-WITH-FEEDBACK（已全部解决）
- **B1（Blocker）#5 时窗不可达**：裸 `KeepAlive:30s` → 165s 超 AC 窗。改用 `net.KeepAliveConfig{Idle:30,Interval:15,Count:3}`=75s（`ListenConfig` + `net.Dialer` 双侧，`KeepAlive:-1`）。已验证 go.mod `go 1.26.4` 支持。
- **B2（Blocker）#4 库优先矛盾 + 定高前提为假**：日志行非定高（`pre-wrap`/`break-all` + 无界 fields）。改 `vue-virtual-scroller` `DynamicScroller`（原生可变行高）为唯一主选，自研定高降为非目标。
- **B3（Major）#7 分页越界 panic**：明确安全切片边界（page≥1、pageSize 默认 50/上限 200、`start=min((page-1)*pageSize,total)`、`end=min(start+pageSize,total)`、空切片不 panic），并入 AC。
- **B4（Major）#5 上游 keepalive 误定位**：澄清客户端 keepalive 才是泄漏主修复；上游侧仅补 `idle=0` 空档（`idleconn.go:23-24` + `server.go:325,359`）+ <300s 探测。
- **B5（Major）#1 teleport 理由反了**：抽屉表 teleport 到 body，`.dp-page` scoped 选择器够不到它。改为显式 `.rule-group-table` class + cell 级 `body .rule-group-table :deep(.el-table__cell)`。
- **B6（Minor）#3 不变量**：AC 明确 `target` 指针在 SetTarget 前恒 nil、`toView` nil 回退 `meta.Target`，绝不初始化 `&""`。
- **B7（Minor）验证强化**：#5 给 75s 客观达标线 + linux socket 机检；#3 须新增 `-race` 测试；i18n zh/en 键集 diff 必空。

### Round 2 — 共识 + 2 项最终改进（本轮应用）
- **改进 1（DRY，Critic）**：keepalive 30/15/3 不再两处重复，集中为 `dialer` 包导出的单一 `KeepAliveConfig`，监听侧与两个 dialer 共用；实测 `server`→`dialer` 已存在依赖、反向不存在，无 import cycle，无需新建包。
- **改进 2（Darwin caveat，Architect/Critic）**：linux 的 `GetsockoptInt(TCP_KEEPIDLE/INTVL/CNT)` 机检以 `//go:build linux` 门控，Darwin/Windows 不跑（sockopt 名异）；跨平台正确性以 75s 行为测试为权威 AC。
- **状态**：置为 **pending approval**；ADR（Decision/Drivers/Alternatives/Why chosen/Consequences/Follow-ups）补齐并与最终 #4（DynamicScroller）/#5（KeepAliveConfig）决策一致。
