# Open Questions

## ralplan-i18n-daemon-ui - 2026-06-14（共识修订后更新）
- [ ] 组件⑦编辑旧记录策略「仅当 name/username 实际变更才校验」是否产品认可 — spec 只说「不强制迁移」，未明说编辑旧含特殊字符记录能否原样保存其他字段（R-7，AC-7.4）。**唯一阻塞性开放项**。
- [ ] R-6 kardianos 确认性 spike（confirm-not-discover）：实现首步跑通 `WorkingDirectory`/Windows `Option["StartType"]`/`Status→Stop→poll→Uninstall→Install→enable/disable→Start` 均生效；个别失效切 A2（手写 systemd unit + sc.exe）。**非 load-bearing 未知，非阻塞**。
- [x] ~~`--startup`/boot-enable 机制~~ — **已提交（R-5）**：Windows `Config.Option["StartType"]="automatic"/"manual"`；Linux `Install()` 后 raw `systemctl enable/disable`。systemd 无 kardianos StartType Option（删除错误说法）。
- [x] ~~vue-i18n 版本~~ — **已提交 ^9**（Vue3 LTS，R-10/AC-1.1）。
- [x] ~~Users.vue 表单结构未读取~~ — **已读取**：表单 `:205` 无 ref/rules、username 输入 `:206-207`（编辑态 disabled）、`save :65`；step 28 给出 `userFormRef`+`userRules` 具体落点（AC-7.2）。

## ralplan-deeproxy-v2 - 2026-06-13（Architect + Critic REVISE 后更新）
- [x] ~~D0：go-socks5 鉴权对象跨阶段传递机制~~ — **已定稿 D0-0**：源码证实 `AuthContext.Payload` 含 user+password 且三阶段同 goroutine/同连接/不跨连接共享；沿用 v1 context(`decisionKey`) 机制，零并发风险。原 D0-A(sync.Map 待取表)已删除（解决伪问题）。
- [x] ~~统计聚合桶时间粒度~~ — **已定稿（M3）**：单一分钟桶（group/user 维度）为唯一存储粒度；7d 视图查询期 `GROUP BY strftime('%Y-%m-%d %H',...)` 降采样。基数预算约 86.4 万行/30 天/20 活跃组合（AC-12/13/24）。
- [x] ~~转发延迟回归阈值~~ — **已定稿（M4）**：p99 回归 >10% 软告警、>25% 硬阻断；固定可复现环境（回环 echo、固定并发/连接数、统一 warmup）（AC-43）。
- [x] ~~系统日志实时推送传输~~ — **已定稿**：默认 Gin SSE（单向更轻、无需 gorilla/websocket）；仅确需双向交互时引入 WebSocket（AC-33/34/35）。
- [x] ~~健康检查对 Type A 组适用性~~ — **已定稿（G2）**：Type A 池为空，health worker 跳过；前端 Type A 隐藏健康检查 UI（AC-15/28）。
- [ ] 管理员会话是否需重启保活 — 当前定稿内存会话（重启需重登）。若运维要求重启不掉线，升级为 SQLite sessions 表（D2-A 变体，AC-20）。**非阻塞**。
- [ ] G4 配置导入冲突策略 — 首版用「整体覆盖 + 导入前备份当前配置」，是否满足运维迁移预期需确认；`schemaVersion` 版本化 + Rebuild 失败不 Swap 回滚已定（AC-37/44）。**非阻塞**。
- [ ] G5 登录限流参数 — 暂定 5 次失败锁定 5 分钟、bcrypt `DefaultCost`，上线前可按安全要求调整。**非阻塞**。

## ralplan-7-fixes-ui-relay - 2026-06-15
- [x] #4 虚拟滚动：已定为 `vue-virtual-scroller` `DynamicScroller`（原生可变行高）为主选并新增依赖（评审 B2 确认日志行非定高，自研定高方案否决）。无悬而未决项。
- [ ] #1 字体：规则抽屉内「规则明细表」是否也需放大到 16px？ — 访谈仅说「规则列表」，默认只放大规则组列表表；若需含明细表请确认。
