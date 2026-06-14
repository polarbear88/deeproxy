# Deep Interview Spec: deeproxy i18n / CLI 系统服务 / UI 优化

## Metadata
- Interview ID: di-deeproxy-i18n-daemon
- Rounds: 5 (+ Round 0 topology gate)
- Final Ambiguity Score: ~9.3%
- Type: brownfield (Go 后端 + Vue3 / Element Plus 前端)
- Generated: 2026-06-14
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.92 | 0.35 | 0.322 |
| Constraint Clarity | 0.88 | 0.25 | 0.220 |
| Success Criteria | 0.90 | 0.25 | 0.225 |
| Context Clarity | 0.93 | 0.15 | 0.140 |
| **Total Clarity** | | | **0.907** |
| **Ambiguity** | | | **0.093** |

## Topology
| # | Component | Status | Description | Coverage Note |
|---|-----------|--------|-------------|---------------|
| 1 | i18n 框架与切换器 | active | 引入 vue-i18n + 右上角中英切换 | AC 1.x |
| 2 | 界面文案中文化 | active | forward/direct/reject、match 列、其他硬编码文案纳入 i18n | AC 2.x |
| 3 | 代理分组类型改名 | active | 去掉 Type A/B 字样 → 动态上游 / 代理池 | AC 3.x |
| 4 | 系统设置面板重组 + 卡片圆角 | active | 拆分臃肿卡片为并排小卡 + 全局圆角调大 | AC 4.x |
| 5 | 动态上游分组流量图表入口 | active | Type A 分组补「流量」按钮 + 图表抽屉 | AC 5.x |
| 6 | CLI 系统服务 | active | --daemon / --startup / --help | AC 6.x |
| 7 | 输入验证 | active | 分组名 + 代理用户名仅允许英文数字 | AC 7.x |

## Goal
在不改动后端代理转发核心逻辑的前提下，对 deeproxy 的 **Web 管理界面**与 **CLI** 做一轮体验与运维增强：
1. 前端引入 vue-i18n 实现中/英双语，右上角可切换、默认跟随系统、localStorage 持久化；
2. 把界面中所有硬编码英文/技术字符串（forward/direct/reject、match 列的 `domain-suffix:`/`ip-cidr:` 类型前缀、Type A/Type B 类型名等）纳入 i18n 中文化；
3. 代理分组类型对用户只呈现「动态上游」「代理池」（后端 A/B 值不变）；
4. 重组系统设置「运行期与默认值设置」单卡片为多张并排小卡片，并把全局卡片圆角调大；
5. 给动态上游（Type A）分组补上查看分组流量图表的入口；
6. CLI 新增 `--daemon`（跨平台系统服务安装并启动）、`--startup`（开机自启）、`--help`（中英双语帮助）；
7. 分组名称与代理用户名新增「仅英文字母与数字」输入校验。

## Constraints
- **后端代理逻辑不变**：SOCKS5 中继、规则引擎、上游解析等行为保持原样；本次仅触及展示层、CLI 入口、输入校验。
- **i18n 不改 API 契约**：forward/direct/reject、A/B、match 字符串在 API 与数据库中保持原始英文/原值，仅前端展示层翻译。
- **服务工作目录** = 可执行文件所在目录：安装服务时设 `WorkingDirectory` 为 `os.Executable()` 解析（去符号链接）后的目录，使 `./deeproxy.db` 仍生成在 exe 旁边，与前台运行一致。不新增 `--db` 参数。
- **服务名固定为 `deeproxy`**：重跑 `--daemon` 改写同名服务而非新增；不按端口区分、不支持多实例。
- **权限不足**：安装/启停服务遇权限错误时直接报错，提示需以 root / 管理员身份运行（不自动 sudo 提权）。
- **`--daemon` 安装并启动服务后，前台进程随即退出**（由 systemd / Windows SCM 拉起后台进程，避免端口冲突）。
- **平台差异**：Linux 走 systemd（`systemctl` 不存在则报错失败）；Windows 走服务管理器（SCM）；macOS 提示「不支持」并退出。
- **服务启动命令保留其他参数**：从 `os.Args[1:]` 过滤掉 `--daemon`/`--startup`（及其等价单破折号形式）后，把剩余参数（如 `--socks5 1000 --web 1001`）写入服务的启动命令。
- **校验只作用于新增/编辑**：分组名、代理用户名在前端表单 + 后端 API 双重校验，拒绝非法输入；已存在的旧数据不强制迁移、不阻断。
- **match 值不做字符限制**（域名/CIDR/IPv6 含 `. / :` 等，限制会误伤合法规则）。
- 编码规范：复用成熟库（CLI 服务用 `github.com/kardianos/service`，统一处理 systemd + Windows SCM + 开机自启）；中文注释；按功能分模块；DRY。

