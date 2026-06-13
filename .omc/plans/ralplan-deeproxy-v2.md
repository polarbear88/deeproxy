# 实施计划：deeproxy v2 — Web 控制后台 + 代理池 + 多对多规则 + 统计

> 状态：草案（consensus / deliberate 模式）。本计划只做架构决策与可执行步骤排序，**不写业务代码、不改 v1 源码**。
> 权威需求：`.omc/specs/deep-interview-socks5-v2-admin.md`（41 条 AC，最终模糊度 ~9%）。
> 上游评审：本草案供 Architect / Critic 评审后再 crystallize 为执行计划。

---

## 一、Requirements Summary（需求摘要）

把 v1（YAML 驱动的单一动态上游 SOCKS5 纯中继）升级为带 **Web 控制后台、Type B 多上游代理池、可视化统计、多对多规则、双身份鉴权** 的完整产品，最终仍交付 **单一静态跨平台二进制**（Vue 前端 embed 进 Go 二进制）。

五大顶层组件（spec Topology）：
1. **转发内核改造**（AC-1~9）：新用户名语法 `user-group[-尾段]`、按组选上游、Type A base64 / Type B 加权轮训、命名变量替换、规则全局+分组合并、埋点。
2. **存储层 SQLite**（AC-10~14）：表结构 + atomic.Value 内存快照热替换 + 统计异步批量落聚合时间桶 + WAL 单写协程 + 自动清理。
3. **健康检查子系统**（AC-15~18）：ping/URL 探测、失败/恢复阈值、剔除/恢复、全挂拒连、前端展示、手动启停。
4. **管理后端 API**（AC-19~24, 33~41）：Gin、独立端口默认 0.0.0.0、登录/会话、各模块 CRUD、仪表盘聚合、系统日志 WS/SSE、配置导入导出、代理测试、规则测试器、bcrypt + 登录限流、连接审计。
5. **Web 前端**（AC-25~31, 35）：Vue3 + Element Plus + pnpm、左侧菜单、暗/亮模式、ECharts、登录/首次设置/仪表盘/SOCKS5 管理/规则/用户/系统设置/系统日志。

**绝对硬约束（用户点名协商）**：任何新增功能 **严禁拖慢转发热路径**。字节中继热路径（`relay.go` io.Copy 双向循环）零锁、零持久化，只做内存原子计数；配置经 `atomic.Value` 快照无锁读；建连阶段的 SWRR 选择允许 per-group 小锁（每连接一次、不在中继循环内）；SQLite 完全脱离转发延迟链路；统计走内存原子计数 + 异步批量 flush。

### v1 现状关键事实（已探明，约束本计划）
- 包：`cmd/deeproxy`、`config`、`auth`(upstream.go DecodeUpstream / credential.go Credential)、`rule`(engine.go Engine/Match/MatchRule)、`server`(server.go connectRule.Allow + handler.connectHandle / relay.go / ctxkey.go decision / logadapter.go)、`dialer`(dialer.go DialDirect/DialUpstream / idleconn.go)、`detect`(sni/http/reader)、`internal/logging`(slog)。
- 依赖：`things-go/go-socks5 v0.1.1`、`golang.org/x/net v0.56.0`、`gopkg.in/yaml.v3 v3.0.1`。零 HTTP/DB/metrics。Go 1.26.3。
- **关键集成点（go-socks5 v0.1.1 数据流，已核对源码）**：`WithCredential(Credential).Valid(user, password, userAddr) bool` → `WithRule(connectRule).Allow(ctx, req) (ctx, bool)` → `WithConnectHandle(handler.connectHandle)`。
  - `auth.go:71-77`：`UserPassAuthenticator.Authenticate` 返回的 `AuthContext.Payload` = `map[string]string{"username":..., "password":...}` —— **password 与 username 都在 Payload 里**，Allow/ConnectHandle 阶段都能取到。
  - `server.go:122` 每连接独立 goroutine；`server.go:131,175` `authContext` 是 `ServeConn` 内的局部变量，挂到 `request.AuthContext` 后贯穿 Allow→ConnectHandle，**不跨连接共享**。
  - v1 `server/server.go:64-65` 已在用 `req.AuthContext.Payload["username"]`，证明 AuthContext 完整流到 ConnectHandle。
- `rule.Engine` 是唯一共享运行期状态，构建后只读、无锁；连接级状态走 context。

> ✅ **集成结论（已由源码证实）**：`Valid`（同时有 user+password）、`Allow`、`ConnectHandle` 三阶段同 goroutine、同连接、顺序执行，AuthContext 与 context 均天然贯穿且零跨连接共享。因此鉴权与解析结果的传递沿用 v1 已验证的 context(`decisionKey`) 机制即可，无任何"跨阶段拿不到数据"或"并发串号"风险。详见 RALPLAN-DR 关键决策 D0-0。

---

## 二、RALPLAN-DR Summary（决策记录）

### Principles（指导原则，3-5 条）
1. **字节中继热路径零锁、零持久化**：字节中继热路径（`relay.go` 的 `io.Copy` 双向循环）**零锁、零持久化**——只做内存原子计数累加。SQLite/HTTP/健康检查/日志一律旁路异步，绝不进入字节中继链路。**建连阶段**（每连接仅一次）允许 per-group 细粒度小锁（如 SWRR 选择），它不在字节中继循环内，对吞吐无影响。
2. **最大化复用 v1 内核**：`detect`、`relay`、`dialer.DialDirect/DialUpstream`、`rule.MatchRule` 的匹配语义是经访谈固化的资产，v2 在其外围包裹热替换/选组/池化，不重写匹配核心（DRY）。
3. **成熟库优先 + 跨平台静态优先**：遵循全局规范，选 Star 多/活跃/纯 Go 免 CGO 的库，保证 `go build` 交叉编译单二进制。
4. **双身份强隔离**：管理员凭据与代理用户凭据两套独立存储、独立校验路径，互不相通（spec 身份模型）。
5. **配置即数据库 + 内存快照**：分组/池/规则/用户/授权的权威源是 SQLite；运行期是从 SQLite 物化的不可变内存快照，写后重建并 atomic 替换。

