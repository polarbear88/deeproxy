// Package auth 负责“上游代理信息”的编解码与认证校验。
//
// 设计背景：本工具的上游 SOCKS5 代理不写在配置文件里，而是由客户端在每条连接的
// SOCKS5 用户名字段里以 base64 编码携带，明文格式为 "user:pwd@host:port"。
// 服务端在认证阶段解码该用户名得到“本连接的动态上游”。密码字段（SOCKS5 的 password）
// 无业务语义，仅作占位。
package auth

import (
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// maxUsernameLen 是 SOCKS5 用户名/密码认证（RFC 1929）允许的用户名最大字节数。
// 用户名长度字段为单字节，故上限为 255。编码后的用户名超过该值无法通过协议传输，
// 因此在解码阶段提前拒绝，给出明确错误。
const maxUsernameLen = 255

// Upstream 表示从客户端用户名解码出的“本连接动态上游 SOCKS5 代理”信息。
type Upstream struct {
	Host string // 上游主机（域名或 IP）
	Port int    // 上游端口
	User string // 上游 SOCKS5 认证用户名（可为空表示上游免认证）
	Pwd  string // 上游 SOCKS5 认证密码
}

// Addr 返回可直接用于拨号的 "host:port"。
// 使用 net.JoinHostPort 以保证 IPv6 主机被正确地加上方括号。
func (u Upstream) Addr() string {
	return net.JoinHostPort(u.Host, strconv.Itoa(u.Port))
}

// DecodeUpstream 解析 base64("user:pwd@host:port") 为 Upstream。
//
// 解析规则（为兼容主机/凭据中可能出现的特殊字符，切分位置经过精心选择）：
//   - 先做标准 base64 解码；
//   - 用最后一个 '@' 切分“凭据部分(user:pwd)”与“地址部分(host:port)”，
//     因为 host 不含 '@'，用 LastIndex 可避免 user/pwd 中出现 '@' 时误切；
//   - 凭据部分用第一个 ':' 切分 user 与 pwd，pwd 中即便含 ':' 也不受影响；
//   - 地址部分用 net.SplitHostPort 切分，天然处理 IPv6 的方括号写法。
//
// 任何一步失败（空串 / 非法 base64 / 缺 '@' / 缺 ':' / 端口非法 / 用户名超长）
// 均返回 error，调用方据此拒绝该连接。
func DecodeUpstream(username string) (Upstream, error) {
	// 提前拒绝超长用户名：SOCKS5 协议无法承载，且通常意味着输入异常。
	if len(username) == 0 {
		return Upstream{}, fmt.Errorf("用户名为空，无法解析上游")
	}
	if len(username) > maxUsernameLen {
		return Upstream{}, fmt.Errorf("用户名长度 %d 超过 SOCKS5 上限 %d", len(username), maxUsernameLen)
	}

	// 第一步：base64 解码。
	raw, err := base64.StdEncoding.DecodeString(username)
	if err != nil {
		return Upstream{}, fmt.Errorf("用户名不是合法的 base64: %w", err)
	}
	plain := string(raw)

	// 第二步：用最后一个 '@' 切分凭据与地址。
	at := strings.LastIndex(plain, "@")
	if at < 0 {
		return Upstream{}, fmt.Errorf("解码后缺少 '@' 分隔符: %q", plain)
	}
	cred := plain[:at]
	hostport := plain[at+1:]

	// 第三步：凭据部分用第一个 ':' 切分 user 与 pwd（strings.Cut 取首个分隔符）。
	user, pwd, ok := strings.Cut(cred, ":")
	if !ok {
		return Upstream{}, fmt.Errorf("凭据部分缺少 ':' 分隔符: %q", cred)
	}

	// 第四步：地址部分切分 host 与 port（net.SplitHostPort 处理 IPv6）。
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return Upstream{}, fmt.Errorf("地址部分无法解析 host:port: %w", err)
	}
	if host == "" {
		return Upstream{}, fmt.Errorf("上游主机为空: %q", hostport)
	}

	// 第五步：端口必须是 1-65535 的合法整数。
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return Upstream{}, fmt.Errorf("上游端口非数字: %q", portStr)
	}
	if port < 1 || port > 65535 {
		return Upstream{}, fmt.Errorf("上游端口越界: %d", port)
	}

	return Upstream{Host: host, Port: port, User: user, Pwd: pwd}, nil
}

// EncodeUpstream 是 DecodeUpstream 的逆操作，将 Upstream 编码为 base64 用户名。
// 主要供测试与文档使用：客户端可据此生成合法的用户名字段。
// 注意：编码结果若超过 maxUsernameLen 字节，将无法通过 SOCKS5 协议传输。
func EncodeUpstream(u Upstream) string {
	plain := fmt.Sprintf("%s:%s@%s", u.User, u.Pwd, u.Addr())
	return base64.StdEncoding.EncodeToString([]byte(plain))
}
