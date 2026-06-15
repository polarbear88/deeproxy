# deeproxy 四问题修复实现计划（RALPLAN-DR · consensus）

> Status: **pending approval**
> 权威需求：`.omc/specs/deep-interview-four-issues-stability-and-bundle.md`
> 所有 file:line 锚点经代码实测核对。

---

## 1. Requirements Summary

四个相互独立的工作项：

| # | 工作项 | 层 | 性质 | 核心动作 |
|---|--------|----|----|---------|
| #1 | 连接驻留 ~20min | 后端 Go | 代码改动（最棘手） | listener + 两个 dialer socket 加 `TCP_USER_TIMEOUT≈90s`，build-tag 分平台优雅降级 |
| #2 | fake-ip(198.18.x.x) 显示 | — | **无代码改动** | 仅文档说明：客户端 fake-ip DNS 占位 IP，嗅探失败时如实显示，非 bug |
| #3 | 系统日志流式卡死 | 前端 Vue | 代码改动 | SSE rAF 批量合批 push + 流式期间默认暂停贴底 + 「跳到最新」按钮 |
| #4 | 前端按需打包瘦身 | 前端 Vue | 代码改动 | main.js 去全量 ElementPlus/CSS/图标循环；EChart.js 改 echarts/core 按需；可选 vendor 拆分 |

通用约束：直接提交 main（禁建分支）；全中文注释解释「为什么」；能用成熟库不造轮子 / DRY；后端验 `go build ./...` + connreg 测试；前端验 `pnpm build`（cwd=web，产物到 ../api/dist）。

### 改变计划的关键代码事实（planner 实测，已二次核实）
- **R1（高危·静默）**：`ElMessage`/`ElMessageBox` 在 8+ 文件以 `import { ElMessage } from 'element-plus'` 显式引入，**不经 resolver**，其 CSS 今天仅靠被删的全量 `index.css` 提供 → 删后弹窗样式裸奔，而 `pnpm build` 仍通过。
- **R2（高危·静默）**：**18 处 `:icon="'Name'"` 字符串属性图标**（SysLog/ProxyGroups/Users/GenerateProxy/Rules/Settings），依赖被删的全量图标注册循环；resolver 只自动导入标签形式 `<el-icon><X/></el-icon>` → 删循环后图标失效，构建仍通过。
- **R3**：`DynamicScroller`(v3.0.4) 不 emit `scroll`、不暴露 `getScroll()`；到底检测须直接读 `scroller.value.$el` 的 `scrollTop/clientHeight/scrollHeight`。
- **R4**：图表 option 中无 `dataZoom`、无 `title:` → echarts `use()` 排除 DataZoomComponent 与 TitleComponent。
- **R5**：`ElNotification` 在 app 代码中零使用 → 从 #4 范围剔除。
- **R6**：`golang.org/x/sys` 已在 go.mod（indirect），使用后转 direct，无需新增依赖；无 vendor 目录。

---

## 2. RALPLAN-DR Summary

### Principles
1. **不碰热路径**：`server/relay.go`（io.Copy 双向中继）绝对零改动，修复后 `git diff --stat server/relay.go` 必须为空。
2. **跨平台单二进制不可破**：平台专属 socket 选项必须 build-tag 分文件优雅降级，三平台 `go build` 必过。
3. **复用既有单一事实源 / DRY**：后端复用 `dialer.KeepAliveConfig` 模式；前端复用既有 vite resolver、`common.*` i18n、既有 SSE 清理钩子。
4. **构建通过 ≠ 正确**：#4 的两个静默回归（CSS / 字符串图标）必须用运行时逐页人工 QA 兜住。
5. **小步可独立验证 + 分步回归**：#4 拆 4a→回归→4b→回归→4c，限制 CSS 注入顺序回归面。

