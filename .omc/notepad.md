# Notepad
<!-- Auto-managed by OMC. Manual edits preserved in MANUAL section. -->

## Priority Context
<!-- ALWAYS loaded. Keep under 500 chars. Critical discoveries only. -->

## Working Memory
<!-- Session notes. Auto-pruned after 7 days. -->
### 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。
### 2026-06-13 10:47
worker-4 状态(2026-06-13): T5 ✅完成已标 completed (pool/selector.go+health.go, stats/counter.go+flush.go, syslog/buffer.go+handler.go+audit.go)，全部 -race 全绿含 AC-42。go build ./... 通过。T8 前端骨架✅已 build 通过(api/dist)，仍 blocked by T7(worker-3 已解锁)。待 T7 完成做 T8 联调对齐(契约表 web/README.md)。注意:全项目 go test 时 server 包 panic 是 worker-2 T3/worker-1 T6 半迁移问题(非我代码)，已 heads-up。给 T6 接线提示已发: sel.Pick(gv.HealthyUpstreams)/ErrNoUpstream→RepHostUnreachable/u.ToAuthUpstream(vars)/SnapshotRefresher 包 holder.RebuildAndSwap/埋点 stats+audit。
### 2026-06-13 11:19
worker-4 T8联调(2026-06-13): 已重写 web/src/api/* 全部对齐 T7 真实契约。关键差异(已适配前端):①无{code,data,msg}包裹,成功裸data/失败{error}+非200;②路径变化:/setup/status,/setup,/login,/logout(无/auth);/dashboard,/dashboard/timeseries,/dashboard/top-groups;/rulegroups;/upstreams/:uid(更新删除不嵌group);/upstreams/:uid/enabled;/users;/tools/{export,import,test-proxy,test-rule};/logs,/logs/stream,/audit;③store模型PascalCase无json tag(Group.HCEnabled等扁平,Upstream.HealthState);④授权方向反转:后端是group→users(PUT /groups/:id/users {userIds})+group→rulegroups(PUT /groups/:id/rulegroups {ruleGroupIds}),前端原是user→groups;⑤SSE事件名'log'需addEventListener;⑥规则测试器传group名非id,返回{action,matched,isIp,sniffNote};⑦TopN仅分组无user/domain,无runtime端点,动作分布来自realtime块。待办:改 views(Dashboard/ProxyGroups/Rules/Users/SysLog/Settings)消费新shape+pnpm build。
### 2026-06-13 11:54
worker-4 T8完成(2026-06-13): 关键教训——我中途读到 worker-3 的 T7 中间版本(路径/字段全不同)就重写了 api/*+views,后来实测发现 worker-3 最终把 T7 完全按我原 README 契约实现了(camelCase DTO/嵌套healthCheck/扁平dashboard overview/{times,up,down,req}/healthState字符串/user有groupIds/rule order/SSE默认事件onmessage/错误{msg})。已把 api/*+全部views 全部 revert 回原契约。唯一真差异:init-status返回{configured}非{initialized}已改。起 /tmp smoke harness(:18080)实测 setup→login→groups/dashboard/CRUD/export/test-rule 全部 shape 匹配通过。pnpm build 通过→api/dist。注意:worker-1 T9重构把 pool/health.go→pool/health/子包、snapshot/rebuild.go→holder.go,我的包仍 -race 全绿。教训:并发 team 中读他人在写的文件易得中间态,应实测 live 端点为准而非读源码。
### 2026-06-13 12:00
worker-4 状态(2026-06-13 晚): T5+T8 均 completed。T8 UI 打磨已做:Top用户/域名改"首版暂不支持"占位(后端只有Top分组),loadTop 只拉group。SSE确认前后端已一致(后端默认无名事件+前端onmessage)无需改。pnpm build 通过。lead 裁定"以我README为契约基准"已事实达成(worker-3 已收敛)。待办:①worker-1 回复 stats 包 AC-43 拆分(stats/counter零依赖 + stats/flush/子包import store)是否我来做(避免与其T13冲突);②worker-3 CONTRACT.md 定稿后若有字段微调我跟进;③worker-3 T7重对齐完成后 lead 通知做真实联调(虽已 smoke 验证过)。我的包 pool/pool.health/stats/syslog -race 全绿。
### 2026-06-13 12:11
worker-4 终态(2026-06-13晚): T5+T8 完成且**真实后端联调验证通过**(临时 api.App harness 跑 live + curl)。worker-3 T7 已全量对齐我 README 契约。最后修了1个真实bug:init-status 后端字段是 {initialized} 我误改读 {configured}→已修回 data.initialized 实测正确。Top user/domain 占位「首版暂不支持」。SSE 前端 onmessage+后端默认无名事件一致。授权用用户侧(Users 内联 groupIds)。pnpm build 通过→api/dist。smoke harness 已清理(cmd/dpsmoke 删)。唯一未决:stats AC-43 拆分等 worker-1 回复(其 T12 可能已覆盖)。我的包 pool/pool.health/stats/syslog -race 全绿。教训:并发 team 读他人在写源码得中间态,以 live 端点+CONTRACT.md 为准。
### 2026-06-13 12:34
worker-4 完结态(2026-06-13): T5+T8 全 PASS,team-lead 确认。QA_REPORT.md 第7节(T5子系统+T8前端+真机复核)已追加(不覆盖 worker-1 内核主线)。做了真实单二进制 E2E:39M CGO=0 二进制、./... -race 全绿、AC-43 server/stats 零 store/sql、embed 首页/SPA fallback/assets/init-status/404 全验。flag 了后端观察项:API 无 per-request slog 日志→系统日志页平时只 2 条启动日志(非前端缺陷,建议 worker-3 加埋点)。剩 T10(跨平台CI)+T11(转发E2E+benchmark) worker-2。待命:worker-2 若需前端 headless E2E 我配合;否则我真机复核已覆盖前端。我的包 pool/pool.health/stats/stats.flush/syslog -race 全绿。授权用户侧。SSE onmessage+后端默认事件。教训:并发 team 以 live 端点+CONTRACT.md 为准非读中间态源码。
### 2026-06-14 09:23
Top 目标域名 feature SHIPPED (ralph, 2026-06-14). Backend: domain_hit table (store/schema.go) + store/domain_hit_repo.go (FlushDomainHits/QueryTopDomains/CleanupDomainHitsBefore, mirrors traffic_stat) + stats/counter.go IncDomain w/ independent domMu + idle-cycle eviction (evictAfterIdleCycles=3) + flush.go dual-bucket independence + server.go埋点 at dialAndRelay(d.host)/handleSniff(routeHost) + handleTop case domain + feature_status flip. Frontend: Dashboard.vue + ProxyGroups.vue horizontal bar charts binding .count. Tests: store/domain_hit_test.go + stats/domain_test.go (incl eviction + -race concurrency). All verified: go build/test, -race, vite build, architect APPROVED (10/10 ACs). Plan: .omc/plans/consensus-top-target-domains.md. Restored api/dist/.gitkeep (required for go:embed).


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。
### 2026-06-13 10:47
worker-4 状态(2026-06-13): T5 ✅完成已标 completed (pool/selector.go+health.go, stats/counter.go+flush.go, syslog/buffer.go+handler.go+audit.go)，全部 -race 全绿含 AC-42。go build ./... 通过。T8 前端骨架✅已 build 通过(api/dist)，仍 blocked by T7(worker-3 已解锁)。待 T7 完成做 T8 联调对齐(契约表 web/README.md)。注意:全项目 go test 时 server 包 panic 是 worker-2 T3/worker-1 T6 半迁移问题(非我代码)，已 heads-up。给 T6 接线提示已发: sel.Pick(gv.HealthyUpstreams)/ErrNoUpstream→RepHostUnreachable/u.ToAuthUpstream(vars)/SnapshotRefresher 包 holder.RebuildAndSwap/埋点 stats+audit。
### 2026-06-13 11:19
worker-4 T8联调(2026-06-13): 已重写 web/src/api/* 全部对齐 T7 真实契约。关键差异(已适配前端):①无{code,data,msg}包裹,成功裸data/失败{error}+非200;②路径变化:/setup/status,/setup,/login,/logout(无/auth);/dashboard,/dashboard/timeseries,/dashboard/top-groups;/rulegroups;/upstreams/:uid(更新删除不嵌group);/upstreams/:uid/enabled;/users;/tools/{export,import,test-proxy,test-rule};/logs,/logs/stream,/audit;③store模型PascalCase无json tag(Group.HCEnabled等扁平,Upstream.HealthState);④授权方向反转:后端是group→users(PUT /groups/:id/users {userIds})+group→rulegroups(PUT /groups/:id/rulegroups {ruleGroupIds}),前端原是user→groups;⑤SSE事件名'log'需addEventListener;⑥规则测试器传group名非id,返回{action,matched,isIp,sniffNote};⑦TopN仅分组无user/domain,无runtime端点,动作分布来自realtime块。待办:改 views(Dashboard/ProxyGroups/Rules/Users/SysLog/Settings)消费新shape+pnpm build。
### 2026-06-13 11:54
worker-4 T8完成(2026-06-13): 关键教训——我中途读到 worker-3 的 T7 中间版本(路径/字段全不同)就重写了 api/*+views,后来实测发现 worker-3 最终把 T7 完全按我原 README 契约实现了(camelCase DTO/嵌套healthCheck/扁平dashboard overview/{times,up,down,req}/healthState字符串/user有groupIds/rule order/SSE默认事件onmessage/错误{msg})。已把 api/*+全部views 全部 revert 回原契约。唯一真差异:init-status返回{configured}非{initialized}已改。起 /tmp smoke harness(:18080)实测 setup→login→groups/dashboard/CRUD/export/test-rule 全部 shape 匹配通过。pnpm build 通过→api/dist。注意:worker-1 T9重构把 pool/health.go→pool/health/子包、snapshot/rebuild.go→holder.go,我的包仍 -race 全绿。教训:并发 team 中读他人在写的文件易得中间态,应实测 live 端点为准而非读源码。
### 2026-06-13 12:00
worker-4 状态(2026-06-13 晚): T5+T8 均 completed。T8 UI 打磨已做:Top用户/域名改"首版暂不支持"占位(后端只有Top分组),loadTop 只拉group。SSE确认前后端已一致(后端默认无名事件+前端onmessage)无需改。pnpm build 通过。lead 裁定"以我README为契约基准"已事实达成(worker-3 已收敛)。待办:①worker-1 回复 stats 包 AC-43 拆分(stats/counter零依赖 + stats/flush/子包import store)是否我来做(避免与其T13冲突);②worker-3 CONTRACT.md 定稿后若有字段微调我跟进;③worker-3 T7重对齐完成后 lead 通知做真实联调(虽已 smoke 验证过)。我的包 pool/pool.health/stats/syslog -race 全绿。
### 2026-06-13 12:11
worker-4 终态(2026-06-13晚): T5+T8 完成且**真实后端联调验证通过**(临时 api.App harness 跑 live + curl)。worker-3 T7 已全量对齐我 README 契约。最后修了1个真实bug:init-status 后端字段是 {initialized} 我误改读 {configured}→已修回 data.initialized 实测正确。Top user/domain 占位「首版暂不支持」。SSE 前端 onmessage+后端默认无名事件一致。授权用用户侧(Users 内联 groupIds)。pnpm build 通过→api/dist。smoke harness 已清理(cmd/dpsmoke 删)。唯一未决:stats AC-43 拆分等 worker-1 回复(其 T12 可能已覆盖)。我的包 pool/pool.health/stats/syslog -race 全绿。教训:并发 team 读他人在写源码得中间态,以 live 端点+CONTRACT.md 为准。
### 2026-06-13 12:34
worker-4 完结态(2026-06-13): T5+T8 全 PASS,team-lead 确认。QA_REPORT.md 第7节(T5子系统+T8前端+真机复核)已追加(不覆盖 worker-1 内核主线)。做了真实单二进制 E2E:39M CGO=0 二进制、./... -race 全绿、AC-43 server/stats 零 store/sql、embed 首页/SPA fallback/assets/init-status/404 全验。flag 了后端观察项:API 无 per-request slog 日志→系统日志页平时只 2 条启动日志(非前端缺陷,建议 worker-3 加埋点)。剩 T10(跨平台CI)+T11(转发E2E+benchmark) worker-2。待命:worker-2 若需前端 headless E2E 我配合;否则我真机复核已覆盖前端。我的包 pool/pool.health/stats/stats.flush/syslog -race 全绿。授权用户侧。SSE onmessage+后端默认事件。教训:并发 team 以 live 端点+CONTRACT.md 为准非读中间态源码。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。
### 2026-06-13 10:47
worker-4 状态(2026-06-13): T5 ✅完成已标 completed (pool/selector.go+health.go, stats/counter.go+flush.go, syslog/buffer.go+handler.go+audit.go)，全部 -race 全绿含 AC-42。go build ./... 通过。T8 前端骨架✅已 build 通过(api/dist)，仍 blocked by T7(worker-3 已解锁)。待 T7 完成做 T8 联调对齐(契约表 web/README.md)。注意:全项目 go test 时 server 包 panic 是 worker-2 T3/worker-1 T6 半迁移问题(非我代码)，已 heads-up。给 T6 接线提示已发: sel.Pick(gv.HealthyUpstreams)/ErrNoUpstream→RepHostUnreachable/u.ToAuthUpstream(vars)/SnapshotRefresher 包 holder.RebuildAndSwap/埋点 stats+audit。
### 2026-06-13 11:19
worker-4 T8联调(2026-06-13): 已重写 web/src/api/* 全部对齐 T7 真实契约。关键差异(已适配前端):①无{code,data,msg}包裹,成功裸data/失败{error}+非200;②路径变化:/setup/status,/setup,/login,/logout(无/auth);/dashboard,/dashboard/timeseries,/dashboard/top-groups;/rulegroups;/upstreams/:uid(更新删除不嵌group);/upstreams/:uid/enabled;/users;/tools/{export,import,test-proxy,test-rule};/logs,/logs/stream,/audit;③store模型PascalCase无json tag(Group.HCEnabled等扁平,Upstream.HealthState);④授权方向反转:后端是group→users(PUT /groups/:id/users {userIds})+group→rulegroups(PUT /groups/:id/rulegroups {ruleGroupIds}),前端原是user→groups;⑤SSE事件名'log'需addEventListener;⑥规则测试器传group名非id,返回{action,matched,isIp,sniffNote};⑦TopN仅分组无user/domain,无runtime端点,动作分布来自realtime块。待办:改 views(Dashboard/ProxyGroups/Rules/Users/SysLog/Settings)消费新shape+pnpm build。
### 2026-06-13 11:54
worker-4 T8完成(2026-06-13): 关键教训——我中途读到 worker-3 的 T7 中间版本(路径/字段全不同)就重写了 api/*+views,后来实测发现 worker-3 最终把 T7 完全按我原 README 契约实现了(camelCase DTO/嵌套healthCheck/扁平dashboard overview/{times,up,down,req}/healthState字符串/user有groupIds/rule order/SSE默认事件onmessage/错误{msg})。已把 api/*+全部views 全部 revert 回原契约。唯一真差异:init-status返回{configured}非{initialized}已改。起 /tmp smoke harness(:18080)实测 setup→login→groups/dashboard/CRUD/export/test-rule 全部 shape 匹配通过。pnpm build 通过→api/dist。注意:worker-1 T9重构把 pool/health.go→pool/health/子包、snapshot/rebuild.go→holder.go,我的包仍 -race 全绿。教训:并发 team 中读他人在写的文件易得中间态,应实测 live 端点为准而非读源码。
### 2026-06-13 12:00
worker-4 状态(2026-06-13 晚): T5+T8 均 completed。T8 UI 打磨已做:Top用户/域名改"首版暂不支持"占位(后端只有Top分组),loadTop 只拉group。SSE确认前后端已一致(后端默认无名事件+前端onmessage)无需改。pnpm build 通过。lead 裁定"以我README为契约基准"已事实达成(worker-3 已收敛)。待办:①worker-1 回复 stats 包 AC-43 拆分(stats/counter零依赖 + stats/flush/子包import store)是否我来做(避免与其T13冲突);②worker-3 CONTRACT.md 定稿后若有字段微调我跟进;③worker-3 T7重对齐完成后 lead 通知做真实联调(虽已 smoke 验证过)。我的包 pool/pool.health/stats/syslog -race 全绿。
### 2026-06-13 12:11
worker-4 终态(2026-06-13晚): T5+T8 完成且**真实后端联调验证通过**(临时 api.App harness 跑 live + curl)。worker-3 T7 已全量对齐我 README 契约。最后修了1个真实bug:init-status 后端字段是 {initialized} 我误改读 {configured}→已修回 data.initialized 实测正确。Top user/domain 占位「首版暂不支持」。SSE 前端 onmessage+后端默认无名事件一致。授权用用户侧(Users 内联 groupIds)。pnpm build 通过→api/dist。smoke harness 已清理(cmd/dpsmoke 删)。唯一未决:stats AC-43 拆分等 worker-1 回复(其 T12 可能已覆盖)。我的包 pool/pool.health/stats/syslog -race 全绿。教训:并发 team 读他人在写源码得中间态,以 live 端点+CONTRACT.md 为准。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。
### 2026-06-13 10:47
worker-4 状态(2026-06-13): T5 ✅完成已标 completed (pool/selector.go+health.go, stats/counter.go+flush.go, syslog/buffer.go+handler.go+audit.go)，全部 -race 全绿含 AC-42。go build ./... 通过。T8 前端骨架✅已 build 通过(api/dist)，仍 blocked by T7(worker-3 已解锁)。待 T7 完成做 T8 联调对齐(契约表 web/README.md)。注意:全项目 go test 时 server 包 panic 是 worker-2 T3/worker-1 T6 半迁移问题(非我代码)，已 heads-up。给 T6 接线提示已发: sel.Pick(gv.HealthyUpstreams)/ErrNoUpstream→RepHostUnreachable/u.ToAuthUpstream(vars)/SnapshotRefresher 包 holder.RebuildAndSwap/埋点 stats+audit。
### 2026-06-13 11:19
worker-4 T8联调(2026-06-13): 已重写 web/src/api/* 全部对齐 T7 真实契约。关键差异(已适配前端):①无{code,data,msg}包裹,成功裸data/失败{error}+非200;②路径变化:/setup/status,/setup,/login,/logout(无/auth);/dashboard,/dashboard/timeseries,/dashboard/top-groups;/rulegroups;/upstreams/:uid(更新删除不嵌group);/upstreams/:uid/enabled;/users;/tools/{export,import,test-proxy,test-rule};/logs,/logs/stream,/audit;③store模型PascalCase无json tag(Group.HCEnabled等扁平,Upstream.HealthState);④授权方向反转:后端是group→users(PUT /groups/:id/users {userIds})+group→rulegroups(PUT /groups/:id/rulegroups {ruleGroupIds}),前端原是user→groups;⑤SSE事件名'log'需addEventListener;⑥规则测试器传group名非id,返回{action,matched,isIp,sniffNote};⑦TopN仅分组无user/domain,无runtime端点,动作分布来自realtime块。待办:改 views(Dashboard/ProxyGroups/Rules/Users/SysLog/Settings)消费新shape+pnpm build。
### 2026-06-13 11:54
worker-4 T8完成(2026-06-13): 关键教训——我中途读到 worker-3 的 T7 中间版本(路径/字段全不同)就重写了 api/*+views,后来实测发现 worker-3 最终把 T7 完全按我原 README 契约实现了(camelCase DTO/嵌套healthCheck/扁平dashboard overview/{times,up,down,req}/healthState字符串/user有groupIds/rule order/SSE默认事件onmessage/错误{msg})。已把 api/*+全部views 全部 revert 回原契约。唯一真差异:init-status返回{configured}非{initialized}已改。起 /tmp smoke harness(:18080)实测 setup→login→groups/dashboard/CRUD/export/test-rule 全部 shape 匹配通过。pnpm build 通过→api/dist。注意:worker-1 T9重构把 pool/health.go→pool/health/子包、snapshot/rebuild.go→holder.go,我的包仍 -race 全绿。教训:并发 team 中读他人在写的文件易得中间态,应实测 live 端点为准而非读源码。
### 2026-06-13 12:00
worker-4 状态(2026-06-13 晚): T5+T8 均 completed。T8 UI 打磨已做:Top用户/域名改"首版暂不支持"占位(后端只有Top分组),loadTop 只拉group。SSE确认前后端已一致(后端默认无名事件+前端onmessage)无需改。pnpm build 通过。lead 裁定"以我README为契约基准"已事实达成(worker-3 已收敛)。待办:①worker-1 回复 stats 包 AC-43 拆分(stats/counter零依赖 + stats/flush/子包import store)是否我来做(避免与其T13冲突);②worker-3 CONTRACT.md 定稿后若有字段微调我跟进;③worker-3 T7重对齐完成后 lead 通知做真实联调(虽已 smoke 验证过)。我的包 pool/pool.health/stats/syslog -race 全绿。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。
### 2026-06-13 10:47
worker-4 状态(2026-06-13): T5 ✅完成已标 completed (pool/selector.go+health.go, stats/counter.go+flush.go, syslog/buffer.go+handler.go+audit.go)，全部 -race 全绿含 AC-42。go build ./... 通过。T8 前端骨架✅已 build 通过(api/dist)，仍 blocked by T7(worker-3 已解锁)。待 T7 完成做 T8 联调对齐(契约表 web/README.md)。注意:全项目 go test 时 server 包 panic 是 worker-2 T3/worker-1 T6 半迁移问题(非我代码)，已 heads-up。给 T6 接线提示已发: sel.Pick(gv.HealthyUpstreams)/ErrNoUpstream→RepHostUnreachable/u.ToAuthUpstream(vars)/SnapshotRefresher 包 holder.RebuildAndSwap/埋点 stats+audit。
### 2026-06-13 11:19
worker-4 T8联调(2026-06-13): 已重写 web/src/api/* 全部对齐 T7 真实契约。关键差异(已适配前端):①无{code,data,msg}包裹,成功裸data/失败{error}+非200;②路径变化:/setup/status,/setup,/login,/logout(无/auth);/dashboard,/dashboard/timeseries,/dashboard/top-groups;/rulegroups;/upstreams/:uid(更新删除不嵌group);/upstreams/:uid/enabled;/users;/tools/{export,import,test-proxy,test-rule};/logs,/logs/stream,/audit;③store模型PascalCase无json tag(Group.HCEnabled等扁平,Upstream.HealthState);④授权方向反转:后端是group→users(PUT /groups/:id/users {userIds})+group→rulegroups(PUT /groups/:id/rulegroups {ruleGroupIds}),前端原是user→groups;⑤SSE事件名'log'需addEventListener;⑥规则测试器传group名非id,返回{action,matched,isIp,sniffNote};⑦TopN仅分组无user/domain,无runtime端点,动作分布来自realtime块。待办:改 views(Dashboard/ProxyGroups/Rules/Users/SysLog/Settings)消费新shape+pnpm build。
### 2026-06-13 11:54
worker-4 T8完成(2026-06-13): 关键教训——我中途读到 worker-3 的 T7 中间版本(路径/字段全不同)就重写了 api/*+views,后来实测发现 worker-3 最终把 T7 完全按我原 README 契约实现了(camelCase DTO/嵌套healthCheck/扁平dashboard overview/{times,up,down,req}/healthState字符串/user有groupIds/rule order/SSE默认事件onmessage/错误{msg})。已把 api/*+全部views 全部 revert 回原契约。唯一真差异:init-status返回{configured}非{initialized}已改。起 /tmp smoke harness(:18080)实测 setup→login→groups/dashboard/CRUD/export/test-rule 全部 shape 匹配通过。pnpm build 通过→api/dist。注意:worker-1 T9重构把 pool/health.go→pool/health/子包、snapshot/rebuild.go→holder.go,我的包仍 -race 全绿。教训:并发 team 中读他人在写的文件易得中间态,应实测 live 端点为准而非读源码。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。
### 2026-06-13 10:47
worker-4 状态(2026-06-13): T5 ✅完成已标 completed (pool/selector.go+health.go, stats/counter.go+flush.go, syslog/buffer.go+handler.go+audit.go)，全部 -race 全绿含 AC-42。go build ./... 通过。T8 前端骨架✅已 build 通过(api/dist)，仍 blocked by T7(worker-3 已解锁)。待 T7 完成做 T8 联调对齐(契约表 web/README.md)。注意:全项目 go test 时 server 包 panic 是 worker-2 T3/worker-1 T6 半迁移问题(非我代码)，已 heads-up。给 T6 接线提示已发: sel.Pick(gv.HealthyUpstreams)/ErrNoUpstream→RepHostUnreachable/u.ToAuthUpstream(vars)/SnapshotRefresher 包 holder.RebuildAndSwap/埋点 stats+audit。
### 2026-06-13 11:19
worker-4 T8联调(2026-06-13): 已重写 web/src/api/* 全部对齐 T7 真实契约。关键差异(已适配前端):①无{code,data,msg}包裹,成功裸data/失败{error}+非200;②路径变化:/setup/status,/setup,/login,/logout(无/auth);/dashboard,/dashboard/timeseries,/dashboard/top-groups;/rulegroups;/upstreams/:uid(更新删除不嵌group);/upstreams/:uid/enabled;/users;/tools/{export,import,test-proxy,test-rule};/logs,/logs/stream,/audit;③store模型PascalCase无json tag(Group.HCEnabled等扁平,Upstream.HealthState);④授权方向反转:后端是group→users(PUT /groups/:id/users {userIds})+group→rulegroups(PUT /groups/:id/rulegroups {ruleGroupIds}),前端原是user→groups;⑤SSE事件名'log'需addEventListener;⑥规则测试器传group名非id,返回{action,matched,isIp,sniffNote};⑦TopN仅分组无user/domain,无runtime端点,动作分布来自realtime块。待办:改 views(Dashboard/ProxyGroups/Rules/Users/SysLog/Settings)消费新shape+pnpm build。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。
### 2026-06-13 10:47
worker-4 状态(2026-06-13): T5 ✅完成已标 completed (pool/selector.go+health.go, stats/counter.go+flush.go, syslog/buffer.go+handler.go+audit.go)，全部 -race 全绿含 AC-42。go build ./... 通过。T8 前端骨架✅已 build 通过(api/dist)，仍 blocked by T7(worker-3 已解锁)。待 T7 完成做 T8 联调对齐(契约表 web/README.md)。注意:全项目 go test 时 server 包 panic 是 worker-2 T3/worker-1 T6 半迁移问题(非我代码)，已 heads-up。给 T6 接线提示已发: sel.Pick(gv.HealthyUpstreams)/ErrNoUpstream→RepHostUnreachable/u.ToAuthUpstream(vars)/SnapshotRefresher 包 holder.RebuildAndSwap/埋点 stats+audit。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。
### 2026-06-13 10:38
worker-4 T5 进度(2026-06-13): 纯内存先行部分完成且 -race 全绿——stats/counter.go(atomic维度计数+CollectDeltas差分)、stats/flush.go(Flusher worker→store.FlushTrafficStats分钟桶+CleanupBefore)、syslog/buffer.go(泛型环形缓冲5000+pub/sub)、syslog/handler.go(slog Handler+LogBuffer门面)、syslog/audit.go(AC-36)。修了2个真实并发bug(close(data)竞态→改close(done);slog WithGroup前缀语义)。store接口已用:StatDelta/FlushTrafficStats/CleanupBefore/QueryTotals。syslog.Subscribe签名=(data,done,unsub)。剩余:pool/selector.go(SWRR)+pool/health.go+接线，等T2 config.Snapshot健康节点列表引用。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。
### 2026-06-13 10:22
worker-4 状态(2026-06-13 更新): team-lead 已确认 T8 完成(8页面+pnpm build通过+契约表，超预期)，契约集中 README 风格批准保留。构建顺序风险(vite emptyOutDir 冲占位)已被 lead 记录将在 T9 提醒 worker-1。T5 待命：需 worker-1 的 T1 store单写入口 + T2 Snapshot健康节点列表引用，T2 一就绪 lead 立即转签名给我即可开 T5 全量(pool/stats/syslog)。预备工作已完成(v1 relay/slog/dialer/ctxkey 研读)。当前纯待命，周期重查 TaskList。


## 2026-06-13 10:16
worker-4 状态(2026-06-13): T8前端骨架已完成并 pnpm build 通过(产物 api/dist)。契约写入 web/README.md 待 T7 对齐。T5(pool/stats/syslog) 阻塞于 T1(in_progress worker-1)+T2(pending)。已读 v1 internal/logging(slog New)、server/ctxkey(decision struct)、dialer(DialUpstream 接收单 auth.Upstream)、auth.Upstream。等 worker-1 给 store模型+config.Snapshot健康列表引用+单写入口签名后立即开 T5。


## MANUAL
<!-- User content. Never auto-pruned. -->

