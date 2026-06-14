# deeproxy v2 — 管理后端 API 契约（权威）

> 状态：**已定稿并实现（方向 A：后端已对齐前端 README）**。team-lead 指定 web/README.md 为权威契约，后端 T7 已据此重写路由/载荷。
> 本文档是前端(T8)与后端(T7)的【单一权威 API 契约】。定稿后双方均以此为准，任何一方变更须先改本文档。
>
> 背景：T7 后端按 spec AC 命名实现，T8 前端按 `web/README.md` 推断契约实现，二者落地后路由/载荷不一致。
> 本草案以【前端 README 推断契约】为基准（方向 A），列出后端需调整项，供裁定。
>
> 通用约定：
> - baseURL = `/api`；会话用 HttpOnly Cookie `deeproxy_admin`，前端 `withCredentials:true` 自动携带。
> - **响应壳（team-lead 最终拍板，写死，勿改）：成功 = 裸数据体（或 `{ok:true}`）；错误 = `{msg}`（HTTP 非 2xx）。
>   **不使用 `{code,data,msg}` 包壳**。前端 request.js 拦截器虽兼容两种，但本项目统一裸数据——已由 worker-4 smoke harness 逐端点实测通过。
> - 错误响应：HTTP 状态码 + `{ "msg": "中文错误说明" }`（前端读 `error.response.data.msg`）。
>   - 注：后端当前用 `{error}` 字段，**方向 A 下需改为 `{msg}`** 以对齐前端拦截器。
> - 401 表示未登录/会话失效，前端据此跳登录页。

---

## 实现状态（方向 A 已落地）

