# 实现计划：前端响应式优化（手机端可正常使用）

> 来源 spec：`.omc/specs/deep-interview-frontend-responsive.md`（ambiguity 13.25%）
> 模式：consensus --direct（RALPLAN-DR short）
> 状态：**pending approval**

---

## Requirements Summary
将 deeproxy Web 控制台改造为两档响应式（手机 <768px / 桌面 ≥768px），消除手机端样式错乱与横向溢出，桌面端观感保持不变。范围 4 组件：应用外壳/导航、仪表盘、表格管理页、认证页。

## RALPLAN-DR Summary

### Principles（原则）
1. **桌面零回归**：所有改动用 `@media (max-width: 768px)` 或等价 JS 守卫包裹，≥768px 路径不变。
2. **复用 Element Plus 原生能力**：优先 el-drawer / el-col 断点 / Dialog 响应式 width，不造轮子（遵循全局规范）。
3. **沿用既有特异度约定**：覆盖 EP 样式用 `body .xxx` 提高特异度，避免被按需注入 chunk 覆盖（见 index.scss 既有注释）。
4. **单一断点真相源**：768px 仅定义一次（SCSS 变量 + JS 常量各一处），不散落魔法数字。
5. **不溢出优先于美观**：手机端首要目标是页面外框不被撑破，表格溢出收敛到表格容器内。

### Decision Drivers（前三）
1. 工作量可控（用户选了横向滚动而非表格→卡片重排）。
2. 桌面端必须不变（回归风险最小化）。
3. 移动端导航可用性（汉堡抽屉是核心交互改动）。

### Viable Options（移动端断点切换机制）
- **Option A：纯 CSS @media**（推荐）
  - Pros：零 JS、无重排闪烁、性能最好；桌面路径完全不执行。
  - Cons：抽屉开关需要状态，纯 CSS 无法管理 el-drawer 的 visible，导航部分仍需少量 JS。
- **Option B：JS 监听 viewport（matchMedia/ResizeObserver）全量驱动**
  - Pros：布局模式集中在 JS，可派生 isMobile 给所有组件用。
  - Cons：首屏可能闪烁；与 keep-alive + ECharts resize 交互复杂；过度工程。

**选择 Option A（CSS 为主）+ 最小 JS（仅 isMobile 用于抽屉开关 & header 文本精简）。** Option B 被否：纯 CSS 已能覆盖 90% 布局问题（卡片堆叠、表格滚动、dialog 宽度、认证页），只有抽屉 visible 与个别条件渲染需要 isMobile 布尔值，引入全量 JS 驱动得不偿失。

### 决策边界规则（共识新增，消除 768px 双真相源张力）
为缓解 Architect 指出的"结构决策跨 CSS/JS 两套真相源"张力，确立明确分工规则：
- **结构 / 显隐 / 条件渲染** → 一律用 JS `isMobile`（aside 显隐用 `v-show="!isMobile"`、抽屉 visible、header 文本精简用 `v-show`）。**不要**用 CSS `display:none` 做结构决策。
- **纯外观样式**（padding、gap、flex-wrap、字号、`overflow-x`、表格容器约束、dialog 宽度）→ 用 CSS `@include r.mobile`。这些不参与结构决策，双写不产生边界撕裂。
- 768px 常量双写不可避免（SCSS 编译期 vs JS 运行期隔离），但锁定为各一处定义：SCSS `$dp-mobile-max: 767.98px`、JS `MOBILE_MAX = 767.98`，互相注释指向。

