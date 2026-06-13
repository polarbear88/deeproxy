// 本文件实现系统日志与连接审计 API（AC-33/34/36）。
//
// 系统日志：仅内存环形缓冲（syslog.LogBuffer），不落库、不查历史、重启丢失。
//   - GET /api/syslog：返回缓冲区当前快照，支持按级别筛选（AC-33）。
//   - GET /api/syslog/stream：SSE 实时推送新日志（AC-34）。
//
// 连接审计：内存环形缓冲（syslog.AuditBuffer），排障用（AC-36）。
package api

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
)

// validLogLevels 是允许的日志级别筛选值（空表示全部）。
var validLogLevels = map[string]bool{"": true, "debug": true, "info": true, "warn": true, "error": true}

// handleLogsSnapshot 返回日志缓冲快照，可按级别筛选（AC-33）。
func (a *App) handleLogsSnapshot(c *gin.Context) {
	level := c.Query("level")
	if !validLogLevels[level] {
		respondError(c, http.StatusBadRequest, "level 非法（应为 debug/info/warn/error 或留空）")
		return
	}
	respondOK(c, a.logs.Snapshot(level))
}

// handleLogsSSE 通过 SSE 实时推送新日志（AC-34）。
//
// 关键对齐：前端用 EventSource.onmessage 接收，它只收【默认(无名)事件】。
// 因此这里用 sse.Encode 发 Event 名为空的默认事件（而非命名事件 "log"），
// 否则前端 onmessage 收不到。Data 为日志 JSON。
//
// 客户端断开（ctx.Done）或订阅注销（done）时退出并 cancel，避免 goroutine 泄漏。
// 可选 level 查询参数在推送侧做筛选（与快照接口一致）。
func (a *App) handleLogsSSE(c *gin.Context) {
	level := c.Query("level")
	if !validLogLevels[level] {
		respondError(c, http.StatusBadRequest, "level 非法（应为 debug/info/warn/error 或留空）")
		return
	}

	// SSE 标准响应头。
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// 禁用 nginx 等反向代理对响应体的缓冲：否则代理会攒够一段才下发，
	// 表现为浏览器端 SSE 一直 pending、收不到实时日志。直连场景下该头无副作用。
	c.Header("X-Accel-Buffering", "no")

	// 关键修复：立即把响应头 flush 给客户端。
	// 原因：c.Header 只是写入 header map，Gin 要等首次写出才真正发送 200 + 响应头。
	// 而下面的 Stream 一进去就阻塞在 channel 上等新日志，系统空闲时永不写出 →
	// 响应头迟迟不发 → 浏览器请求一直挂起(pending)、EventSource.onopen 不触发 →
	// 前端 connected 始终为 false，显示“未连接”。先发一个 SSE 注释行并 flush，
	// 让浏览器立刻收到响应头、触发 onopen，连接状态即时就绪。
	c.Writer.WriteHeaderNow()
	_, _ = io.WriteString(c.Writer, ": connected\n\n")
	c.Writer.Flush()

	ch, done, cancel := a.logs.Subscribe(64) // 缓冲 64，订阅端慢消费时按环形缓冲语义丢弃最旧
	defer cancel()

	// 心跳定时器：空闲时定期发送 SSE 注释行，既保活连接（避免被中间代理的
	// idle timeout 掐断），又能借写出失败感知客户端已断开，及时退出释放订阅。
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	clientGone := c.Request.Context().Done()
	c.Stream(func(w io.Writer) bool {
		select {
		case <-clientGone:
			return false // 客户端断开，结束流
		case <-done:
			return false // 订阅已注销
		case <-heartbeat.C:
			// 注释行(以冒号开头)，EventSource 会忽略，仅用于保活/探活。
			_, _ = io.WriteString(w, ": ping\n\n")
			return true
		case entry := <-ch:
			// 级别筛选：不匹配则跳过本条但继续保持连接。
			if level != "" && entry.Level != level {
				return true
			}
			// 默认(无名)事件，前端 onmessage 可收；Data 直接给结构体，sse 会 JSON 编码。
			_ = sse.Encode(w, sse.Event{Data: entry})
			return true
		}
	})
}

// handleAuditSnapshot 返回连接审计缓冲快照（AC-36）。
func (a *App) handleAuditSnapshot(c *gin.Context) {
	respondOK(c, a.audit.Snapshot())
}