## Non-Goals
- 不改后端转发/规则/上游解析逻辑。
- 不改 API 响应或数据库中 action/type/match 的原始取值。
- 不对存量分组名/用户名做强制改名或数据迁移。
- macOS 不实现系统服务（仅提示不支持）。
- 不新增 `--db` 或多实例服务能力。
- 不对 match 值加字符校验。
- 不引入除中/英外的第三种语言。

## Acceptance Criteria

### Component 1 — i18n 框架与切换器
- [ ] 1.1 `web/package.json` 新增 `vue-i18n`（Vue3 兼容版本），`main.js` 注册 i18n 实例。
- [ ] 1.2 新增 `stores/lang.js`（或 i18n locale store），默认语言取 `localStorage('deeproxy-lang')`，缺省时回退 `navigator.language.startsWith('zh') ? 'zh' : 'en'`。
- [ ] 1.3 切换语言写回 localStorage；刷新后保持。
- [ ] 1.4 MainLayout 顶部右侧（`.header-right`，主题切换与管理员下拉之间）新增中/英切换控件，风格与现有图标/下拉一致。
- [ ] 1.5 至少建立 `locales/zh.js` 与 `locales/en.js` 两份翻译资源。

### Component 2 — 界面文案中文化
- [ ] 2.1 Rules.vue：动作列、编辑对话框 radio、规则测试结果的 forward/direct/reject 通过 i18n 显示中文（如 转发/直连/拒绝），不再裸显英文。
- [ ] 2.2 Dashboard.vue 动作分布饼图的 forward/direct/reject 图例/tooltip 显示中文。
- [ ] 2.3 Rules.vue match 列：**类型前缀中文化、值保留原样**。例 `domain-suffix:ip.sb` → 「域名后缀: ip.sb」；`ip-cidr:10.0.0.0/8` → 「IP段: 10.0.0.0/8」；`domain:example.com` → 「精确域名: example.com」。
- [ ] 2.4 match 编辑下拉的类型选项随 i18n 切换中/英。
- [ ] 2.5 其余可见硬编码文案在中/英切换时同步（核心页面：Dashboard / Rules / ProxyGroups / Settings / 布局与菜单）。
- [ ] 2.6 切到英文时上述文案显示英文（forward/direct/reject、Domain Suffix 等）。

### Component 3 — 代理分组类型改名
- [ ] 3.1 ProxyGroups.vue 分组表「类型」列：A → 「动态上游」、B → 「代理池」（去掉 Type A/Type B 字样）。
- [ ] 3.2 新建分组对话框 radio 文案同样去掉 Type A/B 字样。
- [ ] 3.3 Dashboard.vue「连接用户名格式说明」及 `暂无 Type B 分组` 等处一并改名（经 i18n）。
- [ ] 3.4 内部数据值与 API 仍用 `'A'`/`'B'`，不受影响。

### Component 4 — 系统设置重组 + 卡片圆角
- [ ] 4.1 Settings.vue 左列单张「运行期与默认值设置」卡片拆分为**多张并排小卡片**（如：服务器与连接 / 运行期 / 统计 / 健康检查默认值），仍在同一页面、适度不过度。
- [ ] 4.2 各小卡片字段与原字段一一对应，保存逻辑不丢字段。
- [ ] 4.3 全局卡片圆角调大：在 `styles/index.scss` 的 `:root` 设置 `--el-card-border-radius`（如 12px），全站 el-card 生效。

