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
	"strings"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"

	"deeproxy/syslog"
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

// 审计分页常量：pageSize 默认 50、上限 200（防一次性拉取过多记录拖垮前端渲染与序列化）。
const (
	auditDefaultPageSize = 50
	auditMaxPageSize     = 200
)

// auditPage 是审计分页接口的返回结构（前端据此渲染 el-pagination）。
// total 为「筛选后」的真实条数，使前端分页器页数与筛选结果一致。
type auditPage struct {
	Items    []syslog.AuditEntry `json:"items"`    // 当前页记录（已按最新→最旧排序）
	Total    int                 `json:"total"`    // 筛选后总条数
	Page     int                 `json:"page"`     // 当前页码（从 1 起）
	PageSize int                 `json:"pageSize"` // 每页条数
}

// matchAudit 判定一条审计记录是否命中四维筛选（空参数表示该维不筛）。
// target 用子串模糊匹配（目标常含端口/子域，模糊更实用）；user/group/action 精确匹配。
func matchAudit(e syslog.AuditEntry, user, target, action, group string) bool {
	if user != "" && e.User != user {
		return false
	}
	if group != "" && e.Group != group {
		return false
	}
	if action != "" && e.Action != action {
		return false
	}
	if target != "" && !strings.Contains(e.Target, target) {
		return false
	}
	return true
}

// clampPaging 把原始 page/pageSize 钳制到安全范围：page 下限 1；pageSize <=0 归默认、上限 200。
func clampPaging(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = auditDefaultPageSize
	}
	if pageSize > auditMaxPageSize {
		pageSize = auditMaxPageSize
	}
	return page, pageSize
}

// reverseAudit 原地反转切片，把 buffer.snapshot() 的「最旧→最新」翻成「最新→最旧」展示序。
// 为什么在服务端反转：分页是服务端做的，前端每次只拿一页，无法对全集排序；必须在切片前反转。
func reverseAudit(s []syslog.AuditEntry) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// handleAuditSnapshot 返回连接审计缓冲快照（AC-36），支持服务端分页 + 四维筛选。
//
// 流程：取 ring 全量快照（最旧→最新）→ 四维筛选 → 反转为最新→最旧 → 安全切片分页 →
// 返回 {items,total,page,pageSize}。全程在内存 ring buffer 上操作，不引入数据库。
func (a *App) handleAuditSnapshot(c *gin.Context) {
	// 解析查询参数：page/pageSize 用 atoiDefault（空/非法归默认），四维筛选空串=不筛。
	page, pageSize := clampPaging(atoiDefault(c.Query("page"), 1), atoiDefault(c.Query("pageSize"), auditDefaultPageSize))
	user := c.Query("user")
	target := c.Query("target")
	action := c.Query("action")
	group := c.Query("group")

	// 取全量快照后按四维筛选（仅一次线性扫描）。
	all := a.audit.Snapshot()
	filtered := make([]syslog.AuditEntry, 0, len(all))
	for _, e := range all {
		if matchAudit(e, user, target, action, group) {
			filtered = append(filtered, e)
		}
	}

	// 反转为最新→最旧（稳定展示序）。
	reverseAudit(filtered)

	// 安全分页切片：start/end 均钳到 [0,total]，start>=total 时 items 为空切片，绝不越界 panic。
	total := len(filtered)
	start := min((page-1)*pageSize, total)
	end := min(start+pageSize, total)
	items := filtered[start:end]

	respondOK(c, auditPage{Items: items, Total: total, Page: page, PageSize: pageSize})
}
