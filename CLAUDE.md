# deeproxy — SOCKS5 中继转发工具

> 本文件由 `/deep-interview` 深度访谈（7 轮，最终模糊度 15.8%，阈值 20%）crystallize 而成，作为项目的权威需求说明。
> 详细访谈记录见 `.omc/specs/deep-interview-socks5-relay.md`。

---

## 一、项目概述

deeproxy 是一个用 **Go** 编写、**跨平台（Windows / macOS / Linux）** 的 **SOCKS5 中继转发工具**。

它自身对外提供一个 **SOCKS5 代理服务**，本身不是最终出口，而是一个**中继（relay）**：
按规则把客户端的目标请求转发到**上游 SOCKS5 代理**，或本地直连，或拒绝。

与常见分流代理的关键差异：**上游代理不是写死在配置文件里，而是由客户端在「每条连接」上通过 SOCKS5 用户名字段动态携带（base64 编码）。** 配置文件只承载「分流规则」。

---

## 二、核心数据流（首版权威流程）

每条客户端连接的处理流程如下：

```
客户端
  │  SOCKS5（强制 用户名/密码 认证，RFC 1929）
  │  用户名 = base64("user:pwd@host:port")   ← 编码的“上游代理”信息
  ▼
本地 SOCKS5 服务端
  │  1. base64 解码用户名 → 解析 user:pwd@host:port
  │       └─ 缺失 / 解码失败 / 解析失败 → 拒绝连接（认证失败）
  │  2. 得到“本连接的动态上游” U = {host, port, user, pwd}
  │  3. 接收 SOCKS5 请求；仅支持 CONNECT(TCP)
  │       └─ BIND / UDP ASSOCIATE → 回复“命令不支持”并拒绝
  │  4. 目标探测：直接读取 CONNECT 请求里的目标地址 {域名或IP, 端口}
  │       （客户端用 socks5h/远程 DNS 时即为域名；用 socks5/本地 DNS 时为 IP）
  │       └─ 若为 IP 且未命中 ip-cidr 规则、且开启嗅探 → 先回 success，
  │           嗅探客户端首包 TLS SNI / HTTP Host 还原域名，再按域名规则选路
  │  5. 规则引擎：按目标顺序匹配规则
  │       ├─ 命中 → 取该规则动作
  │       └─ 不命中 → 默认动作（默认 forward，可配置）
  ▼
动作执行（三选一）
  ├─ forward → 经动态上游 U 建立 SOCKS5 连接，双向中继数据
  ├─ direct  → 本机直接 TCP 连接目标，双向中继数据
  └─ reject  → 拒绝/关闭连接（回复 SOCKS5 拒绝码）
```

---

## 三、顶层组件（拓扑）

首版包含 5 个顶层组件，全部为 MVP 范围：

| # | 组件 | 职责 |
|---|------|------|
| ① | 本地 SOCKS5 服务端 | 监听端口，处理 SOCKS5 握手、强制用户名/密码认证、接收 CONNECT 请求 |
| ② | 上游代理中继 | 用「从用户名解码出的动态上游」建立 SOCKS5 连接并做双向数据中继 |
| ③ | 目标探测 | 从 CONNECT 请求读取目标地址（域名 / IPv4 / IPv6 + 端口）；当目标是 IP 且未命中 ip-cidr 时，可嗅探首包 TLS SNI / HTTP Host 还原域名 |
| ④ | 规则引擎 | 按目标地址做顺序首匹配，输出动作：forward / direct / reject；不命中走默认动作 |
| ⑤ | 配置系统 | 加载/校验配置文件（监听地址、默认动作、规则列表、日志级别） |

---

## 四、用户名编码契约（首版核心约定）