### Decision Drivers（top 3）
1. **转发延迟不回归**（AC-11 压测对比 v1）——压倒一切，决定快照/计数/落库架构。
2. **单一静态跨平台二进制**（AC-32）——决定 SQLite 驱动（免 CGO）、前端 embed、构建矩阵。
3. **brownfield 最小侵入**——决定包拆分边界与 v1 内核保留范围，降低回归面。

### 关键决策与可行方案（每项 ≥2 方案带 pros/cons）

#### D0-0：鉴权解析结果如何跨 go-socks5 三阶段传递（**已由源码定稿**）
> 背景（已核对 go-socks5 v0.1.1 源码）：`Valid(user, password, userAddr)` 与 `Allow`/`ConnectHandle` 三阶段同 goroutine、同连接、顺序执行；`AuthContext.Payload` 同时含 `username` 与 `password`（`auth.go:71-77`），且 `authContext` 是 `ServeConn` 内局部变量、不跨连接共享（`server.go:122,131,175`）。v1 已用 `req.AuthContext.Payload["username"]` 证明 AuthContext 完整贯穿。**因此不存在"拿不到 password"或"跨阶段并发串号"的问题——这两个前提已被源码证伪。**

- **定稿方案 D0-0（采纳）：沿用 v1 已验证的 context(`decisionKey`) 机制。**
  - `Credential.Valid` 阶段（此处同时有 user + password）：解析用户名 `user-group[-尾段]` → 查 ProxyUser + bcrypt 验密码 → 验 group 授权 → 按组类型解析尾段（Type A 解 base64 上游 / Type B 解析命名变量 `map[name]value`，**不在此选池**）。鉴权失败直接返回 false。
  - 选组/上游/变量映射等解析结果经扩展后的 `server/ctxkey.go:10` `decision` 结构通过 context 传给 `Allow`/`ConnectHandle`。`password` 在需要时直接从 `Payload["password"]` 读。
  - DRY 保证：用户名解析、尾段解析抽为 `auth` 包共享纯函数。因 Valid 与 Allow 同 goroutine 同连接顺序执行，必要时在 Allow 阶段用同一纯函数重新解析一次用户名（仅一次字符串解析、非字节中继热路径、无 I/O），代价可忽略；**零跨连接共享、零并发风险**。
  - 变量替换时机：Valid 阶段只产出 `map[name]value`；**模板 `{name}` 实际替换延迟到拨号阶段选定具体上游之后**（Type B 选池在拨号阶段，见 D4 与阶段 6）。
- **可选优化（非必需）D0-opt：实现自定义 `socks5.Authenticator`，把解析结果序列化进返回的 `AuthContext.Payload`（`map[string]string`），Allow 阶段反序列化读取，从而避免两阶段重复解析一次用户名。** 仅在性能剖析确认重复解析有意义开销时才采用；当前判断重复解析开销可忽略，**默认不做**。
- **兜底方案（实际不会触发）D0-fallback：自研精简 SOCKS5 服务端（仅 CONNECT + RFC1929）。** 其触发前提（"go-socks5 无法承载鉴权数据传递"）已被源码证伪，**预期不会启用**；仅保留为理论兜底，CLAUDE.md 第十节授权自研。
- **决策结论：采纳 D0-0，不引入任何跨连接共享状态（已删除原 D0-A 的 `sync.Map` 待取表——它解决的是不存在的问题，且会真正引入跨连接共享与并发键 bug）。**

#### D1：SQLite 驱动
- **方案 D1-A（推荐）：`modernc.org/sqlite`（纯 Go，免 CGO）。**
  - Pros：`CGO_ENABLED=0` 可交叉编译全平台/全架构单静态二进制，直击 Driver-2（AC-32）；无 C 工具链依赖，CI 矩阵简单。
  - Cons：性能略低于 CGO 版（对本项目「聚合桶 + 5~10s 批量写」量级完全够用，非热路径）；包体积较大。
- **方案 D1-B：`mattn/go-sqlite3`（CGO 绑定 libsqlite3）。**
  - Pros：性能最高、最成熟、生态最广。
  - Cons：需 CGO → 交叉编译需各平台 C 工具链（zig/musl 等），与「单命令交叉出全平台静态二进制」目标冲突，显著增加构建复杂度。
  - Invalidation note：被 Driver-2 否决——CGO 跨平台静态编译成本远高于 modernc 的轻微性能损失，且本项目 SQLite 不在热路径，性能差异无实际影响。
- **定稿：D1-A `modernc.org/sqlite`。**

#### D2：管理员会话机制
- **方案 D2-A（推荐）：服务端会话表（SQLite `sessions` 或内存 map）+ HttpOnly Cookie 存 sessionID。**
  - Pros：可即时吊销（登出/改密即失效）；单管理员场景表极小；与「配置即 SQLite」一致；无需管理签名密钥跨重启持久化。
  - Cons：每请求查一次会话（极轻，且管理 API 非热路径）。
- **方案 D2-B：JWT（无状态，HMAC 签名）。**
  - Pros：无状态、无存储、利于单二进制。
  - Cons：无法即时吊销（需黑名单，反而引入状态）；签名密钥需持久化（否则重启所有会话失效）或每次重启强制重登；单管理员场景「无状态」优势不明显。
  - Invalidation note：单一管理员 + 需要「改密即踢出」语义（AC-40 安全相关），有状态会话更契合；JWT 的无状态优势在此规模下不构成收益。
- **定稿：D2-A 内存会话 map（重启需重登，可接受）+ HttpOnly Cookie；登录限流计数也放内存。** 若需重启保活会话再升级为 SQLite sessions 表。

#### D3：数据访问层（ORM vs 手写）
- **方案 D3-A（推荐）：`database/sql` + 手写 SQL，薄 repository 封装在 `store` 包。**
  - Pros：零魔法、SQL 完全可控（便于 WAL/单写协程/批量 upsert 聚合桶的精确控制）；契合「中文注释解释为什么」规范；依赖最小。
  - Cons：CRUD 样板较多。**缓解**：实体不多（~8 张表），样板可控；公共扫描/事务逻辑抽 `store/db.go`。
