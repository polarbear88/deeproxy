// 本文件实现仪表盘聚合 API（AC-24/27），端点形态对齐前端契约（web/README.md）。
//
// 数据来源分层（spec 性能约束）：
//   - 实时区：读内存 stats.Counter 瞬时值；上/下行速率由 API 层按采样差值算出（免改 stats）。
//   - 今日/历史区：读 SQLite 聚合时间桶（今日汇总、时序图、TopN）——非热路径。
//   - 运行健康区：runtime 内存/goroutine + 当前快照各分组健康/总上游数。
package api

import (
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// rateSampler 记录上次累计字节采样，用于在 overview 请求间算出瞬时速率（KB/s）。
// 之所以放 API 层而非 stats：stats 只维护累计值，速率是展示侧需求，API 自存上次采样最简洁。
type rateSampler struct {
	mu       sync.Mutex
	lastUp   int64
	lastDown int64
	lastTS   time.Time
}

// sample 传入当前累计上/下行字节，返回自上次采样以来的速率（字节/秒）。
// 首次采样无基线 → 速率 0。
func (r *rateSampler) sample(up, down int64) (upRate, downRate float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if !r.lastTS.IsZero() {
		if dt := now.Sub(r.lastTS).Seconds(); dt > 0 {
			upRate = float64(up-r.lastUp) / dt
			downRate = float64(down-r.lastDown) / dt
			if upRate < 0 {
				upRate = 0 // 计数器重置等异常情况兜底
			}
			if downRate < 0 {
				downRate = 0
			}
		}
	}
	r.lastUp, r.lastDown, r.lastTS = up, down, now
	return upRate, downRate
}

// overviewResp 是仪表盘概览（实时 + 今日扁平字段，对齐前端 dashboard.js）。
type overviewResp struct {
	UpRate          float64 `json:"upRate"`          // 上行速率（字节/秒；前端 formatRate 展示）
	DownRate        float64 `json:"downRate"`        // 下行速率（字节/秒）
	ActiveConns     int64   `json:"activeConns"`     // 当前活跃连接
	TodayUp         int64   `json:"todayUp"`         // 今日上行字节
	TodayDown       int64   `json:"todayDown"`       // 今日下行字节
	TodayReq        int64   `json:"todayReq"`        // 今日请求数
	TodayRejectRule int64   `json:"todayRejectRule"` // 今日规则拒连（累计瞬时值）
	TodayRejectAuth int64   `json:"todayRejectAuth"` // 今日鉴权拒连（累计瞬时值）
	HealthyProxies  int     `json:"healthyProxies"`  // 健康上游总数
	TotalProxies    int     `json:"totalProxies"`    // 上游总数
	UptimeSec       int64   `json:"uptimeSec"`       // 运行时长（秒）
}

// handleDashboardOverview 返回实时+今日扁平概览（AC-24）。
func (a *App) handleDashboardOverview(c *gin.Context) {
	rt := a.stats.RealtimeSnapshot()

	// 今日累计字节（用于速率采样基线 + 今日展示）。
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	totals, err := a.store.QueryTotals(dayStart, now, 0, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取今日统计失败: "+err.Error())
		return
	}

	// 速率：用「今日累计字节」做采样差值（两次 overview 间）。
	upRate, downRate := a.rate.sample(totals.UpBytes, totals.DownBytes)

	// 健康上游汇总（内存快照）。
	snap := a.holder.Load()
	healthy, total := 0, 0
	for _, gv := range snap.Groups() {
		healthy += len(gv.HealthyUpstreams)
		total += len(gv.AllUpstreams)
	}

	respondOK(c, overviewResp{
		UpRate:          upRate,
		DownRate:        downRate,
		ActiveConns:     rt.ActiveConns,
		TodayUp:         totals.UpBytes,
		TodayDown:       totals.DownBytes,
		TodayReq:        totals.ReqCount,
		TodayRejectRule: rt.RejectRule,
		TodayRejectAuth: rt.RejectAuth,
		HealthyProxies:  healthy,
		TotalProxies:    total,
		UptimeSec:       int64(time.Since(a.startedAt).Seconds()),
	})
}

// timeSeriesResp 是时序响应（列式，对齐前端 ECharts 用法）。
type timeSeriesResp struct {
	Times []string `json:"times"`
	Up    []int64  `json:"up"`
	Down  []int64  `json:"down"`
	Req   []int64  `json:"req"`
}

// handleTimeSeries 返回流量/请求数时序（window=1h/24h/7d，AC-27）。
func (a *App) handleTimeSeries(c *gin.Context) {
	dur, downsampleHour, ok := parseWindow(c)
	if !ok {
		return
	}
	var groupID int64
	if gid := c.Query("groupId"); gid != "" {
		id, ok := parseQueryID(c, "groupId")
		if !ok {
			return
		}
		groupID = id
	}

	end := time.Now()
	points, err := a.store.QueryTimeSeries(end.Add(-dur), end, groupID, 0, downsampleHour)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取时序统计失败: "+err.Error())
		return
	}
	// 行式 → 列式。
	resp := timeSeriesResp{
		Times: make([]string, 0, len(points)),
		Up:    make([]int64, 0, len(points)),
		Down:  make([]int64, 0, len(points)),
		Req:   make([]int64, 0, len(points)),
	}
	for _, p := range points {
		resp.Times = append(resp.Times, p.Bucket)
		resp.Up = append(resp.Up, p.UpBytes)
		resp.Down = append(resp.Down, p.DownBytes)
		resp.Req = append(resp.Req, p.ReqCount)
	}
	respondOK(c, resp)
}

