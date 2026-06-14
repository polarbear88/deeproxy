# RALPLAN: deeproxy V2 批量修复与增强（共识精炼计划）

- Status: **PENDING APPROVAL**（共识达成：Architect SOUND + Critic APPROVED-WITH-NITS，2 nits 已折叠）
- Mode: consensus / deliberate (high-risk: auth、config 事务化、relay 核心)
- Source spec: `.omc/specs/deep-interview-v2-batch-fixes.md`
- Generated: 2026-06-14
- Final ambiguity (interview): 13%

---

## RALPLAN-DR Summary

### Principles (5)
1. **快照不可分裂**：DB 权威数据与转发内存快照永远一致；坏配置绝不持久化落库。
2. **热路径零回退**：转发/选路热循环不得因任何新功能增加 per-byte 工作或锁；分页/池化只作用于管理面与探测面。
3. **代码即权威**：所有面向用户的格式/文案以 `auth/authz.go` 实测的 `user-group[-tail]` 为准，反向修正漂移文档。
4. **复用既有原语**：优先复用已存在的 `group_user`/`handleSetUserGroups`/内存 `Counter`/`snapshot.Holder`，不重复造轮子（遵循全局规范）。
5. **可测可验**：每条改动有对应自动化测试或可复现的端到端验证步骤；安全/核心项强制回归测试。

### Decision Drivers (top 3)
1. **正确性优先于便利**：授权语义、未授权日志、relay 不断下载 —— 行为正确是第一位。
2. **规模化**：单池数千上游必须管理流畅、探测不被串行拖垮。
3. **最小侵入热路径**：核心转发代码改动面尽量小且有回归保护。

### Viable Options（关键架构决策）

#### DEC-A 配置发布"写前校验"（AC-5.1, codex HIGH）— **Architect 修订：A2 否决，采用 A1**
- **Option A1 — Validate-before-commit（候选配置预编译，选定）**：写 DB **之前**，用"当前已提交 specs + 本次待写增量"在内存构建候选 `[]RuleGroupSpec`，跑 **`rule.BuildGroupEngine`（rule/merge.go:92，非 `Rebuild`）** 编译校验；通过才 `Write` 然后 swap；不通过直接返回错误、DB 不写。
  - Pros：规则编译类坏配置永不落库（满足 Principle 1 的核心诉求）；**复用已存在的 `BuildGroupEngine` 校验器**（Principle 4），不触碰 store 读层、不引入 Tx；回滚天然（没写就无需回滚）。
  - Cons：每个写 handler 需构造"当前+待写"的内存候选 specs；候选用与写入相同的 `store.*`→spec 映射构建，drift 近零；`rebuildMu`+单写协程已串行化写→rebuild，TOCTOU 窗口在实践中已被互斥关闭。
  - **范围边界（诚实声明）**：A1 只校验**规则引擎编译**；FK/唯一性/跨实体引用（如规则引用已删除分组）由 SQLite 约束在写入时拒绝，不由候选校验覆盖。若 Principle 1 需扩展到这类约束，仍由"写失败即不 swap"保证不分裂（写都没成功）。
- **Option A2 — DB 事务内 rebuild（已否决，Architect 判定不可行）**：`store` 为单写协程 + `SetMaxOpenConns(1)` + 所有读直连 `s.db`（`store/db.go:30-55`），`Rebuild` 经 `st.List*()` 读已提交库（`snapbuild/rebuild.go:79-125`），**无任何 Tx 读取能力**。A2 需把约 15 个 repo 读方法 + `Rebuild` 签名全部改造为接受 `*sql.Tx`，远超"集中在包装层"的承诺，且把回归风险压到核心转发数据源上。
  - **否决理由**：违反 Principle 4（丢弃可用的 `Rebuild(st,cfg)` 校验器去做全读层重写）；"wrapper-only" 范围声明被证伪。
- **选定：A1（写前候选校验）**。A2 记为**未来增强**，前置条件：先把 repo 读改造为 querier-agnostic（`type querier interface{ Query/QueryRow }`，`*sql.DB` 与 `*sql.Tx` 均满足）——该重构是 A2 的诚实前置项，列入 Follow-ups。
- **保持现状（写后 rebuild 失败不回滚）**：否决——违反 Principle 1。

