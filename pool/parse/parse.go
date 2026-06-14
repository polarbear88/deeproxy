// Package parse 实现「批量添加上游」的多格式行解析（AC-3.1/3.2）。
//
// 为什么独立新建而非复用 auth.DecodeUpstream：auth.DecodeUpstream 只认 base64("user:pwd@host:port")
// 的单一编码形态，无法解析后台批量粘贴的明文多格式行；二者输入域不同，不可复用（Critic 钉死）。
//
// 支持两种明文格式（消歧规则钉死，见各函数注释）：
//
//	① 含 '@'：user:pass@host:port —— '@' 后按 host:port 解析，'@' 前按第一个 ':' 分 user/pass。
//	② 不含 '@' 的 user:pass:host:port —— 从右起取最后两段为 host:port，
//	   其余左侧按【第一个】':' 分 user / pass（pass 内可含 ':'，但该形式下 username 不能含 ':'；
//	   需含 ':' 的 username 必须用 '@' 形）。
//
// IPv6 host：必须用 '@' 形或方括号 [::1]:port；裸 IPv6 冒号形视为非法行并报错（无法消歧）。
package parse

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Upstream 是解析出的一条上游（明文，供后台批量入库）。
type Upstream struct {
	Host string // 上游主机（域名 / IPv4 / IPv6，已去方括号）
	Port int    // 上游端口
	User string // 上游认证用户名（可空）
	Pwd  string // 上游认证密码（可空，可含 ':'）
}

// LineResult 是单行解析结果（成功携带 Upstream，失败携带错误原因 + 原始行号）。
type LineResult struct {
	LineNo int       // 行号（从 1 开始，对应输入顺序）
	Raw    string    // 原始行内容（便于前端定位）
	OK     bool      // 是否解析成功
	Up     Upstream  // 成功时的解析结果
	Err    string    // 失败原因
}

// ParseLines 逐行解析批量文本，返回每行结果（成功/失败逐行容错，不因单行失败中断）。
//
// 行处理：去首尾空白；空行与以 '#' 开头的注释行【跳过】（不计入结果）。
func ParseLines(text string) []LineResult {
	var out []LineResult
	lineNo := 0
	for _, raw := range strings.Split(text, "\n") {
		lineNo++
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r")) // 兼容 CRLF
		if line == "" || strings.HasPrefix(line, "#") {
			continue // 空行/注释跳过
		}
		up, err := ParseLine(line)
		if err != nil {
			out = append(out, LineResult{LineNo: lineNo, Raw: line, OK: false, Err: err.Error()})
			continue
		}
		out = append(out, LineResult{LineNo: lineNo, Raw: line, OK: true, Up: up})
	}
	return out
}

// ParseLine 解析单行为 Upstream，按消歧规则区分 '@' 形与无 '@' 形。
func ParseLine(line string) (Upstream, error) {
	if strings.Contains(line, "@") {
		return parseAtForm(line)
	}
	return parseColonForm(line)
}

// parseAtForm 解析 user:pass@host:port 形：
//   - 以【最后一个】'@' 分割（host 部分不会含 '@'，凭据理论上也不含；用 LastIndex 容忍 pass 含 '@' 的少见情况）。
//   - '@' 后用 net.SplitHostPort 解析 host:port（天然支持 [::1]:port 的 IPv6 方括号形）。
//   - '@' 前按【第一个】':' 分 user / pass（pass 可含 ':'）；无 ':' 则整体为 user、pass 空。
func parseAtForm(line string) (Upstream, error) {
	at := strings.LastIndex(line, "@")
	cred := line[:at]
	hostport := line[at+1:]
	if hostport == "" {
		return Upstream{}, fmt.Errorf("'@' 后缺少 host:port")
	}

	host, port, err := splitHostPort(hostport)
	if err != nil {
		return Upstream{}, err
	}

	user, pwd := cred, ""
	if i := strings.Index(cred, ":"); i >= 0 {
		user, pwd = cred[:i], cred[i+1:]
	}
	return Upstream{Host: host, Port: port, User: user, Pwd: pwd}, nil
}

// parseColonForm 解析不含 '@' 的 user:pass:host:port 形：
//   - 从右起取【最后两段】为 host:port（按 ':' 切分后取末两段）。
//   - 其余左侧（去掉末两段后）按【第一个】':' 分 user / pass（pass 内可含 ':'）。
//   - 至少需 host:port 两段；裸 IPv6（冒号过多且无法判定 host:port）落到非法行。
func parseColonForm(line string) (Upstream, error) {
	// 裸 IPv6 防呆：含 '::'（IPv6 压缩写法）却无方括号/无 '@'，无法与 user:pass:host:port 消歧，
	// 直接判非法并指引用 '@' 形或方括号。例如 "2001:db8::1:8080" 不应被误当成 host=1 port=8080。
	if strings.Contains(line, "::") {
		return Upstream{}, fmt.Errorf("疑似裸 IPv6 主机（含 '::'），请用 '@' 形或方括号 [::1]:port")
	}

	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return Upstream{}, fmt.Errorf("缺少 host:port（无 '@' 形需至少 host:port）")
	}

	// 末两段 = host:port。
	host := parts[len(parts)-2]
	portStr := parts[len(parts)-1]
	port, err := parsePort(portStr)
	if err != nil {
		// 端口非法：很可能是裸 IPv6 冒号形（如 2001:db8::1:8080），无法消歧，报错指引用 '@' 或方括号。
		return Upstream{}, fmt.Errorf("端口非法 %q（IPv6 主机请用 '@' 形或方括号 [::1]:port）: %v", portStr, err)
	}
	if host == "" {
		return Upstream{}, fmt.Errorf("host 为空")
	}

	// 剩余左侧段 = user[:pass]。
	left := parts[:len(parts)-2]
	var user, pwd string
	if len(left) > 0 {
		joined := strings.Join(left, ":") // 还原左侧原文（pass 内可能含 ':'）
		if i := strings.Index(joined, ":"); i >= 0 {
			user, pwd = joined[:i], joined[i+1:]
		} else {
			user = joined
		}
	}
	return Upstream{Host: host, Port: port, User: user, Pwd: pwd}, nil
}

// splitHostPort 用 net.SplitHostPort 解析 host:port（支持 [::1]:port 的 IPv6 方括号形），
// 并校验端口为合法数值；去掉 IPv6 的方括号还原纯 host。
func splitHostPort(hostport string) (string, int, error) {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, fmt.Errorf("host:port 解析失败 %q（IPv6 请用方括号 [::1]:port）: %v", hostport, err)
	}
	port, err := parsePort(p)
	if err != nil {
		return "", 0, fmt.Errorf("端口非法 %q: %v", p, err)
	}
	if h == "" {
		return "", 0, fmt.Errorf("host 为空")
	}
	return h, port, nil
}

// parsePort 解析并校验端口范围（1~65535）。
func parsePort(s string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("非数字端口 %q", s)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("端口越界 %d（应为 1~65535）", port)
	}
	return port, nil
}
