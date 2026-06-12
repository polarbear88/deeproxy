# RALPLAN — deeproxy v1 实现计划（consensus v2，已纳入 Architect + Critic 反馈）

> 状态：**pending approval**（非交互 consensus，仅产出计划，不自动执行）
> 模式：SHORT
> 共识迭代：v1（Planner 草案）→ Architect APPROVE_WITH_CHANGES → Critic REJECT（4C+3M）→ **v2 修订（本文件）**
> 落盘路径：`.omc/plans/ralplan-deeproxy-v1.md`

---

## 0. 共识修订记录（changelog vs v1）

| 编号 | 来源 | v1 问题 | v2 修订 |
|------|------|---------|---------|
| C1/C2 | Architect+Critic（源码证据 handle.go:73-81 / 120-148） | 首选在 `WithDialAndRequest` 内 reject，库只会回 `RepHostUnreachable`，**永不回 `RepRuleFailure`** | **reject 与非 CONNECT 改走 `WithRule.Allow`**（库回 `RepRuleFailure` 0x02）；forward/direct 走 `WithDialAndRequest`；判定结果用 `context.WithValue` 单次传递 |
| C3 | Critic | AC7 仅"被拒绝"，弱到错误实现也能通过 | AC7 断言 `REP=0x02`，与上游不可达 `0x04` 显式区分 |
| C4 | Architect+Critic（handle.go FQDN 先解析） | 库在 dial 前对 FQDN 本地解析 → DNS 泄漏 + ip-cidr 对域名目标误命中；AC4 未定义交叉行为 | 注入自定义 `Resolver`：**forward 路径不本地解析**（域名交上游）；定义"域名目标 vs ip-cidr 规则"语义；AC4 补交叉用例；集成断言中继机未对目标域名发起 DNS |
| M1 | Critic | AC5 笼统 | 拆为 AC5a（上游认证）/ AC5b（上游不可达且无泄漏） |
| M2 | Architect+Critic（Proxy 无 SetDeadline） | 半开连接 idle 泄漏，测试无覆盖 | 首版实现 idle 超时 conn 包装 + 测试；详见 Step 5/AC10 |
| M3 | Critic（handleAssociate 主动建 UDP） | AC3 弱，ASSOCIATE 危险性低估 | AC3 断言 BIND/ASSOCIATE 均回 0x02，且 ASSOCIATE 不新增 UDP 监听 |
| minor | Critic | 编码用户名长度未校验（SOCKS5 用户名上限 255 字节） | `EncodeUpstream` 文档化 ≤255 字节约束；解码端容忍 |

---

## 1. Requirements Summary（需求摘要）

构建 Go 编写、跨平台（Windows/macOS/Linux）的 **SOCKS5 中继转发工具** `deeproxy`。每条客户端连接：

1. 客户端用 SOCKS5 用户名/密码认证（RFC1929）连入；**用户名 = `base64("user:pwd@host:port")`**，承载"本连接动态上游 SOCKS5 代理"。**密码字段忽略**（无语义，仅占位）。
2. 服务端 base64 解码用户名并解析为上游 `U={host,port,user,pwd}`；**缺失/解码失败/格式非法/超长 → 拒绝连接**。
3. 仅支持 `CONNECT`(TCP)；`BIND`/`UDP ASSOCIATE` → 回 `RepRuleFailure` 拒绝（经 `WithRule.Allow`，不可依赖默认 handler——ASSOCIATE 默认会真的建 UDP relay）。
4. 目标探测：直接读 CONNECT 请求里的目标地址（域名/IPv4/IPv6 + 端口），无 SNI 嗅探、无 GeoIP。
5. 规则引擎：按书写顺序首匹配；维度 `domain`（精确）/`domain-suffix`（后缀）/`ip-cidr`；动作 `forward`/`direct`/`reject`；不命中走默认动作（默认 `forward`，可配置）。
6. 动作执行：`forward`=经动态上游 U 建 SOCKS5 连接双向中继；`direct`=本机直连双向中继；`reject`=经 Rule 阶段回 `RepRuleFailure`。