#### DEC-B "全部代理组"持久化（AC-1.2, D1）
- **Option B1 — `proxy_user.all_groups BOOLEAN`**：用户级通配标志，快照 `IsAuthorized` 命中标志直接放行。
  - Pros：覆盖未来新组；O(1)；与现有 authz map 叠加简单。
  - Cons：新增列 + 迁移；快照需带 per-user all_groups 集合。
- **Option B2 — 保留哨兵 group_user 行（group_id=0 表示全部）**：不改 schema。
  - Pros：无迁移。
  - Cons：语义晦涩、易与真实 group 混淆、查询特例多。否决。
- **选定：B1**。

#### DEC-C 健康检查并发（AC-5.3, codex 必改）
- **Option C1 — 全局共享 worker pool（信号量/固定 worker）**，默认 150，设置可调，所有分组所有探测共用，per-probe context。
  - Pros：满足用户明确要求；总并发可控；不被单组拖垮。
  - Cons：需在 HealthChecker 注入池并支持热调整大小。
- **Option C2 — per-group bounded concurrency**：每组独立并发上限。
  - Cons：用户明确否决"分组划分"；全局上限不可控。否决。
- **选定：C1**。复用成熟库：`golang.org/x/sync/semaphore` 或 `errgroup.SetLimit`（Go1.20+）。**热调整范围（Architect 修订）**：池大小存原子/带锁字段，**在每轮 `scanOnce`（health.go:186）开头重新读取**即可生效（"下一轮采用新大小"），**不做在途信号量的实时 resize**，避免过度工程。`applyResult` 已持 `h.mu`（health.go:276），并发探测改 `h.states` 安全。

#### DEC-D relay 半开（AC-5.2, D4）
- **Option D1 — 仅出错关两端，正常 EOF 保活**：任一方向 `io.Copy` 返回非 nil error → 关闭两端 conn（解除另一方向阻塞读），cancel 另一 copy；正常 EOF 不动另一方向。
- **选定：D1**（用户确认）。**实现要点（Architect 修订）**：当前 `relayCounted`（relay.go:103-133）**已经**"等待双向 + 半关 CloseWrite"，正常 EOF 保活已满足；**唯一新增的是出错路径**——任一方向 `io.Copy` 返回非 nil error 时，立即关闭两端 conn（含目标 `upConn`，在调用方 `server/server.go:261/376` 在作用域内）以解除另一方向阻塞读、即时回收 fd。**严禁改成"首个完成即返回"**（会重新引入 relay.go:52-54 注释已修复的截断 bug）。语义：**等双向，但首个 error 关两端**。

### 单一主循环权威
本计划为**纯计划产物**，不含执行循环；执行授权在共识完成后单独获取（team/ralph 二选一）。无 `/goal`/Ralph/Team 冲突。

---

## Requirements Summary
落实 6 个顶层组件、29 条验收标准（见 source spec：AC-1.x×5 + AC-2.x×6 + AC-3.x×5 + AC-4.x×4 + AC-5.x×5 + AC-6.x×4）。本计划按"核心健壮性 → 后端能力 → 前端体验"分层，最大化并行、隔离高风险改动。

## Work Breakdown（按可并行的任务包）

### WP-0 数据层与迁移（前置，阻塞 WP-1/WP-3/WP-4）
- **T0.0 迁移原语（Architect 新增，前置 T0.1）**：`store/schema.go:12-25` 的 `migrate()` **当前仅执行 `CREATE TABLE IF NOT EXISTS`，无任何 `ALTER TABLE`/`ADD COLUMN`/`PRAGMA table_info`**。须先建 `columnExists(db, table, col)`（走 `PRAGMA table_info`）+ 幂等 `ALTER TABLE ADD COLUMN` 守卫（裸 ADD COLUMN 二次启动会报错）。WP-0 关键路径。
- T0.1 `store/schema.go`：经 T0.0 守卫为 `proxy_user` 增 `all_groups INTEGER NOT NULL DEFAULT 0`；`system_setting` 增 `server_addr TEXT DEFAULT ''`、`probe_pool_size INTEGER NOT NULL DEFAULT 150`。
- T0.2 `store/models.go`：`ProxyUser.AllGroups bool`；`SystemSetting.ServerAddr string`、`ProbePoolSize int`。
- T0.3 repo 读写更新（`proxy_user_repo.go`、settings repo）。
- 验收：迁移在旧库幂等执行；新字段读写往返正确。