## Acceptance Criteria（继承 spec，可测）
- [ ] AC-1：<768px MainLayout 固定侧边栏隐藏，顶栏出现汉堡图标。
- [ ] AC-2：点击汉堡图标从左滑出 el-drawer 导航，选中菜单后自动关闭并跳转。
- [ ] AC-3：<768px 顶栏不溢出/不错乱；admin 用户名文字与语言文字标签在窄屏隐藏（仅留图标）。
- [ ] AC-4：<768px 仪表盘指标卡片双列堆叠（已有 `:xs="12"`，验证生效）。
- [ ] AC-5：<768px ECharts 三图随容器自适应，不超卡片、不重叠（断点切换后触发 resize）。**含 ProxyGroups 两个 drawer 内的 4 个 EChart（line 560/563/574/577）在 mobile 90% drawer 内正常自适应。**
- [ ] AC-6：<768px 四个表格页表格区域内部横向滚动，**页面整体无横向溢出**（含 SysLog audit tab 表格）。
- [ ] AC-7：<768px 表格操作列 `fixed="right"` 可见可点（含 drawer 内表格的 fixed 列）。
- [ ] AC-8：<768px 所有 el-dialog（520/560px → 响应式 92%）宽度自适应不超屏。
- [ ] AC-9：<768px Login/Setup 表单卡片（Login 380px / Setup 400px → 各自 min(原值,92vw)）居中不溢出。
- [ ] AC-10：≥768px 所有页面观感与改造前一致（回归）。
- [ ] AC-13：<768px Settings.vue 表单 label（label-width 150px）不挤压输入框/不错位（mobile 改 label-position="top" 或缩小 label-width）。
- [ ] AC-11：DevTools 移动模拟（iPhone ~390px）逐页截图无错位/溢出。
- [ ] AC-12：`pnpm build` 通过。

---

## Implementation Steps

### Step 0：建立断点单一真相源
- **新增** `web/src/styles/index.scss`：定义 SCSS 变量 `$dp-mobile-max: 767.98px;`（放文件顶部）与一个 mixin `@mixin mobile { @media (max-width: $dp-mobile-max) { @content; } }`。注意 `.scss` 用 `@use 'sass:...'` 已有，mixin 需 `@forward` 或同文件内定义；因各组件 `<style scoped lang="scss">` 独立编译，mixin 需放到一个可 `@use` 的共享文件。
  - **决策**：新建 `web/src/styles/responsive.scss` 存放 `$dp-mobile-max` + `@mixin mobile`，各组件 `<style>` 顶部 `@use '@/styles/responsive.scss' as r;` 后用 `@include r.mobile { ... }`。
- **新增** JS 常量：`web/src/stores/app.js` 中增加 `isMobile` 响应式状态 + matchMedia 监听（见 Step 1）。
- **【冒烟先行】**`@use '@/styles/responsive.scss'` 在组件 scoped block 中的别名解析在本仓库无先例（现有唯一 `@use` 在 index.scss，且无 `css.preprocessorOptions.scss.additionalData`）。**先在单个组件 scoped style 加一处 `@use` + `@include r.mobile{}` 并 `pnpm build` 验证通过，再向其余组件铺开**。若别名不解析，备选：在 `vite.config.js` 配 `css.preprocessorOptions.scss.additionalData: "@use '@/styles/responsive.scss' as r;"` 自动注入（一处配置，免去各文件 `@use` 样板，更 DRY）。

### Step 1：应用外壳/导航（MainLayout.vue + app.js）— 核心
**app.js（`web/src/stores/app.js`）**：
- 新增 `isMobile` ref，用 `window.matchMedia(\`(max-width: ${MOBILE_MAX}px)\`)` 初始化并监听 `change` 更新（中文注释说明：原生、无轮询、断点切换即时回调）。导出 `MOBILE_MAX = 767.98` 常量（JS 侧单一定义，注释指向 SCSS `$dp-mobile-max`）。
- **HMR 幂等**：用模块级 flag 确保 matchMedia 监听只注册一次；保留 mql/回调引用，`import.meta.hot?.dispose(() => mql.removeEventListener('change', cb))`，避免 dev 热更新叠加监听器（Pinia setup store 为应用级单例，无需组件级清理，但需防 HMR 叠加）。
- 新增 `mobileDrawer` ref（移动端导航抽屉 visible）+ `openDrawer/closeDrawer`。
- 保留现有 `sidebarCollapsed`/`toggleSidebar`（桌面端折叠逻辑不变）。

