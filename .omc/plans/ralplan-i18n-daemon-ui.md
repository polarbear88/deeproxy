# 实施计划：deeproxy i18n / CLI 系统服务 / UI 优化（ralplan）

> 权威需求来源：`/Users/polarbear/code/deeproxy/.omc/specs/deep-interview-i18n-daemon-ui.md`（深度访谈，最终模糊度 9.3%，PASSED）。
> 本计划只规划、不实现。所有行号已对真实文件复核（`cat -n` 校验），非盲信 spec。
> 后端代理转发核心逻辑（SOCKS5 中继 / 规则引擎 / 上游解析）**绝不改动**；本轮仅触及展示层、CLI 入口、输入校验。

---

## 一、需求摘要（Requirements Summary）

7 个组件，分三条互相独立的工作线：

| 工作线 | 组件 | 是否依赖其他组件 |
|--------|------|------------------|
| **前端 i18n** | ① i18n 框架+切换器 → ② 文案中文化 → ③ 类型改名 | ②③ 依赖 ① 先落地 |
| **前端 UI** | ④ 设置重组+卡片圆角 → ⑤ 动态上游流量入口 | ④⑤ 弱依赖 ①（文案走 i18n） |
| **后端 CLI** | ⑥ CLI 系统服务（`--daemon`/`--startup`/`--help`） | 完全独立 |
| **双端校验** | ⑦ 分组名/用户名 `^[A-Za-z0-9]+$` 校验 | 完全独立（前端 rule 文案走 ①） |

施工顺序建议（最小化返工）：**① i18n 脚手架 → 并行推进 {②③④⑤ 前端、⑥ 后端 CLI、⑦ 校验}**。⑥⑦ 可与前端完全并行，无文件冲突。

核心约束（spec §Constraints，逐条复核）：
- i18n 不改 API/DB：`forward/direct/reject`、`A/B`、`match` 字符串在接口与库里保持原始英文/原值，仅前端展示层翻译（spec:47）。
- 服务 `WorkingDirectory` = exe 所在目录（`os.Executable()` + `EvalSymlinks`），使 `./deeproxy.db` 仍生成在 exe 旁（spec:48；DBPath 硬编码见 `cmd/deeproxy/main.go:55`）。
- 服务名固定 `deeproxy`，重跑 `--daemon` 改写同名服务而非新增（spec:49）。
- 权限不足直接报错提示需 root/管理员，不自动提权（spec:50）。
- 安装并启动后前台进程退出（spec:51）。
- 平台差异：Linux=systemd（`systemctl` 不存在则报错失败）、Windows=SCM、macOS=提示不支持并退出（spec:52）。
- 服务启动命令 = exe 路径 + `os.Args[1:]` 过滤掉 `--daemon`/`--startup`（含单破折号等价形式）后的其余参数（spec:53）。
- 校验仅作用于新增/编辑，旧数据不强制迁移、不阻断（spec:54）。
- `match` 值不做字符限制（spec:55）。
- 复用成熟库 `github.com/kardianos/service`；中文注释；按功能分模块；DRY（spec:56，CLAUDE.md 全局规范）。

---

## 二、验收标准（Acceptance Criteria，可测）

> 直接对齐 spec §Acceptance Criteria（行 69-117），按组件分组。每条标注可测手段。

### 组件 ① i18n 框架与切换器
- **AC-1.1** `web/package.json:12-20` `dependencies` 新增 `"vue-i18n": "^9"`（**committed 决策**，Vue3 LTS 稳定线，R-10）；`web/src/main.js` 注册 i18n 实例（`app.use(i18n)`）。【测：`npm ls vue-i18n` 显示 9.x + 构建通过】
- **AC-1.2** 新增 `web/src/stores/lang.js`，默认语言 = `localStorage('deeproxy-lang')`，缺省回退 `navigator.language.startsWith('zh') ? 'zh' : 'en'`。【测：清 localStorage + 改浏览器语言，刷新看默认值】
- **AC-1.3** 切换语言写回 `localStorage('deeproxy-lang')`，刷新后保持。【测：切英文→刷新仍英文】
- **AC-1.4** `MainLayout.vue` 顶栏 `.header-right`（当前 `:76-98`，主题切换 `:78-83` 与管理员下拉 `:85-97` 之间）新增中/英切换控件，风格与现有图标/下拉一致。【测：目视控件位置正确】
- **AC-1.5** 至少建立 `web/src/locales/zh.js` 与 `web/src/locales/en.js` 两份翻译资源（当前无 `locales/` 目录）。【测：文件存在且被 import】

### 组件 ② 界面文案中文化
- **AC-2.1** `Rules.vue`：动作列 tag（`:233` 裸 `{{ row.action }}`）、编辑对话框 radio（`:260-262` 裸 `forward/direct/reject`）、测试结果动作（`:294` 裸 `{{ tester.result.action }}`）经 i18n 显示中文（转发/直连/拒绝）。【测：中文模式看中文、英文模式看英文】
- **AC-2.2** `web/src/views/dashboard/Dashboard.vue` 动作分布饼图（`actionOption` `:164-185`）图例/tooltip 显示中文。**实时数据**（后端返回 `name`）与**占位数据**（`:175-179` 的 `{name:'forward'/'direct'/'reject',value:0}` 三项）都必须经 `t('action.'+name)` 翻译——不能只翻实时漏掉占位。【测：无数据时饼图 legend 仍显示「转发/直连/拒绝」而非英文】
- **AC-2.3** `Rules.vue` match 列（`:230` `prop="match"` 裸渲染）改为「类型中文 + 值原样」：`domain-suffix:ip.sb`→「域名后缀: ip.sb」；`ip-cidr:10.0.0.0/8`→「IP段: 10.0.0.0/8」；`domain:example.com`→「精确域名: example.com」。【测：三类各造一条规则核对显示】
- **AC-2.4** match 编辑下拉（`Rules.vue:248-253`，当前 label 硬编码「domain（精确域名）」等）类型选项随 i18n 切换中/英。【测：切语言看下拉 label】
- **AC-2.5** 核心页面其余可见硬编码文案随中/英切换同步：`Dashboard/Rules/ProxyGroups/Settings` + 布局菜单（`MainLayout.vue:88,93,94`、菜单标题 `router/index.js:32-62` 的 `meta.title`）。【测：逐页切语言巡检】
- **AC-2.6** 切英文时上述文案显示英文（forward/direct/reject、Domain Suffix 等）。【测：英文巡检】

