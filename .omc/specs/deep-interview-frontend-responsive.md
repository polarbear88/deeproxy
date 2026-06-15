# Deep Interview Spec: 前端响应式优化（手机端可正常使用）

## Metadata
- Interview ID: frontend-responsive-2026-06-14
- Rounds: 4 (+ Round 0 拓扑)
- Final Ambiguity Score: 13.25%
- Type: brownfield
- Generated: 2026-06-14
- Threshold: 0.2
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.88 | 0.35 | 0.308 |
| Constraint Clarity | 0.88 | 0.25 | 0.220 |
| Success Criteria | 0.85 | 0.25 | 0.2125 |
| Context Clarity | 0.88 | 0.15 | 0.132 |
| **Total Clarity** | | | **0.8725** |
| **Ambiguity** | | | **0.1325** |

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| 应用外壳/导航 (MainLayout) | active | 固定侧边栏 → 移动端汉堡菜单 + 抽屉；顶栏挤压适配 | AC-1, AC-2, AC-3 |
| 仪表盘 (Dashboard) | active | 卡片栅格 + ECharts 图表小屏自适应 | AC-4, AC-5 |
| 表格管理页 (ProxyGroups/Rules/Users/SysLog) | active | 宽表格横向滚动不撑破页面；弹窗表单小屏适配 | AC-6, AC-7, AC-8 |
| 认证页 (Login/Setup) | active | 登录/初始化表单居中适配小屏 | AC-9 |

## Goal
将 deeproxy Web 控制台前端改造为响应式：以 **768px** 为手机/桌面分界（Element Plus `md` 断点），
在手机宽度（~390px）下消除样式错乱与横向溢出，使四类页面（外壳导航、仪表盘、表格管理页、认证页）
均可正常浏览与操作；**桌面端观感保持不变**。

## Constraints
- 断点：768px 为手机/桌面分界（沿用 Element Plus md=768px 栅格约定），不引入平板中间态（两档：手机/桌面）。
- 桌面端（≥768px）现有布局与观感**保持不变**，仅新增手机端（<768px）适配。
- 宽表格策略：**保留表格 + 横向滚动**（不改卡片式）；保证不撑破页面外框，操作列 `fixed="right"` 保持可见。
- 移动端导航：侧边栏默认隐藏，顶栏汉堡图标点击从左侧滑出抽屉，选中菜单后自动关闭。
- 技术栈：Vue3 + Element Plus 2.9 + SCSS；优先使用 Element Plus 自带响应式能力（el-col xs/sm/md/lg、el-drawer），避免重复造轮子。
- 全项目当前**无任何 @media 查询**，需新增；自定义样式遵循 `body .el-card` 式提高特异度的既有约定（见 index.scss 注释，避免被按需注入 chunk 覆盖）。
- 所有新增代码使用中文注释，说明"为什么"。

## Non-Goals
- 不做表格→卡片式重排（明确选择横向滚动）。
- 不做平板专属中间态布局（仅手机/桌面两档）。
- 不做底部 tab bar 导航。
- 不改动后端 / API。
- 不改动桌面端现有视觉。

## Acceptance Criteria
- [ ] AC-1：<768px 时 MainLayout 侧边栏默认隐藏，顶栏出现汉堡菜单图标。
- [ ] AC-2：点击汉堡图标从左侧滑出导航抽屉（el-drawer），选中任一菜单项后抽屉自动关闭并跳转。
- [ ] AC-3：<768px 顶栏（标题/主题/语言/管理员下拉）不溢出、不换行错乱；必要时精简次要文本（如隐藏 admin 用户名文字，仅留图标）。
- [ ] AC-4：<768px 仪表盘指标卡片单列或双列堆叠，不溢出。
- [ ] AC-5：<768px ECharts 图表（时序/饼图/Top域名）随容器自适应宽度，不超出卡片、不重叠。
- [ ] AC-6：<768px 表格管理页（ProxyGroups/Rules/Users/SysLog）表格区域横向可滚动，**页面整体不出现横向滚动撑破**（溢出限制在表格容器内）。
- [ ] AC-7：<768px 表格操作列 `fixed="right"` 仍可见可点。
- [ ] AC-8：<768px 弹窗（el-dialog 当前固定 560px）改为按视口宽度自适应（如 width 用百分比/响应式），不超出屏幕、表单 label 不错位。
- [ ] AC-9：<768px 认证页（Login/Setup）表单卡片居中、宽度自适应、不溢出。
- [ ] AC-10：桌面端（≥768px）所有页面观感与改造前一致（回归验证）。
- [ ] AC-11：在 Chrome DevTools 移动模拟（iPhone ~390px）下逐页截图，无错位/溢出。
- [ ] AC-12：`pnpm build` 通过。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| "手机版正常"含义模糊 | 是"能用不溢出"还是"原生级体验"？ | 取较高档：移动端体验优化（抽屉导航、卡片堆叠、图表自适应） |
| 宽表格需重排为卡片 | 成本最大的分叉 | 否决——保留表格+横向滚动，成本可控 |
| 侧边栏在手机上如何处理 | 收起/抽屉/tab bar | 汉堡菜单 + el-drawer 抽屉 |
| 桌面端是否必须一成不变 | Contrarian 质询 | 桌面端保持不变，仅新增 <768px 适配 |
| 需要平板中间态吗 | 是否三档 | 否——仅手机/桌面两档，768px 分界 |
| 如何验证 | 主观 vs 客观 | DevTools 移动模拟逐页截图 |

