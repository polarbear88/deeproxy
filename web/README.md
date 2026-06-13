# deeproxy v2 Web 控制台（T8）

Vue3 + Vite + Element Plus + Pinia + vue-router + ECharts，构建产物输出到 `../api/dist`（被 Go `embed` 嵌入单一二进制）。

## 开发 / 构建

```bash
cd web
pnpm install
pnpm dev      # 本地开发，/api 代理到 127.0.0.1:8080（见 vite.config.js）
pnpm build    # 产物输出到 ../api/dist
```

## 目录结构

```
src/
  api/         # 各模块 API 封装（axios 实例 + 业务接口），按后端契约对齐
  components/  # 复用组件：EChart.js（ECharts 封装）、StatCard.vue
  layouts/     # MainLayout.vue（左侧菜单 + 顶栏 + 暗亮切换）
  router/      # 路由 + 守卫（首次设置/登录拦截）
  stores/      # Pinia：theme（暗亮）、user（会话）、app（UI 状态）
  utils/       # format.js（字节/速率/时间格式化，DRY）
  views/       # 各页面：dashboard / proxy / rule / user / system / syslog / auth
```

## 前端推断的 API 契约（待 T7 后端对齐）

> 以下为前端按 spec AC 推断的接口形态，**worker-3(T7) 实现后端时请核对/告知差异**，前端据此调整 `src/api/*`。
> 约定 baseURL=`/api`，会话用 HttpOnly Cookie（`withCredentials`）。响应兼容 `{code,data,msg}` 包裹或裸数据。

### 认证 auth.js
- `GET  /api/auth/init-status` → `{ initialized: bool }`
- `POST /api/auth/setup` `{ username, password }`（AC-19）
- `POST /api/auth/login` `{ username, password }` → set-cookie 会话（AC-20）
- `POST /api/auth/logout`

### 仪表盘 dashboard.js（AC-24）
- `GET /api/dashboard/overview` → `{ upRate,downRate,activeConns,todayUp,todayDown,todayReq,todayRejectRule,todayRejectAuth,healthyProxies,totalProxies,uptimeSec }`
- `GET /api/dashboard/timeseries?window=1h|24h|7d[&groupId]` → `{ times:[], up:[], down:[], req:[] }`
- `GET /api/dashboard/action-dist?window=` → `[{ name:'forward', value }]`
- `GET /api/dashboard/top?kind=group|user|domain&window=&limit=` → group/user: `[{name,bytes}]`，domain: `[{name,count}]`
- `GET /api/dashboard/runtime` → `{ memMB, goroutines, groups:[{id,name,healthy,total,allDown}] }`

### 代理组 group.js（AC-21/28/38，G2）
- `GET/POST /api/groups`，`PUT/DELETE /api/groups/:id`
  - Group: `{ id,name,remark,type:'A'|'B',healthCheck:{enabled,mode:'ping'|'url',url,intervalSec,failThreshold,recoverThreshold},todayUp,todayDown,todayReq }`
- `GET/POST /api/groups/:gid/upstreams`，`PUT/DELETE /api/groups/:gid/upstreams/:id`
  - Upstream: `{ id,host,port,user,usernameTemplate,pwd,weight,enabled,healthState:'healthy'|'unhealthy'|'unknown',latencyMs }`
- `POST /api/groups/:gid/upstreams/:id/toggle` `{ enabled }`（AC-18）
- `POST /api/groups/:gid/upstreams/:id/test` → `{ ok:bool, latencyMs, error? }`（AC-38）

### 规则 rule.js（AC-22/29/39）
- `GET/POST /api/rule-groups`，`PUT/DELETE /api/rule-groups/:id`
  - RuleGroup: `{ id,name,scope:'global'|'group',groupIds:[],groups:[{id,name}],ruleCount }`
- `GET/POST /api/rule-groups/:rgid/rules`，`PUT/DELETE /api/rule-groups/:rgid/rules/:id`
  - Rule: `{ id, match:'domain:x'|'domain-suffix:x'|'ip-cidr:x', action:'forward'|'direct'|'reject', order }`
- `POST /api/rule-groups/test` `{ target, groupId, sniffDomain? }` → `{ matchedRule, fromGroup, action }`（G3 嗅探标注不可模拟）

### 用户 user.js（AC-23/30）
- `GET/POST /api/proxy-users`，`PUT/DELETE /api/proxy-users/:id`
  - ProxyUser: `{ id, username, groupIds:[] }`（密码仅写入，不回显）
- `POST /api/proxy-users/:id/groups` `{ groupIds }`

### 系统 system.js（AC-31/37/40）
- `GET/PUT /api/settings` → `{ statRetentionDays, hcDefaults:{...} }`
- `POST /api/settings/admin-password` `{ oldPassword, newPassword }`
- `GET /api/settings/export` → `{ schemaVersion, data:{...} }`
- `POST /api/settings/import` `{ schemaVersion, data, strategy:'overwrite' }`

### 系统日志 syslog.js（AC-33/34/35/36）
- `GET /api/syslog?level=` → `[{ time, level, message, fields }]`（缓冲快照）
- `GET /api/syslog/stream?level=` → **SSE**，每条 `data:` 为一条日志 JSON
- `GET /api/syslog/audit` → `[{ time,user,group,target,action,upstream,upBytes,downBytes }]`

## 嵌入说明（T9 协同）
- `pnpm build` 输出到 `api/dist`；`vite.config.js` 设 `emptyOutDir:true` 会清空该目录后再写。
- 计划 M2 要求保留占位以保证未构建时 `//go:embed dist/*` 可编译——`api/dist/.gitkeep` 与构建出的 `index.html` 充当占位。注意每次 `pnpm build` 会重写产物；CI 需先 `pnpm build` 再 `go build`。