### Decision Drivers
1. 跨平台编译硬约束 — `TCP_USER_TIMEOUT` 仅 Linux/部分平台 → #1 必须 build-tag 分文件。
2. 静默回归风险 — #4 删全量 import 后 CSS 与字符串图标失效不被 build 捕获 → 必须先调研 + 逐页人工回归。
3. 性能正确性边界 — #3 卡死源于「每条同步 O(N) + 每条强制布局读」→ 批量合批必须「单次 push + 单次 splice + 单次滚动」。

### Decision Point A — #1 TCP_USER_TIMEOUT 实现方式
- **A1（选）build-tag 分文件**（`tcpopt_linux.go` 实装 / `tcpopt_other.go` 空实现）：编译期裁剪、无运行时开销、单二进制干净、空实现优雅降级到既有 keepalive 75s。Cons：新增两文件 + 统一签名。
- **A2 runtime 探测**：**伪选项**。`unix.TCP_USER_TIMEOUT` 在 darwin/windows 的 x/sys 包不存在该常量，单文件引用即编译失败，runtime 判断救不了编译期符号缺失。Invalidation。
- **A3 仅文档/调内核 tcp_retries2**：违背 AC「≤90s」（依赖部署环境内核参数，工具不可控）。Invalidation。

### Decision Point B — #3 批处理实现方式
- **B1（选）原生 rAF 合批**（无新依赖）：零依赖、帧级延迟最低、卸载 `cancelAnimationFrame` 清理简单。Cons：自写约 30 行缓冲队列。
- **B2 定时窗口 100-200ms**：固定延迟即便低速流也延迟，窗口与渲染帧不对齐。
- **B3 引节流库（lodash/vueuse）**：为单点引重依赖，与 #4 瘦身目标冲突，收益（省~5 行）远小于成本。Invalidation。

---

## 3. Implementation Steps（带 file:line 锚点）

> 并行性：**#1（后端）与 #3、#4（前端）三者完全独立，可并行**。#4 内部 4a→4b→4c 顺序（CSS 注入顺序回归需逐步收敛）。#3 与 #4 文件不重叠，可并行。

### 工作项 #1：连接驻留修复（后端）— 可与 #3/#4 并行

**Step 1.1 — 跨平台 socket 选项辅助（build-tag 分文件）**
- 新建 `dialer/tcpopt_linux.go`（`//go:build linux`）：导出与 `net.Dialer.Control`/`net.ListenConfig.Control` 兼容的函数 `ControlTCPUserTimeout(network, address string, c syscall.RawConn) error`，内部 `c.Control(func(fd uintptr){ unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, tcpUserTimeoutMs) })`。import `golang.org/x/sys/unix`（go.mod 已 indirect 持有，用后转 direct，无需新增依赖，R6）。
- 新建 `dialer/tcpopt_other.go`（`//go:build !linux`）：同签名空实现 `return nil`（仅 `import "syscall"` 命名参数类型；`syscall.RawConn` 是各 GOOS stdlib 类型，可移植），macOS/Windows 优雅降级到既有 keepalive 75s。
- 架构校验（Architect 确认）：`unix.TCP_USER_TIMEOUT` 仅定义于 x/sys 的 `zerrors_linux.go`，darwin/windows 无此符号 → 单文件 runtime 判断无法编译（坐实 A2 为伪选项，build-tag 必需）。`Control` 回调作用于真实 TCP socket，三接入点（listener / DialDirect / DialUpstream.fwd）均命中各自的真实 TCP（fwd 命中「本机→上游」那一跳，不触及隧道内 SOCKS 载荷流——正是预期范围）。
- `dialer/dialer.go`（紧邻 `KeepAliveConfig` var，dialer.go:37-42 区域）新增常量 `tcpUserTimeoutMs = 90000`，中文注释说明：与 AC「死连接 ≤90s」对齐；语义不同于 keepalive（USER_TIMEOUT 对有未确认在途数据的活跃连接也生效，覆盖 keepalive 在发送缓冲有未确认数据时被抑制的场景）。
- **新增：四层超时真值表写入 `dialer.go` 注释**（M3，不只 commit message）：
  ```
  握手 10s（lifecycle.handshakeTimeout，仅握手期读）
    → keepalive 75s（KeepAliveConfig，连接【空闲且无未确认数据】时探测死连接）
    → USER_TIMEOUT 90s（本步，连接【有未确认在途数据】时上限化其存活，覆盖 keepalive 被抑制的窗口）
    → idleConn 300s（idleconn.go，双向【无任何数据移动】超时）
  ```
