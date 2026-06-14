# Deep Interview Spec: 代理池代理功能 Bug 修复 + 批量删除（用户名/模板字段统一 + 测试延迟 + 添加即检查 + 列表列改造 + 批量删除）

## Metadata
- Interview ID: dp-pool-username-2026-06-14
- Rounds: 6
- Final Ambiguity Score: 12%
- Type: brownfield
- Generated: 2026-06-14
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.90 | 0.35 | 0.315 |
| Constraint Clarity | 0.86 | 0.25 | 0.215 |
| Success Criteria | 0.86 | 0.25 | 0.215 |
| Context Clarity | 0.88 | 0.15 | 0.132 |
| **Total Clarity** | | | **0.877** |
| **Ambiguity** | | | **0.123 (12%)** |

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| 1. 用户名/模板字段统一 | active | 去掉独立的「用户名模板」概念，用户名本身即模板（可含 `{var}` 占位）。后端模型、认证解析、探测、前端表单全部统一为单一字段 | AC-1..AC-6 |
| 2. 测试后立即更新延迟 | active | 点「测试」后把测得延迟写回该代理并刷新列表，同时更新健康状态 | AC-7..AC-9 |
| 3. 添加后立即健康检查 | active | 新增代理（单个+批量）时若分组开启健康检查，立即探测一次而非等间隔 | AC-10..AC-12 |
| 4. 列表列改造 | active | 把「用户名模板」列换成「用户名密码」列，显示 `user:password` | AC-13..AC-14 |
| 5. 批量删除 | active | 工具栏新增「批量删除」按钮，支持勾选删除 + 跨页全选按筛选删除，二次确认显示具体条数 | AC-16..AC-20 |

## Goal
修复 deeproxy 代理池（Type B 上游代理）的四类缺陷，核心是**消除「用户名」与「用户名模板」两个分裂字段**——用户名本身就是模板（可含 `{var}` 占位），统一为单一字段贯穿后端模型/DB/认证解析/探测/前端表单/列表展示；同时让「测试」立即回写延迟与健康状态、让「添加」在分组开启健康检查时立即探测、并把列表的「用户名模板」列改造为显示 `user:password` 的「用户名密码」列。

## Constraints
- **无旧数据**：当前环境无生产数据，可直接修改 DB schema 与代码，无需写数据迁移脚本（用户确认「无旧数据，直接改」）。
- **统一字段语义**：保留单一 `user` 字段作为「用户名」，可含 `{var}` 占位；运行期由客户端尾段变量（`name_value#...`）经 `auth.SubstituteTemplate` 替换。丢弃 `username_template` 列与 `UsernameTemplate` 结构字段。
- **服务端主动探测的认证用户名**：测试 / 健康检查由服务端发起、无客户端变量。探测时对 `user` 中的 `{var}` 用 `SubstituteTemplate` **空值填充**（缺值→空串），再用替换后的用户名 + 密码认证上游（用户确认「空值填充后探测」）。
- **测试 = 一次完整探测**：点「测试」走与定时检查同一 `applyResult` 逻辑——成功写回延迟 + 标记健康，失败标记不健康；延迟与健康状态在列表同步刷新（用户确认「延迟+健康状态都更新」）。
- **添加即检查覆盖单个+批量**：单个添加与批量添加在入库后都立即探测；批量并发探测所有新增项，复用现有 health worker 并发逻辑；仅当该分组开启了健康检查才探测（用户确认「单个+批量都立即检查」）。
- **延迟字段当前为内存态**：`nodeState.latencyMs` 仅在内存（无 DB 列）。实现需确保测试回写的延迟能被列表读取到（经 `applyResult` 更新内存态，列表 `LatencyMs(id)` 读取）。
- 继承项目编码规范：中文注释、DRY、按功能分模块、优先复用现有 `applyResult` / `Probe` / `SubstituteTemplate` 原语。