**技术选型（已锁定，源码级验证）**：Go 1.26.3；服务端 `github.com/things-go/go-socks5`；上游客户端 `golang.org/x/net/proxy`；配置 `gopkg.in/yaml.v3`；日志 `log/slog`。空仓需 `go mod init`。

**非目标**：UDP/BIND、SNI/Host 还原、GeoIP、配置静态上游、负载均衡、Web/GUI、规则热更新。

---

## 2. RALPLAN-DR 摘要

### Principles（原则）
- **P1 库优先、零造轮子**——但仅限*非核心通用*逻辑（握手/认证子协商/中继 io.Copy）。**回复码语义是产品核心控制点，不让渡给库的默认行为。**
- **P2 单一职责 + DRY**——地址解析、上游编解码、规则匹配各自纯函数可单测。
- **P3 失败显式化**——拒绝路径必须回**语义正确**的 SOCKS5 reply code（策略拒绝=0x02，网络不可达=0x04），不静默、不混淆。
- **P4 跨平台纯 Go**——无 cgo、无平台特定 syscall。
- **P5 可测试性 + 可观测性**——核心纯函数与 IO 解耦；中继可设超时、可记录上下行字节数。

### Decision Drivers（top 3）
- **D1 拒绝语义正确性**：reject 必须回 `RepRuleFailure`（0x02），与网络不可达（0x04）可区分——这是核心验收。
- **D2 每连接动态上游的安全传递**：认证阶段解码的上游，必须无歧义地传到拨号阶段（避免双解析分叉）。
- **D3 库行为与产品意图的对齐**：go-socks5 把"策略判定(Rule)"与"拨号执行(Dial hook)"分两阶段；设计须顺应而非对抗。

### 决策 A：CONNECT 处理与拒绝路径（**v2 重做**）

| 选项 | 方案 | Pros | Cons | 裁决 |
|------|------|------|------|------|
| A1 | 全部逻辑在 `WithDialAndRequest`，reject 返回 error | 样板最少 | **reject 回 0x04 而非 0x02（语义错误，源码 handle.go:120-148 证实）**；ASSOCIATE 不被拦 | ❌ 否决（违反 D1/D3） |
| A2 | 全部接管 `WithConnectHandle`，自写 SendReply + io.Copy | 回复码与超时全可控 | 30+ 行中继样板，half-close 要自己写对（违反 P1） | ❌ 否决（过度造轮子） |
| **A3** | **Rule 阶段判定：reject/非 CONNECT → `Allow` 返回 false（库回 0x02）；forward/direct → `Allow` 返回 true 并把动作塞进 ctx → `WithDialAndRequest` 据 ctx 选择拨号** | reject 精确 0x02、零中继样板（库做 io.Copy+half-close）、规则只跑一次、ASSOCIATE 被拦 | 依赖 `context.WithValue` 传值约定；建连后纯 idle 超时需另解（见 M2/Step5） | ✅ **采纳** |

> A3 证据：`handleRequest`（handle.go:73-81）在任何 dial hook **之前**执行 `rules.Allow`，false → 回 `RepRuleFailure`。这是唯一回 0x02 的路径。`WithDialAndRequest`（option.go:91-96）能从 ctx 取出 Allow 阶段写入的动作。

### 决策 B：用户名解码位置（**v2 微调**）
- **B1' 采纳**：`CredentialStore.Valid` 阶段调 `auth.DecodeUpstream` 做**格式校验+早拒绝**（失败 return false → 库拒连）；`WithRule.Allow` / `WithDialAndRequest` 阶段从 `request.AuthContext.Payload["username"]` 取**原始 username 串**再解码取上游。
- 共用 `auth` 包同一纯函数（DRY）。文档明确：go-socks5 不改写 username 字节（Payload 存的是认证时收到的原串，键名 `"username"` 已源码确认），故两次解析结果一致；并标注 `password` 字段无语义。
- 否决 B2（缓存）：`Valid` 只回 bool 无法写回 Payload，外挂 map+锁+生命周期复杂度不值。