### WP-1 用户授权（Component 1）— 依赖 WP-0
- T1.1 后端：`handleSetUserGroups` 扩展接受 `{allGroups: bool, groupIds: []}`。**数据语义钉死为"并存"（Critic 修订）**：`all_groups` 是独立布尔标志，**永不清空 `group_user` 精细行**；切换 all_groups OFF 后用户原有逐组授权完整保留。`IsAuthorized = all_groups 命中 OR 精细行命中`。补测试：all_groups 开→存→关→存，原精细授权不丢。
- T1.2 快照：`snapbuild/rebuild.go`（:224-232 区）materialize per-user `all_groups` 集合；**修改点钉死在 `snapshot.IsAuthorized`（snapshot.go:162）**：先查 all_groups 集合命中直接 true，再查精细 authz map。**因 Valid phase 与 Allow phase（server.go:85 → `auth.ParseOnly` → `parse` → 同一 `IsAuthorized`，authz.go:84）共用此函数，改这一处即两路径同时生效**；禁止只在 Valid 路径加 all_groups 短路。
- T1.3 未授权日志（AC-1.5）：将 `auth/authz.go` 的 `AuthError{Reason:"用户未授权访问该分组"}` 冒泡到服务端连接日志。**关键（Architect 修订）**：库 `go-socks5 auth.go:60-64` 只看 `Valid` 的 bool 返回、丢弃 reason，`server.go:152` 包成泛化错误。**唯一正确日志点 = `auth/credential.go:48` 的 `Credential.Valid` 返回前**（此处 `*AuthError` 与解析出的 user/group 仍在手）。**禁止**在 `parse`/`ParseOnly` 里打印——Allow 路径也调它，会对每条连接重复日志。
- T1.4 前端：用户管理操作区独立"设置授权分组"按钮 + 独立弹窗（与编辑用户分离，AC-1.1）；弹窗含"授权全部"开关 + 逐组多选；保存后正确回显（AC-1.3 修复"仍显示未授权"）。
- T1.5 端到端验证（AC-1.4）：授权后已授权组放行、未授权组拒绝。

### WP-2 转发/认证核心健壮性（Component 5）— 高风险，**单负责人串行 lane（Architect 修订）**
> WP-2 改 `server/server.go`（relayCounted + 调用方 261/376）与 `pool/health`，触及最热文件。**必须单负责人串行执行、单独 review**，不与触及 `server.go` 的其他工作并行，避免热文件合并冲突。
- T2.1 DEC-A1 写前候选校验（AC-5.1）：写 DB **前**，用"当前已提交 specs + 本次待写增量"在内存构建候选 `[]RuleGroupSpec`，喂给 **`rule.BuildGroupEngine(candidateGlobalSpecs, candidateGroupSpecs, def)`（rule/merge.go:92）** 编译校验——**注意是 `BuildGroupEngine`，不是 `Rebuild`**（`Rebuild` 只读已提交库、无候选入口）。**A1 守护范围明确 = 规则编译类坏配置**（非法 match/action 等）；其余 FK/唯一性/跨实体约束由 SQLite 写入时约束兜底（可接受，记为已知边界）。校验通过才 `Write`+swap，不通过返回错误、DB 不写。补"坏规则不落库"回滚测试。
- T2.2 DEC-D relay 半开（AC-5.2）：**保持 `relayCounted`（relay.go:103-133）现有"等双向 + 半关 CloseWrite"结构不变**（`<-upc` 后 `<-downc`）。**仅新增出错路径**：任一方向 `io.Copy` 返回非 nil error 时，**在 `relayCounted` 内部（join 逻辑 relay.go:126-132 处）直接关闭两端 conn**（含目标 `upConn`，作用域见 server.go:261/376）——**不能只靠调用方 `defer upConn.Close()`（server.go:249/364），那在 `relayCounted` 返回后才触发、为时已晚**。字节计数在 line 128 已先于 error 检查赋值，关闭不影响计数。**正常 EOF（io.Copy 返回 nil）绝不触碰另一方向**。**严禁 select-首完成即返回**（会重现截断 bug）。**关闭客户端端实现细节**：`relayCounted` 签名 `(clientW io.Writer, clientR io.Reader, target net.Conn)`，`clientW` 非 `net.Conn` 无 `Close()`；复用本文件既有 peek 类型断言模式（server.go:446-464）`clientW.(net.Conn)` 取底层 conn 关闭，或仅关 `target`（upConn）即可解除读 upConn 的 downc 方向阻塞、客户端 conn 由 SOCKS5 库在 handler 返回时关闭。**关闭须在 goroutine 侧/不阻塞地触发**（不能先等阻塞 channel 再关，否则无法解除阻塞读）。补回归测试：①上传先 EOF、下载持续 5s 不被中断；②一方向 error → 两端 fd 即时回收无泄漏。
- T2.3 DEC-C 健康检查全局池（AC-5.3）：`probeGroup`（health.go:250）串行→受池限制并发；池大小读 `probe_pool_size`，支持热调整；per-probe context。
- T2.4 域名规范化（AC-5.4）：`rule/engine.go` 入库与匹配前统一小写+去尾点（`MatchRule` host + 规则 pattern）。补大小写/尾点用例。
- T2.5 实时速率改内存计数（AC-5.5）：`dashboard_handler.go` rateSampler 改用 `stats.Counter` 累计快照；SQLite 仅历史。

