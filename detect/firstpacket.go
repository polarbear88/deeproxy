package detect

import (
	"bytes"
	"encoding/binary"
)

// FirstPacketComplete 判断「已累积的首包字节」是否足以做一次嗅探判定，
// 供调用方（server.peekFirstPacket 的循环读）决定还要不要继续读后续 TCP 段。
//
// 为什么需要它（修复的核心缺陷）：
//
//	现代 TLS 1.3 ClientHello 普遍 >1.5KB（浏览器带 GREASE / 后量子密钥交换常 1.7–2KB），
//	超过单个以太网段（MSS≈1460B）就会被拆成多个 TCP 段。若只 Read 一次就交给 SNI 解析，
//	记录层长度校验（sni.go 里 r.take(recLen)）会因 body 不全而失败 → 本可成功的嗅探落空 →
//	回退显示 IP、域名规则静默漏匹配。因此 server 侧需循环读到「一个完整记录」再解析，
//	本函数就是那个「读够了吗」的门控判据。
//
// 返回 true 表示「无需再等，可以交给 Sniff 了」：
//   - TLS（首字节 0x16）：已含完整记录层 = 5 字节记录头 + 头里声明的记录长度；
//   - HTTP：已出现请求头结束标记 CRLF CRLF（Host 头此时一定已到齐）；
//   - 既非 TLS 也非 HTTP、且不可能再变成 HTTP：无法嗅探，继续读也没用，立即停止。
//
// 返回 false 表示「还需要更多字节」，只有三种情况：
//   - TLS 记录头尚未集齐（len<5）；
//   - TLS 记录头已到但 body 未满；
//   - 首包可能是尚未集齐的 HTTP 方法前缀（如只到 "GE"，见 couldBecomeHTTP）。
//
// 局限（有意为之，与 sni.go 的单记录解析保持一致）：本函数只门控「第一个 TLS 记录」。
// 若 ClientHello 被 TLS 记录层分片成多个 record、且 SNI 落在第 2 个 record（RFC 允许但极罕见），
// 这里在第 1 个 record 完整时即返回 true，Sniff 仅解析第 1 个 record 会找不到 SNI 而失败。
// 这与改动前的行为一致（不是本次引入的回归），属可接受的残留局限。
func FirstPacketComplete(data []byte) bool {
	if len(data) == 0 {
		return false // 还没读到任何字节
	}
	// TLS：首字节是记录层 handshake 类型。
	if data[0] == tlsRecordHandshake {
		if len(data) < 5 {
			return false // 记录头(type1 + version2 + length2)未集齐，继续读
		}
		// 记录头第 4-5 字节是大端记录长度；凑齐 5+recLen 即为一个完整记录。
		recLen := int(binary.BigEndian.Uint16(data[3:5]))
		return len(data) >= 5+recLen
	}
	// HTTP：已确认是 HTTP 方法开头，则等请求头收全（出现空行）。
	if looksLikeHTTP(data) {
		return bytes.Contains(data, []byte("\r\n\r\n"))
	}
	// 可能是「方法名尚未发全」的 HTTP（如只到 "GE"/"POS"）：继续读，别误判成非 HTTP 提前收手。
	if couldBecomeHTTP(data) {
		return false
	}
	// 其余：非 TLS、非 HTTP、也不可能补全成 HTTP 方法 → 不可嗅探，无需再等。
	return true
}

// couldBecomeHTTP 判断当前已读字节是否「可能是一个尚未发全的 HTTP 方法前缀」。
//
// 必要性：looksLikeHTTP 要求完整的「方法+空格」前缀（如 "GET "），但首包可能被拆段，
// 第一段只到 "GE"。若此时直接判非 HTTP 并停止循环，碎片化 HTTP 的 Host 嗅探就会回归失败。
// 故当 data 是某个方法 token 的【严格前缀】（更短且匹配）时，返回 true 表示「再等等」。
func couldBecomeHTTP(data []byte) bool {
	if len(data) == 0 {
		return false // 空输入不算「方法前缀」（空串是任何串的前缀，需显式排除以免误判）
	}
	for _, m := range httpMethods {
		// data 比方法 token 短，且正是它的前缀 → 可能后续补全成该方法。
		if len(data) < len(m) && bytes.HasPrefix(m, data) {
			return true
		}
	}
	return false
}
