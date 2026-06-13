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

	ch, done, cancel := a.logs.Subscribe(64) // 缓冲 64，订阅端慢消费时按环形缓冲语义丢弃最旧
	defer cancel()

	clientGone := c.Request.Context().Done()
	c.Stream(func(w io.Writer) bool {
		select {
		case <-clientGone:
			return false // 客户端断开，结束流
		case <-done:
			return false // 订阅已注销
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