### 组件 ③ 代理分组类型改名（去 Type A/B 字样）
- **AC-3.1** `ProxyGroups.vue:362` 类型列 `'Type A 动态上游' : 'Type B 代理池'` → 经 i18n 输出「动态上游」/「代理池」（无 Type A/B 字样）。【测：类型列文本】
- **AC-3.2** 新建分组对话框 radio（`ProxyGroups.vue:393-394` `Type A 动态上游`/`Type B 代理池`）同样去 Type A/B 字样。【测：对话框 radio 文本】
- **AC-3.3** `Dashboard.vue` 连接用户名格式说明（`:369` `Type A 动态上游`、`:372` `Type B 命名变量`）及空态文案（`:344` `暂无 Type B 分组`）经 i18n 改名。【测：仪表盘说明卡 + 空态】
- **AC-3.4** 内部数据值与 API 仍用 `'A'`/`'B'`：`row.type === 'A'`（`:361,374`）、`emptyGroupForm().type='B'`（`:23`）、`<el-radio value="A"/value="B">`（`:393-394`）的 value 不变；后端 `groupReq.Type`/`validateGroupType`（`group_handler.go:39,58-60`）不变。【测：建组后 API 响应 `type` 仍为 A/B】

### 组件 ④ 系统设置重组 + 卡片圆角
- **AC-4.1** `web/src/views/system/Settings.vue:154-230` 单张「运行期与默认值设置」卡片（含 5 段 `el-divider` / 14 字段）拆为多张**并排小卡片**，**布局硬约束：用 `el-row :gutter="16"` + 多个 `el-col :span="12"` 实现 2 列并排**（建议 4 卡：服务器与连接 / 运行期 / 统计 / 健康检查默认值），同页适度不过度。【测：目视两列并排小卡】
- **AC-4.2** 各小卡片字段与原字段一一对应，`saveSettings()`（`:61-79`）提交载荷不丢字段（仍含 `statRetentionDays/serverAddr/probePoolSize/hcDefaults/defaultAction/logLevel/idleTimeoutSec/sniffDomain/sniffTimeoutMs`）。【测：保存后重载核对各字段值往返一致】
- **AC-4.3** `web/src/styles/index.scss` `:root`（当前 `:8-16`，无 `--el-card-border-radius`）新增 `--el-card-border-radius: 12px`，全站 `el-card` 生效。【测：任一页 el-card 圆角变大】

### 组件 ⑤ 动态上游（Type A）流量图表入口
- **AC-5.1** `ProxyGroups.vue` 操作列（`:372-378`）给 Type A 分组行新增「流量」按钮（与现有 `:374 v-if="row.type==='B'"` 的「代理池」按钮并列，用 `v-if="row.type==='A'"`）。【测：Type A 行出现「流量」按钮】
- **AC-5.2** 点击打开一个**仅含分组流量图表**的抽屉（不含代理池表格），复用现有 `loadGroupChart(group.id)`（`:295-308`）+ `groupChartOption`（`:309-319`）+ `groupTopDomainOption`（`:322-335`）。新增独立 `chartDrawer` 状态，不复用 `upstreamDrawer`（后者绑定上游表格/分页/批量）。【测：抽屉只见图表无上游表】
- **AC-5.3** Type B 现有「代理池」按钮（`:374`）+ `upstreamDrawer`（`:434-541`，内含 `loadGroupChart` 调用于 `:97`）行为不变。【测：Type B 代理池抽屉仍含表格+图表】

### 组件 ⑥ CLI 系统服务
- **AC-6.1** 新增 `--daemon`：Linux=systemd 安装（`systemctl` 不存在→报错失败退出）；Windows=SCM 服务；macOS→打印「不支持」并退出（spec:101）。【测：三平台分别运行】
- **AC-6.2** 服务名固定 `deeproxy`；已存在同名服务则**修改**配置而非新增（spec:102）。【测：连跑两次 `--daemon`，`systemctl list-units | grep deeproxy` 只 1 个】
- **AC-6.3** 服务 `WorkingDirectory` = exe 所在目录（`os.Executable()` + `EvalSymlinks`）（spec:103）。【测：服务运行后 `deeproxy.db` 落在 exe 同目录】
- **AC-6.4** 服务启动命令 = exe 路径 + `filterServiceArgs(os.Args[1:])`。**契约（精确 token 移除）**：仅移除恰好等于 `--daemon`/`-daemon`/`--startup`/`-startup`/`--daemon=true`/`--startup=true` 的 token；**其余每个 token 原样按位置透传**——不做 `=` 拆分、不剥前导破折号、不做值配对/吞下一个参数的逻辑。例 `./deeproxy --socks5 1000 --web 1001 --daemon` → `<exe> --socks5 1000 --web 1001`。【测：单元测试 `filterServiceArgs`（见验证 §六.3）；安装后 `systemctl cat deeproxy` 查 ExecStart】
- **AC-6.5** 安装成功后**启动**该服务；**若 `Install()` 成功但 `Start()` 失败 → 立即 `Uninstall()` 回滚**，不留下半注册的服务，并给出清晰中文报错（spec:105）。【测：占用端口造 Start 失败 → 报错且 `systemctl status deeproxy` 显示不存在/已清除】
- **AC-6.6** `--startup`（仅与 `--daemon` 同用有效）：设置服务为开机自启。**已提交机制（非提案）**：Windows 在 `Install` 时 `Config.Option["StartType"]="automatic"`（带 `--startup`）/`"manual"`（不带）；Linux `Install()` 后调用原始 `systemctl enable deeproxy`（带）/`systemctl disable deeproxy`（不带），`systemctl` 已由 AC-6.1 的 `LookPath` 保证存在（spec:106）。【测：`--daemon --startup` 后 `systemctl is-enabled deeproxy`=enabled；仅 `--daemon` 时=disabled；Windows `sc qc deeproxy` 看 START_TYPE】
- **AC-6.7** 安装/启动完成后前台进程退出，不再监听端口（spec:107）。【测：命令返回后前台无监听】
- **AC-6.8** 权限不足报错提示需 root/管理员（spec:108）。【测：非 root 运行看提示】
- **AC-6.9** 新增 `--help`：输出**中英双语**帮助，覆盖所有参数（`--socks5/--web/-v/--daemon/--startup/--help`）（spec:109）。**`--help` 在所有平台均可用，包括 macOS**——因 `flag.Usage` 由 `flag.Parse()`（`cmd/deeproxy/main.go:70`）触发，早于 daemon 分支与平台分发，故 macOS 上 `--help` 正常打印，只有 `--daemon` 才触发「不支持」。【测：macOS `./deeproxy --help` 含中英两段且不报「不支持」；`./deeproxy --daemon` 才报不支持】
- **AC-6.10** 采用 `github.com/kardianos/service`，go.mod 新增依赖（当前 NOT PRESENT）（spec:110）。【测：`grep kardianos go.mod`】

