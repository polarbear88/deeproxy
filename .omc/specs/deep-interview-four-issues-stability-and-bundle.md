# Deep Interview Spec: deeproxy 四问题修复（稳定性 + 前端瘦身）

## Metadata
- Interview ID: di-deeproxy-4issues
- Rounds: 5
- Final Ambiguity Score: ~7%
- Type: brownfield
- Generated: 2026-06-15
- Threshold: 0.2 (20%)
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown（按组件汇总，整体取活跃组件最弱覆盖）

| Component | Goal | Constraints | Criteria | Context | Ambiguity |
|-----------|------|-------------|----------|---------|-----------|
| #1 连接驻留 | 0.95 | 0.90 | 0.95 | 0.90 | ~7% |
| #2 fake-ip 显示 | 1.00 | 1.00 | 1.00 | 1.00 | ~2% |
| #3 日志卡死 | 0.95 | 0.95 | 0.90 | 0.90 | ~7% |
| #4 前端瘦身 | 0.90 | 0.90 | 0.90 | 0.90 | ~10% |
| **整体** | | | | | **~7%** |

## Topology

| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| #1 连接驻留清理 | active | 客户端失联后实时连接残留 ~20 分钟才关闭 | 后端 socket 层修复（lifecycle/dialer），不碰 relay 热路径 |
| #2 fake-ip 假目标地址 | active | 实时连接偶尔显示 198.18.1.255 | **已澄清：非 bug，不改代码**。客户端 fake-ip DNS 正常产物，嗅探失败时如实显示 |
| #3 系统日志流式卡死 | active | 日志产出时点进 SysLog 页直接卡死，停产后不卡 | 前端 SysLog.vue 批处理节流 + 流式时暂停自动滚动 |
| #4 前端按需打包瘦身 | active | 前端 bundle 过大，需按需加载、不打包未用组件 | 删全量引入改按需 + 样式回归验证 |

## Goal

修复 deeproxy 的三个真实缺陷并澄清一个非缺陷现象：

1. **#1 连接驻留**：客户端静默死亡（断电/掉网，无 FIN/RST）时，连接在「服务端正向客户端发数据（有未确认数据）」的场景下，TCP keepalive 被抑制，改由内核 RTO 重传超时接管（Linux `tcp_retries2=15` ≈ 15–20 分钟），导致连接驻留约 20 分钟、注册项永不 Deregister。修复：在 listener 与 dialer 的 socket 上设置 **`TCP_USER_TIMEOUT`（约 90 秒）**，对未确认数据的存活时间设上限，覆盖 keepalive 抑制场景。**严禁改动 relay 热路径（server/relay.go）。**

2. **#2 fake-ip 显示**：`198.18.1.255` 是客户端 fake-ip DNS（Clash/Surge）下发的假 IP，CONNECT 请求里 FQDN 为空、只有该 IP；当域名嗅探失败（超时 / 非 TLS、HTTP / ClientHello 被截断）时，注册表保留原始假 IP 如实显示。**结论：服务无解析错误，本次不改代码**，仅在 spec 与交付说明中澄清原理。

3. **#3 日志卡死**：SysLog.vue 每条 SSE 消息 `logs.value.push` 触发 vue-virtual-scroller 的 `{flush:"sync"}` 同步 O(N) watcher（`C.value.slice()` 全量数组拷贝），且每条都调用 `scrollToBottom()` 触发永续 rAF 强制布局读 `scrollHeight`。高速流下每秒数十万次同步操作 → 卡死；停产后队列排空即恢复。修复：**批量节流**（rAF 或 ~100–200ms 窗口合并多条消息后一次性追加）+ **流式期间默认暂停自动滚到底**（有新日志时显示「跳到最新」按钮，点击才滚动）。

4. **#4 前端瘦身**：`main.js` 全量 `import ElementPlus` + 全量 `element-plus/dist/index.css` + 300+ 图标全量注册循环，架空了 vite 已配好的 `unplugin-vue-components` 按需机制；`EChart.js` 全量 `import * as echarts`。改为按需引入，预计 index.js 从 1.3MB→200-400KB、dashboard.js 从 1.0MB→250-350KB、index.css 354KB→按需 chunk，总减 ~2MB+。完成后逐页样式回归验证。

## Constraints

