# D0-0 源码走查留证（阶段 0）

> 目的：复核 go-socks5 v0.1.1 鉴权三阶段（Valid → Allow → ConnectHandle）能否承载 v2 鉴权解析结果的跨阶段传递。
> 结论：**沿用 v1 的 context(`decisionKey`) 机制即可，零跨连接共享、零并发风险。禁止引入 sync.Map 待取表。**

## 1. AuthContext.Payload 同时含 username + password（已证实）

模块路径：`/Users/polarbear/go/pkg/mod/github.com/things-go/go-socks5@v0.1.1/auth.go`

`UserPassAuthenticator.Authenticate`（auth.go:55-79 附近）在密码校验通过后返回：

```go
// Verify the password
if !a.Credentials.Valid(string(nup.User), string(nup.Pass), userAddr) {
    // ... 写 AuthFailure，返回 statute.ErrUserAuthFailed
}
// ... 写 AuthSuccess
return &AuthContext{
    statute.MethodUserPassAuth,
    map[string]string{
        "username": string(nup.User),
        "password": string(nup.Pass),
    },
}, nil
```

**关键事实**：
- `Credential.Valid(user, password, userAddr)` 在鉴权阶段【同时拿到 user 与 password】。v2 在此完成：解析用户名 `user-group[-尾段]` → 查 ProxyUser + bcrypt 验密码 → 验 group 授权 → 按组类型解析尾段。
- 返回的 `AuthContext.Payload` 是 `map[string]string`，同时含 `username` 与 `password`，因此 Allow / ConnectHandle 阶段也能直接读 `req.AuthContext.Payload["password"]`。

## 2. authContext 是 ServeConn 内局部变量、不跨连接共享（已证实）

`server.go` 中 `ServeConn` 为每条连接独立 goroutine；`authContext` 是该方法内的局部变量，认证后挂到 `request.AuthContext`，贯穿 Allow → ConnectHandle 顺序执行，**绝不跨连接共享**。v1 `server/server.go` 已在用 `req.AuthContext.Payload["username"]`，证明 AuthContext 完整流到 ConnectHandle。

## 3. 集成结论

- 三阶段（Valid、Allow、ConnectHandle）同 goroutine、同连接、顺序执行。
- v2 鉴权解析结果（group/user/组类型/动态上游(A)/命名变量映射(B)）经扩展后的 `server/ctxkey.go` `decision` 结构通过 context 传递（沿用 v1 机制）。
- `password` 在需要处直接从 `Payload["password"]` 读，无需自建跨连接表。
- **删除原 D0-A 的 sync.Map 待取表**：它解决的是不存在的问题（"拿不到 password"已被源码证伪），且会真正引入跨连接共享与并发键 bug。

## 4. Go 版本与依赖

- Go 1.26.3 darwin/arm64（本机验证）。
- go-socks5 v0.1.1、golang.org/x/net v0.56.0、gopkg.in/yaml.v3 v3.0.1（v1 既有）。