### 组件 ⑦ 输入验证
- **AC-7.1** `Group.name`：仅 `^[A-Za-z0-9]+$`，新增/编辑前端+后端双校验。前端 `web/src/views/proxy/ProxyGroups.vue` 组表单（`:384-396`，name 在 `:385-387`，`saveGroup` `:44-56`）；后端 `api/group_handler.go` 的 `handleCreateGroup`（`:118-150`，现校验在 `:123-139`）与 `handleUpdateGroup`（`:152-188`，现校验 `:165-176`）。【测：前端输入 `a-b` 被拦；后端单测 `POST /groups {name:"a-b"}` 返回 400】
- **AC-7.2** `ProxyUser.username`：同 `^[A-Za-z0-9]+$`，前端+后端双校验。前端 `web/src/views/user/Users.vue`（用户表单 `:205`、username 输入 `:206-207`、`save` `:65-88`）；后端 `api/user_handler.go` 的 `handleCreateUser`（`:76-97`，现校验 `:81-84`）与 `handleUpdateUser`（`:99-133`）。**注意**：Users.vue username 编辑态 `:disabled="dialog.isEdit"`（`:207`），编辑载荷 `:72` 仅含 `{username,remark}` 取自只读字段（值未变），故旧记录编辑**不会被前端拦**；后端校验仅作 PUT 直连防御（见 R-7）。【测：新建输入 `a-b` 前端拦+后端 400；编辑旧用户改备注可保存】
- **AC-7.3** 非法输入被拒并给出明确中文（i18n）错误提示。【测：错误文案存在且随语言切换】
- **AC-7.4** 存量旧数据不被强制改写、不阻断既有连接（校验仅 add/edit 路径；编辑旧记录策略见 §五风险表 R-7）。【测：库里已有含特殊字符记录不被后端启动校验拦截】
- **AC-7.5** `match` 值不受此校验影响（`Rules.vue` saveRule `:108-121` 不加字符校验）。【测：建一条 `ip-cidr:10.0.0.0/8` 规则成功】

---

## 三、按组件实施步骤（每步引用真实 file:line + 具体改动）

> **路径图例（本节及全文短名一律指以下全路径）**：`Rules.vue`=`web/src/views/rule/Rules.vue`；`Dashboard.vue`=`web/src/views/dashboard/Dashboard.vue`；`ProxyGroups.vue`=`web/src/views/proxy/ProxyGroups.vue`；`Settings.vue`=`web/src/views/system/Settings.vue`；`MainLayout.vue`=`web/src/layouts/MainLayout.vue`；`Users.vue`=`web/src/views/user/Users.vue`；`group_handler.go`=`api/group_handler.go`；`user_handler.go`=`api/user_handler.go`；`username.go`=`auth/username.go`；`group_repo.go`=`store/group_repo.go`；`main.go`=`cmd/deeproxy/main.go`。

### 组件 ① i18n 脚手架（先行，②③ 依赖之）

1. **加依赖**：`web/package.json:12-20` `dependencies` 新增 `"vue-i18n": "^9"`（**committed ^9，不选 ^10**，R-10）。运行 `npm install`（在 `web/` 目录）。
2. **建翻译资源**：新建 `web/src/locales/zh.js`、`web/src/locales/en.js`、`web/src/locales/index.js`。`index.js` 用 `createI18n({ legacy:false, locale, fallbackLocale:'zh', messages:{zh,en} })` 导出 i18n 实例（key 策略见 §四）。
3. **建语言 store**：新建 `web/src/stores/lang.js`，**照搬 `stores/theme.js` 模式**（`cat -n stores/theme.js` 已读：`STORAGE_KEY` 常量 `:6`、读 localStorage `:21`、`navigator.language` 替代 `matchMedia`、`setLocale()` 写回 localStorage 并 `i18n.global.locale.value = code`）。`STORAGE_KEY='deeproxy-lang'`，默认 `localStorage || (navigator.language.startsWith('zh')?'zh':'en')`。
4. **注册 i18n**：`web/src/main.js:13-19`，在 `app.use(router)` 后加 `app.use(i18n)`；并在 `:27` `useThemeStore(pinia).applyTheme()` 旁初始化 lang store 的 locale（保证首屏语言正确）。
5. **加切换器**：`MainLayout.vue` 顶栏在主题按钮（`:78-83`）与管理员下拉（`:85`）之间插入语言切换控件（建议 `el-dropdown` 选 中文/English，或 `el-switch`/文字按钮）。`<script setup>` 引入 `useLangStore` 与 `useI18n`。

### 组件 ② 文案中文化（依赖 ①）

6. **Rules.vue 动作**：
   - `:233` `{{ row.action }}` → `{{ t('action.' + row.action) }}`。
   - `:260-262` radio label `forward/direct/reject` → `{{ t('action.forward') }}` 等（value 仍 `forward/direct/reject`，AC 约束）。
   - `:294` `{{ tester.result.action }}` → `{{ t('action.' + tester.result.action) }}`。
   - `<script setup>` 加 `import { useI18n } from 'vue-i18n'; const { t } = useI18n()`。
7. **Rules.vue match 列**（AC-2.3）：`web/src/views/rule/Rules.vue:230` 改为带 `#default` 插槽的列，调用 `<script setup>` 辅助函数 `matchLabel(row.match)`：用 **`indexOf(':')` 首冒号切分**（非 `split(':')`，避免 IPv6 值被错切）取 type→`t('matchType.'+type)`，value=冒号后原文（保留所有 `:`），拼成 ``${typeLabel}: ${value}``。逻辑单处（DRY，见 §四 + §七 RALPLAN-DR 选项 B1 的最终函数体）。
8. **Rules.vue match 下拉**（AC-2.4）：`:250-252` `el-option` 的 `label` 由硬编码改为 `:label="t('matchType.domain')"` 等（value 不变 `domain/domain-suffix/ip-cidr`）。
9. **Dashboard.vue 饼图**（AC-2.2）：`actionOption` `:164-185`。后端 action-dist 返回的 `name` 是英文（`forward/direct/reject`）。两种落点：(a) 在 `loadActionDist`（`:97-103`）映射 `name` 为 `t('action.'+name)`；或 (b) 在 `actionOption` computed 内 `data.map` 时翻译 + `:179-181` 占位也翻译。推荐 (b) 保持数据层原值、展示层翻译（与 AC 约束「数据保持原值」一致）。注意 computed 依赖 locale 响应式：用 `useI18n()` 的 `t` 在 computed 内调用即随 locale 重算。
10. **核心页其余文案**（AC-2.5/2.6）：把 `MainLayout.vue:88`「管理员」、`:93`「系统设置」、`:94`「退出登录」、`:31`确认框文案；`router/index.js:32-62` `meta.title`（菜单/页头）；Rules/ProxyGroups/Settings 的卡片标题、按钮、对话框标题、`ElMessage` 提示等逐项替换为 `t(...)`。`meta.title` 在 `MainLayout.vue:74,61` 渲染——方案：`meta.title` 存 i18n key（如 `'menu.dashboard'`），渲染处 `{{ t(route.meta.title) }}`。

### 组件 ③ 类型改名（依赖 ①，与 ② 同批改 ProxyGroups/Dashboard）