- **方案 D3-B：`gorm`。**
  - Pros：CRUD 快、迁移方便。
  - Cons：反射开销与「魔法」不利于精确控制单写协程与批量聚合写；额外学习面；与「禁用长期不更新库」无冲突但增依赖重量。
  - Invalidation note：统计 flush 的批量 upsert + WAL 单写串行化需要精确 SQL 控制，ORM 抽象反成阻碍；配置 CRUD 量不大，ORM 收益有限。
- **可选增强**：`sqlc`（从 SQL 生成类型安全 Go）作为 D3-A 的提效层，非必须。
- **定稿：D3-A `database/sql` + 手写 repository。**

#### D4：加权轮训算法（Type B 代理池选上游）
- **方案 D4-A（推荐）：平滑加权轮训（Smooth Weighted Round-Robin, nginx 同款）。**
  - Pros：权重分布均匀、无突发倾斜；O(n) 选择、状态小（每节点 currentWeight）；剔除/恢复只需重算 effectiveWeight。
  - Cons：选择需在 per-group Selector 上加轻锁（池规模小、且只在「新建连接」时调用一次，不在字节中继循环内，锁极轻）。
- **方案 D4-B：朴素加权随机（按权重区间随机）。**
  - Pros：无状态、可纯原子读快照 + rand，无锁。
  - Cons：短期分布抖动大；连续请求可能集中同一上游。
  - Invalidation note：SWRR 的平滑性对「上游粘性/均衡出口」更友好，且选择仅发生在建连阶段（远低于字节中继频率），轻锁开销可忽略。
- **定稿：D4-A SWRR。状态归属（关键消歧）：**
  - **SWRR 可变状态（各节点 currentWeight）不放进不可变 `Snapshot`**，而放在独立的、长生命周期的 **per-group `pool.Selector` 对象**，该对象自带 `sync.Mutex` 保护选择与 currentWeight 更新。
  - `Snapshot` 只持有「该组**健康节点列表的不可变快照引用**」。健康检查 worker 探测出状态变化后，通过 atomic 替换该组的健康节点列表引用；`Selector` 每次选择时**重读当前健康列表引用**，在自己的小锁内基于该列表更新 currentWeight。
  - 剔除不健康节点 = 健康检查 worker 用新的有效节点列表 atomic 替换旧引用；`Selector` 下次选择自然只在新列表上轮训，无需触碰转发热路径。

### 仅一个可行方案的决策（含 invalidation rationale）
- **HTTP 框架 = Gin**：spec Round 3 用户已点名 Gin，无需再比选（备选 echo/chi 已被需求锁定）。
- **前端栈 = Vue3 + Element Plus + pnpm + ECharts**：spec 已锁定。
- **密码哈希 = `golang.org/x/crypto/bcrypt`**：spec 已锁定，行业标准，无替代必要。
- **配置热替换 = `atomic.Value` 快照**：spec 性能硬约束直接规定，无替代。

---

## 三、Pre-mortem（三个失败场景，deliberate 模式）

### 失败场景 1：转发延迟回归（违反 AC-11 / 一号硬约束）
**设想的失败**：上线后压测发现 v2 p99 转发延迟显著高于 v1。根因可能是：① 埋点在每次 `io.Copy` 循环内加锁累加而非用 `atomic.Add`；② 选组/选上游时直接查 SQLite 而非读内存快照；③ 健康检查 worker 与转发争用同一把锁；④ 日志 Handler 同步写环形缓冲时用了粗粒度 mutex 阻塞转发。
**预防**：
- 计数一律 `atomic.Int64`/分片计数器，flush worker 周期读取，转发侧只 Add 不读不锁。
- 选组/选池/规则全部走 `atomic.Value.Load()` 出的不可变快照，编译期保证转发包不 import `store`/`database/sql`。
- 健康检查改写的是「新快照」，用 atomic 替换，绝不原地改转发正在读的对象。
- 写专门的 benchmark（见测试计划 observability）对比 v1，CI 设回归阈值门禁。

### 失败场景 2：跨平台静态二进制构建失败（违反 AC-32）
**设想的失败**：本地 macOS 开发顺利，CI 交叉编译到 windows/arm64 或 linux/arm64 失败，或产物运行时报 CGO 缺库；前端 embed 路径在 Windows 下因 `/` vs `\` 失效。
**预防**：
- 锁死 `modernc.org/sqlite`（D1-A，免 CGO），`CGO_ENABLED=0`，第一个里程碑就建最小「embed 一个静态文件 + 开一张 SQLite 表」的 spike，在 5 平台矩阵 `go build` 验证后再铺量。
- `embed.FS` 路径统一用正斜杠（embed 规范要求），不依赖 OS 分隔符。
- CI 用 GitHub Actions 5 目标矩阵（win/mac/linux × amd64/arm64，mac 仅 amd64+arm64）+ 冒烟启动测试。

### 失败场景 3：健康检查替换节点列表与 SWRR 选择竞态（违反 AC-4/AC-17，可能 panic 或选到死节点）
**设想的失败**：健康检查 worker 在 atomic 替换某 Type B 组「健康节点列表」引用的瞬间，并发的 `pool.Selector` 正持有旧列表索引做 SWRR 选择 → ① 旧列表被替换后 Selector 仍按旧长度访问导致索引越界 panic；② 选到一个刚被探测剔除的死节点，拨号必失败；③ 整组刚全挂、列表变空，Selector 未防御空列表 → panic 或除零。
**预防**：
- Selector **每次选择开始时重读当前健康列表引用**（`atomic.Value.Load()`），在该次选择内只用这一份不可变快照，绝不跨替换缓存索引。
- 对**空列表/越界严格防御**：列表为空（整组全挂）直接返回「无可用上游」错误 → 上层按 AC-17 拒连（回 `RepHostUnreachable`，见 G6）；选择逻辑只在当前快照长度范围内取模。
- 节点被剔除后，下次选择因读到的是新列表自然跳过；即使偶发选到「刚剔除但本次仍命中」的节点，拨号失败会走 AC-4 故障转移重试下一个健康节点，不影响正确性。
- 测试：`-race` 下健康 worker 高频替换列表 + 1000 并发对同组建连，断言无 data race、无 panic、不返回已剔除节点（对应 AC-42）。

---

## 四、Implementation Steps（分阶段，按依赖排序）

> 总原则：**先底层后上层、先内核后界面、先单点 spike 后铺量**。每阶段结束可独立 `go build` + 单测通过。前 5 阶段不引入前端，保证内核可在无 UI 下用脚本/curl 验证。

### 阶段 0：技术 spike 与依赖确认（降风险，最先做）
- **走查确认 D0-0**（前提已由源码证实）：复读 `go-socks5@v0.1.1/auth.go:71-77`，确认 `AuthContext.Payload` 含 `username` 与 `password`（已知为真，走查留证）；确认 v1 context(`decisionKey`) 机制可直接扩展承载 v2 鉴权结果。**无需验证"能否拿到 password"——已确定可拿到。**
- 最小 spike：`embed` 一个**占位静态文件**（见下 embed 占位机制）+ `modernc.org/sqlite` 建 1 张表 + `CGO_ENABLED=0 go build`，在 5 平台矩阵交叉编译验证（验证失败场景 2 的前提）。
- **embed 占位机制（解决前端未构建时 go build 失败）**：仓库提交占位 `api/dist/.gitkeep` 与最小 `api/dist/index.html`，保证 `//go:embed dist/*` 在前端尚未 `pnpm build` 时也能编译通过；可选用 `//go:build embed` / `//go:build !embed` 双 tag（dev 版从磁盘读或返回 404，发布版 `-tags embed` 嵌入真实产物）。阶段 0 即落地此占位，保证前 6 阶段「内核可独立 build」。
- 引入依赖：`gin-gonic/gin`、`modernc.org/sqlite`、`golang.org/x/crypto/bcrypt`；实时日志推送默认用 **Gin SSE**（更轻、无需 `gorilla/websocket` 依赖），仅在确需双向交互时才引入 WebSocket。
- 产出：D0-0 走查留证 + embed 占位通过 + spike 五平台编译通过证据，更新 `go.mod`。

