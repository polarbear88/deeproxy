# T4 + T7 AC 证据清单（worker-3 自测，供 QA_REPORT.md 引用）

> 本文件是 worker-3 对自己负责的 T4(rule 多对多合并) 与 T7(管理后端 API) 的 AC 自测证据，
> **供 worker-2 在 T11 生成权威 QA_REPORT.md 时引用**。本文件不替代 QA_REPORT.md（46 条 AC 终验 +
> E2E + 性能 benchmark 仍由 T11 owner worker-2 统一产出）。
>
> 复跑命令：`go build ./... && go vet ./api/ ./rule/ && go test ./api/ ./rule/ -count=1 -race`
> 最近一次结果：go build ./... EXIT=0；go vet 干净；go test ./rule/ ./api/(含 -race) 全绿。

## T4 — rule 多对多合并（AC-7）

| AC | 验证点 | 证据（测试函数） | 状态 |
|----|--------|------------------|------|
| AC-7 | 全局组优先于分组组 | `rule.TestBuildGroupEngine_GlobalBeforeGroup` | ✅ |
| AC-7 | 合并顺序(全局→分组、组内书写序) | `rule.TestMergeRuleGroups_Order` | ✅ |
| AC-7 | 三类匹配真值表(domain/domain-suffix/ip-cidr) | `rule.TestBuildGroupEngine_TruthTable` | ✅ |
| AC-7 | 默认动作兜底 | `rule.TestBuildGroupEngine_DefaultFallback` | ✅ |
| AC-7/G4 | 非法规则预编译被拒(回滚前提) | `rule.TestNewMergedEngine_InvalidRuleRejected` | ✅ |
| AC-7 | 空规则组集合 | `rule.TestMergeRuleGroups_Empty` | ✅ |
| — | 集成：每 group 预编译 *Engine 挂 Snapshot | `snapshot.TestGlobalBeforeGroupOrder` / `TestRebuildMaterializesViews` | ✅ |

## T7 — 管理后端 API（AC-19~24,33,34,37~41 + G3/G4/G5）

| AC | 验证点 | 证据（测试函数 / 端点） | 状态 |
|----|--------|--------------------------|------|
| AC-19 | 首次设置引导 + 已配置拒重复 | `api.TestSetupStatusAndFirstSetup`（GET /auth/init-status, POST /auth/setup） | ✅ |
| AC-20 | 登录签发会话 + 受保护接口需会话 + 登出失效 | `api.TestLoginAndSession` | ✅ |
| AC-40/G5 | bcrypt + 登录失败限流(5次锁5分钟→429) | `api.TestLoginRateLimit` | ✅ |
| AC-21/22 | 分组/规则组/规则 CRUD + 写后快照热替换 | `api.TestGroupAndRuleCRUD` | ✅ |
| AC-44/G4 | 非法规则→Rebuild失败→不Swap→500回滚 | `api.TestBadRuleRollback` | ✅ |
| AC-37/G4 | 配置导入导出回环 + schemaVersion 校验 + 导入前备份 | `api.TestImportExportRoundTrip`（GET/POST /settings/export\|import） | ✅ |
| AC-38 | 代理测试连接(嵌套路由，mock 探测) | `api.TestTestUpstream`（POST /groups/:id/upstreams/:uid/test） | ✅ |
| AC-24 | 仪表盘聚合(实时内存 + 今日SQLite) | `api.TestDashboard`（GET /dashboard/overview） | ✅ |
| AC-23/30 | 代理用户 CRUD + 用户维度授权 | 端点 /proxy-users{,/:id/groups}（CRUD 经 TestGroupAndRuleCRUD 同链路覆盖；授权覆盖式 store.SetUserGroups 单测于 store） | ✅(端点) |
| AC-31 | 系统设置读写 + 改密(校验旧密码+清会话) | 端点 GET/PUT /settings, POST /settings/admin-password | ✅(实现) |
| AC-33/34 | 系统日志快照+级别筛选 + SSE 实时(默认事件) | 端点 GET /syslog, /syslog/stream（SSE 默认事件已对齐前端 onmessage） | ✅(实现) |
| AC-36 | 连接审计快照 | 端点 GET /syslog/audit | ✅(实现) |
| AC-39/G3 | 规则测试器(直接匹配 + IP未命中标注嗅探不可模拟) | 端点 POST /rule-groups/test | ✅(实现) |
| AC-41 | 后台端口独立默认 0.0.0.0 | cfg.AdminListen 默认 0.0.0.0:8080；App.Run() 绑定 | ✅(实现) |

## 已知首版占位（team-lead 已批准，记为后续项，不阻塞）

- `GET /dashboard/top?kind=user|domain` → 返回 `[]` + 响应头 `X-Feature-Status: not-implemented`。
  原因：store 仅有 QueryTopGroups；Top 用户需 QueryTopUsers，Top 域名需对 CONNECT 目标域名埋点（属 stats/server 范畴）。`kind=group` 完整可用。
- 规则测试器 `matchedRule` 为「target → action」概述串：rule.Engine 当前不暴露命中规则原始表达式文本。

## 标注说明

- "✅"=有自动化测试断言；"✅(实现)"=端点已实现且经手工/集成链路覆盖，但未单列独立断言测试（worker-2 T11 E2E 可补端到端断言）。
- 响应壳：成功=裸数据(或 {ok:true})；错误={msg}；不包壳（team-lead 拍板，见 api/CONTRACT.md）。
- 前后端联调：worker-4 已用 live smoke harness 逐端点验证 shape 匹配通过（init-status→setup→login→dashboard→export→test-rule）。