11. **ProxyGroups 类型列**（AC-3.1）：`:362` `row.type === 'A' ? 'Type A 动态上游' : 'Type B 代理池'` → `t('groupType.' + row.type)`（`groupType.A='动态上游'`、`groupType.B='代理池'`）。
12. **ProxyGroups 建组 radio**（AC-3.2）：`:393-394` label 文本去 Type A/B → `{{ t('groupType.A') }}`/`{{ t('groupType.B') }}`，`value="A"/"B"` 不变（AC-3.4）。
13. **Dashboard 改名**（AC-3.3）：`:344`「暂无 Type B 分组」→ `t('dashboard.noPoolGroup')`；`:369`「Type A 动态上游」label → `t('groupType.A')` 或专用说明 key；`:372`「Type B 命名变量」→ 去 Type B 字样的 i18n key。注意这些是 `el-descriptions-item` 的 `label`，用 `:label="t(...)"`。

### 组件 ④ 设置重组 + 圆角（弱依赖 ①）

14. **圆角变量**（AC-4.3）：`styles/index.scss:8-16` `:root` 块内新增一行 `--el-card-border-radius: 12px;`（中文注释说明全局生效）。单点改动。
15. **拆卡片**（AC-4.1/4.2）：`Settings.vue:152-231` 左列 `el-col`。把当前单 `el-card`（`:154-230`）按 `el-divider` 边界拆成 4 张小卡：
    - 卡A「服务器与连接」= `:157-159`(管理员账号只读) + `:161-169`(serverAddr/probePoolSize)；
    - 卡B「运行期设置」= `:171-200`(defaultAction/logLevel/idleTimeoutSec/sniffDomain/sniffTimeoutMs)；
    - 卡C「统计」= `:202-206`(statRetentionDays)；
    - 卡D「健康检查默认值」= `:207-225`(hcDefaults.*)。
    - 「保存设置」按钮（`:226-228`）放在卡片组底部一处（仍调同一 `saveSettings()` `:61-79`，载荷不变）。
    - 排版：用 `el-row :gutter` + 多个 `el-col`（如各 `:span="12"` 两两并排）或并排小卡，保持同页适度。**字段一一对应、不删字段**（AC-4.2 硬约束）。

### 组件 ⑤ 动态上游流量入口（弱依赖 ①）

16. **加按钮**（AC-5.1）：`ProxyGroups.vue:373-377` 操作列，在 `:374`「代理池」按钮旁加 `<el-button v-if="row.type === 'A'" link type="primary" @click="openGroupChart(row)">{{ t('group.traffic') }}</el-button>`。
17. **新增图表抽屉**（AC-5.2）：`<script setup>` 新增 `const chartDrawer = reactive({ visible:false, group:null })` 与 `function openGroupChart(g){ chartDrawer.group=g; chartDrawer.visible=true; loadGroupChart(g.id) }`（复用现有 `loadGroupChart` `:295-308`）。模板在 `upstreamDrawer`（`:434-541`）之后新增独立 `el-drawer`，内只放 `分组流量(24h)` `EChart :option="groupChartOption"`（`:309-319`）+ `Top 目标域名` `EChart :option="groupTopDomainOption"`（`:322-335`），**不含上游表格/分页/批量**。
18. **Type B 不动**（AC-5.3）：`:374` 代理池按钮与 `upstreamDrawer` 保持原样；`groupTs/groupTopDomains` 为两抽屉共享 ref，因 A/B 不会同时打开，无冲突（如需隔离可在 `openGroupChart` 先清空，属可选硬化）。

### 组件 ⑥ CLI 系统服务（完全独立）

19. **加依赖**（AC-6.10）：`go get github.com/kardianos/service`，go.mod/go.sum 落地（当前 NOT PRESENT；go 1.26.3 `go.mod:3`）。
20. **新建模块**：建 `service/`（按功能分模块，CLAUDE.md §三）放 `service/daemon.go`（封装安装/启动/平台分发）+ `service/args.go`（参数过滤）+ 单测 `service/args_test.go`。`args_test.go` 须覆盖（AC-6.4 契约）：移除 `--daemon`/`-daemon`/`--startup`/`-startup`/`--daemon=true`/`--startup=true`；**原样保留** `--socks5 1000 --web 1001`、`--socks5=1000`（含 `=` 不拆）、`-socks5 1000`（两 token 都留）、`-web=1001`（留）。捆绑短选项（如 `-dv`）在 stdlib `flag` 下不可能出现 → **无需测试**（注释说明）。
21. **main.go 加 flag + 入口硬化**：`cmd/deeproxy/main.go:67-70` flag 区新增 `daemon := flag.Bool("daemon", false, "...")`、`startup := flag.Bool("startup", false, "...")`；`--help` 用自定义 `flag.Usage`（中英双语，覆盖全部 flag，AC-6.9）。daemon 分支**必须放在 `flag.Parse()`（`:70`）与 `-v` 分支（`:73-76`）之后、`store.Open`（`:86`）之前**：`if *daemon { service.InstallAndStart(*startup); os.Exit(0/1) }`。它在打开 DB / 启动任何服务前就 `return`/`os.Exit`，**绝不触碰 SQLite（避免 WAL 写争用）也不绑定端口（避免与后台服务进程端口冲突）**（AC-6.7）。
22. **service.InstallAndStart 设计**（AC-6.1/6.2/6.3/6.4/6.5/6.6/6.8）：
    - **平台分发**：`runtime.GOOS=="darwin"` → 打印「macOS 不支持系统服务」并 `os.Exit(1)`（spec:52）。`linux` → 先 `exec.LookPath("systemctl")`，缺失则报错失败退出（AC-6.1）。`windows` → 走 SCM（kardianos 自动）。
    - **WorkingDirectory（检查错误，不吞）**（AC-6.3）：`exe, err := os.Executable()` → err 非空打印中文报错 + `os.Exit(1)`；`exe, err = filepath.EvalSymlinks(exe)` → 同样检查 err；`workDir = filepath.Dir(exe)`，写入 `service.Config{WorkingDirectory: workDir}`。**禁止 `exe,_:=os.Executable()`**——空 workDir 会让 `./deeproxy.db` 落错位置，使 AC-6.3 失效。
    - **服务名固定 + 幂等重装**（AC-6.2）：`Config{Name:"deeproxy"}`。重跑改写流程：`Status()` → 若 `StatusRunning` 则 `Stop()` 后**轮询 `Status()` 直到非 Running（带上限超时，如 5s）** → `Uninstall()`（忽略 not-installed 错误）→ `Install()`。先停稳再卸载，避免旧进程仍占端口导致新服务 `Start()` 偶发 bind 失败。
    - **启动命令参数**（AC-6.4）：`Config{Executable: exe, Arguments: filterServiceArgs(os.Args[1:])}`。`filterServiceArgs` 按 AC-6.4 契约只移除精确 token。
    - **开机自启（已提交机制，非提案）**（AC-6.6）：
      - **Windows**：`Install` 前设 `Config.Option["StartType"] = "automatic"`（带 `--startup`）或 `"manual"`（不带）。`StartType` 是 kardianos 真实的 Windows Option（值 `automatic`/`manual`）。
      - **Linux**：systemd **没有**对应的 kardianos StartType Option（不要假设有）。机制：`Install()` 后调原始 `systemctl enable deeproxy`（带 `--startup`）/ `systemctl disable deeproxy`（不带），`systemctl` 已由前述 `LookPath` 保证存在。kardianos 的 `SystemdScript` 模板覆写仅作模板微调的文档化备选，**不**用于 enable/disable 控制。
      - 顺序：分发检查 → `Install()`（含 StartType）→ Linux 按 `--startup` enable/disable → `Start()`。
    - **Start 失败回滚**（AC-6.5）：若 `Install()` 成功但 `Start()` 返回错误 → 调 `Uninstall()` 清理半注册服务，再打印中文报错 + `os.Exit(1)`。
    - **权限不足**（AC-6.8）：捕获 Install/Start 的权限类错误，包装为「需以 root / 管理员身份运行」中文提示后 `os.Exit(1)`，不自动 sudo。
    - **权限不足**（AC-6.8）：捕获 Install/Start 的权限类错误，包装为「需以 root / 管理员身份运行」中文提示后 `os.Exit(1)`，不自动 sudo。
