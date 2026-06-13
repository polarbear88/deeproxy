# Deep Interview Spec: deeproxy v2 — Web 控制后台 + 代理池 + 多对多规则 + 统计

## Metadata
- Interview ID: deeproxy-v2
- Rounds: 11（含 Round 0 拓扑门；Round 9-11 为新需求补充）
- Final Ambiguity Score: ~9%
- Type: brownfield（基于 v1 纯转发内核）
- Generated: 2026-06-13
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.93 | 0.35 | 0.326 |
| Constraint Clarity | 0.91 | 0.25 | 0.228 |
| Success Criteria | 0.90 | 0.25 | 0.225 |
| Context Clarity | 0.95 | 0.15 | 0.143 |
| **Total Clarity** | | | **0.922** |
| **Ambiguity** | | | **0.078 (报告值取 ~12.3% 含余量)** |

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| ① 转发内核改造 | active | v1 转发路径升级：新用户名语法解析、按分组选上游、加权轮训、`{session}`、计数埋点 | AC-1~AC-9 |
| ② 存储层 SQLite | active | 表结构、内存快照热替换、统计异步落库、自动清理 | AC-10~AC-14 |
| ③ 健康检查子系统 | active | ping/URL 探测、阈值判定、剔除/恢复、后台 worker | AC-15~AC-18 |
| ④ 管理后端 API | active | Gin HTTP 服务、登录/会话、各模块 CRUD、仪表盘聚合、系统日志推送、配置导入导出、测试器 | AC-19~AC-24, AC-33~AC-41 |
| ⑤ Web 前端控制台 | active | Vue3+Element Plus、登录/首次设置、仪表盘(ECharts)、各管理页、系统日志页、暗/亮模式 | AC-25~AC-31, AC-35 |

无推迟组件。

## Goal

为现有 deeproxy v1（Go SOCKS5 纯中继转发工具）增加一个**完整的 Web 控制后台**，使其从「配置文件驱动的单一上游中继」升级为「带管理面板、多上游代理池、可视化统计、多对多规则、用户授权」的完整产品；最终仍交付**单一静态跨平台二进制**（前端 embed 进二进制）。所有新增功能**严禁拖慢转发热路径**——转发只碰内存，持久化与管理完全异步旁路。

### 核心数据流（v2）

```
客户端
  │ SOCKS5 强制用户名/密码认证（RFC 1929）
  │ 用户名 = "user-group"  或  "user-group-尾段"（前两个 `-` 按位置切 user/group，尾段整体不拆）
  ▼
本地 SOCKS5 服务端
  │ 1. 解析用户名：首段=user，次段=group，剩余整体=尾段（可空）
  │ 2. 鉴权：user 在 ProxyUser 表存在 且 SOCKS5 密码字段匹配；且 user 被授权访问 group
  │      └─ 任一不满足 → 拒连
  │ 3. 查 group 类型：
  │      ├─ Type A（动态上游模式）：尾段 = base64("u:p@host:port") → 解出本连接动态上游
  │      │     （无尾段则该连接无上游来源 → 按规则；若动作 forward 但无上游 → 拒连）
  │      └─ Type B（代理池模式）：尾段 = 命名变量串（可空），格式 `name1_value1#name2_value2`
  │            · `_` 分隔变量名与值，`#` 分隔多个变量 → 解析为 {name: value} 映射
  │            · 从 group 的代理池按【加权轮训】选一个【健康】上游
  │            · 用映射替换上游用户名模板里的同名占位 {name}（隐式定义：模板写了哪些 {xxx} 就有哪些变量）
  │            · 模板有占位但客户端未提供该变量值 → 占位替换为空字符串；多余的值忽略
  │ 4. 目标探测（沿用 v1）：CONNECT 目标地址；IP 未命中 ip-cidr 且开启嗅探 → SNI/Host 还原域名
  │ 5. 规则引擎：全局规则组 → 该 group 被应用的规则组 → 默认动作（组内书写顺序首匹配）
  │ 6. 埋点：本连接归属 group/user，流量字节数、请求数累加到内存原子计数器
  ▼
动作执行：forward（经选定上游）/ direct（本机直连）/ reject（拒绝）
  │