- **新增：注释一句「为何 idleConn 不能覆盖此场景」**（M3/Architect#4）：idleConn 的 `touch()`（idleconn.go:34-35）在每次成功 Read/Write 时滑动 300s 读截止——它按「数据移动」keyed，不按「ACK 存活」keyed；下载中（服务端→客户端数据仍在发）会不断重置该截止，故无法检出「客户端已死但服务端仍在向其发数据、数据卡在未确认」的场景。
- 验证：`GOOS=linux/darwin/windows go build ./...` 三平台过。

**Step 1.2 — 监听器接入（客户端方向）**
- `server/lifecycle.go:96-99` 现有 `lc := net.ListenConfig{KeepAlive:-1, KeepAliveConfig: dialer.KeepAliveConfig}`：补 `Control: dialer.ControlTCPUserTimeout`。
- 注释更新：覆盖「服务端正向客户端发数据、发送缓冲有未确认数据致 keepalive 被抑制」的死连接场景；同步修订 dialer.go:31-33 「75s 窗口」措辞 → 补「USER_TIMEOUT 90s 与 keepalive 75s 并存：空闲死连接 keepalive ~75s 检出，活跃在途数据死连接 USER_TIMEOUT ~90s 兜底」。
- 验证：`go build ./...`。

**Step 1.3 — 两个 dialer 接入（出站方向）**
- `dialer/dialer.go:49` `DialDirect` 的 `d := &net.Dialer{...}`：补 `Control: ControlTCPUserTimeout`。
- `dialer/dialer.go:72` `DialUpstream` 的内层 `fwd := &net.Dialer{...}`（本机→上游 SOCKS5 这一跳 TCP）：补 `Control: ControlTCPUserTimeout`。
- **M3 决议（误杀风险的处置，已拍板，不再留作开放问题）**：采用 Architect 推荐的**全量接入 + 注释记录权衡**，而非去范围。三接入点（listener / DialDirect / DialUpstream.fwd）全部接入 USER_TIMEOUT 90s。理由：(a) 90s 内零 ACK 进展对健康连接（含移动端切换/拥塞）在实践中罕见，绝大多数停滞秒级恢复；(b) forward 隧道里的长轮询/WebSocket 只要底层仍有 ACK 心跳（TCP 层）就不会触发，仅在「整条 TCP 彻底 ACK 停滞 90s」时才重置——此时连接事实上已不可用；(c) 90s 已是 AC 上限，不再额外加配置项（避免 net-new config + admin UI 面，config 已迁后台）。**在 dialer.go 注释显式记录：这是「死连接 ≤90s 清理」与「极端弱网活跃连接可能被误断」之间的已接受权衡**；若未来出现误断投诉，再考虑做成 `dead_conn_timeout_sec` 可配置（列入 Follow-ups，非本次范围）。
- 注释标注三接入点（listener / DialDirect / DialUpstream.fwd）共用同一辅助函数（DRY）。
- 验证：`go build ./...`；`git diff --stat server/relay.go` **为空**。

**Step 1.4 — 边界确认（不改代码）**
- 核对 USER_TIMEOUT 不干扰既有握手读超时（lifecycle.go handshakeTimeout/deadlineListener）与中继空闲超时（dialer/idleconn.go）——三者作用层不同。commit message 点明。

### 工作项 #2：fake-ip 显示（无代码改动）