## Non-Goals
- 不做数据迁移脚本（无旧数据）。
- 不改动 Type A（动态上游组）的认证语义。
- 不新增 latency 的持久化 DB 列（除非实现需要；首版维持内存态，只要列表能正确显示测试后的新延迟即可）。
- 不改动 SWRR 选择器算法、规则引擎、SOCKS5 握手等无关模块。

## Acceptance Criteria
- [ ] AC-1: 后端移除 `UpstreamProxy.UsernameTemplate` 字段与 DB `username_template` 列；`user` 成为唯一用户名字段（可含 `{var}`）。
- [ ] AC-2: `snapshot.ResolveUser` 改为始终对 `user` 调用 `SubstituteTemplate(user, vars)`（不再二选一 user/template）。
- [ ] AC-3: 单个添加 API（`upstreamReq`）移除 `usernameTemplate` 字段，只收 `user`。
- [ ] AC-4: 批量添加：每行解析出的 `user` 即为模板，移除 `batchUpstreamReq.UsernameTemplate` 批级字段。
- [ ] AC-5: 前端单个添加表单移除 `usernameTemplate` 输入，只保留 `user`（标签可仍叫「用户名」）。
- [ ] AC-6: 批量添加后该代理的用户名（含 `{var}`）能正确入库并在列表显示，不再为空（修复 Bug 1）。
- [ ] AC-7: 单个添加的代理点「测试」时，探测对 `user` 的 `{var}` 空值填充后认证上游，不再因 user 为空报「认证失败」（修复 Bug 2）。
- [ ] AC-8: `handleTestUpstream` 走 `applyResult`（或等价逻辑），测试成功后把延迟写入内存态 `nodeState.latencyMs`。
- [ ] AC-9: 测试完成后列表的「延迟」字段立即刷新为本次测得值；健康状态同步更新（成功=健康，失败=不健康）。
- [ ] AC-10: 单个添加代理后，若分组 `HCEnabled`（开启健康检查），立即对该代理探测一次。
- [ ] AC-11: 批量添加代理后，若分组开启健康检查，并发对所有新增代理各探测一次（复用 health worker 并发）。
- [ ] AC-12: 立即检查的结果（延迟+健康状态）写回并在列表可见，无需等下一个 `HCInterval`。
- [ ] AC-13: 列表把「用户名模板」列替换为「用户名密码」列，显示 `user:password` 形式。
- [ ] AC-14: i18n 键 `proxyGroups.usernameTemplate` 相应更新/替换为「用户名密码」对应键（中英文）。
- [ ] AC-15: `go build` / 前端 `pnpm build` 均通过，无回归。

### 组件5：批量删除（新增任务，单独可独立交付）
- [ ] AC-16: store 新增 `BulkDeleteUpstreamsByIDs(groupID, ids)`（分块 IN、同事务、groupID 限定）与 `BulkDeleteUpstreamsByFilter(filter)`（复用 `buildUpstreamWhere`，一条 SQL），镜像现有 BulkUpdate 方法。
- [ ] AC-17: API 新增 `handleBulkDeleteUpstreams`（`POST /groups/:id/upstreams/bulk-delete`），请求体 `{ ids }` 或 `{ filter:{keyword,healthState} }`；ids 非空走 id 模式，否则走 filter 模式；删除后 `rebuildAndSwap`，返回 `{ affected }`。
- [ ] AC-18: 前端 bulk-bar 新增「批量删除」按钮（type=danger，`:disabled="!hasBulkSelection"`），复用 `buildSelectionPayload()`（勾选→ids；跨页全选→filter）。
- [ ] AC-19: 删除前二次确认弹窗显示**具体将删除条数**：勾选模式=选中行数；跨页全选模式=当前筛选下的 `upstreamDrawer.total`；文案含「不可恢复」。确认后调用 API、清空选择、刷新列表、提示影响条数。
- [ ] AC-20: i18n 新增 `bulkDelete` / `bulkDeleteConfirm`（含条数占位）/ `bulkDeleteDone` 三键（zh/en）。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| 用户名模板是独立属性 | 用户名本身就是模板，无需独立字段 | 统一为单一 `user` 字段，可含 `{var}` |
| 探测可直接用 user 认证 | 服务端探测无客户端变量，`{var}` 无值 | 探测时 `SubstituteTemplate` 空值填充再认证 |
| 测试只返回延迟给前端 | 测试本质是一次探测 | 测试同时回写延迟 + 健康状态，列表同步刷新 |
| 立即检查只针对单个 | 批量也需覆盖 | 单个+批量都立即检查，批量并发 |
| 统一字段需数据迁移 | 真的有旧数据吗？ | 无旧数据，直接改 schema，无需迁移脚本 |