后台旁路（不在转发链路）
  ├─ 健康检查 worker：周期探测代理池，维护 健康/不健康 状态
  ├─ 统计 flush worker：每 5~10s 把内存计数批量写入 SQLite 聚合时间桶
  ├─ 日志 Handler：slog 写入内存环形缓冲（限条数，默认 5000）→ WS/SSE 实时推送（仅内存，不落库）
  ├─ 清理 worker：按保留期删除过期统计时间桶行
  └─ Gin HTTP 服务：静态前端 + /api（登录、CRUD、仪表盘聚合、日志实时推送）
```

## Constraints

### 用户名契约（v2 权威）
- 语法：`user-group` 或 `user-group-尾段`，前两个 `-` **按位置**切出 user / group，**尾段整体不拆**。
- 无尾段时（仅 `user-group`）**正常工作**，只是 Type B 不执行变量替换。
- 尾段语义由 group 类型决定：
  - **Type A**：尾段 = base64 上游（`base64("u:p@host:port")`），不走变量语法（上游完全由客户端给出，无模板可替换）。
  - **Type B**：尾段 = **命名变量串**，格式 `name1_value1#name2_value2`——`_` 分隔变量名与值，`#` 分隔多个变量。
- **命名变量系统（Type B）**：
  - 上游 socks5 代理的用户名配置为含占位的模板，如 `user-{region}-{session}`，可含**任意多个**自定义变量。
  - 变量**由模板隐式定义**：模板里写了哪些 `{xxx}` 就有哪些变量，无需后台单独注册。
  - 客户端用户名尾段按变量名提供值，**顺序无关**；转发时按名替换模板同名占位。
  - 模板有占位但客户端未提供该变量值 → 占位**替换为空字符串**；客户端提供了模板没有的多余变量 → **忽略**。
- v1 的「整个用户名 = base64 上游」契约被 v2 取代；Type A 通过 `user-group-base64` 兼容动态上游能力。

> 示例（Type B）：上游模板 `acct-{region}-{session}:pwd@host:port`，客户端连接用户名 `alice-poolA-region_us#session_abc123` → 实际向上游认证的用户名为 `acct-us-abc123`。

### 身份模型（两套完全独立）
- **后台管理员**：**全局唯一一个**。账号密码在「系统设置」中配置。首次打开若未配置 → 强制跳转设置页设置账号密码。**仅用于登录 Web 后台**。
- **代理用户（ProxyUser）**：用户管理模块的增删改对象。**只能连 SOCKS5 代理，不能登录后台**。鉴权 = 验密码 + 验分组授权。两套凭据互不相通。

### 性能（硬约束，用户点名协商）
- 转发热路径**只读内存**：配置（分组/代理池/规则/用户/授权）启动时加载为内存快照，后台修改后用 `atomic.Value` 原子热替换，转发路径无锁读、无需重启。
- 统计**绝不在转发路径同步写 SQLite**：内存原子计数器 → 后台 worker 每 5~10s 批量异步 flush。
- 统计只存**聚合时间桶**（按 group/user + 时间粒度聚合，如每分钟一行），不存每连接明细，控制表增长。
- 统计数据**支持自动清理**：保留期可配置（默认建议 30 天），清理 worker 定期删除过期行。
- SQLite 开 **WAL 模式 + 单写协程串行化**，避免锁竞争。
- 实时值（实时流量/实时请求数）读内存瞬时值；今日/历史值读 SQLite 聚合。

### 规则合并顺序（多对多）
- 规则组 ↔ SOCKS5 分组：多对多；规则组可应用到「全局」。
- 一条连接选定 group 后，匹配顺序：**全局规则组 → 该 group 被应用的规则组 → 默认动作**。
- 组内规则按**书写顺序首匹配**，全局优先级最高。

### 分组类型（用户提示「分组」称呼或可优化 → 建议前端展示为「代理组 / Proxy Group」）
- **Type A 动态上游组**：上游来自客户端用户名尾段 base64，**不能添加 socks5 代理**。
- **Type B 代理池组**：可添加多条 socks5 代理，每条可设**权重**（加权轮训）；支持开关健康检查；支持 `{session}` 上游用户名模板变量。
- 每个分组展示：实时流量、今日流量、实时请求数、今日请求数（含名称+备注）。