**Step 2.1 — 仅文档**
- 交付说明标注「#2 = 无代码改动」。
- `CLAUDE.md`（第七节约束与边界 或新增 FAQ）或 README 追加原理：198.18.0.0/15 是客户端本地 fake-ip DNS 占位地址段；目标为该段 IP 且首包嗅探（TLS SNI / HTTP Host）失败/超时/非 TLS-HTTP 时如实显示，属预期；推荐客户端用远程 DNS（socks5h）发域名。`CLAUDE.md` 属可直写范围。

### 工作项 #3：系统日志流式卡死（前端）— 可与 #1/#4 并行

文件：`web/src/views/syslog/SysLog.vue`

**Step 3.1 — rAF 合批（B1）替换逐条 push**
- 模块级新增 `let logBuffer = []`、`let rafHandle = null`（脚本顶层，~SysLog.vue:26）。
- 改 `appendLog`（L53-59）：级别过滤后 push 进 `logBuffer`；**buffer 硬上限（M1，独立于 flush）**：`if (logBuffer.length > MAX_RENDER) logBuffer.splice(0, logBuffer.length - MAX_RENDER)`——这一步必须在 appendLog 内做，不能只放在 flushBuffer 里，否则后台标签页 rAF 暂停时 flush 不跑、buffer 无界增长；`rafHandle` 为空则 `rafHandle = requestAnimationFrame(flushBuffer)`。
- 新增 `flushBuffer()`：一次性 `logs.value.push(...logBuffer.map(withId))`；`logBuffer=[]`；单次 splice 截断（MAX_RENDER=L36）；`rafHandle=null`；末尾按新滚动策略条件滚动。注释解释「为什么合批」。
- 验证：`pnpm build`；高速流不卡死、可滚动/筛选。

**Step 3.1b — 后台标签页处理（M1，Architect/Critic blocking）**
- 背景：SysLog.vue:185-188 处于 `<keep-alive :max="6">`，浏览器**标签页隐藏**（`document.hidden`）与 Vue `onDeactivated` 是**两个轴**——标签页后台时 rAF 被浏览器暂停/降频但 Vue 组件仍 active、SSE 仍在 `appendLog`。Step 3.1 的 buffer 硬上限已防无界增长；此步补 visibilitychange 兜底。
- 新增 `document.addEventListener('visibilitychange', onVisibilityChange)`（onMounted/onActivated 绑定，onBeforeUnmount/onDeactivated 解绑）：`document.hidden` 转可见时若 `logBuffer` 非空则立即 `flushBuffer()`（排空积压）。
- 验证 AC：「后台标签页（document.hidden）期间高速流，`logBuffer.length ≤ MAX_RENDER` 恒成立；切回前台一次性排空」。

**Step 3.2 — 流式默认暂停贴底 + 跳到最新（含 R3 到底检测）**
- 到底检测 `isAtBottom()`：直接读 `scroller.value.$el`（`.vue-recycle-scroller`）的 `scrollTop + clientHeight >= scrollHeight - 40`；原生 `$el.addEventListener('scroll', ...)`（onMounted/onActivated 绑定，onBeforeUnmount/onDeactivated 解绑）。**不用** `@scroll`/`getScroll()`（R3）。
- 新增 `userAtBottom=ref(true)`，滚动事件更新。`flushBuffer` 末尾：仅 `userAtBottom.value` 时 `scrollToBottom()`，否则 `hasNew=ref(true)` 显示浮层。
- 模板 `DynamicScroller`（L229-257）旁加 `v-show="hasNew && !userAtBottom"` 「跳到最新」按钮 → 点击 `scrollToBottom()` + `hasNew=false`。
- **单一事实源裁定**（消除双控件冲突）：保留 `autoScroll`（L23/219）为总开关；「流式默认暂停」仅在 `autoScroll=true` 且用户不在底部时生效（用户主动上滑看历史时不打断）。`scrollToBottom`（L61-68）守卫保留并叠加 `userAtBottom` 判断。注释写交互真值表。
- 初始快照 `loadSnapshot`（L42-51）一次性贴底不受暂停影响。
- 验证：上滑看历史新日志不打断、出现「跳到最新」；点击回底并恢复跟随。