> ⚠️ **v2 权威更正（以代码 `auth/username.go` / `auth/authz.go` 实测为准）**：本节描述的是 **v1**
> 「用户名整段 = base64(上游)」契约，**已被 v2 取代**。v2 的权威用户名语法为
> **`user-group[-尾段]`**（按位置用前两个 `-` 切出 `user`/`group`，第三段为尾段整体保留、不再拆分）：
> - `user`：代理用户名（`ProxyUser.username`）；`group`：分组名（`Group.name`）。
> - **尾段语义由分组类型决定**：
>   - **Type A（动态上游组）**：尾段 = `base64("u:p@host:port")`，即把 v1 的「上游编码」下移为尾段（`auth.DecodeUpstream` 解析）。
>   - **Type B（代理池组）**：尾段 = 命名变量串 `name_value#name_value...`（`auth.ParseVariables` 解析），用于上游用户名模板替换。
> - **认证**：本地服务强制 SOCKS5 用户名/密码认证；密码字段为 `ProxyUser` 的明文连接密码（v2 明文存储、微秒级比对，仅管理员后台密码用 bcrypt）。
> - **失败处理**：用户名为空 / 缺分组段 / user 段为空 / group 段为空 / 用户不存在 / 未授权该分组 / 密码不符 / Type A 尾段非法 base64 → 拒连。
>
> 下文 v1 描述仅作历史背景保留；实现请以 v2 语法为准。


- 客户端连接本地服务时**必须**使用 SOCKS5 用户名/密码认证（RFC 1929）。
- **用户名字段** = `base64("user:pwd@host:port")`
  - 解码后的明文格式为 `上游用户名:上游密码@上游主机:上游端口`，例如 `user:pwd@aa.com:888`。
  - `host` 支持域名或 IP，`port` 为上游 SOCKS5 端口。
- **密码字段**：首版不使用，可填任意值。
- **失败处理**：用户名缺失、非法 base64、或解码后不符合 `user:pwd@host:port` 格式 → **拒绝该连接**（不回退、不使用任何默认上游）。

> 说明：之所以让上游随连接动态传入，是为了让同一个本地服务能被不同客户端复用，各自指定自己的上游，而无需为每个上游单独配置/重启。

---

## 五、规则引擎语义

- **规则来源**：配置文件（YAML，见下）。
- **匹配维度（首版）**：
  - `domain`：精确域名匹配
  - `domain-suffix`：域名后缀匹配（如 `google.com` 命中 `www.google.com`）
  - `ip-cidr`：IP / CIDR 匹配（当目标是 IP 时）
- **匹配方式**：按配置中规则的**书写顺序**，**首个命中**即生效。
- **动作（action）**：
  - `forward`：经「本连接动态上游」转发
  - `direct`：本机直连目标
  - `reject`：拒绝连接
- **默认动作**：所有规则都不命中时使用，**默认值 `forward`**，可在配置中改为 `direct` 或 `reject`。

### 判定真值表（示意）

| 目标 | 命中规则 | 规则动作 | 最终行为 |
|------|----------|----------|----------|
| `www.google.com` | `domain-suffix: google.com → forward` | forward | 走动态上游 |
| `intranet.local` | `domain-suffix: .local → direct` | direct | 本机直连 |
| `ads.example.com` | `domain-suffix: ads.example.com → reject` | reject | 拒绝 |
| `203.0.113.5` | `ip-cidr: 203.0.113.0/24 → direct` | direct | 本机直连 |
| `unknown.site.com` | 无 | （默认）forward | 走动态上游 |

---

## 六、配置文件结构（示例，YAML）

```yaml
# 本地 SOCKS5 服务监听地址（默认 127.0.0.1:1080）
listen: "127.0.0.1:1080"

# 规则都不命中时的默认动作：forward | direct | reject（默认 forward）
default_action: forward

# 日志级别：debug | info | warn | error
log_level: info

# 连接双向空闲超时（秒，默认 300）
idle_timeout_sec: 300

# 域名嗅探：IP 目标未命中 ip-cidr 时，嗅探 TLS SNI / HTTP Host 还原域名再选路（默认 true）
sniff_domain: true
# 嗅探首包等待超时（毫秒，默认 300）
sniff_timeout_ms: 300

# 分流规则（顺序首匹配）
rules:
  - { match: "domain-suffix:google.com", action: forward }
  - { match: "domain:example.com",       action: direct  }
  - { match: "ip-cidr:192.168.0.0/16",   action: direct  }
  - { match: "domain-suffix:ads.com",    action: reject  }
```

> 注意：**上游代理不在配置文件里**，由客户端通过用户名动态携带。

---

## 七、约束与边界（Constraints）