23. **--help 双语**（AC-6.9）：自定义 `flag.Usage = func(){ fmt.Println(中文段); fmt.Println(English block) }`，列全 `--socks5/--web/-v/--daemon/--startup/--help` 用途。

### 组件 ⑦ 输入验证（完全独立，前端文案走 ①）

24. **后端正则 + 不变式注释**：新建 `auth/identifier.go`（或 `api/validate.go`）：`var idRe = regexp.MustCompile(\`^[A-Za-z0-9]+$\`)` + `func ValidIdentifier(s string) bool`。**强制中文 code-comment 说明不变式**：真正的结构约束是「**不含 `-`**」——因 `auth/username.go:48` `SplitN(username,"-",3)` 用 `-` 切 user/group 段，名字含 `-` 会破坏解析。`^[A-Za-z0-9]+$` 是**刻意更严的超集**（顺带规避未来新分隔符与展示边界问题）。**警告后人：不得放宽为允许 `_`/`#`**——这些字符虽在 v2 尾段（变量串 `name_value#...`，见 CLAUDE.md 用户名契约）合法，但 user/group 段绝不允许。（根因：spec:Assumption 行 126 + `auth/username.go:42-69`）
25. **后端 group**（AC-7.1）：`api/group_handler.go` `handleCreateGroup`（`:118`）在空校验 `:123-126` 之后、重名校验 `:133` 之前插入 `if !ValidIdentifier(req.Name) { respondError(c,400,"分组名只能包含英文字母与数字"); return }`。`handleUpdateGroup`（`:152`）按 R-7「仅变更才校验」改造（见 step 26 同模式）：**先 `old, err := a.store.GetGroup(id)`**（`store/group_repo.go:31`，`WHERE id=?`，**不是** `GetGroupByName`）→ nil-guard（不存在返回 404）→ `if req.Name != old.Name && !ValidIdentifier(req.Name) { 400; return }`。（`handleUpdateGroup` 当前未按 id 取旧 group，需新增此查询；现有重名校验 `:170` 用的 `GetGroupByName(req.Name)` 是按新名查、用途不同，保留它做撞名检查，二者并存。）
26. **后端 user**（AC-7.2）：`api/user_handler.go` `handleCreateUser`（`:76`）在 `:81-84` 空校验后插入 `if !ValidIdentifier(req.Username) { 400; return }`。`handleUpdateUser`（`:99`）已在 `:104` 取得 `old`（`GetProxyUser(id)`），在 `:117` `old.Username=req.Username` 之前插入 `if req.Username != old.Username && !ValidIdentifier(req.Username) { 400; return }`——**仅当用户名实际变更才校验**（R-7：前端编辑态 username 只读、不会变；此后端校验仅防 PUT 直连）。
27. **前端 group 表单**（AC-7.1/7.3）：`web/src/views/proxy/ProxyGroups.vue` 组对话框（`:383-432`）改用 `el-form ref="groupFormRef" :model :rules="groupRules"`，`name` 的 `el-form-item` 加 `prop="name"`，`groupRules.name=[{required,message,trigger:'blur'},{pattern:/^[A-Za-z0-9]+$/,message:t('validate.alnum'),trigger:'blur'}]`。`saveGroup`（`:44`）先 `await groupFormRef.value.validate()` 通过再提交。
28. **前端 user 表单（具体落点）**（AC-7.2）：`web/src/views/user/Users.vue` 当前用户表单（`:205` `<el-form :model="dialog.form">`）**无 `ref`、无 `:rules`**；username 输入 `:206-207`；`save`（`:65`）用命令式 `if(!f.username)ElMessage.warning`（`:67`）。具体改造：给表单加 `ref="userFormRef"` + `:rules="userRules"`；给 username 的 `el-form-item`（`:206`）加 `prop="username"`；定义 `userRules.username=[{required,message,trigger:'blur'},{pattern:/^[A-Za-z0-9]+$/,message:t('validate.alnum'),trigger:'blur'}]`；`save` 内提交前先 `await userFormRef.value.validate()`。**与 step 27 的 ProxyGroups 校验写法保持一致**（同一 Element Plus form-validation 模式）。注：因 username 编辑态 `:disabled`（`:207`），rule 实际只在新建态触发，符合 R-7。
29. **i18n 错误文案**（AC-7.3）：`locales/zh.js`/`en.js` 加 `validate.alnum`（「只能包含英文字母与数字」/ "Only letters and digits allowed"）。后端中文提示固定（后端不接 i18n，返回中文即可，前端展示 `ElMessage`）。
30. **match 不校验**（AC-7.5）：`Rules.vue:108-121` saveRule 不加字符校验，确认无回归。

---

## 四、i18n key 策略（命名空间结构）

**实例配置**：`createI18n({ legacy:false, globalInjection:true, locale, fallbackLocale:'zh', messages:{zh,en} })`。`fallbackLocale:'zh'` 保证缺 key 时回退中文而非显示 key（见 §五 R-3）。

**命名空间（zh.js/en.js 同结构）**：