// distItem 是动作分布/排行的通用 name-value 项。
type distItem struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// handleActionDist 返回动作分布饼图数据（forward/direct/reject 占比，AC-27）。
func (a *App) handleActionDist(c *gin.Context) {
	rt := a.stats.RealtimeSnapshot()
	respondOK(c, []distItem{
		{Name: "forward", Value: rt.ActForward},
		{Name: "direct", Value: rt.ActDirect},
		{Name: "reject", Value: rt.ActReject},
	})
}

// topItem 是排行项；bytes 用于 group/user，count 用于 domain（前端按 kind 取用）。
type topItem struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes,omitempty"`
	Count int64  `json:"count,omitempty"`
}

// handleTop 返回 Top N 排行（kind=group|user|domain，AC-27）。
//
// 依赖现状：store 当前仅有 QueryTopGroups。user/domain 维度需 store 增查询 +
// 目标域名埋点（属 T5/T6，见 api/CONTRACT.md 依赖缺口），首版返回空数组占位，
// 前端正常渲染空排行，待埋点补齐后接入。
func (a *App) handleTop(c *gin.Context) {
	kind := c.Query("kind")
	limit := 10

	end := time.Now()
	dayStart := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, end.Location())

	switch kind {
	case "group":
		tops, err := a.store.QueryTopGroups(dayStart, end, limit)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "读取 Top 分组失败: "+err.Error())
			return
		}
		snap := a.holder.Load()
		out := make([]topItem, 0, len(tops))
		for _, t := range tops {
			name := ""
			if gv, ok := snap.GroupByID(t.GroupID); ok {
				name = gv.Name
			}
			out = append(out, topItem{Name: name, Bytes: t.UpBytes + t.DownBytes})
		}
		respondOK(c, out)
	case "user":
		// kind=user：store.QueryTopUsers 已由 worker-1 提供（按 user_id 聚合）。
		tops, err := a.store.QueryTopUsers(dayStart, end, limit)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "读取 Top 用户失败: "+err.Error())
			return
		}
		// 反查 user_id → username（后台读，直接走 store；构建 id→name 映射避免 N+1）。
		users, err := a.store.ListProxyUsers()
		if err != nil {
			respondError(c, http.StatusInternalServerError, "读取用户失败: "+err.Error())
			return
		}
		nameByID := make(map[int64]string, len(users))
		for _, u := range users {
			nameByID[u.ID] = u.Username
		}
		out := make([]topItem, 0, len(tops))
		for _, t := range tops {
			out = append(out, topItem{Name: nameByID[t.UserID], Bytes: t.UpBytes + t.DownBytes})
		}
		respondOK(c, out)
	case "domain":
		// 依赖缺口（首版未实现，team-lead 已确认记为已知后续项）：
		//   - kind=domain 需对 CONNECT 目标域名【按连接维度】埋点；traffic_stat 是 group/user
		//     分钟聚合桶、无 domain 维度，需另加 domain 计数表/内存 TopK（属 T5/T6 后续）。
		// 保持数组契约 + 响应头显式标注"首版未实现"，前端据此显示"首版暂不支持"占位。
		c.Header("X-Feature-Status", "not-implemented")
		respondOK(c, []topItem{})
	default:
		respondError(c, http.StatusBadRequest, "kind 非法（应为 group/user/domain）")
	}
}

// runtimeGroup 是运行健康区单分组项。
type runtimeGroup struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Healthy int    `json:"healthy"`
	Total   int    `json:"total"`
	AllDown bool   `json:"allDown"`
}

// runtimeResp 是运行健康区响应（内存/goroutine + 各组健康）。
type runtimeResp struct {
	MemMB      uint64         `json:"memMB"`
	Goroutines int            `json:"goroutines"`
	Groups     []runtimeGroup `json:"groups"`
}

// handleRuntime 返回运行时健康（内存/goroutine/各组健康概览，AC-27 运行健康区）。
func (a *App) handleRuntime(c *gin.Context) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	snap := a.holder.Load()
	var groups []runtimeGroup
	for _, gv := range snap.Groups() {
		total := len(gv.AllUpstreams)
		healthy := len(gv.HealthyUpstreams)
		groups = append(groups, runtimeGroup{
			ID:      gv.ID,
			Name:    gv.Name,
			Healthy: healthy,
			Total:   total,
			AllDown: string(gv.Type) == "B" && total > 0 && healthy == 0,
		})
	}

	respondOK(c, runtimeResp{
		MemMB:      ms.Alloc / (1024 * 1024),
		Goroutines: runtime.NumGoroutine(),
		Groups:     groups,
	})
}

// parseWindow 解析 window 查询参数为时长 + 是否小时降采样。
func parseWindow(c *gin.Context) (dur time.Duration, downsampleHour bool, ok bool) {
	switch c.Query("window") {
	case "1h":
		return time.Hour, false, true
	case "24h":
		return 24 * time.Hour, false, true
	case "7d":
		return 7 * 24 * time.Hour, true, true // 7d 查询期降采样到小时（M3）
	default:
		respondError(c, http.StatusBadRequest, "window 非法（应为 1h/24h/7d）")
		return 0, false, false
	}
}