### WP-3 代理池上游（Component 3）— 依赖 WP-0
- T3.1 后端批量添加（AC-3.1/3.2）：新增 `POST /groups/:id/upstreams/batch`，解析多格式，逐行容错，返回成功数 + 失败行号/原因。**新建独立解析器 `pool/parse`（Critic 修订：`auth.DecodeUpstream` 是 base64/@-only，不可复用）**。**格式消歧规则钉死**：① 含 `@` → 按 `user:pass@host:port`（`@` 后 `SplitHostPort`，`@` 前第一个 `:` 分 user/pass）。② 不含 `@` 的 `user:pass:host:port` → **从右起取最后两段为 host:port**，其余左侧按**第一个** `:` 分 user / pass（pass 内可含 `:`，但**该形式下 username 不能含 `:`**；需含 `:` 的 username 用 `@` 形）。③ **IPv6 host 必须用 `@` 形或方括号** `[::1]:port`；裸 IPv6 冒号形视为非法行并报错。补单测覆盖：pass 含 `:`、IPv6、缺字段。
- T3.2 后端分页（AC-3.3）：`handleListUpstreams` 加 `page/pageSize/keyword/healthState`，SQL LIMIT/OFFSET，返回 total。
- T3.3 后端批量改权重/启用（AC-3.4）：新增批量 endpoint，两模式。**执行策略钉死为单写操作（Critic 修订，现仅有 per-id 原语）**：新增 repo 原语——筛选模式 `UPDATE upstream_proxy SET weight=?/enabled=? WHERE group_id=? AND <keyword/health 筛选>` **一条 SQL**；id 列表模式 `WHERE id IN (...)`（按 SQLite 参数上限分块）。**一次写操作，非 N 次**（避免单写协程 `writeCh` 串行 3000 次的管理面停顿）。批量更新对匹配行原子（事务内 all-or-nothing）。补断言：操作数=O(1) 而非 O(rows)。
- T3.4 前端（AC-3.1/3.3/3.4）：抽屉内表格服务端分页（默认100）、多选、跨页全选、批量设权重/启用、批量添加文本框。
- T3.5 性能评估（AC-3.5）：评估数千上游对选路/内存/探测的影响，结论写交付说明。

### WP-4 系统设置与连接提示（Component 4）— 依赖 WP-0
- T4.1 后端：`PUT /settings` 接受 `server_addr`、`probe_pool_size`；首次默认自动探测本机非回环 IP。**新增 IP 探测 util（`net.InterfaceAddrs`，放 `utils/`/`common/`，Critic 修订：无现成 util 可复用）**。**多网卡/容器消歧**：取第一个非回环 IPv4；探测失败或多址时回退空串由用户手填（此值仅作提示文案，非绑定）。
- T4.2 后端：暴露监听端口来源（SOCKS5/Web 端口）给前端（dashboard overview 或 settings）。
- T4.3 前端首页：显示监听端口（AC-2.6）+ 具体连接示例（AC-4.2，真实格式）。
- T4.4 前端用户管理：每用户"复制代理地址"按钮（AC-4.3），复制 `socks5://<user>-{group}:<pwd>@<server-addr>:<socks5-port>`。
- T4.5 前端系统设置（AC-4.4，Critic 新增）：系统设置页新增"健康检查协程池大小"输入字段（默认 150），与"服务器域名/IP"字段，绑定 `PUT /settings`。