**MainLayout.vue（`web/src/layouts/MainLayout.vue`）**：
- 顶栏汉堡图标 `@click`：桌面端→`toggleSidebar`（原行为）；移动端→`openDrawer`。用 `appStore.isMobile` 分支。
- el-aside（line 49-69）：移动端用 **`v-show="!appStore.isMobile"`** 隐藏（结构决策走 JS 真相源，**不用** CSS display:none）。
- 新增移动端 `<el-drawer v-model="appStore.mobileDrawer" direction="ltr" :with-header="false" size="220px">`，内部复用菜单。
- **菜单复用契约（共识明确，消除歧义）**：采用 **inline `v-for` 片段在 aside 与 drawer 中各写一份**（最简，轻微 DRY 成本可接受），**不**抽取子组件——避免子组件 props/emit 契约（`go()`、`activeMenu`、`menuRoutes` 透传）带来的额外复杂度与 wiring 风险。两处 `<el-menu>` 均绑定同一响应式 `:default-active="activeMenu"`（computed，line 28），EP 各实例独立维护 active 且随路由同步高亮，无双激活冲突（Critic 已验证）。drawer 内菜单 `@select` 调 `go(name)` 后再 `appStore.closeDrawer()`。
- 顶栏 header-right（scoped style line ~205）：`@include r.mobile { gap: 8px; }`；`.admin-name`/`.lang-label` 在 mobile 下用 **`v-show="!appStore.isMobile"`**（结构走 JS）隐藏文字仅留图标，消除溢出（AC-3）。
- header-title 在极窄屏可缩小字号或省略（`@include r.mobile`，纯样式）。

### Step 2：表格管理页横向滚动收敛（4 文件）
对 `ProxyGroups.vue` / `Rules.vue` / `Users.vue` / `SysLog.vue`：
- el-table 本身已有横向滚动能力（固定列宽 + fixed 列），核心问题是**外层 `.dp-page`/卡片是否约束宽度**。
- **统一方案**：确保表格所在容器 `max-width:100%; overflow-x:auto`（EP table 自带，但需保证父级不被内容撑宽）。在 `dp-page` 全局类（index.scss line ~92）已 `padding:16px`；mobile 下减小 padding 为 8px 释放横向空间，并确保 `.dp-page` 不产生横向溢出（`overflow-x:hidden` 在最外层 layout-main，使溢出限制在 el-table 自身滚动容器内）。
- **layout-main**（MainLayout scoped，line ~228）：`@include r.mobile { overflow-x: hidden; }` —— 防止任何子页面把整个内容区撑出横向滚动条（AC-6 关键）。
- toolbar / filter-bar / drawer-toolbar / pager（flex 行）：mobile 下 `flex-wrap: wrap; gap` 调小，避免按钮/筛选器一行挤爆。逐文件加 `@include r.mobile`。

### Step 3：弹窗响应式宽度（全局 CSS，零 !important）
- **EP 版本实测 2.14.2**（`package.json ^2.9.4` 实际解析为 2.14.2）。EP 把 `width="560px"` 渲染为 inline CSS 变量 `--el-dialog-width:560px`，真实宽度来自 `.el-dialog{width:var(--el-dialog-width,50%)}`（特异度 0,1,0）。因此 **`body .el-dialog`（特异度 0,1,1）在 `width` 属性上天然胜出，无需 `!important`**（Critic 已验证；纠正了"inline width 压不过"的误判）。
- **方案（采纳）**：在 index.scss 加一处全局规则——
  ```scss
  body .el-dialog {
    @include r.mobile { width: 92%; max-width: 92%; }
  }
  ```
  一处生效全部 7 个 dialog（ProxyGroups:407,582；Users:239,263；Rules:283,338,368），桌面不变、零 `!important`、保留逐 dialog 后续调优能力（不再有 `!important` 维护地雷）。
- **drawer 内 dialog 非嵌套**：ProxyGroups dialog(582) 与 drawer(459) 是 append-to-body 平级兄弟（Critic 已验证），全局规则不会产生"drawer 内 dialog 被误伤"问题。
- 内容 el-drawer（ProxyGroups `size="60%"` line 459/568、Rules `size="55%"` line 307）：mobile 下改为响应式 `:size="appStore.isMobile ? '90%' : '60%'"`（2 文件 3 处，结构尺寸走 JS）。