**Step 3.3 — i18n 双语 key（parity gate）**
- 「跳到最新」加入 `web/src/locales/zh.js` 与 `en.js`，优先 `common.*`（如 `common.jumpToLatest`）。zh/en key 集合必须完全一致（G-2）。
- SysLog 模板内其它硬编码中文 i18n 化 = **可选**，不强求（避免范围蔓延）；若做则 zh/en 同步。
- 验证：zh/en key diff 为空；`pnpm build`。

**Step 3.4 — 缓冲/rAF/滚动监听清理（防泄漏 + 状态重置，M2）**
- **teardown 清理**：`onDeactivated`（L188）与 `onBeforeUnmount`（L197）的 `closeStream` 之外，同时 `cancelAnimationFrame(rafHandle); rafHandle=null`、`logBuffer=[]`、解绑 `$el` scroll 监听 + `visibilitychange` 监听。
- **M2 状态重置（Architect/Critic blocking）**：`clearScreen`（L107）与 `watch(level)`（L102）回调、以及 `loadSnapshot`（L42-51）重置 `logs.value` **之前**，必须 `cancelAnimationFrame(rafHandle); rafHandle=null; logBuffer=[]`。否则挂起的 rAF + 非空 buffer 会在清屏/切级别后 flush，把旧级别/已清屏条目灌进新列表。抽取一个 `resetBuffer()` 辅助（DRY），三处复用。
- 验证：切走/切回无残留 rAF、无第二个 EventSource；**清屏/切级别后挂起 rAF 不会把旧条目 flush 进新列表**（M2 AC）。

### 工作项 #4：前端按需打包瘦身（前端）— 可与 #1/#3 并行；内部 4a→4b→4c 顺序

**Step 4.1（4a）— main.js 去全量（先解 R1/R2 静默回归）**
文件：`web/src/main.js`（L4-6, 22, 24-27）。顺序：
1. **先迁移 18 处字符串图标（R2，前置阻塞）**：`:icon="'Name'"`（SysLog.vue:220/222/294/295、ProxyGroups.vue:429/516、Users.vue:218、GenerateProxy.vue:240/244/255/261、Rules.vue:250/251/314/316/318、Settings.vue:271/273）→ 标签形式 `<el-icon><Name/></el-icon>`，由 resolver 自动按需。**必须在删图标循环前完成并验证。**
2. **保住 ElMessage/ElMessageBox CSS（R1）**：推荐 main.js 显式补 `import 'element-plus/theme-chalk/el-message.css'`、`el-message-box.css`、`el-overlay.css`（默认推荐，改动面小不动 8 个调用点）；备选：移除 8+ 文件显式 import 改由 AutoImport 注入。
3. 删 `main.js:4` `import ElementPlus`、`:5` 全量 CSS、`:22` `app.use(ElementPlus)`、`:6` 图标全量 import、`:24-27` 注册循环。
4. 保留 `v-loading`（6 处，resolver 已映射 + 带 CSS sideEffect，无需 `app.use(ElementPlus)`）；`ElNotification` 零使用不注册（R5）。
5. **M4 承重依赖保护（Architect/Critic blocking）**：`web/src/styles/index.scss:6` 已 `@use 'element-plus/theme-chalk/dark/css-vars.css'`——这是删除全量 `index.css`（main.js:5）后，EP **暗色** CSS 变量（`html.dark{--el-*}`）存活的**唯一来源**（已验证全量 index.css 含 0 条 html.dark 规则；base/light 根变量由每组件 base.css 链自动带入、不受影响）。**计划禁止改动 index.scss:6，并在该行旁加注释标注其承重性**，防止未来「顺手清理」连带删除致暗色静默失效。
6. **m1 CSS 漂移护栏（minor，建议）**：手维护的 message/message-box/overlay CSS import 清单会随新增第 9 个命令式 `El*` 服务而静默漂移。最低要求：在 Risks 记录该漂移风险；建议加一个 grep 守卫（CI 或本地脚本：grep `from 'element-plus'` 的命名 `El*` 服务 import，断言有对应 CSS import）或列入「长期迁移到 AutoImport 注入」Follow-up。
- 验证：`pnpm build` 过；**运行时** QA：触发 ElMessage + ElMessageBox.confirm，亮/暗主题样式正常；18 个迁移图标显示正常；暗色逐页回归覆盖 index.scss:6 暗色变量项。

