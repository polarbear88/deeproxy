# Deep Interview Spec: deeproxy — SOCKS5 中继转发工具

## Metadata
- Interview ID: deeproxy-socks5-relay
- Rounds: 7
- Final Ambiguity Score: 15.8%
- Type: greenfield
- Generated: 2026-06-12
- Threshold: 0.2 (20%)
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED
- Primary Deliverable: 项目根目录 CLAUDE.md（用户显式要求）

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.92 | 0.40 | 0.368 |
| Constraint Clarity | 0.78 | 0.30 | 0.234 |
| Success Criteria | 0.80 | 0.30 | 0.240 |
| **Total Clarity** | | | **0.842** |
| **Ambiguity** | | | **0.158** |

## Topology
| Component | Status | Description | Coverage |
|-----------|--------|-------------|----------|
| ① 本地 SOCKS5 服务端 | active | 监听、SOCKS5 握手、强制用户名/密码认证、接收 CONNECT | 编码契约 + 仅 CONNECT 约束已定 |
| ② 上游代理中继 | active | 用解码出的动态上游建立 SOCKS5 连接并双向中继 | forward 动作 + 上游认证已定 |
| ③ 目标探测 | active | 读取 CONNECT 目标地址（域名/IP+端口），不做 SNI/GeoIP | 范围收敛为“只读地址” |
| ④ 规则引擎 | active | 顺序首匹配，动作 forward/direct/reject，默认 forward | 匹配维度 + 默认动作真值表已定 |
| ⑤ 配置系统 | active | 加载/校验 YAML（监听、默认动作、规则、日志） | 配置结构已定；上游不入配置 |

## Goal
实现一个 Go 跨平台 SOCKS5 中继：本地提供强制认证的 SOCKS5 服务，从客户端 SOCKS5 用户名字段 base64 解码出“本连接动态上游”（user:pwd@host:port），读取 CONNECT 目标地址后由规则引擎判定 forward（走该上游）/ direct（本机直连）/ reject，默认动作 forward。

## Constraints
- 仅 SOCKS5 CONNECT(TCP)；BIND / UDP ASSOCIATE 不支持（回复命令不支持）。
- 目标探测仅读 CONNECT 地址；不做 SNI 嗅探 / HTTP Host / GeoIP（前提客户端用 socks5h 远程 DNS）。
- 本地服务强制用户名/密码认证；无有效编码用户名一律拒绝。
- 跨平台 Windows/macOS/Linux，单一静态二进制，建议 amd64+arm64。
- CONNECT 目标支持 域名 / IPv4 / IPv6。

## Non-Goals
- UDP ASSOCIATE / BIND。
- IP→域名还原（SNI/Host 嗅探）、GeoIP 分流。
- 配置文件静态上游、多上游负载均衡/故障切换。
- Web/GUI 管理界面。
- 规则热更新 / 远程规则订阅。

## Acceptance Criteria
- [ ] 用户名 base64("user:pwd@host:port") 正确解码并解析上游 {host,port,user,pwd}。
- [ ] 用户名缺失 / 非法 base64 / 格式不符 → 拒绝连接。
- [ ] 仅 CONNECT 被接受；BIND / UDP ASSOCIATE 拒绝且回复正确 reply code。
- [ ] 顺序首匹配 + 默认动作真值表正确（domain / domain-suffix / ip-cidr 三类覆盖）。
- [ ] forward：经动态上游正确建立连接并双向中继（含上游需认证）。
- [ ] direct：本机直连目标并双向中继。
- [ ] reject：连接被拒绝。
- [ ] 配置缺失/非法时清晰报错或用文档化默认值。
- [ ] 三平台均能构建并通过端到端测试（CI 矩阵）。

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| “探测域名”需深度解析 | SOCKS5 请求本身已含目标地址；socks5h 时即为域名 | 仅读 CONNECT 地址，不做 SNI/GeoIP |
| “skip 跳过上游”含义不明 | skip 是本机直连还是换下一个上游？ | skip = 本机直连目标（direct） |
| “配置好的上游”=单个固定上游 | Contrarian：若上游是多个/动态？ | 上游由客户端 base64 用户名“每连接动态”携带，不入配置 |
| 是否需要 UDP | UDP ASSOCIATE 会改变架构 | 首版仅 TCP CONNECT |
| 默认动作未定 | 不命中规则怎么办？ | 默认 forward（走上游），可配置 |

