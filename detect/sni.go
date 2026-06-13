// Package detect 从客户端发往目标的首包数据中嗅探目标域名。
//
// 用途：当客户端使用本地 DNS（socks5://）时，SOCKS5 请求里只带 IP、没有域名，
// 域名类分流规则无法命中。本包尝试从应用层首包还原域名：
//   - HTTPS：解析 TLS ClientHello 的 SNI 扩展；
//   - HTTP：解析请求头里的 Host。
//
// 所有解析函数对截断/畸形/恶意输入都必须安全（只做边界内读取，绝不 panic）。
package detect

// 提取 SNI 用到的 TLS 常量。
const (
	tlsRecordHandshake = 0x16 // TLS 记录层类型：handshake
	tlsHandshakeHello  = 0x01 // handshake 类型：ClientHello
	extServerName      = 0x00 // 扩展类型：server_name(SNI)
	sniHostNameType    = 0x00 // server_name 列表项类型：host_name
)

// SNI 解析 TLS ClientHello 字节流，返回其中的 server_name(SNI)。
//
// 解析路径（每一步都先校验剩余长度，越界即返回 false）：
//
//	记录层: type(1)=0x16 | version(2) | length(2) | fragment...
//	握手:   type(1)=0x01 | length(3) | version(2) | random(32) |
//	        session_id(len1+data) | cipher_suites(len2+data) |
//	        compression(len1+data) | extensions(len2 + 列表)
//	扩展项: type(2) | length(2) | data    （找 type==0x0000 的 server_name）
//	SNI:    list_len(2) | name_type(1)=0 | name_len(2) | name
func SNI(data []byte) (string, bool) {
	r := reader{buf: data}

	// —— TLS 记录层 ——
	if b, ok := r.u8(); !ok || b != tlsRecordHandshake {
		return "", false
	}
	if !r.skip(2) { // 记录层版本
		return "", false
	}
	recLen, ok := r.u16()
	if !ok {
		return "", false
	}
	// 把后续解析限制在记录层声明的长度内（防止越界读到其它数据）。
	body, ok := r.take(int(recLen))
	if !ok {
		// 记录层不完整：ClientHello 通常在单个记录内，截断则放弃。
		return "", false
	}
	h := reader{buf: body}

	// —— 握手层 ——
	if b, ok := h.u8(); !ok || b != tlsHandshakeHello {
		return "", false
	}
	if !h.skip(3) { // 握手消息长度(3 字节)
		return "", false
	}
	if !h.skip(2 + 32) { // client_version(2) + random(32)
		return "", false
	}
	if !h.skipVec8() { // session_id
		return "", false
	}
	if !h.skipVec16() { // cipher_suites
		return "", false
	}
	if !h.skipVec8() { // compression_methods
		return "", false
	}

	// —— 扩展列表 ——
	extTotal, ok := h.u16()
	if !ok {
		return "", false
	}
	exts, ok := h.take(int(extTotal))
	if !ok {
		return "", false
	}
	e := reader{buf: exts}
	for {
		extType, ok := e.u16()
		if !ok {
			return "", false // 遍历完所有扩展也没找到 SNI
		}
		extData, ok := e.takeVec16()
		if !ok {
			return "", false
		}
		if extType == extServerName {
			return parseServerName(extData)
		}
	}
}

// parseServerName 解析 server_name 扩展的数据部分，取第一个 host_name。
func parseServerName(data []byte) (string, bool) {
	r := reader{buf: data}
	list, ok := r.takeVec16() // server_name_list
	if !ok {
		return "", false
	}
	l := reader{buf: list}
	for {
		nameType, ok := l.u8()
		if !ok {
			return "", false
		}
		name, ok := l.takeVec16()
		if !ok {
			return "", false
		}
		if nameType == sniHostNameType && len(name) > 0 {
			return string(name), true
		}
	}
}