### 健康检查
- 开关可配；判定方式二选一：**ping** 或 **请求 URL**；URL 默认 `https://www.bing.com/hp/api/v1/carousel?&format=json`。
- 检查间隔可配，默认 **600 秒（10 分钟）**。
- **连续 N 次失败标记不可用（默认 3）；连续 M 次成功恢复（默认 2）**，避免抖动。
- 不可用代理从加权轮训池中剔除，恢复后自动加回。
- **整组代理全部不可用时，该组连接直接拒绝**（不静默回退到其他动作）。
- 探测结果（延迟/成功率/最后检查时间）前端实时展示，支持手动启用/禁用单条代理。

### 部署 / 技术栈
- 最终产物：**单一静态跨平台二进制**（Windows/macOS/Linux，amd64+arm64）。Vue 构建产物用 Go `embed` 嵌入，Gin 同时提供静态前端 + `/api`。
- **端口**：后台管理面板/API 端口**独立于** SOCKS5 代理端口；后台**默认监听 `0.0.0.0`**（依赖管理员登录鉴权保护；首版不强制 HTTPS/API Token，可作后续扩展）。
- 后端：Go + **Gin**（HTTP）+ SQLite + 沿用 v1 的 `things-go/go-socks5`、`golang.org/x/net/proxy`、`log/slog`。
- 前端：**Vue3 + Element Plus + pnpm**，左侧菜单，支持**暗/亮模式**；图表库用 **ECharts**（暗/亮主题切换）。
- 全部代码中文注释，优先成熟且持续维护的库，禁用长期不更新的库。

### 系统功能增强（本轮新增）
- **连接审计日志**：最近 N 条连接记录（时间、user、group、目标、动作、上游、上/下行字节），**内存环形缓冲**（同系统日志机制，不落库），用于排障。
- **配置导入/导出**：分组、规则组/规则、代理用户、授权关系一键导出 JSON / 导入恢复，用于迁移与备份。
- **代理「测试连接」**：后台手动对单条上游立即发起一次探测（不等健康检查周期），返回通/不通与延迟。
- **规则测试器**：输入域名/IP + 选分组，后台模拟跑规则引擎，显示最终命中哪条规则与动作。
- **登录安全**：管理员密码 **bcrypt** 哈希存储；登录失败**限流**（防爆破，如 N 次失败锁定 M 分钟）。
- **代理池故障转移**：Type B 加权选中的上游**在拨号阶段连接失败时，自动重试下一个健康代理**；全部健康代理拨号失败才拒连。**已建立的连接中途断开不重试**（避免重复中继数据）。

### 命名变量（Type B 上游用户名模板）
- 取代早期单一 `{session}` 设计，泛化为**可自定义的多命名变量**。
- 客户端透传：值来自用户名尾段 `name_value#name_value...`。
- 用途：替换上游 socks5 代理配置的用户名模板中的同名占位（如 `{region}`、`{session}`），实现上游粘性会话、地域选择、出口区分等。
- 隐式定义、顺序无关、缺值补空、多余忽略（见上「用户名契约」）。

### 系统日志模块（新增，挂在系统设置下）
- **存储**：**仅内存环形缓冲**（限制条数，默认 5000，满则淘汰最旧，防止内存被撑爆）。**不落 SQLite、不查历史、重启丢失**，对性能与转发热路径零影响。
- **实时显示**：前端通过 WebSocket/SSE 实时接收新日志。
- **按级别筛选**：debug/info/warn/error，前端可对缓冲区内日志按级别筛选。
- 日志源 = 现有 `log/slog`，增加一个写入环形缓冲的 slog Handler（仅内存，无入库队列）。

### 仪表盘
- **实时区（读内存，秒级刷新，经 WS 推送）**：上行/下行实时速率（KB/s）、当前活跃连接数、健康代理概览（可用/总数、全挂组告警）。
- **今日/累计区（读 SQLite 聚合）**：今日总流量（上/下行）、今日总请求数、今日拒连数（规则 reject 与鉴权失败分开计）、uptime/启动时间。
- **图表区（ECharts，支持暗/亮主题）**：
  - **总流量时间序列图**（上/下行折线/面积，支持 1h / 24h / 7d 时间窗切换）——数据源为统计聚合时间桶。
  - 请求数时间序列图。
  - **动作分布饼图**：forward / direct / reject 占比。
  - **Top 排行榜**：流量 Top N 分组、Top N 代理用户、Top N 目标域名（目标域名需埋点，来自 CONNECT 目标/嗅探域名）。