**Step 4.2（4b）— EChart.js 改 echarts/core 按需**
文件：`web/src/components/EChart.js:3`
- `import * as echarts from 'echarts'` → `import * as echarts from 'echarts/core'` + 显式 `echarts.use([...])`。
- use 列表（实测 R4）：`LineChart, PieChart, BarChart`；`TooltipComponent, LegendComponent, GridComponent`；`CanvasRenderer`。**排除** DataZoomComponent（无 dataZoom）、TitleComponent（无 title:）。`registerTheme`（L12）保留。
- 注释列出实测依据（Dashboard.vue + ProxyGroups.vue 的 option）。
- 验证：`pnpm build`；运行时仪表盘（line/pie/bar）、代理组详情图渲染/暗色/resize 正常。

**Step 4.3（4c · 可选）— vite manualChunks vendor 拆分**
文件：`web/vite.config.js:32-39`（build 块）
- `build.rollupOptions.output.manualChunks` 拆 `vue 系（vue/vue-router/pinia）` 与 `echarts`。
- 顺序约束：在 4a/4b 回归通过**之后**单独做并再次回归（manualChunks 改 chunk 加载顺序 → 再次影响 CSS 注入顺序）。风险大可不做（optional）。
- 验证：`pnpm build`；逐页样式回归。

---

## 4. Acceptance Criteria

**#1 连接驻留**
- [ ] Linux：客户端「下载中途断电/掉网」（服务端持续发数据、发送缓冲有未确认数据）场景，连接 **≤90s** 内清理（goroutine + 上游连接 + 注册表条目回收）。
- [ ] macOS/Windows：编译运行不报错（build-tag 空实现），死客户端经既有 keepalive **≤~80s** 清理（降级仍不无限驻留）。
- [ ] 三平台 `GOOS=linux/darwin/windows go build ./...` 全过，单一静态二进制。
- [ ] `git diff --stat server/relay.go` **为空**（机械校验）。
- [ ] 正常活跃连接（含上游需认证 forward）不被误断。

**#2 fake-ip**
- [ ] 无代码改动；CLAUDE.md/README 记录 198.18.x.x 原理；`git diff` 不含任何 .go/.vue/.js 代码改动（仅文档）。

**#3 系统日志**
- [ ] 高速日志流（**可测目标：持续 ~200 条/秒突发 ≥10s**）期间不卡死：主线程无 >50ms 长任务连续堆积，可滚动/切级别/切 tab。
- [ ] 上滑看历史时新日志不打断；出现「跳到最新」按钮，点击回底并恢复跟随。
- [ ] 初始快照打开仍落到最新一条。
- [ ] 「跳到最新」i18n zh/en key 齐全（parity diff 为空）。
- [ ] 切走/切回无 rAF 残留、无 EventSource 泄漏。
- [ ] **（M1）后台标签页（document.hidden）期间高速流，`logBuffer.length ≤ MAX_RENDER` 恒成立；切回前台一次性排空。**
- [ ] **（M2）清屏 / 切级别后，挂起的 rAF 不会把旧级别/已清屏条目 flush 进新列表。**