### Component 5 — 动态上游分组流量图表入口
- [ ] 5.1 ProxyGroups.vue 给动态上游（Type A）分组行新增「流量」按钮。
- [ ] 5.2 点击打开一个**只含分组流量图表**的抽屉（不含代理池表格），复用现有 `loadGroupChart(group.id)`。
- [ ] 5.3 代理池（Type B）现有「代理池」按钮+抽屉行为不变。

### Component 6 — CLI 系统服务
- [ ] 6.1 新增 `--daemon`：Linux 用 systemd 安装服务；`systemctl` 不存在 → 报错失败退出。Windows 安装为 SCM 服务。macOS → 打印「不支持」并退出。
- [ ] 6.2 服务名固定 `deeproxy`；已存在同名服务则**修改**配置而非新增。
- [ ] 6.3 服务 `WorkingDirectory` = 当前可执行文件所在目录（`os.Executable()` + EvalSymlinks）。
- [ ] 6.4 服务启动命令 = exe 路径 + 过滤掉 `--daemon`/`--startup` 后的其余参数。例：`./deeproxy --socks5 1000 --web 1001 --daemon` → 服务命令 `<exe> --socks5 1000 --web 1001`。
- [ ] 6.5 安装成功后**启动**该服务；启动失败给出清晰报错。
- [ ] 6.6 `--startup`（仅与 `--daemon` 同用有效）：设置服务为开机自启。
- [ ] 6.7 安装/启动完成后前台进程退出（不再继续监听端口）。
- [ ] 6.8 权限不足时报错提示需 root/管理员。
- [ ] 6.9 新增 `--help`：输出**中英双语**命令帮助（覆盖所有参数）。
- [ ] 6.10 采用成熟服务库（`github.com/kardianos/service`）实现，go.mod 增加依赖。

### Component 7 — 输入验证
- [ ] 7.1 分组名称（`Group.name`）：仅允许英文字母与数字（`^[A-Za-z0-9]+$`），新增/编辑时前端 + 后端双重校验。
- [ ] 7.2 代理用户名（`ProxyUser.username`）：同样仅允许英文字母与数字，前端 + 后端双重校验。
- [ ] 7.3 非法输入被拒绝并给出明确中文（i18n）错误提示。
- [ ] 7.4 存量旧数据不被强制改写、不阻断既有连接。
- [ ] 7.5 match 值不受此校验影响。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| 服务能直接用 `./deeproxy.db` | systemd/SCM 的 CWD 是 / 或 System32 | 服务 WorkingDirectory 设为 exe 目录 |
| 可能需要按端口建多个服务 | 「重跑则修改不新增」需固定识别符 | 固定服务名 deeproxy，重跑改写 |
| `--daemon` 后前台继续运行 | 与服务进程端口冲突 | 安装并启动后前台退出 |
| 「规则命令」是 match 或 action | deeproxy 规则无「命令」字段 | 实指**分组名 + 代理用户名** |
| 「仅英文数字」适用于 match | 域名/CIDR 含 . / :，会误伤 | match 不校验；仅校验分组名/用户名 |
| 设置卡片如何「适度」拆分 | 主观 | 多张并排小卡片，同页不过度 |
| Type A 图表入口形态 | 多种可能 | 独立「流量」按钮 + 仅图表抽屉，复用 loadGroupChart |