### 决策 C：DNS 解析策略（**v2 新增，应对 C4**）
- 证据：go-socks5 `handleRequest` 在命令分发前对 `DestAddr.FQDN` 调 `resolver.Resolve()`（默认 `DNSResolver` 用本机系统 DNS），写回 `DestAddr.IP`。后果：(a) **DNS 泄漏**——解析在中继机而非上游；(b) ip-cidr 规则对域名目标会拿到被解析的 IP。
- **方案**：注入自定义 `socks5.NameResolver`，其 `Resolve` **不做实际解析**（返回 `nil` IP 或原样透传 ctx），使库跳过本地 DNS。规则引擎只用**原始 FQDN 或字面 IP** 判定：
  - 目标是域名 → 只匹配 `domain`/`domain-suffix`，**不匹配任何 ip-cidr**（域名不参与 CIDR 判定）。
  - 目标是 IP 字面量 → 只匹配 `ip-cidr`（及理论上等值的 domain，但通常不会）。
  - forward 路径：把**域名原样**交给上游 SOCKS5（`x/net/proxy` 的 SOCKS5 dialer 支持域名地址，由上游解析）→ 无本地 DNS 泄漏。
  - direct 路径：`net.Dial("tcp", "域名:端口")` 由本机解析（direct 本就是本地出网，解析在本地合理）。

---

## 3. Acceptance Criteria（验收标准，逐条可测）

- **AC1 上游解码**：`auth.DecodeUpstream(base64("user:pwd@host:port"))` → `Upstream{Host,Port,User,Pwd}`。覆盖 IPv4 host、域名 host、IPv6 host（`[::1]:888`）、user/pwd 含特殊字符（非冒号非@）。
- **AC2 解码失败拒绝**：空 / 非法 base64 / 缺 `@` / 缺 `:` / 端口非数字或越界 / 编码后用户名 >255 字节 → `DecodeUpstream` 返回 error 且 `Credential.Valid` 返回 false（端到端：认证失败、连接被拒）。
- **AC3 命令过滤（强化）**：`CONNECT` 放行；`BIND` 与 `UDP ASSOCIATE` 经 `WithRule.Allow` 拒绝，断言客户端收到 **`REP=0x02 (RepRuleFailure)`**；额外断言发起 ASSOCIATE 后进程**未新增 UDP 监听端口**。
- **AC4 规则真值表（含交叉用例）**：`rule.Engine.Match(host)` 返回动作符合下表；新增"域名目标 × ip-cidr 规则 = 不命中"与"IP 目标 × domain 规则 = 不命中"交叉项。

  | 目标 | 规则集 | 期望 |
  |---|---|---|
  | `www.google.com` | suffix:google.com→forward | forward |
  | `google.com` | suffix:google.com→forward | forward（后缀含自身） |
  | `notgoogle.com` | suffix:google.com→forward | 默认（不误命中） |
  | `ads.example.com` | domain:ads.example.com→reject | reject |
  | `203.0.113.5` | ip-cidr:203.0.113.0/24→direct | direct |
  | `203.0.114.5` | ip-cidr:203.0.113.0/24→direct | 默认 |
  | `example.com`（域名） | ip-cidr:0.0.0.0/0→direct | **不命中 ip-cidr → 默认**（交叉用例） |
  | `1.2.3.4`（IP） | domain:1.2.3.4→reject | **不命中 domain → 默认**（交叉用例） |
  | `unknown.site.com` | 无 | 默认 forward |

- **AC5a forward + 上游认证**：动作 forward 时经动态上游建 SOCKS5 连接双向中继；假上游校验固定凭据，**正确凭据成功**、**错误凭据导致 forward 失败**且回复码合理。
- **AC5b forward + 上游不可达**：上游地址不可达时，dial 失败、客户端连接被清理、回复码为 `0x04/0x05`（HostUnreachable/ConnectionRefused），且**无 goroutine/fd 泄漏**（测试前后 goroutine 数回落）。
- **AC6 direct**：动作 direct 时本机直连目标双向中继，假上游计数不变。
- **AC7 reject（强化）**：动作 reject 时客户端收到 **`REP=0x02 (RepRuleFailure)`**，明确区别于 AC5b 的 `0x04`。
- **AC8 配置校验**：缺省补默认（`listen=127.0.0.1:1080`、`default_action=forward`、`log_level=info`）；非法 `default_action`（非三枚举）、未知 `match` 前缀、空 `listen` → 启动失败 + 清晰中文错误。
- **AC9 跨平台构建**：`GOOS∈{windows,darwin,linux}`×`GOARCH∈{amd64,arm64}` 均 `go build` 成功；端到端三路径在本机通过。
- **AC10 DNS 不泄漏 + idle 超时**：(a) forward 路径下，中继机**未**对目标域名发起本地 DNS 查询（注入计数型 resolver 断言调用次数为 0，或断言上游收到的是域名）；(b) 建立的连接在双向空闲超过配置 idle 超时后被回收（断言连接关闭、无泄漏）。

