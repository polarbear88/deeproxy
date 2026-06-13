# deeproxy

> 一个用 **Go** 编写、**跨平台（Windows / macOS / Linux）** 的 **SOCKS5 中继转发工具**，内置 **Web 管理后台**，编译为**单一静态二进制**（前端 embed 进二进制，无外部依赖、无需配置文件）。

deeproxy 自身对外提供一个 SOCKS5 代理服务，但它**不是最终出口，而是一个中继（relay）**：按你配置的分流规则，把客户端的目标请求转发到**上游 SOCKS5 代理**、本地直连、或拒绝。

所有配置（代理组 / 规则 / 用户 / 授权 / 系统设置）都存在内置的 **SQLite** 里，通过 Web 后台管理；**不需要任何配置文件**。

---

## 核心特性

- **SOCKS5 中继**：仅支持 `CONNECT`（TCP）。支持目标为域名 / IPv4 / IPv6。
- **两种代理组**
  - **Type A 动态上游**：上游由客户端在「每条连接」上通过 SOCKS5 用户名携带（base64），同一服务可被不同客户端复用、各自指定上游。
  - **Type B 代理池**：组内维护多个上游，按**加权平滑轮询（SWRR）**选择 + **健康检查**自动剔除/恢复 + 拨号失败**故障转移**；上游用户名支持 `{变量}` 模板，由客户端连接时传入命名变量替换。
- **规则引擎**：`domain` / `domain-suffix` / `ip-cidr` 三类匹配，多规则组多对多应用，**全局规则优先于分组规则**，**顺序首匹配**，命中输出 `forward` / `direct` / `reject`；不命中走默认动作。
- **域名嗅探**：当目标是 IP 且未命中任何 `ip-cidr` 规则时，可嗅探客户端首包的 **TLS SNI / HTTP Host** 还原域名再按域名规则选路（可在后台开关）。
- **Web 管理后台**：仪表盘（实时 / 今日流量、请求数、Top 排行、动作分布、时序图）、代理组与上游管理、规则管理、用户与授权管理、系统设置、系统日志（SSE 实时推送）。
- **零配置文件**：配置全在 SQLite；启动只需指定两个端口。运行期设置（默认动作 / 日志级别 / 空闲超时 / 嗅探开关与超时）在后台动态修改，多数即时或新连接生效。

---

## 连接用户名格式（使用者最关心）

客户端连接本地 SOCKS5 时**必须**使用用户名 / 密码认证（RFC 1929）。

**用户名**按位置切分，格式为：

```
user-group            （无尾段）
user-group-尾段        （带尾段；尾段整体不再按 '-' 拆分，可含 '-'）
```

- `user`：代理用户名（后台创建的代理用户）。
- `group`：代理组名（该用户须被授权访问此组）。
- `尾段`：随组类型而不同：
  - **Type A 组** → 尾段 = `base64("上游user:上游pwd@上游host:上游port")`
    例：上游 `uu:pp@1.2.3.4:8388` → 尾段 = base64 后的串，完整用户名 `alice-gA-dXU6cHBAMS4yLjMuNDo4Mzg4`。
  - **Type B 组** → 尾段 = 命名变量串 `名1_值1#名2_值2`（`_` 分隔名与值、`#` 分隔变量），用于替换上游用户名模板里的 `{名}`。
    例：`alice-poolA-region_us#session_abc123`。

**密码**：通过 SOCKS5 密码字段传入代理用户的连接密码（明文比对）。

> 建议客户端使用**远程 DNS**（`socks5h://`）直接发送域名：路径最短、无需嗅探、规则命中 `reject` 也能回精确拒绝码。

---

## 编译

**前置**：Go 1.21+、Node + pnpm（用于构建前端）。

前端产物会输出到 `api/dist`，由 Go 通过 `//go:embed` 嵌入二进制，因此**必须先构建前端再编译 Go**：