## Technical Context
**前端**（`web/`，Vue 3.5 + Element Plus 2.9 + Vite 6 + Pinia + ECharts，构建产物嵌入 Go 二进制）
- 无 vue-i18n，全部中文硬编码；需从零接入。
- forward/direct/reject 文案：`Rules.vue:233,260-262,294`、`Settings.vue:174-176`、`Dashboard.vue:177-183`。
- match 列裸渲染：`Rules.vue:230`（`prop="match"`），值形如 `<type>:<value>`，编辑处 `Rules.vue:103-105` 已按 `:` 切分。
- Type A/B 标签：`ProxyGroups.vue:362-363,393-394`；`Dashboard.vue:344,369,372`。
- Type A 缺图表入口原因：图表在「代理池」抽屉内（`ProxyGroups.vue:535-536`），该抽屉仅 Type B 的按钮（`:374 v-if="row.type==='B'"`）可开。`loadGroupChart(group.id)`（`:97` 调用）独立于 `loadUpstreams()`，可单独复用。
- 设置页：`Settings.vue:154-230` 单卡片 5 段 14 字段。
- 卡片圆角：`styles/index.scss` `:root` 未设 `--el-card-border-radius`，单点改即可全局生效。
- 顶栏右侧：`MainLayout.vue:76-98` `.header-right`；语言切换插在主题切换与管理员下拉之间。主题 store（`stores/theme.js`）已示范 localStorage + matchMedia 跟随系统的模式，语言 store 照此实现。

**后端**（Go 1.26.3，module `deeproxy`）
- 入口 `cmd/deeproxy/main.go`（171 行），stdlib `flag`，现有 `--socks5`(1768)/`--web`(1769)/`-v`。
- 无任何 service/daemon/`os.Executable` 代码；go.mod 无服务库 → 新增 `github.com/kardianos/service`。
- 启动：SOCKS5 `srv.ListenAndServe`（`server/server.go:490+`）、Web `app.Run()`（`api/server.go:183+`），均阻塞调用。
- DBPath 硬编码 `./deeproxy.db`（相对 CWD）。
- 分组名/用户名校验：需在 group / proxyuser 的新增/编辑 API handler 加正则校验，前端对应表单加 rule。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| Group | core domain | name, type(A/B) | 含多个 ProxyUser / Upstream |
| ProxyUser | core domain | username, password | 授权访问某 Group |
| Rule | core domain | match(type:value), action | 顺序首匹配 |
| Locale | supporting | code(zh/en), messages | 驱动全部 UI 文案 |
| SystemService | external system | name(deeproxy), execCommand, workingDir, startup | 由 systemd/SCM 管理 |
| Settings | supporting | server/runtime/stat/health 字段组 | 重组为多卡片 |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 2 (Service, DB) | 2 | - | - | N/A |
| 2 | 3 (+Service naming) | 1 | - | 2 | 67% |
| 3 | 5 (+Settings, Validation) | 2 | - | 3 | 60% |
| 4 | 6 (Validation→Group/ProxyUser 明确) | 1 | 1 | 4 | 83% |
| 5 | 6 (+Locale, Rule 稳定) | 0 | 0 | 6 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (Round 0 + 5 rounds)</summary>

### Round 0 — Topology
确认 7 个顶层组件（i18n 框架 / 文案中文化 / 类型改名 / 设置重组+圆角 / 流量图表入口 / CLI 服务 / 输入验证）。

### Round 1 — Component 6 服务工作目录
**Q:** 服务的数据库路径/工作目录怎么处理？
**A:** WorkingDir = exe 所在目录（推荐）。

### Round 2 — Component 6 服务名/权限 + 前台行为
**Q1:** 服务名与权限？ **A:** 固定名 deeproxy + 权限不足报错。
**Q2:** --daemon 后前台进程？ **A:** 安装后本进程退出。

### Round 3 — Component 4 拆分 + Component 7 校验范围
**Q1:** 设置卡片怎么拆？ **A:** 拆成多张并排小卡片。
**Q2:** 校验加在哪、旧数据怎么办？ **A:** 仅校验新增/编辑。

### Round 4 — Contrarian：Component 7 实体澄清
**Q:** 「规则命令」到底指哪个输入？ **A:** 指**规则组名称和代理用户名**，仅允许英文数字。match 字符问题不存在。

### Round 5 — Component 5 入口 + Component 2 match 展示
**Q1:** Type A 流量图表入口？ **A:** 独立「流量」按钮 + 图表抽屉。
**Q2:** match 列中文化展示？ **A:** 类型中文 + 值原样。

</details>