- **协议范围**：仅支持 SOCKS5 **CONNECT（TCP）**。`BIND` 与 `UDP ASSOCIATE` 不支持，遇到时回复 SOCKS5「命令不支持」并拒绝。
- **目标探测范围**：默认读取 CONNECT 请求中的目标地址；当目标是 IP 且未命中任何 ip-cidr 规则、且 `sniff_domain` 开启时，会嗅探客户端首包的 **TLS SNI** 或 **HTTP Host** 还原域名后再按域名规则选路。**不做** GeoIP / 按地理位置分流。
  - 嗅探需先回 success 再读首包，故「嗅探后命中 `reject`」表现为**连接被关闭**（无法再回标准 0x02 拒绝码）；嗅探超时或非 TLS/HTTP 流量则回退到默认动作。
  - 仍推荐客户端用远程 DNS（`socks5h`）直接发域名：路径最短、无需嗅探、规则命中也能回精确拒绝码。
- **认证**：本地服务强制用户名/密码认证；无有效编码用户名一律拒绝。
- **跨平台**：必须在 Windows / macOS / Linux 编译运行；目标产物为**单一静态二进制**，建议覆盖 `amd64` 与 `arm64`。
- **目标地址类型**：CONNECT 目标支持域名 / IPv4 / IPv6。

---

## 八、非目标（Non-Goals，首版明确不做）

- 不支持 UDP ASSOCIATE / BIND。
- 不做 GeoIP / 按地理位置分流。
- 不做配置文件里的静态上游、多上游负载均衡 / 故障切换（上游由用户名动态决定）。
- 不做 Web / GUI 管理界面（首版 CLI + 配置文件即可）。
- 不做规则热更新 / 远程规则集订阅（可作为后续扩展）。

---

## 九、验收标准（Acceptance Criteria，可测试）

- [ ] 用户名 = `base64("user:pwd@host:port")` 能被正确解码并解析出上游 `{host,port,user,pwd}`。
- [ ] 用户名缺失 / 非法 base64 / 格式不符 → 连接被拒绝。
- [ ] 仅 `CONNECT` 被接受；`BIND` / `UDP ASSOCIATE` 被拒绝且回复正确的 SOCKS5 reply code。
- [ ] 给定规则集与目标地址，最终动作符合「顺序首匹配 + 默认动作」真值表（域名后缀 / 精确域名 / IP-CIDR 三类匹配均覆盖）。
- [ ] `forward` 动作：数据经「动态上游」正确建立连接并双向中继（含上游需要认证的情况）。
- [ ] `direct` 动作：本机直连目标并双向中继。
- [ ] `reject` 动作：连接被拒绝。
- [ ] 配置文件缺失/字段非法时给出清晰报错并拒绝启动（或使用文档化的默认值）。
- [ ] 在 Windows / macOS / Linux 三平台均能构建并通过上述端到端测试（建议 CI 矩阵）。

---

## 十、技术选型建议

遵循全局规范「能用成熟库就不造轮子」：

- **SOCKS5 服务端**：优先评估 `things-go/go-socks5`（社区活跃、支持自定义认证与 dial），或 `armon/go-socks5`。需要能自定义：① 用户名/密码认证回调（在此解码用户名拿到上游）；② 每连接自定义 Dial（在此接入规则引擎与上游中继）。
- **上游 SOCKS5 客户端**：`golang.org/x/net/proxy`（提供带认证的 SOCKS5 dialer），用于 `forward` 动作连上游。
- **配置解析**：`gopkg.in/yaml.v3`。
- **日志**：标准库 `log/slog`（Go 1.21+），按 `log_level` 输出连接与规则命中日志。
- **构建发布**：`go build` 交叉编译多平台/多架构单二进制，可选 `goreleaser`。

若选用的 SOCKS5 库无法在「每连接」粒度同时自定义认证与上游 Dial，可基于其原语自行实现精简 SOCKS5 服务端（仅 CONNECT），但需在注释中说明原因。

---

## 十一、编码规范（继承全局 + 本项目约定）

- **全部代码使用中文注释**，解释「为什么」，复杂逻辑说明思路；函数注释含用途/参数/返回值。
- **单文件职责单一、按功能分模块**，建议目录：`server/`（SOCKS5 服务端）、`relay/`（中继）、`rule/`（规则引擎）、`detect/`（目标探测）、`config/`（配置）、`auth/`（用户名编解码）、`cmd/`（入口）、`internal/` 等。
- **DRY**：双向数据拷贝、地址解析等公共逻辑抽取到 `utils/` 或 `common/`，禁止重复实现。
- 引入新依赖前先确认是否已有同类库；优先 Star 多、维护活跃、文档完善、无重大安全漏洞的库。