```bash
# 1) 构建前端（产物 → api/dist）
cd web && pnpm install --frozen-lockfile && pnpm build && cd ..

# 2) 编译单一静态二进制（CGO_ENABLED=0：modernc.org/sqlite 纯 Go，免 C 工具链）
CGO_ENABLED=0 go build -o deeproxy ./cmd/deeproxy
```

或使用 Makefile / 构建脚本：

```bash
make web        # 构建前端
make build      # 本机单平台构建（需 api/dist 已存在）
make release    # 调 build.sh 交叉编译全部 6 个目标（win/linux/mac × amd64/arm64）单一静态二进制
make test       # go test ./...
make race       # go test -race ./...
```

> `CGO_ENABLED=0` 让交叉编译无需各平台 C 工具链，一条命令产出所有平台的单一静态二进制。

---

## 运行

启动参数只有两个端口（其余设置都在后台动态管理）：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--socks5` | `1768` | SOCKS5 中继监听端口 |
| `--web` | `1769` | Web 管理后台监听端口 |
| `-v` | — | 打印版本号并退出 |

监听地址固定为 `0.0.0.0`（全网卡），数据库文件固定为当前目录下 `./deeproxy.db`（数据库路径承载所有设置，无法做成设置项）。

```bash
# 默认：SOCKS5 :1768，Web 后台 :1769，监听 0.0.0.0，数据库 ./deeproxy.db
./deeproxy

# 自定义端口
./deeproxy --socks5 1768 --web 1769
```

首次打开 Web 后台 `http://localhost:1769` 会引导你**设置管理员账号与密码**。

---

## 快速上手

1. 启动 `./deeproxy`，浏览器打开 `http://localhost:1769`，设置管理员账号密码并登录。
2. **新建代理组**：选 Type A（动态上游）或 Type B（代理池，可加上游、设权重、配健康检查）。
3. **新建代理用户**并**授权**其访问该组。
4. **配置分流规则**（domain / domain-suffix / ip-cidr → forward / direct / reject），按需调整默认动作。
5. 客户端用 `user-group`（或带尾段）作为用户名、代理用户密码作为密码，连接本机 SOCKS5 `:1768`。

---

## 系统设置（后台动态修改）

以下设置在后台「系统设置」页修改，无需重启或改文件：

| 设置 | 默认 | 生效 |
|------|------|------|
| 默认动作 `default_action` | `forward` | 新连接 |
| 日志级别 `log_level` | `info` | 立即热生效 |
| 空闲超时 `idle_timeout_sec` | `300` 秒 | 新连接 |
| 域名嗅探 `sniff_domain` | 开启 | 新连接 |
| 嗅探首包超时 `sniff_timeout_ms` | `300` 毫秒 | 新连接 |

---

## 跨平台

支持 **Windows / macOS / Linux**，每个平台覆盖 **amd64 / arm64**。`make release` 一次产出全部目标的单一静态二进制。

---

## 技术栈

- **后端**：Go · [Gin](https://github.com/gin-gonic/gin) · [modernc.org/sqlite](https://gitlab.com/cznic/sqlite)（纯 Go，免 CGO） · [things-go/go-socks5](https://github.com/things-go/go-socks5)
- **前端**：Vue 3 · Element Plus · ECharts · pnpm（构建产物 embed 进二进制）

---

## 安全说明

- **代理用户连接密码为明文存储**：这是自建中继工具的取舍——该密码仅用于 SOCKS5 连接鉴权，明文比对为微秒级，避免每连接 bcrypt 成为转发建连瓶颈。
- **管理员后台密码使用 bcrypt 哈希**存储。
- **后台默认监听 `0.0.0.0`**：若部署在公网，请务必配置防火墙 / 反向代理 / 仅内网访问，避免管理后台暴露。
- 上游代理凭据由客户端在每条连接动态携带（Type A）或经命名变量注入模板（Type B），不写死在服务端。
