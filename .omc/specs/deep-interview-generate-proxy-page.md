# Deep Interview Spec: 生成代理页面（Generate Proxy）+ proxyGroups.optionalRemark 翻译修复

## Metadata
- Interview ID: genproxy-2026-06-15
- Rounds: 3 (+ Round 0 topology gate)
- Final Ambiguity Score: 10%
- Type: brownfield
- Generated: 2026-06-15
- Threshold: 0.2 (20%)
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.95 | 0.35 | 0.3325 |
| Constraint Clarity | 0.85 | 0.25 | 0.2125 |
| Success Criteria | 0.92 | 0.25 | 0.2300 |
| Context Clarity | 0.92 | 0.15 | 0.1380 |
| **Total Clarity** | | | **0.913** |
| **Ambiguity** | | | **0.087 (~10%)** |

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| 生成代理页面 | active | 用户管理下新增菜单+路由+页面：选用户、选代理组，按组类型收集上游(A)/变量(B)，生成两种格式代理串 | AC-1..AC-11 |
| i18n 修复 | active | `proxyGroups.optionalRemark` 翻译缺失修复 | AC-12 |

## Goal
在 deeproxy 管理后台「用户管理」菜单**下方**新增一个「生成代理」菜单页（Vue 3 + Element Plus，挂在 `web/src/`）。页面让运维选择一个**已有代理用户**和一个**代理组**，根据组类型动态收集补充信息，实时/按需生成「连接本机 SOCKS5 服务」的代理连接串，并提供与用户管理页**完全一致的两种复制格式**。

最终用户名遵循 v2 契约 `user-组名[-尾段]`：
- **Type A（动态上游）**：尾段 = `base64("u:p@host:port")` → 用户名 = `<user>-<groupName>-<base64>`
- **Type B（代理池）**：尾段 = `name_value#name_value...`；**无变量时无尾段** → 用户名 = `<user>-<groupName>`；有变量 → `<user>-<groupName>-session_abc#region_us`

两种复制格式（密码取用户明文连接密码 `pwd`，服务器地址/端口取 `getServerInfo()`）：
- **格式1**：`socks5://<username>:<pwd>@<serverAddr>:<socks5Port>`
- **格式2**：`<serverAddr>:<socks5Port>:<username>:<pwd>`

## Constraints
- 复用现有逻辑，禁止重复造轮子：复制工具 `copyText`（含 navigator.clipboard + execCommand 回退）、`getServerInfo()`（`web/src/api/system.js`）、`listUsers()`（`web/src/api/user.js`）、`listGroups()`（`web/src/api/group.js`）。
- 菜单通过 `web/src/router/index.js` 在 `user` 路由**之后**新增一条扁平子路由（`MainLayout.vue` 由 router meta 自动生成菜单，顺序即菜单顺序），meta 含 `title: 'menu.generateProxy'` 与 `icon`。
- Type A 上游输入 = 4 字段表单：host、port、上游user（可选）、上游pwd（可选）；上游 user/pwd 允许为空（上游免认证），仍编码为 `:` 分隔的凭据，符合 `auth/upstream.go DecodeUpstream`。
- Type B 变量输入 = 动态键值行（可增删），每行「变量名 + 值」；拼接为 `name_value#name_value...`（`'#'` 连接、`'_'` 连接名与值），符合 `auth/variables.go ParseVariables`。
- 服务器地址/端口缺失时沿用 Users.vue 的占位符 `<server-addr>` / `<socks5-port>`；用户密码缺失沿用 `<pwd>`。
- base64 使用标准编码（`btoa`，对应 Go `base64.StdEncoding`）。
- 全部新增代码用**中文注释**，解释「为什么」；页面单文件职责单一，置于 `web/src/views/`（如 `views/proxy/GenerateProxy.vue` 或 `views/user/GenerateProxy.vue`）。
- 新增 i18n key 必须同时加 `zh.js` 与 `en.js`，避免再次出现翻译缺失。

## Non-Goals
- 不改后端：不新增 API，仅消费现有 `listUsers/listGroups/getServerInfo`。
- 不做用户/代理组的增删改（已在 Users.vue / ProxyGroups.vue）。
- 不做嵌套菜单（路由模型是扁平的）。
- 不改动 Users.vue 现有两种复制格式的行为。