- **#1 不碰 relay 热路径**：修复仅限 `server/lifecycle.go`（listener）与 `dialer/dialer.go`（dialer），通过 socket 选项实现；`server/relay.go` 的 `io.Copy` 双向中继逻辑保持不变（项目硬约束，见 project-memory build 检查）。
- **#1 清理时长上限 ≤ 90 秒**：与现有 keepalive（30s/15s×3≈75s）对齐，灵活与容错平衡，避免误断弱网/高延迟正常客户端。
- **#1 跨平台**：`TCP_USER_TIMEOUT` 是 Linux/部分平台 socket 选项；macOS/Windows 无此选项，需用 `Control`/`SyscallConn` 优雅降级（不支持的平台不报错，退回 keepalive 行为），保持单一静态二进制跨平台编译通过。
- **#3 接受批量节流延迟**：单条日志可延迟 ~100–200ms 上屏（人眼几乎无感），换取高速流下不卡。
- **#3 复用现有节流/工具**：优先用成熟方案（rAF 合并 / VueUse `useThrottleFn` 等），勿重复造轮子。
- **#4 保留必要的全局指令/服务**：删全量引入后，`v-loading`、`ElMessage`/`ElMessageBox`/`ElNotification` 等指令与服务需显式按需注册，确保不破坏现有调用。
- **#4 CSS 注入顺序风险**：切到按需 CSS 后注入顺序改变，可能影响现有 Element Plus 样式覆盖（见 [[deeproxy-ep-ondemand-css-specificity]]）；必须逐页回归验证。
- **i18n key parity**：若 #3/#4 改动涉及文案（如「跳到最新」按钮），zh.js/en.js key 集合须完全一致（见 [[i18n-key-parity-gate]]），共享文案走 common.*。
- **直接提交 main**：本仓库禁止建分支，所有代码直接提交 main（见 [[no-branches-commit-to-main]]）。
- **全中文注释**：所有新增/修改代码使用中文注释，解释「为什么」。

## Non-Goals

- 不修改 relay 双向中继热路径逻辑。
- 不为 #2 fake-ip 做反查表 / 改路由 / 改显示标记（用户明确：仅理解，不改）。
- 不改造 SSE 后端协议（#3 仅前端消费侧批处理）。
- 不重写路由懒加载（已正确配置为动态 import）。
- 不引入 webpack 或更换构建工具（继续用 vite）。
- 不追求逐条实时上屏（#3 已接受批量节流权衡）。

## Acceptance Criteria

### #1 连接驻留
- [ ] 在 listener 接受的客户端 conn 与 dialer 建立的上游/直连 conn 上设置 `TCP_USER_TIMEOUT`（约 90s），通过 `net.ListenConfig.Control` / `net.Dialer.Control` + `SyscallConn` 实现。
- [ ] 不支持 `TCP_USER_TIMEOUT` 的平台（macOS/Windows）优雅降级，编译与运行不报错，退回现有 keepalive 行为。
- [ ] `server/relay.go` 无任何改动（`git diff --stat server/relay.go` 为空）。
- [ ] 端到端：模拟客户端在「下载中（服务端持续发数据）」突然静默死亡，连接在 ≤90s 内从实时连接列表消失（Deregister 触发）。
- [ ] Go 后端 `go build ./...` 通过；现有 `connreg` 测试通过。

### #2 fake-ip 显示
- [ ] 代码无改动。
- [ ] 在交付说明中记录：198.18.x.x 是客户端 fake-ip DNS 假 IP，嗅探失败时如实显示，非服务端解析错误。

### #3 日志卡死
- [ ] SSE 消息改为批量节流追加（rAF 或 ~100–200ms 窗口），不再每条 push 同步触发全量渲染。
- [ ] 流式期间默认暂停自动滚到底；有新日志时显示「跳到最新」按钮，点击才滚动到底部。
- [ ] 高速日志产出（如每秒数十~数百条）期间，SysLog 页面交互不卡死，可正常滚动/筛选。
- [ ] 「跳到最新」等新增文案 zh/en 双语 key 齐全。
- [ ] `pnpm build`（或现有构建命令）通过。

### #4 前端瘦身
- [ ] `main.js` 删除 `import ElementPlus` + `import 'element-plus/dist/index.css'` + 全量 `app.use(ElementPlus)` + 图标全量注册循环；改为按需注册必要指令/服务，图标交由 `unplugin-vue-components` 自动按需。
- [ ] `EChart.js` 改为 `echarts/core` + 按需 `echarts.use([...])`（line/pie/bar + tooltip/legend/grid/dataZoom + CanvasRenderer）。
- [ ] 构建产物体积明显下降（index.js、dashboard.js、index.css 三项对比修复前显著减小）。
- [ ] 逐页样式回归验证：仪表盘/代理组/规则/系统日志/生成代理/用户等页面 UI 正常，Element Plus 样式覆盖未被打乱。
- [ ] `pnpm build` 通过，无报错。

## Assumptions Exposed & Resolved

| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| #2 是服务端解析错误 | 代码探索证实是客户端 fake-ip DNS 假 IP | 非 bug，不改代码，仅澄清 |
| #1 是旧构建未加 keepalive | git 确认 663138d keepalive 已在 HEAD 历史中，用户在最新构建仍见驻留 | 根因为 keepalive 被「未确认数据」抑制 + RTO 接管，需 TCP_USER_TIMEOUT |
| #3 卡是因为日志总量大 | 用户报告「停产后多也不卡」，定位为每条消息开销 | 根因为同步 O(N) watcher + 永续 rAF，批处理+暂停滚动解决 |
| #4 路由没做懒加载 | 探索发现路由已全部动态 import | 路由层无需改；瓶颈在 ElementPlus/echarts 全量引入 |
| #4 改按需零风险 | 用户记忆记录过 EP 按需 CSS 特异度坑 | 需逐页样式回归验证 |

## Technical Context（brownfield 代码探索发现）

**#1 关键文件**：
- `server/lifecycle.go` `Listen()` — listener keepalive 已配（`dialer.KeepAliveConfig` 30s/15s×3）；需补 `TCP_USER_TIMEOUT`。
- `dialer/dialer.go` — `KeepAliveConfig` 单一来源；`DialDirect`/`DialUpstream` 需补 `Control`。
- `dialer/idleconn.go` `WrapIdle` — idle_timeout 仅作用于上游 conn 的 SetReadDeadline，不覆盖客户端侧读阻塞。
- `server/relay.go` `relayCounted` L95-167 — `io.Copy` 双向；**禁止改动**。
- `connreg/registry.go` — `Register`/`Deregister`，`defer Deregister` 依赖 relay 返回。

**#2 关键文件**：`server/server.go` `targetHost()` L143-153、`handleSniff()` L377-440（仅嗅探成功才 `SetTarget`）、`connreg/registry.go` `toView()` L183-207（fallback 到 meta.Target）。

**#3 关键文件**：`web/src/views/syslog/SysLog.vue` — `appendLog` L53-59、`scrollToBottom` L61-68、`openStream` L70-92；`web/src/api/syslog.js` `openLogStream` L21-24（EventSource SSE）；`DynamicScroller`（vue-virtual-scroller@^3）。

**#4 关键文件**：`web/src/main.js`（全量 ElementPlus/CSS/图标）、`web/src/components/EChart.js` L3（全量 echarts）、`web/vite.config.js`（已配 AutoImport+Components+ElementPlusResolver，build.outDir=../api/dist）、`web/src/router/index.js`（懒加载已正确）。

## Ontology (Key Entities)

| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| Connection (实时连接) | core domain | connID, Target, Action, User, Group, Client, Start | Registry has many Connection；relay 生命周期决定其存活 |
| Registry (连接注册表) | core domain | active(map), hint | Register/Deregister/SetTarget Connection |
| TCP Socket Option | supporting | TCP_USER_TIMEOUT, KeepAliveConfig | 作用于 listener conn 与 dialer conn |
| Sniff Result | supporting | host, ok | 决定是否 SetTarget(域名) 覆盖 fake-ip |
| LogEntry | core domain | id, level, message, fields, ts | SSE 流推送，批量追加进 logs 数组 |
| Bundle Chunk | supporting | index.js, dashboard.js, index.css | 由 vite/rollup 按引入方式生成 |

## Ontology Convergence

| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 3 (fake-ip 域) | 3 | - | - | N/A |
| 2-5 | 6 (累积四组件) | 逐轮新增 | 0 | 全稳定 | ~100% |

四个组件的核心实体互相独立，无重命名/漂移，模型稳定。

## Interview Transcript

<details>
<summary>Full Q&A (5 rounds + Round 0)</summary>

### Round 0 — Topology
**Q:** 4 个独立顶层工作项拆分是否正确？
**A:** 4 项都做，一起修。

### Round 1 — #2 Goal
**Q:** 198.18.1.255 是客户端 fake-ip 假 IP，嗅探失败时如实显示，你希望怎么处理？
**A:** 只要理解，不改。

### Round 2 — #1 Criteria
**Q:** 663138d 已加客户端 keepalive，你的「20 分钟」是哪个版本观察到的？
**A:** 最新构建仍驻留。（→ 锁定 RTO 接管根因）

### Round 3 — #1 Criteria
**Q:** 可接受的「客户端死亡→连接清理」最大时长？
**A:** ≤90 秒（推荐）。

### Round 4 — #3 Constraints
**Q:** 日志卡死修复需批处理/节流，你接受哪种权衡？
**A:** 批量 + 流式时暂停滚动。

### Round 5 — #4 Constraints
**Q:** 切到按需 CSS 后注入顺序会变可能影响样式覆盖，修复后怎么验证？
**A:** 要样式回归验证（推荐）。

</details>