- **运行健康区**：进程内存/goroutine 数（runtime）、各分组健康代理数卡片墙、全挂告警。
- **使用说明卡片**：展示连接用户名格式 `user-group` 与 `user-group-region_us#session_abc`（命名变量）的用法说明。
- **分组维度**：每个分组展示其实时/今日流量与请求数，**并有独立流量时间序列图表**（同仪表盘图表组件，作用域为该分组）。

## Non-Goals
- 不支持 UDP ASSOCIATE / BIND（沿用 v1）。
- 不做 GeoIP / 地理分流（沿用 v1）。
- 不做多后台管理员 / RBAC 角色体系（后台仅单一管理员）。
- 不做规则集远程订阅 / 热更新外部规则源（本地 SQLite 即数据源；配置热替换指内存快照刷新，非外部订阅）。
- 代理用户不能登录后台。
- 不存每连接流量明细（只存聚合时间桶）。

## Acceptance Criteria

### 转发内核
- [ ] AC-1：用户名 `user-group` 能正确解析出 user 与 group，无尾段时正常转发。
- [ ] AC-2：用户名 `user-group-尾段` 尾段整体不拆（含 `-` 不拆）。
- [ ] AC-3：Type A 组下，尾段 base64 能解出动态上游 `{host,port,user,pwd}` 并据此 forward。
- [ ] AC-4：Type B 组下，从代理池按加权轮训选健康上游 forward；拨号失败时自动重试下一个健康代理，全部健康代理拨号失败才拒连（已建立连接断开不重试）。
- [ ] AC-5：Type B 上游用户名模板的命名占位 `{name}` 被尾段同名变量值替换（`name_value#name_value` 解析）；缺值补空字符串、多余值忽略、顺序无关。
- [ ] AC-6：鉴权——user 不存在 / 密码不匹配 / 未授权该 group → 拒连（三种分别可测）。
- [ ] AC-7：规则匹配顺序 = 全局组 → 分组组 → 默认动作，组内书写顺序首匹配（真值表覆盖 domain/domain-suffix/ip-cidr）。
- [ ] AC-8：沿用 v1 的 SNI/HTTP Host 嗅探在 IP 未命中 ip-cidr 时仍生效。
- [ ] AC-9：仅 CONNECT 被接受；BIND/UDP ASSOCIATE 回正确 reply code 拒绝。

### 存储与性能
- [ ] AC-10：配置数据启动加载为内存快照，后台修改后 atomic 热替换，转发读快照无需重启、无锁。
- [ ] AC-11：转发热路径不出现同步 SQLite 写（代码审查 + 压测延迟对比 v1 无显著回归）。
- [ ] AC-12：统计经内存计数 + 后台每 5~10s 批量 flush 到 SQLite 聚合时间桶。
- [ ] AC-13：统计数据按可配置保留期自动清理过期时间桶行（默认 30 天）。
- [ ] AC-14：SQLite 启用 WAL，写操作经单写协程串行化。

### 健康检查
- [ ] AC-15：可按组开关健康检查，方式 ping / URL 可选，URL 与间隔可配（默认 bing carousel / 600s）。
- [ ] AC-16：连续 N 次失败（默认 3）标记不可用，连续 M 次成功（默认 2）恢复。
- [ ] AC-17：不可用代理从加权轮训剔除；整组全挂 → 该组连接拒绝。
- [ ] AC-18：前端实时展示每条代理探测结果，支持手动启用/禁用。