## Acceptance Criteria
- [ ] AC-1：左侧菜单在「用户管理」**下方**出现「生成代理」项，点击进入新页面；菜单文案随语言切换（zh/en）。
- [ ] AC-2：页面有用户选择器（来自 `listUsers()`）和代理组选择器（来自 `listGroups()`，含 type A/B）。
- [ ] AC-3：选中组为 **Type A** 时，显示 4 字段上游表单（host/port/user/pwd），user/pwd 可空。
- [ ] AC-4：选中组为 **Type B** 时，显示动态键值行（变量名+值，可增删）。
- [ ] AC-5：Type A 生成的用户名 = `<user>-<groupName>-base64("<u>:<p>@<host>:<port>")`，base64 与 Go 标准编码一致（可被 `DecodeUpstream` 解析）。
- [ ] AC-6：Type B 有变量时用户名 = `<user>-<groupName>-name_value#...`；**无变量时 = `<user>-<groupName>`（无尾段、无结尾 `-`）**。
- [ ] AC-7：格式1 = `socks5://<username>:<pwd>@<serverAddr>:<socks5Port>`。
- [ ] AC-8：格式2 = `<serverAddr>:<socks5Port>:<username>:<pwd>`。
- [ ] AC-9：两种格式各有独立只读展示框 + 复制按钮，复制成功/失败有 ElMessage 提示（复用 `copyText`）。
- [ ] AC-10：服务器信息或密码缺失时使用占位符（`<server-addr>`/`<socks5-port>`/`<pwd>`），不报错崩溃。
- [ ] AC-11：未选用户或未选组时，给出明确提示、不生成非法串。
- [ ] AC-12：`ProxyGroups.vue:467` 的 `t('proxyGroups.optionalRemark')` 正常显示译文 —— 在 `zh.js`/`en.js` 的 `proxyGroups` 块下补 `optionalRemark`（zh: 可选备注 / en: Optional remark）。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| 上游输入用单行明文串就够 | 校验弱、与 DecodeUpstream 反向逻辑不贴合 | 改为 4 字段表单 |
| Type B 变量让用户填整串 | 易格式错误 | 改为动态键值行，页面负责拼接 |
| 无变量时尾段输出 `user-组名-` | ParseUsername 允许空尾段但语义模糊 | 无变量 → 无尾段 `user-组名` |
| 上游必须有 user/pwd | 上游可能免认证 | 允许为空 |
| optionalRemark 已存在于 proxyGroups | 实测只在 users 块下 | 需在 proxyGroups 块补 key |

## Technical Context
- 复制格式源：`web/src/views/user/Users.vue` `buildProxyAddr`/`buildProxyAddr2`/`copyText`（line ~151-194）。注意现有代码用字面 `{group}` 占位且**无尾段**；新页面需用真实组名并补尾段。
- 用户 DTO（`api/user_handler.go:32-36`）返回明文 `pwd`（D3：供拼可用 URL）、`username`、`remark`、`allGroups`、`groupIds`。
- 组 DTO（`api/group_handler.go`）：`{ id, name, remark, type:'A'|'B', ... }`。
- 上游编码：`auth/upstream.go EncodeUpstream` = `base64.StdEncoding(fmt.Sprintf("%s:%s@%s", user, pwd, host:port))`。
- 变量串：`auth/variables.go ParseVariables`（`#` 分变量、首个 `_` 分名值）。
- i18n：`web/src/locales/{zh,en}.js`，`proxyGroups` 块在 zh.js line ~302。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| ProxyUser | core domain | username, pwd, groupIds | has many authorized ProxyGroup |
| ProxyGroup | core domain | id, name, type('A'/'B') | A→needs Upstream; B→needs Variables |
| Upstream (Type A) | supporting | host, port, user, pwd | encoded → base64 tail |
| Variable (Type B) | supporting | name, value | joined → name_value#... tail |
| ConnectionString | output | username, pwd, serverAddr, socks5Port | rendered in 2 formats |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 3 | 3 | - | - | N/A |
| 2 | 4 | 1 | 0 | 3 | 100% |
| 3 | 5 | 1 | 0 | 4 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (Round 0 + 3 rounds)</summary>

### Round 0 — Topology
**Q:** 拆成 2 个顶层组件（生成代理页面 + i18n 修复）对吗？
**A:** 对，就这两个。

### Round 1
**Q:** Type A 上游怎么输入？user/pwd 可空吗？
**A:** 4 字段（host/port/user/pwd）。
**Ambiguity:** ~38%

### Round 2
**Q:** Type B 变量怎么输入？无变量时输出 `user-组名` 还是 `user-组名-`？
**A:** 动态键值行（推荐）。
**Ambiguity:** ~22%

### Round 3
**Q:** 输出/呈现方式？确认 username 与两种格式模型。
**A:** 两个只读框+复制按钮（推荐）。
**Ambiguity:** ~10%

</details>