---

## 4. Implementation Steps（按文件/模块）

```
deeproxy/
├── go.mod
├── config.yaml                  # 示例配置
├── cmd/deeproxy/main.go         # 入口：flag→config.Load→rule.NewEngine→logging→server.New→ListenAndServe
├── config/
│   ├── config.go                # Config 结构 + Load + 校验 + 默认值
│   └── config_test.go
├── auth/
│   ├── upstream.go              # Upstream + DecodeUpstream/EncodeUpstream/Addr（纯函数）
│   ├── credential.go            # Credential 实现 CredentialStore.Valid（校验+早拒绝）
│   └── upstream_test.go
├── rule/
│   ├── rule.go                  # RuleSpec 解析→内部 Rule{Kind,Pattern,Action}；CIDR 预编译
│   ├── engine.go                # Engine.Match：IP/域名分流 + 顺序首匹配 + 默认动作
│   └── engine_test.go
├── dialer/                      # （v1 的 relay 改名，避免“relay 却不 copy”误导）
│   ├── dialer.go                # DialDirect / DialUpstream（仅造 conn，io.Copy 由库做）
│   └── idleconn.go              # idleConn 包装：滚动 SetReadDeadline 实现 idle 超时（M2）
├── server/
│   ├── server.go                # 装配 go-socks5：Credential / connectRule / dialAndRequest / NopResolver / Logger
│   └── ctxkey.go                # context key（Allow→hook 传递动作判定）
└── internal/logging/logging.go  # slog 初始化（按 log_level）
```

### Step 1 — 模块与依赖
- `go mod init <module>`（模块名见 Open Questions）；`go get` 三依赖；`go mod tidy`；`go build ./...` 通过。

### Step 2 — `auth` 包（核心契约，TDD 先行）
- `upstream.go`：`DecodeUpstream`（base64 StdEncoding 解码 → `strings.LastIndex(@)` 切 cred/hostport → cred 首个 `:` 切 user/pwd → `net.SplitHostPort` 处理 hostport（含 IPv6）→ 端口 1-65535 校验）；`EncodeUpstream`（反向，文档化 ≤255 字节）；`Addr()`（`net.JoinHostPort`）。
- `credential.go`：`Credential{}` 的 `Valid(user,password,userAddr) bool` 调 `DecodeUpstream(user)`，失败 false。
- 测试：AC1/AC2 真值表。

### Step 3 — `config` 包
- `Config{Listen,DefaultAction,LogLevel,IdleTimeoutSec,Rules[]RuleSpec}`；`Load(path)` 读 YAML + 默认值 + 校验枚举/前缀；中文错误。测试 AC8。

### Step 4 — `rule` 包（核心逻辑，TDD）
- `NewEngine(specs, defaultAction)`（预编译 ip-cidr 为 `*net.IPNet`，未知前缀报错）。
- `Match(host)`：先 `net.ParseIP(host)` 判 IP/域名；域名→domain/domain-suffix；IP→ip-cidr；首匹配；不命中→默认。`domain-suffix:p` 命中 `host==p || strings.HasSuffix(host,"."+p)`。测试 AC4（含交叉用例）。

### Step 5 — `dialer` 包
- `DialDirect(ctx,addr)`：`(&net.Dialer{}).DialContext`（带超时）。
- `DialUpstream(ctx,up,addr)`：`proxy.SOCKS5("tcp",up.Addr(),&proxy.Auth{User,Password},proxy.Direct)`（user 空则 auth=nil）→ 断言 `proxy.ContextDialer` → `DialContext(ctx,"tcp",addr)`（**addr 可为域名，交上游解析**）。
- `idleconn.go`：`idleConn` 包装 `net.Conn`，每次 Read/Write 滚动 `SetReadDeadline(now+idle)`，实现 idle 超时（应对 M2 库中继无 deadline）。hook 返回的 conn 用它包装。测试 AC10(b)。