```
action:    { forward, direct, reject }            // 组件②：动作 → 转发/直连/拒绝
matchType: { domain, 'domain-suffix', 'ip-cidr' } // 组件②：match 类型前缀 → 精确域名/域名后缀/IP段
groupType: { A, B }                               // 组件③：A→动态上游、B→代理池（key 用内部值 A/B，值是展示名）
menu:      { dashboard, proxy, rule, user, syslog, system }  // 组件②：菜单/页头标题
common:    { save, cancel, edit, delete, confirm, ... }      // 通用按钮
group:     { traffic, ... }                       // 组件⑤：「流量」按钮等
dashboard: { noPoolGroup, ... }                   // 组件③：暂无代理池分组等
settings:  { serverConn, runtime, stat, hcDefaults, ... }    // 组件④：小卡片标题
validate:  { alnum }                              // 组件⑦：校验错误
rules / proxyGroups / settings.*                  // 各页专有文案
```

**关键设计：action / matchType / groupType label 集中存放**，组件用内部英文值拼 key：
- 动作：`t('action.' + row.action)`（值 `forward/direct/reject`）。
- 类型：`t('matchType.' + type)`（值 `domain/domain-suffix/ip-cidr`，注意带连字符的 key 用引号）。
- 组类型：`t('groupType.' + row.type)`（值 `A/B`）。
这样数据层始终是原始英文（满足 AC「API/DB 保持原值」），仅在渲染处映射，零数据污染。

**match 列「type label + 原始 value」组合**（AC-2.3）——采用 §七 RALPLAN-DR 选项 B（computed 拼接，推荐）：
```
function matchLabel(match){
  const i = match.indexOf(':')
  if (i < 0) return match
  const type = match.slice(0, i)          // 'domain-suffix'
  const value = match.slice(i + 1)        // 原始值，保留所有 ':'（IPv6/CIDR 安全）
  return `${t('matchType.' + type)}: ${value}`
}
```
注意用 `indexOf(':')` 首个冒号切分而非 `split(':')`，避免 IPv6 值（含多个 `:`）被错切——比 `Rules.vue:103` 的 `split(':')` 更稳，且 value 原样保留（AC 约束）。

---

## 五、风险与缓解（Risks & Mitigations）

| # | 风险 | 影响 | 缓解 |
|---|------|------|------|
| **R-1** | CLI 服务**权限不足**（非 root/admin）安装失败 | `--daemon` 报错 | 捕获权限错误→中文提示「需以 root/管理员身份运行」，`os.Exit(1)`，不自动 sudo（AC-6.8）。CI 里此路径需 root 容器或跳过。 |
| **R-2** | **WorkingDirectory/db 路径**：服务 CWD 默认为 `/`（systemd）或 System32（SCM），`./deeproxy.db`（`main.go:55`）会落错位置 | 服务起来后读不到/新建空库 | `Config.WorkingDirectory = filepath.Dir(EvalSymlinks(os.Executable()))`（AC-6.3）。需在实现期**验证 kardianos systemd 模板确实写入 `WorkingDirectory=`**（其 Config 支持该字段）。 |
| **R-3** | **systemctl 缺失**（非 systemd Linux，如 Alpine/OpenRC） | 服务无法管理 | `exec.LookPath("systemctl")` 失败即报错失败退出（AC-6.1），不静默降级。 |
| **R-4** | **Windows SCM** 行为与 systemd 不一致（启动账户、依赖、StartType 命名） | 跨平台语义偏差 | kardianos 抽象 SCM；`--startup` 映射 StartType auto/manual（见 R-5）。Windows 需 admin（R-1）。 |
| **R-5** | **`--startup` vs 开机自启语义**：systemd `Install()` 默认写 `WantedBy` 即倾向自启，需可控表达「装但不自启」 | AC-6.6 语义 | **已提交机制（非提案）**：`Install()` 装服务 → **Windows** 用 `Config.Option["StartType"]="automatic"/"manual"`（kardianos 真实 Windows Option）；**Linux** 用原始 `systemctl enable/disable deeproxy`（`systemctl` 已由 AC-6.1 LookPath 保证）。**systemd 无 kardianos StartType Option（删除此前错误说法）**，raw systemctl 是唯一路径；`SystemdScript` 模板仅作模板微调的文档化备选，不用于 enable 控制。「自启与否」与「本次是否启动」解耦。 |
| **R-6** | **kardianos 能力确认**（非发现）：`Config.WorkingDirectory` 写入 systemd unit、`Option["StartType"]` 在 Windows 生效、`Status/Stop/Uninstall/Install` 幂等 | 设计前提 | 这些均为 kardianos **已文档化能力**，非未知量。实现首步做一次**确认性 spike**（confirm-not-discover）跑通 `Status→Stop→poll→Uninstall→Install→enable/disable→Start`；若个别字段未生效则回退 §七选项 A2（手写 systemd unit + `sc.exe`）。**不作为 load-bearing 未知**。 |
| **R-7** | **校验拦旧数据（重定范围）** | 与 AC-7.4 冲突 | **Users（无陷阱）**：Users.vue username 编辑态 `:disabled`（`web/src/views/user/Users.vue:207`），编辑载荷（`:72`）的 username 取自只读字段、值不变，故**编辑旧用户绝不被拦**；后端 `handleUpdateUser` 的「仅变更才校验」（`if req.Username!=old.Username && !ValidIdentifier(...)`，`old` 来自 `:104`）仅作 PUT 直连防御。**Groups（name 可改）**：`handleUpdateGroup`（`api/group_handler.go:152`）须**先 `old,err:=a.store.GetGroup(id)`**（`store/group_repo.go:31`，`WHERE id=?`）→ nil-guard → `if req.Name!=old.Name && !ValidIdentifier(req.Name){400}`。**不得用 `GetGroupByName(req.Name)`**（`group_repo.go:37`，`WHERE name=?` 按**新名**查、rename 后取到错记录或 nil）取旧值；`GetGroupByName` 保留仅用于原有撞名检查（`:170`），二者用途不同、并存。前端 rule 同理仅校验实际输入。 |
| **R-8** | **i18n 缺 key** 显示原始 key（如 `action.forward`） | 文案露馅 | `fallbackLocale:'zh'` + 巡检脚本核对 zh/en key 集合一致；②⑤ 巡检覆盖核心页。 |
| **R-9** | **Dashboard 饼图/computed 不随语言响应式重算** | 切语言饼图仍英文 | 在 computed 内调用 `useI18n().t`（`t` 依赖响应式 locale），切 locale 自动重算；避免在 `onMounted` 一次性求值后缓存英文。 |
| **R-10** | **vue-i18n 版本**与 Vue 3.5（`package.json:18`）兼容性 | 构建/运行报错 | 选 vue-i18n ^9（成熟稳定，Vue3 兼容）；`legacy:false` 用 Composition API；构建验证。 |
| **R-11** | **嵌入式 dist 过期**：`api/static.go:27 //go:embed all:dist`，后端嵌入 `web/dist`。前端改完未重建则 Go 二进制仍是旧 UI | 改动不生效 | 验证步骤含 `npm run build` 后再 `go build`；CI 顺序固化。 |
| **R-12** | **Settings 拆卡丢字段**：手工搬运 14 字段易漏 | 保存载荷缺字段 | `saveSettings()`（`:61-79`）载荷不变是硬基线；拆卡只动模板不动 script；保存往返核对（AC-4.2）。 |