### 阶段 1：存储层 `store`（AC-10 表结构 / AC-14 WAL+单写）
- 新增 `store/` 包：`db.go`（打开 SQLite、WAL pragma、单写协程/写串行化通道）、`schema.go`（建表/迁移：admin/system_setting、proxy_user、group、upstream_proxy、rule_group、rule、group_user 授权、group_rulegroup 关联、traffic_stat 聚合桶）、`models.go`（Go 结构体，全中文注释）、各实体 `*_repo.go`（手写 SQL CRUD）。
- 实现统计聚合桶 upsert 与按保留期删除（AC-13 默认 30 天）的 SQL。**M3 桶粒度定稿**：采用**分钟级桶**为唯一存储粒度（`bucketTime` 截断到分钟，维度 = groupId/userId），7d 视图在**查询期服务端降采样**（`GROUP BY strftime('%Y-%m-%d %H', bucketTime)` 聚成小时）——避免双写两套桶、保持写路径简单。**基数预算**：行数 ≈ 活跃 (group×user) 组合数 × 60 × 24 × 保留天数；例如 20 个活跃组合 × 1440 分钟/天 × 30 天 ≈ 86.4 万行（SQLite 可轻松承载，配 `(groupId,userId,bucketTime)` 索引）；若实测组合数远超预期再引入小时汇总桶。
- 文件参考：新增 `store/*`；不动 v1。
- 验收触点：AC-10（表结构）、AC-13（清理 SQL）、AC-14（WAL+单写）。

### 阶段 2：配置快照与热替换 `config` 改造（AC-10）
- `config/config.go`：YAML 仅保留启动引导项（`listen` 即 SOCKS5 端口、`admin_listen` 后台端口默认 `0.0.0.0:<port>`、`log_level`、`idle_timeout_sec`、`sniff_*`、SQLite 路径、统计保留期/健康检查默认值的兜底）。分组/规则/用户/授权迁出 YAML、改由 SQLite 加载。
- 新增 `config/snapshot.go`：定义不可变 `Snapshot`（含：分组表[type/池/健康配置]、用户表、授权关系、规则组与关联、编译好的规则引擎集合），`atomic.Value` 持有；`Rebuild(store) (*Snapshot,error)` 从 SQLite 物化 + 预编译规则/池 SWRR 状态，`Swap(newSnap)` 原子替换。
- 后台任何写操作成功后调用 `Rebuild`+`Swap`（在管理 goroutine，转发侧无锁读）。
- 文件参考：改 `config/config.go`，新增 `config/snapshot.go`。
- 验收触点：AC-10、AC-11（架构保证）。

### 阶段 3：auth 内核改造（AC-1/2/3/5/6）
- `auth/`：新增 `username.go`（解析 `user-group[-尾段]`：前两个 `-` 按位置切，尾段整体不拆）；新增 `variables.go`（命名变量串 `name_value#name_value` 解析为 `map[string]string` + 模板 `{name}` 替换：隐式定义、缺值补空、多余忽略、顺序无关）；保留并复用 `upstream.go DecodeUpstream`（Type A 尾段 base64）；新增 `authz.go`（ProxyUser 密码校验[bcrypt]+ 授权校验，读内存快照）。
- 重写 `credential.go`：按 D0-0 定稿，在 `Valid` 鉴权阶段（此处同时有 user+password）完成「解析用户名 → 查 user/bcrypt 验密码 → 验 group 授权 → 按组类型解析尾段（Type A 解 base64 / Type B 解析命名变量 `map[name]value`，**不在此选池、不在此替换模板**）」，结果经扩展后的 `decision` 结构通过 context 传给后续阶段（沿用 v1 机制，零跨连接共享）。
- **消歧（变量替换时机）**：auth 阶段只产出 `map[name]value`；模板 `{name}` 实际替换延迟到拨号阶段选定具体上游之后（见阶段 6）。
- **边界用例（G1）**：Type A 组「无尾段（仅 `user-group`）但规则最终命中 `forward`」→ 无上游来源 → 拒连；需 E2E 覆盖（见测试计划）。
- 文件参考：改 `auth/credential.go`、`auth/upstream.go`(可能微调签名)，新增 `auth/username.go`、`auth/variables.go`、`auth/authz.go`。
- 验收触点：AC-1、AC-2、AC-3、AC-5、AC-6。