**#4 打包瘦身**
- [ ] `pnpm build` 通过，产物到 ../api/dist。
- [ ] 主 chunk 体积明显下降（记录 before/after 字节数）；**（m2 数值底线）index 主 chunk 与 dashboard chunk 合计减少 ≥ 40%，否则视为按需未生效需排查**（基线参考：index.js 1.3MB + dashboard.js 1.0MB + index.css 354KB）。
- [ ] 运行时：ElMessage toast 与 ElMessageBox confirm 亮/暗主题样式正常（R1）。
- [ ] 运行时：18 个原字符串图标按钮迁移后正常显示（R2）。
- [ ] 运行时逐页回归：仪表盘/代理组/规则/系统日志/生成代理/用户/连接审计/设置/登录页样式无破（含 `body .el-card` 特异度覆盖）。
- [ ] **（M4）验证 index.scss 保留 `@use 'element-plus/theme-chalk/dark/css-vars.css'`；暗色逐页回归覆盖此项。**
- [ ] EChart 各图（line/pie/bar）渲染、暗色、resize 正常。

---

## 5. Risks & Mitigations

| 风险 | 影响 | 缓解 |
|------|------|------|
| #1 跨平台编译：`unix.TCP_USER_TIMEOUT` 仅 Linux | macOS/Windows 编译失败 | build-tag 分文件，签名一致；三平台 `go build` 全跑；空实现降级 |
| #1 误断弱网客户端：USER_TIMEOUT 对活跃在途数据也生效 | 高延迟/隧道切换大下载 90s 内 ACK 停滞→误杀 | 90s 远大于常规 RTT 抖动；**开放问题：是否可配置** `dead_conn_timeout_sec`；注释记录权衡 |
| #3 批处理显示延迟 | rAF 帧级延迟；单帧积累上百条 | 帧级（~16ms）几不可感；单次 push+splice+滚动吸收 |
| #4 CSS 注入顺序回归（最棘手前端风险） | 按需后 EP CSS 惰性注入晚于 index.scss → 特异度覆盖失效 | 分步 4a→回归→4b→回归→4c；逐页人工回归；失效则提特异度（`body .el-X` 先例）或显式控制 import 顺序 |
| #4 R1 静默 CSS 回归 | ElMessage/Box 弹窗裸奔，build 仍过 | 显式补 message/message-box/overlay CSS；加运行时 QA AC |
| #4 R2 静默图标回归 | 18 个字符串图标失效，build 仍过 | 删循环前先迁移全部 18 处为标签形式并验证 |
| #4 R3 到底检测 API 不存在 | DynamicScroller 无 scroll 事件/getScroll | 直接读 `$el` scrollTop/clientHeight/scrollHeight + 原生 scroll 监听 |

---

## 6. Verification Steps

**后端（#1）**
1. `cd /Users/polarbear/code/deeproxy && go build ./...`
2. `GOOS=linux go build ./... && GOOS=darwin go build ./... && GOOS=windows go build ./...`
3. `git diff --stat server/relay.go` → **必须空**（硬门禁）。
4. `go test ./server/...` + `go test ./dialer/...`（含 connreg 相关测试）。
5. （Linux 可选）端到端：建连下载→断网→计时 ≤90s 注册表条目消失。

**前端（#3 / #4）**
1. `cd /Users/polarbear/code/deeproxy/web && pnpm build`。
2. i18n parity（#3）：zh.js / en.js key 集合 diff 为空。
3. `git diff --stat server/relay.go` 复核仍空。
4. 逐页样式回归（亮/暗双主题）：仪表盘 / 代理组 / 规则 / 系统日志 / 生成代理 / 用户 / 连接审计 / 设置 / 登录初始化 / 全局触发 ElMessage + ElMessageBox.confirm。
5. 记录 build 产物 before/after 体积。

---

## Open Questions（共识后已全部裁定）