---

## 五-bis、i18n 客观完成门控（Done-Gate，组件①②③）

> 组件①②③原本只有「逐页切语言巡检」的主观 AC（不可证伪）。本节给出**可机械执行**的完成判据，verifier 必须逐项通过。

### G-1 grep 漏网检查（无裸展示字面量）
```
grep -rn -E "Type A|Type B|>forward<|>direct<|>reject<|domain-suffix（|ip-cidr（" web/src/views web/src/layouts
```
要求：返回结果**只能**落在 `locales/*.js`（翻译资源本体）或内部枚举属性（`value="A"`/`value="forward"`/`value="domain-suffix"` 等 `value="..."`）行；**模板里不得残留任何裸展示字面量**。任何在 `{{ }}`、`label="中文/英文硬串"`、`:title="'...'"` 等展示位的命中 = 未完成。（注：`locales/` 不在 `views/layouts` 下，故上面 grep 命中即为漏网；如把 locales 放他处需相应排除。）

### G-2 zh/en key 集合一致（R-8 缺 key 风险）
`web/src/locales/zh.js` 与 `en.js` 导出**完全相同的 key 集合**。落地一个极小 node 断言（CI 或本地）：
```
node -e "const z=require('./web/src/locales/zh.js').default,e=require('./web/src/locales/en.js').default;const f=(o,p='')=>Object.entries(o).flatMap(([k,v])=>typeof v==='object'?f(v,p+k+'.'):[p+k]);const zs=new Set(f(z)),es=new Set(f(e));const miss=[...zs].filter(x=>!es.has(x)).concat([...es].filter(x=>!zs.has(x)));if(miss.length){console.error('KEY MISMATCH:',miss);process.exit(1)}console.log('keys parity OK',zs.size)"
```
（ESM 导出时改用动态 import；目的固定：两文件 key 全等，缺一即失败。）

### G-3 字面量 → key 映射表（verifier 勾选清单，组件②③）

| 组件 | 文件:行 | 原字面量 | 目标 key | 备注 |
|------|---------|----------|----------|------|
| ② | `web/src/views/rule/Rules.vue:233` | `{{ row.action }}`（裸 forward/direct/reject） | `action.{forward,direct,reject}` | value 不变 |
| ② | `web/src/views/rule/Rules.vue:260-262` | radio `forward`/`direct`/`reject` 文案 | `action.*` | `value="forward"` 等不变 |
| ② | `web/src/views/rule/Rules.vue:294` | `{{ tester.result.action }}` | `action.*` | |
| ② | `web/src/views/rule/Rules.vue:230` | match 列裸 `type:value` | `matchType.* + ': ' + value` | `indexOf(':')` 切，value 原样 |
| ② | `web/src/views/rule/Rules.vue:250-252` | 下拉 label「domain（精确域名）」等 | `matchType.{domain,domain-suffix,ip-cidr}` | value 不变 |
| ② | `web/src/views/dashboard/Dashboard.vue:175-179` | 饼图占位 `forward/direct/reject` | `action.*` | 实时+占位都翻 |
| ② | `web/src/layouts/MainLayout.vue:88,93,94` | `管理员`/`系统设置`/`退出登录` | `common.admin`/`menu.system`/`common.logout` | |
| ② | `web/src/layouts/MainLayout.vue:31` | 退出确认框文案 | `common.logoutConfirm.*` | |
| ② | `web/src/router/index.js:32-62` | `meta.title` 中文串 | `menu.*` | 渲染处 `{{ t(route.meta.title) }}`（`MainLayout.vue:61,74`） |
| ③ | `web/src/views/proxy/ProxyGroups.vue:362` | `Type A 动态上游`/`Type B 代理池` | `groupType.{A,B}`（动态上游/代理池） | `row.type` 值不变 |
| ③ | `web/src/views/proxy/ProxyGroups.vue:393-394` | radio `Type A 动态上游`/`Type B 代理池` | `groupType.{A,B}` | `value="A"/"B"` 不变 |
| ③ | `web/src/views/dashboard/Dashboard.vue:344` | `暂无 Type B 分组` | `dashboard.noPoolGroup` | |
| ③ | `web/src/views/dashboard/Dashboard.vue:369` | `Type A 动态上游`（label） | `groupType.A` 或说明 key | |
| ③ | `web/src/views/dashboard/Dashboard.vue:372` | `Type B 命名变量`（label） | 去 Type B 字样的说明 key | |

> 通过判据：G-1 无模板漏网 + G-2 key 全等 + G-3 表中每行均已替换。三者全绿才算组件①②③完成。

## 六、验证步骤（Verification）

**后端（组件⑥⑦）**：
1. `go build ./...`（本机）通过。
2. 交叉编译：`GOOS=linux GOARCH=amd64 go build`、`GOOS=linux GOARCH=arm64`、`GOOS=windows GOARCH=amd64`、`GOOS=darwin GOARCH=arm64` 各产物生成（CLAUDE.md 跨平台约束）。
3. `go test ./service/...`：`filterServiceArgs` 单测覆盖 — **移除** `--daemon`/`-daemon`/`--startup`/`-startup`/`--daemon=true`/`--startup=true`；**原样保留** `--socks5 1000 --web 1001`、`--socks5=1000`、`-socks5 1000`（两 token）、`-web=1001`（AC-6.4）。捆绑短选项 `-dv` 在 stdlib flag 下不可能 → 不测。
4. `go test ./auth/...` 或 `./api/...`：`ValidIdentifier` 单测（合法 `abc123`、非法 `a-b`/`a_b`/`空`/`中文`）（AC-7.1/7.2）。
5. Linux root：`./deeproxy --socks5 1000 --web 1001 --daemon` → 服务起、前台退出、`deeproxy.db` 在 exe 旁、`systemctl cat deeproxy` ExecStart 无 `--daemon`；连跑两次只 1 个服务；`--startup` 时 `is-enabled`=enabled、不带时 disabled。macOS 跑 `--daemon` 见「不支持」。`--help` 见中英双段。

**前端（组件①②③④⑤⑦）**：
6. `cd web && npm install && npm run build` 通过（vue-i18n 解析、无缺 import）。
7. **i18n 完成门控（先跑）**：执行 §五-bis 的 G-1（grep 无模板漏网）+ G-2（zh/en key 全等）+ G-3（映射表逐行已替换）。三者全绿是组件①②③的硬性完成判据。
8. 手动切换测试（人工巡检补充）：起 dev（`npm run dev`），右上角切 中/英 → 巡检 Dashboard/Rules/ProxyGroups/Settings/菜单：动作显示转发/直连/拒绝（英文 forward/...）、match 列「域名后缀: ip.sb」、类型列「动态上游/代理池」无 Type A/B、设置两列并排小卡、el-card 圆角变大、Type A 行有「流量」按钮且抽屉仅图表。
9. localStorage 持久化：切英文→刷新仍英文；清 `deeproxy-lang`→按 `navigator.language` 默认。
10. 校验：分组名/用户名输入 `a-b` 前端拦截+后端 400；编辑旧含 `-` 记录改备注可保存（R-7）。