### Step 4：仪表盘图表自适应 + ECharts 容器级 resize（EChart.js 主线改造）
- 卡片栅格已用 `:xs`/`:lg`，验证 <768px 生效（AC-4），必要时微调 `:xs` 跨度。
- **【主线·必改】EChart.js 增加 ResizeObserver**（提升为主线，非可选增强）：
  - 根因（Architect/Critic 一致）：`EChart.js` 仅监听 `window.resize`（line 71）+ `onActivated`（line 77，仅 keep-alive 切换触发）。**抽屉开合、aside 显隐改变的是容器宽度而非视口**，两个现有触发源都不覆盖。ProxyGroups 两个 drawer 内有 **4 个 EChart（line 560/563/574/577）**，mobile 下 drawer 改 90% 时这些图表会停留在旧/零尺寸 → 直接威胁 AC-5。
  - 改法：setup 内对 `el.value` 挂 `ResizeObserver`，回调中 `hasSize() && chart.resize()`（复用现有 hasSize 守卫，debounce 可选）；`onBeforeUnmount` 内 `disconnect()`。ResizeObserver 是浏览器全局，无需 import（AutoImport 仅注入 vue/router/pinia）。
  - 一处改动同时覆盖：视口缩放、aside 显隐、抽屉开合/尺寸变化、断点切换——取代脆弱的 window.resize 依赖。
- Dashboard `grid:{left:50,right:50}`（tsOption）窄屏可能挤压，mobile 下可用更小 left/right（可选，按截图结果决定，纯样式）。

### Step 5：认证页（Login.vue + Setup.vue）—— 各自原值，禁止统一
- `Login.vue:95` `.auth-card { width: 380px }` → `width: min(380px, 92vw)`。
- `Setup.vue:102` `.auth-card { width: 400px }` → `width: min(400px, 92vw)`。**注意：Setup 是 400px 不是 380px**（Critic 已验证），必须各自用原值，否则 Setup 桌面端从 400→380 缩水 = 违反 AC-10/桌面零回归。
- `min()` 自适应居中，无需 media query（AC-9）。

### Step 5b：Settings.vue 表单 label 适配（共识新增，AC-13）
- `Settings.vue:159` `<el-form label-width="150px">` 在 390px 下仅剩 ~240px 给输入框，可能换行/裁切。
- 改法：mobile 下用 `:label-position="appStore.isMobile ? 'top' : 'right'"`（label 置顶，输入框占满宽度）或 mobile 缩小 label-width。其余两处 `label-width="110px"`（line 246/264）在 `:xs="24"` 单列下风险较低，一并按截图判断。

### Step 6：验证
- `cd web && pnpm build` 通过（AC-12）。
- 启动 dev / 用构建产物，DevTools 移动模拟（iPhone 390px）逐页截图：Dashboard / ProxyGroups / Rules / Users / SysLog（**含 audit tab**）/ Settings / Login / Setup（AC-11）。
- **drawer 内场景专项截图**（mobile 390px）：ProxyGroups 上游池 drawer（含 4 个 EChart 是否自适应=AC-5）、drawer 内编辑 dialog 宽度（AC-8）、drawer 内表格 fixed 操作列（AC-7）；Rules 规则 drawer。
- 桌面端（≥768px）逐页回归对比（AC-10），**重点确认 Setup 卡片仍 400px、Login 仍 380px**。

---

## Risks and Mitigations
| 风险 | 缓解 |
|------|------|
| scoped style 无法用全局 mixin | 新建 `responsive.scss` 用 `@use` 导入到各 scoped block（Vite+sass 支持） |
| 全局 `body .el-dialog` mobile 宽度 !important 影响特殊弹窗 | 仅在 mobile media 内生效，桌面不变；EP dialog 默认无冲突 |
| 按需注入 chunk 覆盖新样式 | 沿用 `body .xxx` 高特异度约定（既有成功经验，见 index.scss 注释） |
| 抽屉菜单与桌面菜单重复代码 | 抽取 `<el-menu>` 为单一片段/子组件复用（DRY） |
| ECharts 抽屉切换后不 resize | 优先验证；不足再加 ResizeObserver（可选增强，不阻塞主线） |
| keep-alive 页面在 mobile 切换时图表零尺寸 | EChart.js 已有 hasSize 守卫 + onActivated 重建，复用现有机制 |