### 阶段 4：rule 多对多合并 + 热替换（AC-7，复用 v1 匹配核心）
- `rule/engine.go`：保留 `MatchRule` 匹配语义不变；新增「规则组集合」概念。**消歧（合并方式）**：按 group 维度，在 **Snapshot 重建时把「全局规则组 → 该 group 被应用的规则组 → 默认动作」预编译为一条该 group 专属的「单一有序规则序列」**（即每个 group 物化出一个扁平化的 `*Engine`），转发时只对该单一序列做一次顺序首匹配，**不在运行时遍历多个 engine**。组内书写顺序保持，全局组排在分组组之前。预编译结果挂在 Snapshot 上（阶段 2 的 atomic 热替换覆盖它）。
- 文件参考：改/扩 `rule/engine.go`，新增 `rule/merge.go`（多组合并匹配）。
- 验收触点：AC-7（真值表覆盖 domain/domain-suffix/ip-cidr + 全局/分组优先级）。

### 阶段 5：pool + stats + syslog（AC-4/15/16/17/18，AC-12，AC-33/34/36）
- 新增 `pool/`：`selector.go`（per-group 长生命周期 `Selector`，自带 `sync.Mutex`，持 SWRR currentWeight；每次选择重读 atomic 健康节点列表引用，空列表返回「无可用上游」错误；**SWRR 状态不入 Snapshot**，见 D4）、`health.go`（健康检查 worker：ping/URL 探测、失败/恢复阈值 3/2、剔除/恢复时 atomic 替换该组健康节点列表引用、整组全挂标记、手动启停单条、`TestProxy` 立即探测 AC-38）。
- **G2（Type A 不参与健康检查）**：Type A 组池为空（上游由客户端每连接动态给出），健康检查与故障转移仅对 Type B 有意义；health worker **跳过 Type A 组**，前端 Type A 组隐藏健康检查 UI（见阶段 8）。
- 新增 `stats/`：`counter.go`（`atomic.Int64` 内存计数器，按 group/user 维度 + 实时速率瞬时值）、`flush.go`（每 5~10s 批量 flush 到 SQLite 聚合桶，经 store 单写协程；AC-12）、清理调度调 store 删除过期桶（AC-13）。
- 新增 `syslog/`：`buffer.go`（内存环形缓冲限 5000，满淘汰最旧）、`handler.go`（slog Handler 写入缓冲，仅内存不落库）、`audit.go`（连接审计环形缓冲 AC-36）。
- 文件参考：新增 `pool/*`、`stats/*`、`syslog/*`。
- 验收触点：AC-4（含拨号故障转移：拨号失败重试下一健康代理，全挂拒连，已建连不重试）、AC-12、AC-15~18、AC-33/34/36。

### 阶段 6：server 装配改造（AC-4/8/9 + 埋点）
- `server/`：`server.New` 接收 Snapshot 提供者（atomic）、pool（per-group Selector 注册表）、stats、syslog 而非裸 `*Config`+`*Engine`。`connectRule.Allow` 改为读快照做选组/鉴权结果消费 + 合并规则预判（保留 IP 未命中嗅探放行逻辑 AC-8）。`handler.connectHandle`：Type B 在拨号阶段经 `pool.Selector` SWRR 选健康上游、**用 auth 阶段产出的 `map[name]value` 替换该上游用户名模板 `{name}`**、拨号失败重试下一个健康节点（AC-4 故障转移，已建连不重试）；Type A 用解出的动态上游。中继前后埋点到 stats + 写 audit。`ctxkey.go decision` 扩展承载 group/user/组类型/动态上游(A)/变量映射(B)。
- **G6（整组全挂回复码）**：Type B 组全部节点不可用时 `Selector` 返回「无可用上游」→ 在 Allow/ConnectHandle 拒连并回 `statute.RepHostUnreachable`（AC-17 的 E2E 需断言此具体 reply code）。
- 复用：`detect`、`relay.go`、`dialer.DialDirect/DialUpstream`、`idleconn.go` 不动或微调（DialUpstream 仍接收单个 Upstream，pool 在外层选出后传入 → DRY，故障转移在 server/pool 编排层做循环重试）。
- 文件参考：改 `server/server.go`、`server/ctxkey.go`；`server/relay.go`、`dialer/*`、`detect/*` 尽量不动。
- 验收触点：AC-4、AC-8、AC-9（CONNECT only 保留）。

### 阶段 7：管理后端 API `api`（AC-19~24, 33, 34, 37, 38, 39, 40, 41）
- 新增 `api/`：`server.go`（Gin 引擎，独立端口默认 0.0.0.0，AC-41）、`middleware.go`（会话校验 D2-A、登录限流）、`auth_handler.go`（首次设置 AC-19 / 登录签发会话 AC-20 / bcrypt + 限流 AC-40）、`group_handler.go`(AC-21)、`rule_handler.go`(AC-22)、`user_handler.go`(AC-23)、`dashboard_handler.go`(聚合：实时读内存 + 今日读 SQLite，AC-24)、`syslog_handler.go`(WS/SSE 推送 + 级别筛选 AC-33/34)、`tools_handler.go`(配置导入导出 AC-37 / 代理测试连接 AC-38 / 规则测试器 AC-39)、`static.go`(embed 前端 + SPA fallback)。
- 所有写操作 handler 在 SQLite 事务 commit 后触发 `config.Rebuild+Swap`（连接阶段 2）。**G4 回滚路径（关键）**：若 `Rebuild` 失败（新数据物化/预编译出错）则**不 Swap、保留旧快照、向调用方返回错误**，保证转发侧永远读到一致可用的快照（不会因一次坏写入导致内核读到半成品）。
- **G3（规则测试器 AC-39 边界）**：测试器只对「域名 / IP 的直接规则匹配」做模拟（输入域名/IP + 选 group → 跑该 group 合并后的单一规则序列 → 返回命中规则与动作）；**嗅探路径（IP 未命中 ip-cidr 后读首包还原域名）无真实首包，标注为"不可模拟"**，或允许用户额外传入一个「模拟嗅探域名」再跑一次匹配，UI 明示该限制。
- **G4（配置导入/导出 AC-37）**：导出 JSON 带 `schemaVersion` 字段（版本化）；导入时校验版本兼容、提供「覆盖 / 合并」冲突策略（首版可先做整体覆盖 + 导入前备份当前配置），导入成功并 commit 后走上面的 Rebuild+Swap（失败回滚）。
- **G5（登录安全 AC-40）**：bcrypt 采用库默认 cost（`bcrypt.DefaultCost`，约 10）；登录失败限流窗口与锁定时长记一笔（如 5 次失败锁定 5 分钟），计数放内存（D2-A），窗口需与 bcrypt 验证耗时协调避免计时侧信道。
- 文件参考：新增 `api/*`。
- 验收触点：AC-19~24、AC-33/34、AC-37~41。