**端到端**：
11. `npm run build` → `go build` → 跑二进制，浏览器访问 Web 端口确认嵌入的新 UI 生效（R-11）。

---

## 七、RALPLAN-DR 决策记录

**Mode**: SHORT（brownfield、展示层/CLI 增强，风险有界；非高危重构）。

### Principles（3-5）
1. **数据层零污染**：i18n/改名只动展示层，`forward/direct/reject`、`A/B`、`match` 在 API/DB 保持原始英文/原值（spec:47,60）。
2. **复用成熟库不造轮子**：CLI 服务用 `kardianos/service` 统一 systemd+SCM+自启（CLAUDE.md 全局 §1，spec:56）。
3. **DRY + 模块化**：action/matchType/groupType label 集中于 locales；match 组合逻辑单处函数；服务逻辑独立 `service/` 包（CLAUDE.md §三/§四）。
4. **存量不破坏**：校验仅作用于「实际变更的输入」，旧数据可继续编辑其他字段（spec:54，AC-7.4，R-7）。
5. **跨平台诚实降级**：能力缺失（systemctl/macOS）直接报错或明确不支持，绝不静默假成功（spec:52）。

### Decision Drivers（top 3）
1. **不改后端转发核心**（一号硬约束，spec:46）→ 所有 i18n 走前端、CLI 走入口层、校验走 handler 边界。
2. **跨平台单二进制**（CLAUDE.md 约束）→ 服务库必须同时覆盖三平台，参数过滤需可单测。
3. **可测性 / 验收闭环**（spec 9 条 AC 组）→ 优先选「行为可单测 + 可目视」的实现路径。

### Viable Options（≥2，bounded pros/cons）

**决策 A：CLI 系统服务实现方式**
- **A1（选定）kardianos/service 库**：
  - Pros：一库覆盖 systemd + Windows SCM，spec 钦定（行 56/110）；`Config.WorkingDirectory`/`Arguments`/`Executable`/`Option["StartType"]` 直接表达需求；维护活跃。
  - Cons：systemd 的「装但不自启」需补一行原始 `systemctl enable/disable`（已提交机制，R-5）；幂等改写需自行组合 `Status/Stop/poll/Uninstall/Install`（已设计，step 22）。均为有界的小封装，非未知量。
- **A2 手写 systemd unit + `sc.exe`**：
  - Pros：完全掌控 unit 字段（WorkingDirectory/WantedBy 精确控制 enable）；无第三方依赖。
  - Cons：违反「复用成熟库」原则；三平台各写一套、维护成本高、易漏边界；与 spec:56 冲突。
- **裁决**：A1（已提交机制见 step 22 + R-5）。A2 仅作 R-6 确认性 spike 万一某字段失效时的回退预案，非默认路径。

**决策 B：match 列「type label + 原值」组合方式**
- **B1（选定）computed/函数拼接**（`matchLabel(match)` 用 `indexOf(':')` 切分）：
  - Pros：首冒号切分对 IPv6/CIDR 安全；value 原样保留（AC 约束）；逻辑单处、DRY；随 locale 响应式重算。
  - Cons：需在 script 写辅助函数（极小成本）。
- **B2 vue-i18n 插值**（`t('match.tpl', {type, value})`，模板 `{type}: {value}`）：
  - Pros：模板化、zh/en 可各自定语序。
  - Cons：仍需先在 JS 算出 type 的中文 label（matchType.* 还是得查），等于两次 t 调用，反而绕；语序差异本需求不存在（都是「label: value」）。
- **裁决**：B1。语序无差异，函数拼接最简且最易测。

### 仅一可行项的说明
组件①④⑤⑦的前端落点（store 照搬 theme.js、Settings 按 divider 拆卡、新增独立 chartDrawer、handler 边界加正则）均为该代码结构下的唯一自然路径，已在 §三给出具体 file:line；其替代方案（如把 i18n 塞进每组件局部、或复用 upstreamDrawer 装图表）会违反 DRY/AC-5.2「不含代理池表格」，已被原则 3 与 AC 排除，故不展开备选。

### ADR（架构决策记录）
- **Decision**：(1) CLI 系统服务用 `kardianos/service`，开机自启用「Windows StartType + Linux raw systemctl enable/disable」组合机制；(2) i18n 走纯展示层翻译、数据层保留原始英文/原值，match 列用 `indexOf(':')` 函数拼接；(3) 名称校验在前后端双侧用 `^[A-Za-z0-9]+$`，更新接口「仅变更才校验」。
- **Drivers**：不改后端转发核心（spec:46）；跨平台单二进制（CLAUDE.md）；可测性/验收闭环（spec 9 组 AC + §五-bis done-gate）。
- **Alternatives considered**：CLI = 手写 systemd unit + `sc.exe`（A2，违反复用成熟库，仅作回退）；match 组合 = vue-i18n 插值（B2，反而两次 t 调用、无语序收益）；校验位置 = 仅前端（被否：API 直连可绕过）。
- **Why chosen**：A1+B1 在「复用成熟库 / DRY / 可测」三原则下占优；双端校验 + 仅变更才校验同时满足 AC-7.1/7.2（防绕过）与 AC-7.4（不破坏存量）。
- **Consequences**：(+) 三平台一致的服务管理、零数据契约污染、存量数据可继续编辑；(−) systemd 自启需补一行 raw systemctl（有界小封装）；(−) 前端每页需引 `useI18n`、zh/en 双份资源需维护（G-2 门控兜底）。
- **Follow-ups**：R-6 确认性 spike（kardianos 字段生效）；R-7 编辑旧记录策略待产品确认（open-questions 唯一阻塞项）；上线后补 en 文案润色（非阻塞）。

---

## 八、开放问题（写入 .omc/plans/open-questions.md）
- 组件⑦编辑旧记录策略（R-7「仅变更时校验」）需实现时确认产品认可（spec 仅说「不强制迁移」，未明说编辑旧记录能否原样保存其他字段）。
- R-6 kardianos 确认性 spike：实现首步跑通 `WorkingDirectory`/`Option["StartType"]`/`Status/Stop/Uninstall/Install` 均生效（已文档化、非 load-bearing 未知）；若个别失效切 A2 回退。

> 已关闭（本轮提交决策，不再开放）：`--startup`/boot-enable 机制（R-5 已提交：Windows StartType + Linux raw systemctl）；vue-i18n 版本（committed ^9，R-10）；Users.vue 表单结构（已读取，step 28 给出 `:205-207` 具体落点）。