- [x] **#4 R1**：ElMessage/ElMessageBox CSS 保留策略 → **裁定：显式补 theme-chalk 三个 css**（message/message-box/overlay，改动面小、不动 8 个调用点）；+ 记录漂移风险（m1）并列 AutoImport 迁移为 Follow-up。
- [x] **#1**：90s TCP_USER_TIMEOUT 是否可配置 → **裁定：本次硬编码 90s 全量接入 + 注释记录权衡**（M3 决议见 Step 1.3）；可配置化列入 Follow-ups。
- [x] **#3**：高速无卡死可测目标 → **裁定：持续 ~200 条/秒突发 ≥10s，无 >50ms 长任务连续堆积**（写入 AC）。
- [x] **#3**：「流式默认暂停」与 autoScroll 交互 → **裁定：autoScroll 为总开关；流式默认暂停仅在 autoScroll=true 且用户不在底部时生效**（Step 3.2 真值表）。
- [x] **#4**：SysLog 模板硬编码中文是否 i18n 化 → **裁定：本次不做**（避免范围蔓延）。

---

## ADR（共识达成）

- **Decision**：四问题分四独立工作项并行修复。#1 在 listener + DialDirect + DialUpstream.fwd 三处 socket 经 build-tag 分文件注入 `TCP_USER_TIMEOUT=90s`（relay.go 零改动）；#2 仅文档；#3 SysLog.vue 原生 rAF 合批 + buffer 硬上限 + visibilitychange + 流式暂停贴底 + 状态重置；#4 main.js/EChart.js 去全量改按需（先迁 18 图标 + 保 ElMessage CSS + 守护 index.scss:6 暗色依赖）+ 逐页回归。
- **Drivers**：跨平台编译硬约束；静默回归风险（build 通过≠正确）；性能正确性边界（每条同步 O(N)）。
- **Alternatives considered**：#1 A2 runtime 探测（编译期符号缺失，伪选项）/ A3 纯文档调内核（违背 AC）；#3 B2 定时窗口（固定延迟）/ B3 引节流库（与瘦身冲突）；#1 去范围仅 listener+DialDirect（评估后选全量+注释权衡）。
- **Why chosen**：build-tag 是唯一能编译期裁剪 + 单二进制干净 + 优雅降级的方案；原生 rAF 帧级延迟最低且零依赖；#4 分步回归在瘦身与 CSS 注入顺序回归面之间取平衡。
- **Consequences**：(+) 死连接 ≤90s 清理、日志高速流不卡、bundle 减 ≥40%；(−) 极端弱网活跃连接可能被 90s 误断（已接受权衡，注释记录）；(−) #4 手维护 CSS 清单有漂移风险（m1 护栏缓解）。
- **Follow-ups**：`dead_conn_timeout_sec` 可配置化；命令式 EP 服务 CSS 的 AutoImport 迁移 / grep 护栏；SysLog 模板硬编码中文 i18n 化。

## Changelog（本轮共识应用的改进）

- M1：#3 appendLog 加 buffer 硬上限 + Step 3.1b visibilitychange（后台标签页 rAF 暂停时防 buffer 无界增长）。
- M2：#3 Step 3.4 clearScreen/level-watch/loadSnapshot 重置前 resetBuffer（防挂起 rAF flush 旧条目）。
- M3：#1 Step 1.3 拍板全量接入 + 权衡注释；Step 1.1 加四层超时真值表 + 「为何 idleConn 不能覆盖」说明，写入 dialer.go 注释。
- M4：#4 Step 4.1 标注 index.scss:6 为暗色变量唯一来源、禁删 + AC。
- m1：#4 记录 CSS import 清单漂移风险 + grep 护栏/AutoImport 迁移 Follow-up。
- m2：#4 bundle 减少加 ≥40% 数值底线；#3 加 ~200 条/秒可测吞吐目标。
- Architect 技术校验并入 Step 1.1（Control 命中真实 socket、x/sys 仅 Linux 坐实 build-tag、syscall.RawConn 可移植）。