### 阶段 8：前端 Web 控制台（AC-25~31, 35）
- 新增 `web/`（Vue3 + Vite + Element Plus + pnpm + ECharts + Pinia + vue-router）：登录页 + 首次设置页（AC-26）；布局（左侧菜单 + 暗/亮切换 AC-25）；仪表盘（实时速率/活跃连接/今日流量/今日拒连分两类/动作分布饼图/总流量&请求数时序图 1h-24h-7d/Top N 排行/运行健康区/用户名格式说明卡 AC-27）；SOCKS5 代理组管理（CRUD + Type B 加代理设权重 + 健康检查配置 + 分组独立流量图 + 单条代理测试按钮 AC-28）；规则管理（规则组/规则 CRUD + 应用到分组/全局 AC-29）；用户管理（代理用户 CRUD + 授权 AC-30）；系统设置（管理员账密/统计保留期/健康检查默认 AC-31）；系统日志页（WS/SSE 实时滚动 + 级别筛选 + 暗亮适配 AC-35）。
- 文件参考：新增 `web/*`；约定 `pnpm build` 产物输出到 `api/dist`（被 embed）。
- 验收触点：AC-25~31、AC-35。

### 阶段 9：embed 打包 + 入口装配（AC-32 前置）
- `api/static.go`：`//go:embed dist/*` 嵌入构建产物（阶段 0 已落地占位 `api/dist/.gitkeep` + `index.html` 保证未构建时也能编译），Gin 提供静态 + `/api` + SPA history fallback；embed 路径统一用正斜杠。
- `cmd/deeproxy/main.go`：装配新启动流程——加载 YAML 引导 → 打开 store(WAL) → 物化首个 Snapshot → 启动 SOCKS5 服务(读 atomic 快照) + 启动 Gin 后台(独立端口) + 启动 health/flush/cleanup worker + syslog handler 接入 slog。
- 文件参考：改 `cmd/deeproxy/main.go`、新增 `api/static.go`。

### 阶段 10：跨平台构建与发布（AC-32）
- 新增 `Makefile`/`build.sh` 或 `goreleaser` 配置：`CGO_ENABLED=0` 交叉编译 win/mac/linux × amd64/arm64；前端先 `pnpm build` 再 `go build`（注入版本号）。
- CI（GitHub Actions）：lint + test + 5 平台构建矩阵 + 启动冒烟 + 转发延迟 benchmark 门禁（失败场景 1）。
- 验收触点：AC-32。

---

## 五、Expanded Test Plan（deliberate 模式：unit / integration / e2e / observability）

### Unit（包内纯逻辑）
- `auth`：用户名解析（无尾段 / 含尾段 / 尾段含 `-` 不拆 AC-1/2）；命名变量解析与模板替换（隐式定义/缺值补空/多余忽略/顺序无关 AC-5）；Type A base64 解码复用 v1 用例 AC-3；bcrypt 校验 + 授权矩阵（user 不存在/密码错/未授权三分支 AC-6）。
- `rule`：v1 既有真值表 + 新增全局组优先于分组组、组内首匹配、默认动作兜底 AC-7。
- `pool/swrr`：权重分布统计断言（大样本下比例趋近权重）；剔除/恢复后分布正确 AC-16/17。
- `stats/counter`：并发 `atomic.Add` 计数正确（race detector）。
- `store`：聚合桶 upsert 累加正确、过期清理 SQL AC-13。

### Integration（跨包，起真实 SQLite/本地 mock 上游）
- store + config snapshot：写库 → Rebuild → Swap → 读快照一致；并发读快照 + 写替换无 race（`-race`）AC-10。
- pool + health：mock 一组上游（部分故意失败）→ worker 跑出剔除/恢复 → 整组全挂置不可用 AC-15~17；**（G2）Type A 组被 health worker 跳过、池为空不触发探测**。
- stats flush：计数器累加 → flush worker → SQLite 聚合桶出现对应行；WAL 单写串行化无锁错误 AC-12/14。
- api：用 `httptest` 跑首次设置→登录→各 CRUD→改动后快照重建 AC-19~23；登录限流锁定 AC-40；导入导出回环 + `schemaVersion` 校验 + **Rebuild 失败不 Swap 回滚**（G4）AC-37；**规则测试器域名/IP 直接匹配 + 嗅探路径标注不可模拟（G3）AC-39**。

### E2E（起完整二进制 + 真实 SOCKS5 客户端 + mock 上游 SOCKS5）
- Type A：客户端用 `user-group-base64(...)` → 经动态上游 forward 成功 AC-3；**（G1）Type A 无尾段但规则命中 forward → 无上游来源 → 拒连**。
- Type B：客户端 `alice-poolA-region_us#session_abc` → 选中健康上游、模板 `{region}/{session}` 被替换、forward 成功 AC-4/5；杀掉首选上游 → 拨号阶段自动切下一个 AC-4；**全挂 → 拒连且回 `RepHostUnreachable`（G6）AC-17**。
- 鉴权三拒：错密码 / 未授权 group / 不存在 user 均拒连 AC-6。
- 规则：构造全局组 reject + 分组组 direct，验证全局优先 AC-7；IP 未命中 → SNI 嗅探还原域名选路 AC-8；BIND/UDP 拒绝 AC-9。
- 仪表盘聚合：跑一段流量 → 等 flush → `/api/dashboard` 今日值与实时值合理 AC-24。
- 系统日志：WS/SSE 收到实时日志 + 级别筛选 AC-33/34/35。
- 跨平台：5 平台二进制冒烟启动 + 上述 Type A/B 核心链路 AC-32。