### Step 6 — `server` 包（集成点，A3 三件套）
- `ctxkey.go`：`type actionKey struct{}`。
- `connectRule.Allow(ctx,req)`：① `req.Command!=CommandConnect` → `(ctx,false)`（库回 0x02，拦 BIND/ASSOCIATE，应对 AC3/M3）；② 解码 `req.AuthContext.Payload["username"]` 取上游、取 `req.DestAddr` 的 host → `engine.Match(host)`；③ 动作 `reject` → `(ctx,false)`（库回 0x02，应对 AC7）；④ forward/direct → `context.WithValue(ctx, actionKey{}, 判定结果{action,upstream,host})` 返回 `(newCtx,true)`。
- `dialAndRequest(ctx,network,addr,req)`：从 ctx 取判定（**规则只跑一次**）；forward→`DialUpstream` 包 `idleConn`；direct→`DialDirect` 包 `idleConn`；记录 slog（user/目标/动作/上游/字节数可观测）。
- `NopResolver.Resolve` 返回 `(ctx,nil,nil)` 跳过本地 DNS（应对 C4/AC10a）。
- `New(cfg,engine,logger)` 用 `WithCredential/WithRule/WithDialAndRequest/WithResolver/WithLogger` 装配。

### Step 7 — `internal/logging` + `cmd/deeproxy/main.go`
- `logging.New(level)`；`main.go` flag `-c`（默认 `./config.yaml`）→ Load→NewEngine→New→ListenAndServe，失败中文报错 `os.Exit(1)`。`config.yaml` 示例（同 CLAUDE.md 第六节 + `idle_timeout_sec`）。`go build ./... && go vet ./...` 干净。

### Step 0（前置冒烟，30 行，写核心前必做）
锁死库行为分界：① `Allow` 返回 false → curl 实测客户端收 `0x02`；② `WithDialAndRequest` 返回 error → 收 `0x04`；③ CONNECT 带域名 + `NopResolver` 时库是否跳过本地 DNS（验证 C4 解法生效）。

---

## 5. Risks and Mitigations

| 风险 | 缓解 |
|---|---|
| R1 库 Payload 键名/认证传递 | 已源码确认键名 `"username"`；Step 0 冒烟再实测 |
| R2 reject 回复码语义（**已消除**，非"接受"） | A3：reject 走 `WithRule.Allow` → 库回 `RepRuleFailure` 0x02（源码 handle.go:77）|
| R3 IPv6 host/port 切分 | 统一 `net.SplitHostPort`/`JoinHostPort`；IPv6 进单测 |
| R4 domain-suffix 误命中 | `host==p || HasSuffix(host,"."+p)`；边界单测 |
| R5 上游不可达/认证失败泄漏 | DialUpstream 带 ctx 超时；失败返回 error 让库关连接；AC5b 断言无泄漏 |
| R6 跨平台 | 全纯 Go 无 cgo；CI 矩阵三平台双架构 |
| R7 配置错误静默 | 严格校验 + 启动即失败 + 中文错误 |
| R8 DNS 本地解析泄漏（C4） | NopResolver 跳过；forward 把域名交上游；AC10a 断言零本地 DNS |
| R9 库中继无 idle 超时半开泄漏（M2） | idleConn 滚动 SetReadDeadline；AC10b 断言空闲回收 |
| R10 编码用户名超 255 字节 | EncodeUpstream 文档化上限；DecodeUpstream 对超长返回 error |

---

## 6. Verification Steps