### WP-5 仪表盘与图表（Component 2）— 前端，独立可并行
- T5.1 卡片等高（AC-2.1）：el-row flex stretch / el-card height:100%。
- T5.2 暗色图表背景（AC-2.2/2.5）：注册自定义 ECharts 主题或显式 `backgroundColor:'transparent'` + 文本/轴色随主题；覆盖仪表盘 + 分组流量抽屉图。
- T5.3 饼图黑边（AC-2.3）：`itemStyle.borderColor` 由 `var(--el-bg-color)` 改为运行时解析的真实色值（读 computed style 或主题色常量）。
- T5.4 主题切换错乱（AC-2.4）：`EChart.js` 增 `onActivated` → resize/重建；隐藏容器零尺寸保护。
- T5.5 验证明暗 + 跨页切换无错乱。

### WP-6 次要修复（Component 6）— 独立可并行
- T6.1 router 初始化判断（AC-6.1）：`router/index.js:84` 反转条件/加 `initChecked`，仅未确认时查一次。
- T6.2 crypto/rand（AC-6.2）：`session.go:77` 不忽略错误，失败登录走 500，删除错误注释。
- T6.3 feature-status（AC-6.3/6.4）：维持 `top?kind=domain` 未实现契约 + 前端占位；建立权威 feature-status 表；修正 `CLAUDE.md` 漂移的用户名格式与未实现项。

## 任务依赖与并行
- 前置：**WP-0** 必须先完成，其中 **T0.0 迁移原语 → T0.1 列变更** 顺序固定。
- WP-0 完成后：**WP-1、WP-3、WP-4 可并行**（共享数据层但触及不同 handler）。
- **WP-2（核心健壮性）= 单负责人串行 lane**，单独 review（高风险）；因其编辑 `server/server.go` 热文件，**不与任何同样触及 `server.go` 的工作并行**；与纯前端/纯 pool 以外的后端 handler 工作错峰。
- **WP-5、WP-6（前端）完全独立**，全程可并行。
- 合并点：WP-4 的连接示例依赖 WP-1 的真实授权格式（文案一致性）。

## Risks and Mitigations
| 风险 | 缓解 |
|------|------|
| 配置事务化重构破坏现有 rebuild 调用方（多处 handler） | 保持 `rebuildAndSwap(c)` 外部签名不变，仅内部改为事务化；全 handler 回归 |
| relay 改动引入下载被切断（用户最在意） | 严格按 D4：仅 err 关两端；新增"大下载 + 上传先结束"回归用例必须通过 |
| 健康池热调整并发安全 | 池大小用原子/带锁 setter；探测任务通过 channel/semaphore，不共享可变状态 |
| all_groups 与精细授权语义冲突 / 切换丢数据 | "并存"语义：all_groups 独立标志永不清精细行；IsAuthorized = all_groups OR 精细；补 toggle-off 不丢测试 |
| schema 迁移在旧库失败 | T0.0 `columnExists`(PRAGMA table_info) 守卫；ADD COLUMN 幂等；3 列逐列守卫，单列失败不影响已加列（重启续加） |
| 跨页全选批量更新在数千行下停顿 | 单条 `UPDATE...WHERE 筛选` / `WHERE id IN(分块)`，O(1) 写操作；断言操作数不随行数增长 |
| 批量解析格式歧义（pass 含`:`/IPv6） | 钉死消歧规则（@优先、右起两段 host:port、IPv6 必须 @或方括号）；非法行报错不静默 |
| ECharts 自定义主题与 Element Plus 变量不同步 | 主题色从 CSS 变量运行时读取，theme watch 重建 |

## Verification Steps
1. `go build ./...` + `go vet ./...` 全绿。
2. `go test ./...`：新增 relay 半开、config 回滚、域名规范化、批量解析、health 并发用例通过。
3. 前端 `npm run build` 成功；组件测试断言：饼图 `itemStyle.borderColor` 解析为真实色值（非 `var(...)`)、卡片等高、复制按钮产出字符串格式正确、分页请求带 page/pageSize。其余明暗主题/跨页切换图表为人工验证补充。
4. 端到端：授权后已授权组放行/未授权组拒绝并打印正确日志；连接示例可直接 `curl -x` 成功。
5. 迁移幂等：对旧 DB 启动两次无报错。