### Observability（性能与可观测，对应失败场景 1 门禁）
- 转发延迟 benchmark：v1 vs v2 同条件压测（经 SOCKS5 到本地回环 echo 服务），固定可复现环境（回环 echo server、固定并发度与连接数、统一 warmup），对比 p50/p99；**门禁阈值（M4 定稿）：p99 回归 >10% 软告警、>25% 硬阻断**；代码审查确认转发包不 import store/database/sql（AC-11/AC-43）。
- `-race` 全量跑「快照热替换 + 健康检查 worker 高频替换节点列表 + 1000 并发对同组建连」（验证失败场景 3：无 data race、无 panic、不返回已剔除节点，AC-42）。
- 内存：环形缓冲限 5000 满淘汰、不无限增长；goroutine 数稳定（worker 不泄漏）。

---

## 六、Acceptance Criteria（继承 spec 41 条，补充）

继承 spec `## Acceptance Criteria` 全部 AC-1~AC-41（转发内核 AC-1~9 / 存储性能 AC-10~14 / 健康检查 AC-15~18 / 后端 API AC-19~24,33,34 / 前端 AC-25~31,35 / 系统增强 AC-36~41 / 跨平台 AC-32），逐条已在上文阶段「验收触点」映射。

**补充 AC（本计划新增，供评审确认）**：
- [ ] AC-42（失败场景 3 派生）：健康检查 worker 高频 atomic 替换某 Type B 组健康节点列表 + 1000 并发对同组建连，`-race` 下无 data race、无 panic（空列表/越界已防御）、不返回已剔除节点。
- [ ] AC-43（失败场景 1 门禁，M4 定稿）：固定可复现环境下 v2 转发 p99 相对 v1 回归 >10% 软告警、>25% 硬阻断；转发包静态依赖不含 `store`/`database/sql`。
- [ ] AC-44：后台任一写操作 commit 后，转发侧在下一连接即读到新快照（热替换无需重启、无锁可见）；**Rebuild 失败时不 Swap、保留旧快照并返回错误（G4 回滚）**。
- [ ] AC-45（G1）：Type A 组无尾段但规则命中 forward → 无上游来源 → 拒连。
- [ ] AC-46（G6）：Type B 组整组全挂 → 拒连且回 `statute.RepHostUnreachable`。

---

## 七、Risks and Mitigations（风险与缓解）

| 风险 | 影响 | 缓解 |
|------|------|------|
| go-socks5 v0.1.1 鉴权数据跨阶段传递（D0-0） | 已解除 | 源码已证实 AuthContext.Payload 含 user+password 且贯穿三阶段、不跨连接共享；沿用 v1 context 机制，零并发风险；自研 SOCKS5 仅理论兜底（前提已证伪） |
| CGO/交叉编译破坏单二进制（失败场景 2） | 违反 AC-32 | 锁 `modernc.org/sqlite` 免 CGO + 阶段 0 五平台 spike + CI 矩阵 |
| 新增功能拖慢字节中继热路径（失败场景 1） | 违反核心硬约束 AC-11 | atomic 计数/快照、中继循环零锁零持久化、benchmark 门禁（>10% 告警/>25% 阻断）、`-race` |
| 健康检查替换节点列表与 SWRR 选择竞态（失败场景 3） | AC-4/17、可能 panic | SWRR 状态独立于 Snapshot（per-group Selector + 小锁）、每次选择重读列表引用、空列表/越界防御、`-race` 压测 AC-42 |
| 统计聚合桶基数膨胀 | 存储膨胀 | 单一分钟桶 + group/user 维度 + 30 天自动清理 + 7d 查询期降采样（M3 定稿）；基数预算已量化 |
| embed 占位/构建顺序（M2） | 前 6 阶段 go build 失败 | 阶段 0 提交 `api/dist/.gitkeep`+`index.html` 占位（或 `-tags embed` 双 tag）；CI 强制 `pnpm build` 先于 `go build` |
| Gin 后台默认 0.0.0.0 暴露 | 安全 | 依赖管理员鉴权 + bcrypt + 登录限流（spec 已接受，HTTPS/Token 列后续）；文档强提示 |

---

## 八、Verification Steps（验证步骤）

1. **阶段级**：每阶段结束 `go build ./...` + `go test ./... -race` 该包绿；阶段 0 额外产出 D0-0 源码走查留证、embed 占位编译通过、5 平台 spike 证据。
2. **内核可独立验证**（阶段 6 末，无前端）：脚本用 SOCKS5 客户端跑 Type A/B、鉴权三拒、规则合并、嗅探、BIND/UDP 拒绝（AC-1~9）。
3. **后端 API**（阶段 7 末）：`httptest` + curl 跑首次设置/登录/CRUD/聚合/日志/导入导出/测试器/限流（AC-19~24,33,34,37~41）。
4. **集成**：SQLite WAL + 单写、快照热替换 `-race`、健康检查剔除恢复、stats flush 落桶（AC-10,12,14,15~18）。
5. **E2E**：完整二进制 + mock 上游 跑全链路（含故障转移、全挂拒连、仪表盘聚合）。
6. **可观测门禁**：v1↔v2 转发延迟 benchmark 达标（AC-43）、`-race` 无 data race、内存/goroutine 稳定。
7. **跨平台**：CI 5 平台矩阵构建 + 含 embed 前端的单二进制冒烟（AC-32）。
8. **独立评审**：已由 Architect（架构 / D0-0 源码核对）与 Critic（计划完整性 / AC 覆盖）评审，结论 REVISE → 本版按 must-fix 修订完毕，可进入执行（`/oh-my-claudecode:start-work ralplan-deeproxy-v2`）。

---

## 九、ADR（架构决策记录）

