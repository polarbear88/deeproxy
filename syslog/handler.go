package syslog

import (
	"context"
	"log/slog"
	"time"
)

// handler.go 实现写入内存环形缓冲的 slog.Handler，以及对外的日志缓冲门面 LogBuffer。
//
// 用法：用 NewLogBuffer 建缓冲 → 取 Handler() 接入 slog（可与 stderr Handler 用 multi 组合）→
// API 层用 Snapshot()/Subscribe() 提供历史快照与 SSE 实时推送。

// LogEntry 是一条系统日志（仅内存，对应 spec LogEntry 实体）。
type LogEntry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`   // debug/info/warn/error（小写）
	Message string         `json:"message"` // 日志正文
	Fields  map[string]any `json:"fields,omitempty"`
}

// LogBuffer 是系统日志的内存缓冲门面，封装环形缓冲与订阅。
type LogBuffer struct {
	ring *ringBuffer[LogEntry]
}

// NewLogBuffer 创建容量为 capacity 的日志缓冲（<=0 用默认 5000）。
func NewLogBuffer(capacity int) *LogBuffer {
	return &LogBuffer{ring: newRingBuffer[LogEntry](capacity)}
}

// Snapshot 返回当前缓冲全部日志（最旧→最新），可选按级别过滤（level 为空则全部）。
func (b *LogBuffer) Snapshot(level string) []LogEntry {
	all := b.ring.snapshot()
	if level == "" {
		return all
	}
	out := make([]LogEntry, 0, len(all))
	for _, e := range all {
		if e.Level == level {
			out = append(out, e)
		}
	}
	return out
}

// Subscribe 注册实时订阅，返回新日志 channel、done channel 与注销函数（SSE handler 用）。
// data channel 不会被 close；消费者应监听 done channel 判断订阅结束。
func (b *LogBuffer) Subscribe(bufSize int) (<-chan LogEntry, <-chan struct{}, func()) {
	return b.ring.subscribe(bufSize)
}

// Len 返回当前日志条数（测试用）。
func (b *LogBuffer) Len() int { return b.ring.len() }

// Handler 返回写入本缓冲的 slog.Handler，可接入 slog 日志链路。
func (b *LogBuffer) Handler() slog.Handler {
	return &bufferHandler{buf: b}
}

// bufferHandler 是把 slog 记录写入内存环形缓冲的 slog.Handler。
//
// 它仅做内存写（push），不落库、不阻塞；level 始终放行（缓冲内保留全部级别，
// 由前端/API 按级别筛选），故 Enabled 恒为 true。
//
// 分组语义（与标准库一致）：WithGroup 只影响其之后添加的属性。故 attrs 在 WithAttrs/Handle
// 捕获时即按“当时的 group 前缀”解析好 key 存入，不在 Handle 阶段对历史属性追加前缀。
type bufferHandler struct {
	buf       *LogBuffer
	preFields map[string]any // 经 WithAttrs 累积、已带正确前缀的属性
	group     string         // 当前分组前缀（作用于之后添加的属性）
}

// Enabled 恒为 true：缓冲保留全部级别，筛选交给读取侧。
func (h *bufferHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle 把一条 slog 记录转为 LogEntry 写入环形缓冲。
func (h *bufferHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make(map[string]any, r.NumAttrs()+len(h.preFields))
	// 先并入 WithAttrs 累积的（已带前缀的）属性
	for k, v := range h.preFields {
		fields[k] = v
	}
	// 再并入本条记录的属性（按当前 group 前缀解析）
	r.Attrs(func(a slog.Attr) bool {
		fields[h.prefixed(a.Key)] = a.Value.Any()
		return true
	})

	h.buf.ring.push(LogEntry{
		Time:    r.Time,
		Level:   levelString(r.Level),
		Message: r.Message,
		Fields:  fields,
	})
	return nil
}

// prefixed 按当前 group 给字段名加前缀（group 为空则原样）。
func (h *bufferHandler) prefixed(key string) string {
	if h.group == "" {
		return key
	}
	return h.group + "." + key
}

// WithAttrs 返回带累积属性的新 handler；属性 key 在此即按当前 group 前缀固化（slog 约定克隆）。
func (h *bufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	// 拷贝 preFields，避免父子 handler 共享底层 map。
	nh.preFields = make(map[string]any, len(h.preFields)+len(attrs))
	for k, v := range h.preFields {
		nh.preFields[k] = v
	}
	for _, a := range attrs {
		nh.preFields[h.prefixed(a.Key)] = a.Value.Any()
	}
	return &nh
}

// WithGroup 返回带分组前缀的新 handler（只影响之后添加的属性）。
func (h *bufferHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	if nh.group == "" {
		nh.group = name
	} else {
		nh.group = nh.group + "." + name
	}
	return &nh
}

// levelString 把 slog.Level 转为小写级别字符串（与前端筛选值一致）。
func levelString(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}