后端 T7 已按本契约重写并通过测试（go build + go test ./api/ 含 -race 全绿）。要点：
- 所有路径前缀/分形已对齐前端 src/api：/auth/*、/dashboard/{overview,timeseries,action-dist,top,runtime}、/groups/:id/upstreams/:uid{/toggle,/test}、/rule-groups{/test,/:id/groups,/:id/rules/:rid}、/proxy-users{/:id/groups}、/settings{/admin-password,/export,/import}、/syslog{/stream,/audit}。
- 错误响应统一 {msg}（对齐前端拦截器 error.response.data.msg）；成功回裸数据。
- 响应字段已对齐：group.healthCheck 嵌套 + today*；upstream.healthState 三态 + latencyMs；ruleGroup.groupIds/groups/ruleCount；rule.order；user.groupIds；settings.hcDefaults 嵌套。
- ✅【SSE 已修正】/syslog/stream 改发**默认(无名)事件**（sse.Encode Event 名为空），前端 EventSource.onmessage 可正常接收（原命名事件 "log" 会收不到）。
- ✅ uptimeSec / runtime 内存+goroutine：API 层自取（App.startedAt + runtime 包）。
- ✅ upRate/downRate：API 层基于「今日累计字节」两次 overview 采样差值算出（免改 stats）。
- ✅【已落地】/dashboard/top：kind=group 与 kind=user 返回 `[{name,bytes}]`；**kind=domain 返回 `[{name,count}]`**（按 domain_hit 分钟桶命中次数降序，支持可选 `?groupId=` 过滤；空库返回空数组），不再带 `X-Feature-Status` 头。Top 域名经 stats.IncDomain 对 CONNECT 目标按连接维度埋点（dialAndRelay/handleSniff）+ domain_hit 表落库实现。
- matchedRule：rule.Engine 仅返回 (action,matched) 不含命中表达式，规则测试器 matchedRule 以「target → action」概述；如需精确命中规则文本，需 rule 包扩展 API（后续）。


---

## 1. 认证 auth（AC-19/20/40）

| 方法 | 路径 | 请求体 | 响应 | 后端现状(T7) | 需改 |
|------|------|--------|------|--------------|------|
| GET  | `/api/auth/init-status` | — | `{ initialized: bool }` | `/api/setup/status` → `{configured}` | 改路径+字段名 |
| POST | `/api/auth/setup` | `{ username, password }` | `{ ok:true }` | `/api/setup` | 改路径 |
| POST | `/api/auth/login` | `{ username, password }` | set-cookie；`{ ok:true }` | `/api/login` | 改路径 |
| POST | `/api/auth/logout` | — | `{ ok:true }` | `/api/logout` | 改路径 |

限流：连续失败 5 次锁 5 分钟，锁定回 429 `{msg}`。

---

## 2. 仪表盘 dashboard（AC-24/27）

| 方法 | 路径 | 参数 | 响应 |
|------|------|------|------|
| GET | `/api/dashboard/overview` | — | `{ upRate, downRate, activeConns, todayUp, todayDown, todayReq, todayRejectRule, todayRejectAuth, healthyProxies, totalProxies, uptimeSec }` |
| GET | `/api/dashboard/timeseries` | `window=1h\|24h\|7d[&groupId]` | `{ times:[], up:[], down:[], req:[] }` |
| GET | `/api/dashboard/action-dist` | `window=` | `[{ name:'forward', value }]` |
| GET | `/api/dashboard/top` | `kind=group\|user\|domain&window=&limit=` | group/user:`[{name,bytes}]`；domain:`[{name,count}]` |
| GET | `/api/dashboard/runtime` | — | `{ memMB, goroutines, groups:[{id,name,healthy,total,allDown}] }` |

后端现状(T7)：单 `/dashboard`(嵌套 realtime/today/health) + `/dashboard/timeseries`(返回 StatPoint 数组) + `/dashboard/top-groups`。
需改：
- 拆 `/dashboard` → `/dashboard/overview`（扁平字段）+ `/dashboard/runtime`（runtime.MemStats+goroutine）。
- `timeseries` 响应改为 `{times,up,down,req}` 列式（现为 `[{bucket,up,down,req}]` 行式）。
- 新增 `/dashboard/action-dist`（用 stats 的 actForward/actDirect/actReject）。
- `/dashboard/top` 用 `kind` 区分（现仅 top-groups）。
  - **现状**：`kind=group`、`kind=user` 已落地（store.QueryTopGroups / QueryTopUsers）；`kind=domain` 仍占位，需目标域名埋点（spec AC-27，属 T5/T6 stats 维度）。→ 见文末「依赖缺口」。
- 实时速率 upRate/downRate（KB/s）：需 stats 暴露瞬时速率（现 Counter 有累计与 active，**速率需新增**）。→ 见「依赖缺口」。

---

## 3. 代理组 group（AC-21/28/38，G2）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/api/groups` | 列表/新建 |
| PUT/DELETE | `/api/groups/:id` | 改/删 |
| GET/POST | `/api/groups/:gid/upstreams` | 该组上游列表/新建 |
| PUT/DELETE | `/api/groups/:gid/upstreams/:id` | 改/删上游（**嵌套路径**） |
| POST | `/api/groups/:gid/upstreams/:id/toggle` | `{ enabled }` 手动启停(AC-18) |
| POST | `/api/groups/:gid/upstreams/:id/test` | 测试连接(AC-38) → `{ ok, latencyMs, error? }` |

Group 形态：`{ id,name,remark,type:'A'|'B',healthCheck:{enabled,mode,url,intervalSec,failThreshold,recoverThreshold},todayUp,todayDown,todayReq }`
Upstream 形态：`{ id,host,port,user,usernameTemplate,pwd,weight,enabled,healthState:'healthy'|'unhealthy'|'unknown',latencyMs }`

后端现状(T7)：扁平 `/upstreams/:uid`、`/upstreams/:uid/enabled`、`/tools/test-proxy`(body传ID)；Group 的 HC 字段平铺(hcEnabled...)而非嵌套 healthCheck；Group 无 today* 字段；Upstream healthState 是 bool 而非三态字符串、无 latencyMs。
需改：
- 上游路由改嵌套 `/groups/:gid/upstreams/:id` + `/toggle` + `/test`。
- Group 响应包 `healthCheck` 嵌套对象 + 补 today*（读 store QueryTotals 按 groupId）。
- Upstream 响应 healthState 映射为三态字符串 + latencyMs（latency 取自 health.LatencyMs(id)）。

---

## 4. 规则 rule（AC-22/29/39）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/api/rule-groups` | 规则组列表/新建 |
| PUT/DELETE | `/api/rule-groups/:id` | 改/删 |
| GET/POST | `/api/rule-groups/:rgid/rules` | 规则列表/新建 |
| PUT/DELETE | `/api/rule-groups/:rgid/rules/:id` | 改/删规则 |
| POST | `/api/rule-groups/test` | `{ target, groupId, sniffDomain? }` → `{ matchedRule, fromGroup, action }`(G3) |

RuleGroup 形态：`{ id,name,scope:'global'|'group',groupIds:[],groups:[{id,name}],ruleCount }`
Rule 形态：`{ id, match, action, order }`

后端现状(T7)：`/rulegroups`(无连字符)、`/tools/test-rule`、规则字段 `orderIdx`；规则组应用到分组用 `PUT /groups/:id/rulegroups`；RuleGroup 无 groupIds/groups/ruleCount 聚合字段。
需改：
- 路径加连字符 `/rule-groups`、规则嵌套、`/rule-groups/test`。
- RuleGroup 响应补 groupIds（从 group_rulegroup 反查）+ groups + ruleCount。
- 规则字段 `orderIdx` → `order`（或前端接受 orderIdx，二选一，建议统一 `order`）。
- 规则组↔分组关联入口：前端通过规则组的 groupIds 设置；后端可保留 `PUT /groups/:id/rulegroups` 或改为规则组维度 `PUT /rule-groups/:id/groups`（**待 worker-4 确认前端从哪侧设**）。

---

## 5. 用户 user（AC-23/30）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/api/proxy-users` | 列表/新建 |
| PUT/DELETE | `/api/proxy-users/:id` | 改/删 |
| POST | `/api/proxy-users/:id/groups` | `{ groupIds }` 设置授权 |

ProxyUser 形态：`{ id, username, groupIds:[] }`（密码仅写入不回显）。

后端现状(T7)：`/users`；授权用 `PUT /groups/:id/users`(分组维度)；用户响应无 groupIds。
需改：路径 `/proxy-users`；授权改用户维度 `POST /proxy-users/:id/groups`；列表响应补 groupIds（反查 group_user）。

---

## 6. 系统 system（AC-31/37/40）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/PUT | `/api/settings` | `{ statRetentionDays, hcDefaults:{mode,url,intervalSec,failThreshold,recoverThreshold} }` |
| POST | `/api/settings/admin-password` | `{ oldPassword, newPassword }` |
| GET | `/api/settings/export` | `{ schemaVersion, data:{...} }` |
| POST | `/api/settings/import` | `{ schemaVersion, data, strategy:'overwrite' }` |

后端现状(T7)：改密并入 `PUT /settings`(无 old 校验)；导出裸 bundle(无 data 包一层)；导入 `/tools/import`。
需改：
- 独立 `POST /settings/admin-password`，**校验 oldPassword**（更安全），成功后清会话。
- settings 的 hcDefaults 改嵌套对象（现平铺 hcDefaultMode...）。
- 导出/导入挪到 `/settings/export|import`，导出包 `{schemaVersion, data:{groups,upstreams,...}}`（data 包一层）；导入读 strategy（首版仅 overwrite）。

---

## 7. 系统日志 syslog（AC-33/34/35/36）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/syslog` | `?level=` 缓冲快照 `[{time,level,message,fields}]` |
| GET(SSE) | `/api/syslog/stream` | `?level=` EventSource，每条 `data:` 为日志 JSON |
| GET | `/api/syslog/audit` | `[{time,user,group,target,action,upstream,upBytes,downBytes}]` |

后端现状(T7)：`/logs`、`/logs/stream`、`/audit`。
需改：路径 `/syslog`、`/syslog/stream`、`/syslog/audit`。SSE 已用 Gin c.SSEvent("log",entry)，前端 EventSource 监听 `log` 事件——**需确认前端监听的 event 名**（前端用 onmessage 则需用默认 event，不能命名 "log"）。→ 见「依赖缺口/确认项」。

---

## 依赖缺口与待确认项（需其他 worker 配合）

1. **实时速率 upRate/downRate（KB/s）**：stats.Counter 现有累计字节与 active 连接，无瞬时速率。需 T5(worker-4) 在 Counter 暴露速率（如基于上次采样差/时间窗）。**或** 后端在 dashboard 自存上次采样值算差值（API 层可做，倾向此，免改 stats）。
2. **Top 域名**：✅ 已落地。store 提供 QueryTopDomains（domain_hit 分钟桶），stats.IncDomain 对 CONNECT 目标域名按连接维度埋点（dialAndRelay/handleSniff，纯 IP 也计入），flush worker 同周期落库、按保留期清理。kind=domain 返回 `[{name,count}]`，支持 `?groupId=` 全局/分组查询。
3. **runtime 内存/goroutine**：API 层用 runtime.ReadMemStats + runtime.NumGoroutine 自取即可，无需他人。
4. **uptimeSec**：需进程启动时间，cmd(T9) 装配时传入 App，或 App 内记 NewApp 时间。
5. **SSE event 名对齐**：后端 c.SSEvent("log",...) 发命名事件 `log`；前端 EventSource 若用 onmessage 收的是默认(无名)事件，收不到 `log` 命名事件。需统一——**建议后端发默认事件**（c.Render SSE 不带 event 名）或前端 addEventListener('log',...)。请 worker-4 确认前端监听方式。

---

## 后端改造工作量评估（方向 A）

纯路由重注册 + 响应字段映射(DTO toResp)，业务逻辑(store调用/G3/G4/限流/会话)全部不变。新增少量：dashboard 拆分+速率差值计算+runtime、各列表响应补反查字段(groupIds/ruleCount/today*)。预计 1.5-2 小时，go test 同步更新。