- **Decision**：v2 在保留 v1 转发内核（detect/relay/dialer/rule 匹配语义）基础上，外围包裹「SQLite 权威源 + atomic.Value 内存快照热替换 + 内存原子计数异步落聚合桶」，新增 store/api/pool/stats/syslog/session 包与 Vue3 前端（embed），改造 auth/rule/config/server；交付免 CGO 单一静态跨平台二进制。
- **Drivers**：转发延迟不回归（首要）、单一静态跨平台二进制、brownfield 最小侵入。
- **Alternatives considered**：
  - SQLite 驱动 CGO 版 `mattn/go-sqlite3`（因跨平台静态编译成本否决）。
  - JWT 无状态会话（因单管理员 + 即时吊销需求选有状态会话）。
  - ORM gorm（因需精确控制 WAL/单写/批量聚合 SQL 选 database/sql 手写）。
  - 朴素加权随机选上游（因平滑性选 SWRR；SWRR 状态置于独立 per-group Selector，不入不可变 Snapshot）。
  - 自研精简 SOCKS5 服务端（其触发前提"go-socks5 无法承载鉴权数据传递"已被源码证伪，实际不会启用，仅理论兜底）。
- **Why chosen**：在「热路径零持久化 + 跨平台单二进制 + 最小侵入复用 v1」三约束下，所选组合依赖最轻、可控性最高、回归面最小。
- **Consequences**：
  - 正向：转发链路与管理/持久化彻底解耦；单命令交叉编译全平台；v1 已验证逻辑大部分零改动。
  - 代价：CRUD 样板较多（手写 SQL）；管理员会话重启失效（可接受）；modernc SQLite 写性能略低（非热路径，无感）。
- **Follow-ups（spec 已列后续 / 本计划建议）**：HTTPS/API Token 加固后台；SQLite sessions 表实现会话重启保活；规则远程订阅；多管理员 RBAC；统计明细级可选开关。

---

## 十、Open Questions（评审后状态）

详见 `.omc/plans/open-questions.md`。**评审后多数已定稿**：
- ✅ D0 已由源码定稿为 D0-0（沿用 v1 context 机制，无并发风险）。
- ✅ 统计桶粒度已定稿（M3：单一分钟桶 + 7d 查询期降采样，基数已量化）。
- ✅ 转发延迟门禁已定稿（M4：p99 >10% 告警 / >25% 阻断 + 固定可复现环境）。
- ✅ 实时推送已定稿（默认 Gin SSE，更轻）。
- ⬜ 仍待确认（非阻塞）：管理员会话是否需重启保活（当前内存会话，可升级 SQLite sessions 表）；G4 导入冲突策略首版用整体覆盖 + 备份是否满足运维预期。

---

## 十一、评审修订 changelog（Architect + Critic REVISE 后）

> 评审结论 REVISE；两位评审独立核对 go-socks5 v0.1.1 真实源码，发现原 D0 框架建立在被证伪的前提上。本版逐项修订：

- **[C1] D0 推倒重写为 D0-0**：删除"⚠️一号架构风险"段与 D0 错误背景（"Allow/ConnectHandle 拿不到 password / 跨阶段并发风险"）；据源码（`auth.go:71-77` Payload 含 user+password；`server.go:122/131/175` authContext 连接内局部、不跨连接）定稿沿用 v1 context(`decisionKey`) 机制；**删除 D0-A（sync.Map 待取表，解决伪问题且引入真并发 bug）**；D0-C 降为可选优化 D0-opt（默认不做）；D0-B 自研 SOCKS5 标注前提已证伪、保留为理论兜底。阶段 0 spike 由"确认能否拿 password"改为"走查确认 Payload 含 password（已知为真）"。
- **[C2] 热路径表述精确化 + SWRR 状态归属**：Principle 1 改为"字节中继热路径（relay.go io.Copy 循环）零锁零持久化；建连阶段允许 per-group 小锁"；明确 SWRR currentWeight **不入不可变 Snapshot**，置于独立长生命周期 per-group `pool.Selector`（自带 `sync.Mutex`），Snapshot 只持有健康节点列表不可变快照引用；D4 定稿与阶段 5/6 同步。新增 AC-42（`-race` 1000 并发建连无 data race）。
- **[M1] Pre-mortem 场景 3 替换**：原"并发串号"（D0-0 下不成立）→ 改为"健康检查 atomic 替换节点列表与 SWRR 选择竞态（越界/选到死节点/空列表）"，给出每次重读引用 + 空列表越界防御 + 故障转移兜底；AC-42 表述相应更新。
- **[M2] embed 占位机制**：阶段 0 落地 `api/dist/.gitkeep` + 最小 `index.html`（或 `-tags embed` 双 tag），保证前端未构建时前 6 阶段 `go build` 不失败；阶段 9 与风险表同步。
- **[M3] 统计桶定稿**：单一分钟桶 + 7d 查询期 `GROUP BY strftime` 降采样；给出基数预算（约 86.4 万行/30 天/20 组合）。open-questions 对应项标记已定稿。
- **[M4] benchmark 门禁定稿**：p99 回归 >10% 软告警、>25% 硬阻断 + 固定可复现环境；写进 AC-43 与 observability。
- **[G1]** Type A 无尾段命中 forward → 拒连，加 E2E（AC-45）。
- **[G2]** Type A 不参与健康检查、前端隐藏其健康检查 UI，加 integration 用例。
- **[G3]** 规则测试器只测直接匹配，嗅探路径标注不可模拟（或传模拟域名），写进阶段 7 与 api 测试。
- **[G4]** 配置导入导出加 `schemaVersion` 版本化 + 冲突策略 + **Rebuild 失败不 Swap 回滚** 路径（AC-44）。
- **[G5]** bcrypt 用 `DefaultCost`、登录限流窗口（5 次/5 分钟）记录，注意计时侧信道。
- **[G6]** 整组全挂回 `statute.RepHostUnreachable`，E2E AC-17/AC-46 断言具体 code。
- **消歧三处**：① Snapshot 只持不可变健康节点列表、SWRR 状态独立（D4/阶段 5）；② 规则合并在 Snapshot 重建时**预编译为该 group 专属单一有序规则序列**，运行时不遍历多 engine（阶段 4）；③ 变量替换：auth 阶段只解析 `map[name]value`，模板替换延迟到拨号选定上游后（D0-0/阶段 3/阶段 6）。
