package syslog

import "time"

// audit.go 实现连接审计日志的内存环形缓冲（AC-36）：
// 记录最近 N 条连接（时间/user/group/目标/动作/上游/上下行字节），仅内存、不落库，用于排障。
//
// 埋点时机（由 server 装配层调用，非字节中继循环内）：连接结束时调 AuditBuffer.Record 写一条，
// 字节数取自 stats 计数或中继返回值。本缓冲与转发热路径解耦。

// AuditEntry 是一条连接审计记录（对应 spec ConnAuditEntry 实体）。
type AuditEntry struct {
	Time      time.Time `json:"time"`
	User      string    `json:"user"`     // 代理用户名
	Group     string    `json:"group"`    // 分组名
	Target    string    `json:"target"`   // 目标地址（域名/IP:port）
	Action    string    `json:"action"`   // forward/direct/reject
	Upstream  string    `json:"upstream"` // 实际使用的上游（forward 时；direct/reject 为空）
	UpBytes   int64     `json:"upBytes"`  // 上行字节
	DownBytes int64     `json:"downBytes"`// 下行字节
}

// AuditBuffer 是连接审计的内存缓冲门面。
type AuditBuffer struct {
	ring *ringBuffer[AuditEntry]
}

// NewAuditBuffer 创建容量为 capacity 的审计缓冲（<=0 用默认 5000）。
func NewAuditBuffer(capacity int) *AuditBuffer {
	return &AuditBuffer{ring: newRingBuffer[AuditEntry](capacity)}
}

// Record 写入一条审计记录（连接结束时调用）。Time 为空时自动填当前时间。
func (b *AuditBuffer) Record(e AuditEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.ring.push(e)
}

// Snapshot 返回当前全部审计记录（最旧→最新）。
func (b *AuditBuffer) Snapshot() []AuditEntry {
	return b.ring.snapshot()
}

// Subscribe 注册实时订阅（如前端审计实时刷新）。返回数据 channel、done channel 与注销函数。
func (b *AuditBuffer) Subscribe(bufSize int) (<-chan AuditEntry, <-chan struct{}, func()) {
	return b.ring.subscribe(bufSize)
}

// Len 返回当前审计条数（测试用）。
func (b *AuditBuffer) Len() int { return b.ring.len() }
