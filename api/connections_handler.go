// 本文件实现「实时连接」列表 API：返回当前活跃 SOCKS5 连接的有界 Top-N 快照。
// 数据源是 connreg.Registry（SOCKS5 服务端在连接开始/结束登记/注销）。被拒绝的连接
// 在规则判定阶段即关闭、从不进入活跃列表，故 action 仅 forward/direct。
package api

import (
	"github.com/gin-gonic/gin"

	"deeproxy/connreg"
)

// connItemResp 是单条活跃连接的响应体（snake_case 对齐前端契约）。
type connItemResp struct {
	ID          int64  `json:"id"`
	Target      string `json:"target"`       // 目标主机
	Action      string `json:"action"`       // forward/direct
	Upstream    string `json:"upstream"`     // 实际上游地址；"" 表示未知/直连（前端渲染 —）
	User        string `json:"user"`         // 代理用户名
	Group       string `json:"group"`        // 分组名
	Client      string `json:"client"`       // 客户端来源地址
	StartTs     int64  `json:"start_ts"`     // 连接开始时间（unix 秒）
	DurationSec int64  `json:"duration_sec"` // 连接时长（秒）
}

// connListResp 是实时连接列表的响应体。
type connListResp struct {
	Items     []connItemResp `json:"items"`
	Total     int            `json:"total"`     // 当前活跃连接总数（精确，可能 > len(items)）
	Limit     int            `json:"limit"`     // 本次返回上限
	Truncated bool           `json:"truncated"` // total > limit 时为 true（前端显示"显示 X / 共 Y 条"）
}

// handleListConnections 返回有界 Top-N 活跃连接快照。
// query: limit（默认 connreg.DefaultLimit，由 Snapshot 内部钳制）、sort（start|duration，默认 start）。
func (a *App) handleListConnections(c *gin.Context) {
	limit := atoiDefault(c.Query("limit"), connreg.DefaultLimit)
	sort := c.DefaultQuery("sort", "start") // 未知值由 Snapshot 内部归一为 start
	views, total, truncated := a.connReg.Snapshot(limit, sort)

	items := make([]connItemResp, 0, len(views))
	for _, v := range views {
		items = append(items, connItemResp{
			ID:          v.ID,
			Target:      v.Target,
			Action:      v.Action,
			Upstream:    v.Upstream,
			User:        v.User,
			Group:       v.Group,
			Client:      v.Client,
			StartTs:     v.StartUnix,
			DurationSec: v.DurationSec,
		})
	}
	respondOK(c, connListResp{Items: items, Total: total, Limit: limit, Truncated: truncated})
}