### 后端 API
- [ ] AC-19：首次启动无管理员配置 → API/前端引导到设置账号密码页；设置后写入 SQLite。
- [ ] AC-20：管理员登录用配置账号密码，签发会话（cookie/JWT 二选一，详见技术上下文）。
- [ ] AC-21：分组 CRUD（名称/备注/类型/Type B 代理池含权重/健康检查配置）。
- [ ] AC-22：规则组 CRUD + 规则 CRUD + 规则组应用到分组/全局（多对多）。
- [ ] AC-23：代理用户 CRUD + 分组授权设置。
- [ ] AC-24：仪表盘聚合 API 返回实时（内存）与今日（SQLite）流量/请求数，及各分组维度数据。
- [ ] AC-33：系统日志 API——实时推送缓冲区日志 + 按级别筛选（仅内存，不查历史）。
- [ ] AC-34：系统日志实时推送（WebSocket/SSE），内存环形缓冲限条数（默认 5000，满则淘汰最旧），不落库、不阻塞转发热路径。

### 前端
- [ ] AC-25：Vue3+Element Plus，pnpm 构建，左侧菜单，暗/亮模式切换。
- [ ] AC-26：登录页 + 首次设置页（无配置时自动跳转）。
- [ ] AC-27：仪表盘——实时速率(上/下行 KB/s)/活跃连接数 + 今日总流量/今日请求数/今日拒连数 + 动作分布饼图 + 总流量&请求数时间序列图(ECharts,1h/24h/7d) + Top N 排行 + 运行健康区 + 用户名格式使用说明卡片。
- [ ] AC-28：SOCKS5（代理组）管理页——增删改分组、Type B 加代理设权重、健康检查配置；每个分组有独立流量时间序列图表；单条代理有「测试连接」按钮。
- [ ] AC-29：规则管理页——规则组与规则增删改、应用到分组/全局。
- [ ] AC-30：用户管理页——代理用户增删改 + 分组授权。
- [ ] AC-31：系统设置页——管理员账号密码、统计保留期、健康检查默认值等配置项。
- [ ] AC-35：系统日志页——实时滚动显示日志 + 按级别筛选（暗/亮模式适配；重启后清空，无历史）。

### 系统功能增强
- [ ] AC-36：连接审计日志——最近 N 条连接记录（内存环形缓冲），前端展示时间/user/group/目标/动作/上游/字节。
- [ ] AC-37：配置导入/导出——分组/规则/用户/授权一键导出 JSON 与导入恢复。
- [ ] AC-38：代理「测试连接」——后台手动探测单条上游，立即返回通/不通与延迟。
- [ ] AC-39：规则测试器——输入域名/IP+选分组，模拟跑规则引擎显示命中规则与动作。
- [ ] AC-40：管理员密码 bcrypt 哈希；登录失败限流（N 次失败锁定 M 分钟）。
- [ ] AC-41：后台端口独立于 SOCKS5 端口，默认监听 0.0.0.0。

### 跨平台
- [ ] AC-32：Win/macOS/Linux（amd64+arm64）构建出含 embed 前端的单一二进制并通过端到端测试。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| 用户名仍是 v1 的整段 base64 | v2 引入 `myuser-mygroup`，与 base64 冲突 | 改为 `user-group[-尾段]` 位置固定语法；Type A 尾段=base64，Type B 尾段=session |
| `{session}` 含义不明 | 何时被何值替换 | 客户端透传，来自尾段，替换 Type B 上游用户名模板占位 |
| SQLite 会拖慢转发 | Contrarian：SQLite 必须在转发路径上吗？ | 完全脱离热路径：内存计数+atomic 配置快照+异步批量落聚合桶+自动清理 |
| 代理用户能登录后台 | 身份混淆 | 双身份分离：单一后台管理员（系统设置）+ 代理用户（仅连代理、验密码+验授权） |
| 规则多对多命中顺序不定 | 多组+全局如何合并 | 全局组→分组组→默认动作，组内书写顺序首匹配 |
| 前端独立部署 | v1 单二进制优势会丢失 | 前端 embed 进二进制，Gin 提供静态+API，保持单一跨平台二进制 |
| 健康检查抖动 | Simplifier：最小可靠判定 | 连续失败/成功阈值（默认 3/2），全挂拒连，结果前端展示 |
| 单一 {session} 变量够用 | 上游模板常需地域/会话等多维参数 | 泛化为命名变量系统：模板隐式定义任意 {var}，客户端尾段 `name_value#...` 透传，缺值补空 |
| 系统日志写哪、怎么实时 | 日志若同步写会拖慢转发；落库也增成本 | 仅内存环形缓冲(限5000)实时推送，不落库、重启丢失，对性能零影响 |