## Technical Context
- 栈：Vue3 + Element Plus 2.9.4 + ECharts 5.6 + vue-i18n + Pinia + Vite 6 + SCSS。
- 现状关键事实：
  - 全项目 0 个 @media 查询。
  - `MainLayout.vue`：el-aside 固定宽度（220/64px），无移动端处理；折叠由 appStore.sidebarCollapsed 控制（适合复用为抽屉触发）。
  - `Dashboard.vue` / `Settings.vue`：已用 el-col xs/sm/md/lg 断点（部分就绪）。
  - 表格视图 `ProxyGroups.vue`(722行)/`Rules.vue`/`Users.vue`/`SysLog.vue`：多固定列宽，未做响应式；弹窗 `el-dialog width="560px"` 固定。
  - `styles/index.scss`(127行)：全局变量与卡片覆盖；既有特异度约定（`body .el-card`）需沿用。
- 实现要点（建议）：
  - 新增全局/布局级 @media (max-width: 768px) 或用 JS 监听视口宽度切换 sidebarCollapsed→drawer 模式。
  - el-dialog width 改为响应式（如 computed：移动端 `90%`，桌面 `560px`）。
  - 表格外层加 `overflow-x:auto` 容器约束，确保溢出不撑破 dp-page。
  - ECharts 容器宽度自适应（EChart.js 通常已 resize，需确认在抽屉/断点切换后 resize 生效）。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| 视口断点 (Breakpoint) | core | 768px 手机/桌面分界 | 决定所有组件布局模式 |
| 应用外壳 (MainLayout) | core | sidebar, header, drawer | 包含所有页面；手机端切抽屉 |
| 导航抽屉 (NavDrawer) | core | visible, menuItems | 手机端替代固定侧边栏 |
| 表格容器 (TableWrapper) | supporting | overflow-x | 约束宽表格溢出 |
| 弹窗 (Dialog) | supporting | width(响应式) | 表格页表单 |
| 图表 (EChart) | supporting | width(自适应) | 仪表盘 |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 0 (拓扑) | 4 | 4 | - | - | N/A |
| 2 | 5 | 1 | 0 | 4 | 80% |
| 4 | 6 | 2 | 0 | 4 | 67% (新增断点/抽屉细化) |

## Interview Transcript
<details>
<summary>Full Q&A (Round 0 + 4 rounds)</summary>

### Round 0 — 拓扑确认
**Q:** 4 个顶层组件（外壳/仪表盘/表格页/认证页）对吗？
**A:** 4 个都要，确认。

### Round 1 — 成功标准
**Q:** "手机版正常"验收时怎样算达标？
**A:** 移动端体验优化（取较高档）。
**Ambiguity:** 40%

### Round 2 — 宽表格策略（约束）
**Q:** 手机上列多的表格怎么处理？
**A:** 保留表格 + 横向滑动。
**Ambiguity:** 28%

### Round 3 — 移动端导航（约束）
**Q:** 手机上左侧菜单怎么处理？
**A:** 汉堡菜单 + 抽屉。
**Ambiguity:** 20%

### Round 4 — 断点与验证 (Contrarian)
**Q:** 桌面端必须一成不变吗？怎么验证手机正常？
**A:** 768px 分界 + 桌面不变 + DevTools 验证。
**Ambiguity:** 13.25%

</details>