## Technical Context（brownfield 关键代码点）
- `store/models.go:124-141` — `UpstreamProxy` 结构（含待删的 `UsernameTemplate`）。
- `store/schema.go:167-180` — `upstream_proxy` 表（含待删的 `username_template` 列）。
- `snapshot/snapshot.go:70-89` — `ResolveUser` / `ToAuthUpstream`（二选一逻辑→改为始终模板替换）。
- `auth/variables.go:43-89` — `SubstituteTemplate`（复用，空值填充语义已具备）。
- `api/group_handler.go:232-240` (`upstreamReq`), `:332-368` (单个添加), `:370-459` (批量添加), `:639-676` (`handleTestUpstream`)。
- `pool/health/health.go:79-83` (probe 用 `up.User`), `:164-188` (`Run`/启动即扫), `:226-233` (interval 门控), `:340` (`applyResult` 写 latency), `:408-411` (`TestProxy` 当前绕过 applyResult)。
- `web/src/views/proxy/ProxyGroups.vue:170` (单个表单), `:201-228` (批量), `:296-307` (testUpstream), `:524` (待改列)。
- i18n：`proxyGroups.usernameTemplate` 键（zh/en）。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| UpstreamProxy | core domain | id, groupID, host, port, **user(=模板)**, pwd, weight, enabled, healthState, latencyMs(内存) | belongs to Group(Type B) |
| Group (Type B) | core domain | id, name, type, HCEnabled, HCMode, HCURL, HCInterval | has many UpstreamProxy |
| ProbeResult | supporting | OK, Latency, Err | produced by HealthChecker.Probe |
| ConnVariables | supporting | map[string]string (来自客户端尾段) | fills `{var}` in user |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 4 | 4 | - | - | N/A |
| 2 | 4 | 0 | 1 (User→统一模板) | 3 | 100% |
| 3 | 4 | 0 | 0 | 4 | 100% |
| 4 | 4 | 0 | 0 | 4 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (4 rounds + topology gate)</summary>

### Round 0 — Topology
**Q:** 4 组件拆分是否正确？
**A:** 拆分正确，4 个都做。

### Round 1 — Component 1 / Constraint
**Q:** 统一后 user 含 `{var}`，服务端探测无变量，用什么认证？
**A:** 空值填充后探测（SubstituteTemplate 缺值→空串）。

### Round 2 — Component 2 / Constraint
**Q:** 测试除写回延迟，是否也更新 health_state？
**A:** 延迟+健康状态都更新。

### Round 3 — Component 3 / Constraint
**Q:** 立即检查只针对单个还是批量也覆盖？
**A:** 单个+批量都立即检查。

### Round 4 — Component 1 / Criteria（Contrarian）
**Q:** 真的需要改数据库结构吗？怎么处理两列和已有数据？
**A:** 无旧数据，直接改。

### Round 5 — Component 5（批量删除）/ Constraint
**Q:** 批量删除要支持跨页全选（按筛选删全部匹配项），还是只删当前勾选的行？
**A:** 勾选 + 跨页全选都支持。

### Round 6 — Component 5 / Criteria
**Q:** 删除前二次确认弹窗要不要显示具体将删除多少条？
**A:** 显示具体条数。

</details>