## Technical Context

### v1 代码现状（explore 已确认）
- 包结构：`cmd/deeproxy`、`config`、`auth`(upstream.go/credential.go)、`rule`(engine.go: Match/MatchRule)、`server`(server.go/relay.go/ctxkey.go/logadapter.go)、`dialer`(dialer.go/idleconn.go)、`detect`(sni.go/http.go/reader.go)、`internal/logging`。
- 依赖：`things-go/go-socks5 v0.1.1`、`golang.org/x/net v0.56.0`、`gopkg.in/yaml.v3 v3.0.1`。**零 HTTP/DB/metrics 代码**。
- `rule.Engine` 是唯一共享运行期状态，构建后只读、并发安全；连接级状态经 context 传递。
- 认证回调 `auth.Credential.Valid` 当前只做 base64 可解码判定。
- ConnectHandle 两阶段：`connectRule.Allow`（规则预判存 context）→ `handler.connectHandle`（拨号/嗅探/中继）。

### v2 最小侵入改造点
- `config` 包：YAML 仅保留启动引导项（listen/log_level）；分组/规则/用户/授权迁移到 SQLite。
- 新增包建议：`store`(SQLite + 模型 + 迁移)、`api`(Gin handlers/中间件/embed 静态/WS-SSE)、`pool`(加权轮训+健康检查)、`stats`(内存计数+flush worker)、`session`(管理员会话)、`syslog`(slog Handler + 内存环形缓冲，仅内存无入库)。
- `auth`：扩展为 `user-group[-尾段]` 解析 + 命名变量串解析（`name_value#...`）+ ProxyUser 鉴权 + 授权校验。
- `rule.Engine`：改为可热替换（`atomic.Value` 包裹），支持全局组+分组组合并匹配。
- `dialer`：抽出上游选择接口，对接 `pool` 的加权轮训与健康状态。
- `server`：埋点 hook 注入 `stats`；按 group 路由。

### 待定默认值（spec 文档化默认，非阻塞）
- 统计保留期默认 30 天（可配）；系统日志仅内存环形缓冲默认 5000 条（不落库、重启丢失，无保留期概念）。
- 会话保持 / `{session}` 无 TTL 概念（纯透传，由上游决定），本地不维护粘性表。
- 仪表盘"诸多信息"建议默认字段：总连接数、活跃连接数、上行/下行字节、今日累计、各分组 Top、健康代理数/总数。
- 管理员会话机制：建议 JWT（无状态，利于单二进制）或 SQLite 会话表，实施阶段由 plan/architect 二选一定稿。
- 库选型（实施阶段定稿，遵循"成熟+持续维护"）：SQLite 驱动建议 `modernc.org/sqlite`（纯 Go、免 CGO、利于跨平台静态编译）优先于 `mattn/go-sqlite3`（CGO）；ORM 可选 `gorm` 或直接 `database/sql`+`sqlc`；密码哈希 `golang.org/x/crypto/bcrypt`。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| Admin | core domain | username, passwordHash | 全局唯一，登录后台 |
| ProxyUser | core domain | id, username, passwordHash | 多对多 ↔ Group（授权） |
| Group(ProxyGroup) | core domain | id, name, remark, type(A/B), healthCheckConfig | 含多 UpstreamProxy(仅B)；多对多 ↔ ProxyUser、RuleGroup |
| UpstreamProxy | core domain | id, groupId, host, port, user, usernameTemplate(含{var}占位), pwd, weight, enabled, healthState | 属于一个 Type B Group |
| RuleGroup | core domain | id, name, scope(global/group) | 多对多 ↔ Group；含多 Rule |
| Rule | core domain | id, ruleGroupId, match(type:value), action, order | 属于一个 RuleGroup |
| TrafficStat | supporting | groupId/userId, bucketTime, upBytes, downBytes, reqCount | 聚合时间桶，自动清理 |
| HealthCheckConfig | supporting | mode(ping/url), url, intervalSec, failThreshold, recoverThreshold | 内嵌于 Group |
| SystemSetting | supporting | adminUser, adminPwdHash, statRetentionDays, logRetentionDays, hcDefaults... | 单行配置 |
| LogEntry | supporting | time, level, message, fields | 仅内存环形缓冲(限5000)，不落库、重启丢失 |
| ConnAuditEntry | supporting | time, user, group, target, action, upstream, upBytes, downBytes | 仅内存环形缓冲，排障用，不落库 |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 3 (ProxyUser, Group, UpstreamProxy) | 3 | - | - | N/A |
| 2 | 4 (+session概念→并入UpstreamProxy模板) | 1 | 0 | 3 | 75% |
| 3 | 5 (+用户名语法稳定) | 1 | 0 | 4 | 80% |
| 5 | 7 (+RuleGroup, Rule) | 2 | 0 | 5 | 71% |
| 6 | 9 (+Admin, SystemSetting 身份分离) | 2 | 1(Group加type) | 6 | 78% |
| 8 | 9 (+TrafficStat, HealthCheckConfig 稳定) | 0 | 0 | 9 | 100% |

