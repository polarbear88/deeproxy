package detect

import "encoding/binary"

// reader 是一个只前进、带边界检查的字节读取器。
// 所有读取方法在剩余字节不足时返回 ok=false，绝不 panic，
// 用于安全解析可能被截断或恶意构造的协议字节流。
type reader struct {
	buf []byte
	pos int
}

// remaining 返回尚未读取的字节数。
func (r *reader) remaining() int { return len(r.buf) - r.pos }

// u8 读取一个字节。
func (r *reader) u8() (byte, bool) {
	if r.remaining() < 1 {
		return 0, false
	}
	b := r.buf[r.pos]
	r.pos++
	return b, true
}

// u16 读取一个大端 uint16。
func (r *reader) u16() (uint16, bool) {
	if r.remaining() < 2 {
		return 0, false
	}
	v := binary.BigEndian.Uint16(r.buf[r.pos:])
	r.pos += 2
	return v, true
}

// skip 跳过 n 字节。
func (r *reader) skip(n int) bool {
	if n < 0 || r.remaining() < n {
		return false
	}
	r.pos += n
	return true
}

// take 取出接下来的 n 字节切片（不复制，引用原 buf）。
func (r *reader) take(n int) ([]byte, bool) {
	if n < 0 || r.remaining() < n {
		return nil, false
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, true
}

// skipVec8 跳过一个“1 字节长度前缀 + 数据”的向量。
func (r *reader) skipVec8() bool {
	n, ok := r.u8()
	if !ok {
		return false
	}
	return r.skip(int(n))
}

// skipVec16 跳过一个“2 字节长度前缀 + 数据”的向量。
func (r *reader) skipVec16() bool {
	n, ok := r.u16()
	if !ok {
		return false
	}
	return r.skip(int(n))
}

// takeVec16 取出一个“2 字节长度前缀 + 数据”的向量的数据部分。
func (r *reader) takeVec16() ([]byte, bool) {
	n, ok := r.u16()
	if !ok {
		return nil, false
	}
	return r.take(int(n))
}
