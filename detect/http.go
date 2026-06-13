package detect

import (
	"bytes"
	"strings"
)

// httpMethods 是用于快速判断首包是否疑似 HTTP 的常见方法前缀。
var httpMethods = [][]byte{
	[]byte("GET "), []byte("POST "), []byte("HEAD "), []byte("PUT "),
	[]byte("DELETE "), []byte("OPTIONS "), []byte("PATCH "), []byte("CONNECT "),
	[]byte("TRACE "),
}

// HTTPHost 从明文 HTTP 请求的首包中解析 Host 头（去掉端口）。
// 仅在 data 中已包含 Host 头时才能解析成功；非 HTTP 或 Host 缺失返回 false。
func HTTPHost(data []byte) (string, bool) {
	if !looksLikeHTTP(data) {
		return "", false
	}
	// 按行扫描请求头，查找 "Host:"（大小写不敏感）。
	// 只在首包已有的字节里找；找不到（可能头未收全）则放弃。
	for _, line := range bytes.Split(data, []byte("\r\n")) {
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		if !strings.EqualFold(string(line[:colon]), "Host") {
			continue
		}
		host := strings.TrimSpace(string(line[colon+1:]))
		if host == "" {
			return "", false
		}
		// 去掉可能的端口部分（host:port）。IPv6 字面量带方括号不在此路径出现。
		if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host, "]") {
			host = host[:i]
		}
		if host == "" {
			return "", false
		}
		return host, true
	}
	return "", false
}

// looksLikeHTTP 判断首包是否以常见 HTTP 方法开头。
func looksLikeHTTP(data []byte) bool {
	for _, m := range httpMethods {
		if bytes.HasPrefix(data, m) {
			return true
		}
	}
	return false
}

// Sniff 综合判断首包并尝试还原目标域名：
//   - 首字节为 0x16(TLS 记录层 handshake) → 走 SNI；
//   - 疑似 HTTP 方法开头 → 走 Host；
//   - 其它（无法识别）→ 返回 ok=false，调用方应回退到默认动作。
func Sniff(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	if data[0] == tlsRecordHandshake {
		return SNI(data)
	}
	if looksLikeHTTP(data) {
		return HTTPHost(data)
	}
	return "", false
}