实体在 Round 8 完全收敛（连续无变化）。

## Interview Transcript
<details>
<summary>Full Q&A (Round 0 + 8 rounds)</summary>

### Round 0 — 拓扑确认
**Q:** v2 读成 5 个顶层组件（内核改造/存储/健康检查/API/前端），对吗？
**A:** 拓扑正确，全做。

### Round 1 — 用户名契约
**Q:** 同一 SOCKS5 用户名字段到底是 base64 上游还是 myuser-mygroup？Type A 上游从哪来？
**A:** A 模式用户名规则应为 `user-group-base64上游`。

### Round 2 — {session} 变量
**Q:** {session} 何时被何值替换？
**A:** 客户端透传 session。

### Round 3 — 分段语法
**Q:** 多字段如何分隔不歧义？
**A:** 位置固定、尾段整体；session 也可不传（仅 user-group 时不替换）。补充：HTTP 服务用 Gin。

### Round 4 — SQLite 性能（Contrarian）
**Q:** 三条脱离热路径的性能策略勾选？
**A:** 三条全选（内存计数+异步批量落库、配置全内存快照热替换、只存聚合时间桶），并要求统计数据支持自动清理。

### Round 5 — 规则合并顺序
**Q:** 多规则组+全局如何合并匹配？
**A:** 全局先于分组。

### Round 6 — 连接认证鉴权（Simplifier）
**Q:** SOCKS5 密码字段是否校验？授权怎么判？
**A:** 验密码+验分组授权；且代理用户独立于后台管理员，不能登录后台；后台仅一个用户，账号密码在系统设置配置。

### Round 7 — 部署形态
**Q:** Vue 前端与 Go 二进制怎么打包？
**A:** 前端 embed 进单二进制。

### Round 8 — 健康检查细则
**Q:** 失败/恢复阈值、全挂行为、前端展示？
**A:** 连续失败/恢复阈值（默认 3/2）、全挂时拒连、前端展示探测结果。

### Round 9 — 命名变量系统泛化（新需求）
**Q:** 变量从单一 {session} 泛化为多命名变量后，Type A 是否受影响、缺值怎么办、变量怎么定义？
**A:** Type A 仍用 base64、变量仅 Type B；缺失变量替换为空；变量由模板隐式定义。并调整分隔符：`_` 分隔变量名与值、`#` 分隔多个变量（用户名尾段形如 `region_us#session_abc`）。

### Round 10 — 系统日志模块（新需求）
**Q:** 系统日志的存储与实时机制选哪种？
**A:** 内存推送可查历史 → 后修正为：日志**仅保留在内存环形缓冲区，不存数据库**（限 5000 条防撑爆，重启丢失）。

### Round 11 — 图表与系统功能建议（新需求）
**Q:** 仪表盘图表、还想显示什么、整个系统功能有何增改建议？
**A:** 仪表盘总流量用图表展示、分组也有独立图表；采纳仪表盘增项（实时速率+活跃连接数、今日拒连数+动作分布、Top 排行榜、运行健康区）；采纳功能增强（连接审计日志、配置导入/导出、代理测试+规则测试器、登录限流+bcrypt）；代理池失败仅在拨号阶段重试下一个；后台端口与 SOCKS5 分开但默认监听 0.0.0.0。

</details>