### 本地手动（darwin/arm64）
1. 起假上游：用 go-socks5 起"直连出口"server 监听 `127.0.0.1:11080`（可选 `WithCredential` 固定账号验 forward 认证）。
2. 起 deeproxy：`./deeproxy -c config.yaml`（listen `127.0.0.1:1080`）。
3. `USER=$(printf 'uu:pp@127.0.0.1:11080' | base64)`。
4. **forward**：`curl -v --socks5-hostname --proxy-user "$USER:x" -x 127.0.0.1:1080 https://example.com`（`--socks5-hostname` 发域名；`--proxy-user` 首个冒号切分，base64 无冒号故完整送达）。
5. **direct**：规则 `domain-suffix:example.com→direct`，重测不经上游。
6. **reject**：规则 `→reject`，curl 应收 SOCKS5 `0x02` 失败。
7. **认证失败**：`--proxy-user "not_base64:x"` → 连接被拒。

### 自动化
- `go test ./...`（单元+集成）、`go vet ./...`、`gofmt -l`（无输出）。
- 交叉编译冒烟：`for os in linux darwin windows; do for arch in amd64 arm64; do GOOS=$os GOARCH=$arch go build ./cmd/deeproxy; done; done`。

---

## 7. 测试计划（含 e2e / observability 维度）

- **单元**：`auth`（AC1/AC2 真值表）、`rule`（AC4 含交叉用例 + 默认动作可配）、`config`（AC8）。
- **集成（e2e 三路径）**：go-socks5 假上游（计数器）+ deeproxy（随机端口）+ `x/net/proxy` 测试客户端（编码用户名）+ `httptest` 目标：
  - forward（AC5a：含上游认证成功/失败）、direct（AC6：上游计数不变）、reject（AC7：断言 `REP=0x02`）。
  - AC5b：上游不可达，断言回复码 + goroutine 数回落。
  - AC3/M3：构造 BIND/ASSOCIATE 请求，断言 `REP=0x02` + ASSOCIATE 后无新 UDP 监听。
  - AC10a：注入计数 resolver，断言 forward 路径本地 DNS 调用 0 次。
- **observability**：断言连接日志含 user/目标/动作/上游/上下行字节数。
- **idle（AC10b）**：建连后双向静默 > idle 超时，断言连接被回收。

---

## 8. ADR（Architecture Decision Record）

- **Decision**：采用 A3——`WithRule.Allow` 承担"策略判定（reject + 非 CONNECT 拒绝，回 `RepRuleFailure`）"，`WithDialAndRequest` 承担"拨号执行（forward/direct）"，二者经 `context.WithValue` 单次传递判定结果；注入 `NopResolver` 跳过库的本地 DNS；用 `idleConn` 补 idle 超时。
- **Drivers**：D1 拒绝语义正确性、D2 上游安全传递、D3 顺应库两阶段设计。
- **Alternatives considered**：A1（全 hook，reject 回 0x04，**源码证伪**）、A2（全 ConnectHandle，过度造轮子）、B2（缓存解码，锁/生命周期不值）。
- **Why chosen**：A3 同时满足"零中继样板（库做 io.Copy+half-close）"与"reject 精确 0x02"，规则只跑一次，ASSOCIATE 被主动拦截。
- **Consequences**：依赖 ctx 传值约定（需注释+测试守护）；建连后纯 idle 由自研 idleConn 兜底；DNS 解析移交上游（forward）。
- **Follow-ups**：上下行字节计数可观测；未来若需 SNI 嗅探/多上游/规则热更新，在 rule/detect 包扩展。

---

## 9. Open Questions
- [ ] **模块路径**：`go mod init` 的模块名（`github.com/<owner>/deeproxy`？）。
- [ ] **idle 超时默认值**：建议 `idle_timeout_sec: 300`（5 分钟），是否合适。
- [ ] **上游 dial 超时**：建议 10s，是否写入配置。
- [ ] **CI**：是否首版即配置 GitHub Actions 三平台双架构 workflow。

---

## 10. 复杂度与交付物
- 7 步 + Step 0 冒烟，约 14 源文件，跨 auth/config/rule/dialer/server/cmd/internal 七模块。复杂度 **MEDIUM**。
- 关键交付：① 用户名编解码契约 ② 顺序首匹配规则引擎（IP/域名分流 + 交叉语义）③ A3 三件套（Rule 判定 + hook 拨号 + ctx 传递）+ NopResolver + idleConn ④ YAML 配置/slog/跨平台入口。
- 全中文注释，单文件单职责，DRY。