## Pre-mortem（deliberate，3 场景）
1. **「下载被切断」回归**：relay 改动误判正常 EOF 为出错 → 大文件下载在上传结束后断。**预防**：用例覆盖"上传先 EOF、下载持续 5s"；只在 `err!=nil` 分支关两端，EOF(`io.Copy` 返回 nil)绝不触发。
2. **「坏规则仍落库」**：A1 候选校验遗漏某 handler（如 rule_handler 仍走旧"写后 rebuild"路径）→ DB/快照分裂复现。**预防**：统一所有写 handler 走同一"写前校验"封装；grep 全部 `a.store.Create/Update/Delete` 调用点（约 23 处）核对均已切换。
3. **「授权改完仍未授权」未根治**：前端回显修了但快照 materialize 漏 all_groups → 端到端仍拒绝。**预防**：AC-1.4 端到端测试为硬门禁，覆盖 all_groups 与逐组两路径。
4. **「relay 出错关两端误伤」**：错误地在正常 EOF（`io.Copy` 返回 nil）也关两端 → 半双工被破坏。**预防**：只在 `err!=nil` 分支关 `upConn`/客户端；EOF 分支绝不触碰另一方向；回归用例断言"上传先 EOF、下载持续"不被中断。

## Expanded Test Plan（deliberate）
- **Unit**：域名 canonicalize（大小写/尾点）；批量解析两格式 + 非法行；all_groups IsAuthorized；rateSampler 内存计数；本机 IP 探测。
- **Integration**：config 事务化（坏规则 rollback、好规则 swap）；health 全局池并发数 ≤ 池大小；分页 SQL total/limit/offset。
- **E2E**：授权放行/拒绝 + 日志；relay 大下载不断 + 出错 fd 回收；复制地址可用；明暗主题跨页图表正常。
- **Observability**：未授权日志含 user/group；health 池利用率/单轮耗时日志；config 发布成功/回滚日志。

## ADR
- **Decision**：(A1) config 写前候选编译校验；(B1) all_groups 列；(C1) 全局健康池默认150可调（按轮生效）；(D1) relay 等双向、首个 error 关两端。
- **Drivers**：正确性优先、规模化、最小侵入热路径。
- **Alternatives considered**：A2 Tx 事务化（Architect 判定不可行：store 单写+`SetMaxOpenConns(1)`+直连读，需重写整个读层，已否决并降级为 Follow-up）、B2 哨兵行、C2 per-group 并发、保持现状不回滚——均记录否决理由（见 RALPLAN-DR）。
- **Why chosen**：满足 Principle 1（不分裂）+ Principle 4（复用 `Rebuild` 校验器）+ 用户明确要求（全局池、不断下载、通配含未来组）。
- **Consequences**：新增迁移原语 T0.0 + 2 处列；relay/config/health 核心改动需强回归；快照结构扩展 per-user all_groups。
- **Follow-ups**：A2 真事务化（前置：repo 读改 querier-agnostic 接口）；`top?kind=domain` 仍为后续；密码哈希化范围外；可选 goreleaser CI 矩阵。

## Changelog
- (draft v1) 初稿。
- (draft v3, Critic 共识修订) 8 项 must-fix 已纳入：①T2.2 删除"select-首完成"，钉死"等双向+仅 error 关两端"；②T2.1/A1 校验器钉死 `BuildGroupEngine`（非 Rebuild）并声明守护范围=规则编译类；③T1.1 授权语义钉死"并存/永不清精细行"+toggle 测试；④T3.3 批量更新钉死单条 SQL（筛选 WHERE / IN 分块）O(1) 写；⑤T3.1 新增 colon/IPv6 消歧规则、声明 DecodeUpstream 不可复用；⑥新增 T4.5 健康池大小设置 UI 字段（AC-4.4）；⑦T4.1 改"新增 IP util"+多网卡 tie-break；⑧T1.2 修改点钉死 `snapshot.IsAuthorized` 覆盖 Valid+Allow 双路径。
- (draft v4, Architect 复审) 2 项澄清：①T2.2 关闭须在 `relayCounted` 内部执行（defer upConn.Close 太晚）；②T3.1 colon 形 username 不能含 `:`，需含则用 @ 形。Architect 判定 SOUND，5 项核心机制均 FEASIBLE、build 绿。
- (v5 FINAL, Critic 终审 APPROVED-WITH-NITS) 2 nits 折叠：①T2.2 补客户端端关闭实现细节（clientW 非 net.Conn，复用 peek 类型断言；关闭须不阻塞触发）；②AC 计数 24→29 修正。全部 29 条 AC 均有任务归属，零孤儿。共识达成，标记 PENDING APPROVAL。