## Verification Steps
1. `cd web && pnpm build` → 退出码 0。
2. DevTools 390px 逐页截图，对照 AC-1~AC-9 检查无溢出/错乱。
3. 桌面 ≥768px 逐页回归，对照 AC-10 确认无变化。
4. 真机/响应模式实测抽屉开关、表格横滚、弹窗宽度。

---

## ADR（架构决策记录）
- **Decision**：以 CSS `@media (max-width:767.98px)` 为主 + 最小 JS（app.js `isMobile` matchMedia 仅驱动抽屉开关与 header 文本精简）实现两档响应式；表格保留横向滚动；弹窗/抽屉宽度响应式；断点单一真相源放 `responsive.scss`。
- **Drivers**：工作量可控、桌面零回归、移动导航可用。
- **Alternatives considered**：(A) 纯 CSS——抽屉 visible 无法纯 CSS 管理；(B) JS 全量 viewport 驱动——过度工程、首屏闪烁、与 keep-alive/ECharts 交互复杂。
- **Why chosen**：CSS 覆盖绝大多数布局问题且桌面路径不执行；仅抽屉与条件渲染需要 isMobile 布尔，最小 JS 即可。
- **Consequences**：新增 1 个共享 scss + app.js 小幅扩展；各组件 scoped style 增 mobile 块；MainLayout 增抽屉。无后端/ API 改动。
- **Follow-ups**：若 ECharts 抽屉切换不贴合，追加 ResizeObserver；后续如需平板态再引入 sm 断点。

## Changelog（共识改进合并）
经 Architect（APPROVE_WITH_CHANGES）+ Critic（REVISE）评审，合并以下改进：
- **[Major·Setup 回归]** Step 5 拆分 Login(380px)/Setup(400px) 各用原值 + `min(原值,92vw)`，杜绝 Setup 400→380 桌面缩水（Critic 实测验证）。
- **[Major·AC-5]** Step 4 将 EChart.js ResizeObserver 从"可选增强"提升为**主线必改**；新增 ProxyGroups 两 drawer 内 4 个 EChart 的自适应验收（AC-5 扩写）。
- **[Major·dialog]** Step 3 弃用全局 `!important` 方案：实测 EP 2.14.2 用 `--el-dialog-width` CSS 变量，`body .el-dialog` 特异度天然胜出，**零 `!important`**；澄清 drawer 与 dialog 是 append-to-body 平级兄弟、无嵌套误伤（纠正 Architect 误判）。
- **[决策边界]** RALPLAN-DR 新增"结构走 JS isMobile / 纯样式走 CSS"分工规则，缓解 768px 双真相源张力（Principle 4）；aside 显隐、header 文本精简改用 `v-show` 而非 CSS display:none。
- **[菜单契约]** Step 1 明确：菜单用 inline `v-for` 各写一份（不抽子组件），消除 props/emit 契约歧义；确认双 el-menu 无 default-active 双激活（Critic 验证）。
- **[新增 AC-13]** Settings.vue `label-width:150px` mobile 改 `label-position="top"`（Step 5b）。
- **[冒烟]** Step 0 要求先在单组件验证 `@use '@/styles/responsive.scss'` 别名解析 + build 通过再铺开；备选 `additionalData` 自动注入。
- **[HMR]** Step 1 app.js matchMedia 监听幂等 + `import.meta.hot.dispose` 清理。
- **[验收完整性]** Step 6 补 SysLog audit tab、drawer 内 dialog/表格/图表专项截图。
- **[版本]** 记录实测 EP 版本 2.14.2（spec/计划原写 2.9.4，无行为差异，dialog 宽度行为按 2.14.2 验证）。