## Technical Context (greenfield)
- SOCKS5 服务端：things-go/go-socks5 或 armon/go-socks5（需自定义认证回调 + 每连接 Dial）。
- 上游 SOCKS5 客户端：golang.org/x/net/proxy（带认证 SOCKS5 dialer）。
- 配置：gopkg.in/yaml.v3；日志：log/slog；发布：go 交叉编译 / 可选 goreleaser。

## Ontology (Key Entities)
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| Client | external system | - | 经 SOCKS5 连接本地服务，用户名携带上游 |
| 本地 SOCKS5 服务 | core domain | listen, default_action, rules | 解码用户名、应用规则、中继 |
| 动态上游 (Upstream) | core domain | host, port, user, pwd | 由编码用户名解出；forward 时使用 |
| 目标地址 (Target) | core domain | hostOrIP, port | 来自 CONNECT；规则匹配输入 |
| 规则 (Rule) | core domain | match(domain/domain-suffix/ip-cidr), action | 顺序首匹配 → 动作 |
| 编码用户名 | supporting | base64(user:pwd@host:port) | 解码失败→拒绝 |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 4 | 4 | - | - | N/A |
| 2 | 5 | 1 | 0 | 4 | 80% |
| 3 | 5 | 0 | 0 | 5 | 100% |
| 4 | 6 | 1 | 0 | 5 | 83% |
| 5 | 6 | 0 | 0 | 6 | 100% |
| 6 | 6 | 0 | 0 | 6 | 100% |
| 7 | 6 | 0 | 0 | 6 | 100% |

## Interview Transcript
<details>
<summary>Full Q&A (7 rounds + Round 0)</summary>

### Round 0 — Topology
**Q:** 5 个顶层组件拓扑是否正确？
**A:** 拓扑正确，5 个都要。

### Round 1 — 规则引擎 / Goal
**Q:** “跳过上游(skip)”是什么行为？
**A:** skip = 本机直连目标。 Ambiguity: 65.5%

### Round 2 — 目标探测 / Goal
**Q:** “自动探测目标域名/主机”到什么程度？（澄清：socks5h→域名，本地DNS→IP）
**A:** 只读目标地址（假设 socks5h）。 Ambiguity: 55.3%

### Round 3 — 协议范围 / Constraints
**Q:** 支持哪些 SOCKS5 命令/传输类型？
**A:** 只做 TCP（CONNECT）。 Ambiguity: 48.5%

### Round 4 — Contrarian：上游中继 / Goal
**Q:** 上游单个还是多个？（用户中途纠正：首版用户名编码携带上游）首版是否保留域名探测+规则？
**A:** 用户名=base64(user:pwd@host:port) 动态上游；首版动态上游 + 规则都要。 Ambiguity: 43.5%

### Round 5 — 规则引擎+配置 / Criteria
**Q:** 规则怎么配置和匹配？默认动作？
**A:** 配置文件 + 后缀/CIDR + 默认动作（Clash 风格）。 Ambiguity: 30.3%

### Round 6 — Simplifier：本地服务+中继 / Constraint
**Q:** 用户名编码格式与解码失败行为？
**A:** 用户名=base64(user:pwd@host:port)，失败则拒。 Ambiguity: 21.4%

### Round 7 — 规则引擎 / Goal
**Q:** 规则都不命中时的默认动作？
**A:** 默认 forward（走上游），可配置。 Ambiguity: 15.8% ✅

</details>
