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
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"deeproxy/internal/netutil"
)

// overviewResp 是仪表盘概览（实时 + 今日扁平字段，对齐前端 dashboard.js）。
type overviewResp struct {
	UpRate          float64 `json:"upRate"`          // 上行速率（字节/秒；前端 formatRate 展示）
	DownRate        float64 `json:"downRate"`        // 下行速率（字节/秒）
	ActiveConns     int64   `json:"activeConns"`     // 当前活跃连接
	TodayUp         int64   `json:"todayUp"`         // 今日上行字节
	TodayDown       int64   `json:"todayDown"`       // 今日下行字节
	TodayReq        int64   `json:"todayReq"`        // 今日请求数
	TodayRejectRule int64   `json:"todayRejectRule"` // 今日规则拒连（按本地自然日归零，M2）
	TodayRejectAuth int64   `json:"todayRejectAuth"` // 今日鉴权拒连（按本地自然日归零，M2）
	HealthyProxies  int     `json:"healthyProxies"`  // 健康上游总数
	TotalProxies    int     `json:"totalProxies"`    // 上游总数
	UptimeSec       int64   `json:"uptimeSec"`       // 运行时长（秒）

	// —— 首页连接提示（AC-2.6/4.2）：服务器地址 + 监听端口，供首页展示端口与连接示例 ——
	ServerAddr string `json:"serverAddr"` // 服务器域名/IP（设置值优先，空则回探测的本机 IPv4）
	Socks5Port int    `json:"socks5Port"` // 本地 SOCKS5 监听端口
	WebPort    int    `json:"webPort"`    // Web 后台监听端口

	Version string `json:"version"` // deeproxy 构建版本号（健康卡片展示，编译期 ldflags 注入）
}

// handleDashboardOverview 返回实时+今日扁平概览（AC-24）。
func (a *App) handleDashboardOverview(c *gin.Context) {
	rt := a.stats.RealtimeSnapshot()

	// 今日累计字节（仅用于今日展示，来自 SQLite 聚合桶——非热路径历史区）。
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	totals, err := a.store.QueryTotals(dayStart, now, 0, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取今日统计失败: "+err.Error())
		return
	}

	// 实时速率（AC-5.5）：P4——改读由 flush worker 固定周期采样好的速率，不再在每次请求间差分。
	// 旧实现（a.rate.sample）把速率耦合到仪表盘轮询节奏：不轮询则无速率、多标签页并发互相
	// 污染基线。现由 stats.Counter 在 flush 周期内对累计字节差分一次、存入 atomic，本处只读。
	upRate := float64(rt.UpRateBps)
	downRate := float64(rt.DownRateBps)

	// 今日拒连（M2）：改读 Counter 的【今日】计数（按本地自然日归零），不再用进程累计瞬时值。
	// 拒连不落 SQLite 时间桶，故无法像今日流量那样从库聚合；Counter 内维护今日计数 + 跨日归零。
	todayRejRule, todayRejAuth := a.stats.TodayRejects()

	// 健康上游汇总（内存快照）。
	snap := a.holder.Load()
	healthy, total := 0, 0
	for _, gv := range snap.Groups() {
		healthy += len(gv.HealthyUpstreams)
		total += len(gv.AllUpstreams)
	}

	// 首页连接提示：服务器地址（设置值优先，空则探测本机 IPv4）+ 监听端口（来自启动配置）。
	// 本端点非转发热路径，额外读一次系统设置可接受。
	serverAddr := ""
	if ss, err := a.store.GetSystemSetting(); err == nil && ss != nil {
		serverAddr = ss.ServerAddr
	}
	if serverAddr == "" {
		serverAddr = netutil.DetectLocalIP()
	}

	respondOK(c, overviewResp{
		UpRate:          upRate,
		DownRate:        downRate,
		ActiveConns:     rt.ActiveConns,
		TodayUp:         totals.UpBytes,
		TodayDown:       totals.DownBytes,
		TodayReq:        totals.ReqCount,
		TodayRejectRule: todayRejRule,
		TodayRejectAuth: todayRejAuth,
		HealthyProxies:  healthy,
		TotalProxies:    total,
		UptimeSec:       int64(time.Since(a.startedAt).Seconds()),
		ServerAddr:      serverAddr,
		Socks5Port:      portOf(a.cfg.Listen),
		WebPort:         portOf(a.cfg.AdminListen),
		Version:         a.version,
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

// topItem 是排行项；group/user 用 bytes，domain 同时带 count（命中数）与 bytes（总流量）。
// 字段不加 omitempty：domain 命中已发生但字节尚未随连接关闭落库时 bytes 合法为 0，
// 显式返回 0 比省略字段更利于前端按 sort 维度直接取用（契约明确）。
type topItem struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Count int64  `json:"count"`
}

// handleTop 返回 Top N 排行（kind=group|user|domain，AC-27）。
//
// group/user 按 traffic_stat 总流量降序（topItem.bytes）；domain 按 domain_hit 命中次数
// 降序（topItem.count）。domain 支持可选 ?groupId= 过滤（缺省=全局）。
func (a *App) handleTop(c *gin.Context) {
	kind := c.Query("kind")
	// 前端可通过 ?limit= 动态指定返回条数；默认 10，上限钳制 100 防滥用。
	// domain 卡片改为 top50 列表后透传 limit:50，group/user 保持原来的 limit:5。
	limit := 10
	if lq := c.Query("limit"); lq != "" {
		if v, err := strconv.Atoi(lq); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}

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
			// 分组已删除（快照查不到）则跳过：删掉的分组不在流量 Top 排行里显示。
			gv, ok := snap.GroupByID(t.GroupID)
			if !ok {
				continue
			}
			out = append(out, topItem{Name: gv.Name, Bytes: t.UpBytes + t.DownBytes})
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
			// 用户已删除（映射里没有）则跳过：删掉的用户不在流量 Top 排行里显示。
			name, ok := nameByID[t.UserID]
			if !ok {
				continue
			}
			out = append(out, topItem{Name: name, Bytes: t.UpBytes + t.DownBytes})
		}
		respondOK(c, out)
	case "domain":
		// kind=domain：已落地。按 domain_hit 分钟桶按命中次数降序返回 Top N。
		// 可选 ?groupId=：缺省（<=0）表示全局合并所有分组；>0 仅统计该分组（镜像 handleTimeSeries）。
		var groupID int64
		if gid := c.Query("groupId"); gid != "" {
			id, ok := parseQueryID(c, "groupId")
			if !ok {
				return
			}
			groupID = id
		}
		// 可选 ?window=24h|7d：目标域名卡片支持滚动时间窗（默认 24h）。
		// 与 group/user 的「今日 dayStart→now」不同，domain 用「now-dur→now」滚动窗，
		// 这样 7d 能跨自然日聚合命中（缺省/非法值兜底 24h，避免对该卡片报 400）。
		domainStart := end.Add(-parseTopDomainWindow(c))
		// 可选 ?sort=count|bytes：排序维度（默认 count=命中数）。仅白名单两值，
		// 经 QueryTopDomains 内部映射为受控列表达式，绝不把原始参数拼进 SQL。
		tops, err := a.store.QueryTopDomains(domainStart, end, limit, groupID, parseTopDomainSort(c))
		if err != nil {
			respondError(c, http.StatusInternalServerError, "读取 Top 域名失败: "+err.Error())
			return
		}
		out := make([]topItem, 0, len(tops))
		for _, t := range tops {
			out = append(out, topItem{Name: t.Domain, Count: t.HitCount, Bytes: t.Bytes})
		}
		respondOK(c, out)
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

// parseTopDomainWindow 解析 Top 目标域名卡片的滚动时间窗，仅支持 24h / 7d，默认 24h。
//
// 与 parseWindow 不同：① 只接受 24h/7d 两档（该卡片需求）；② 缺省或非法值不报 400，
// 而是兜底 24h——因为该卡片是仪表盘常驻区，宁可降级到默认窗也不让整张卡片报错。
func parseTopDomainWindow(c *gin.Context) time.Duration {
	switch c.Query("window") {
	case "7d":
		return 7 * 24 * time.Hour
	case "24h":
		return 24 * time.Hour
	default:
		return 24 * time.Hour // 缺省/非法 → 默认 24h
	}
}

// parseTopDomainSort 解析 Top 目标域名卡片的排序维度，仅接受 count / bytes，默认 count。
//
// 与 parseTopDomainWindow 同理：常驻卡片宁可降级到默认值也不报 400。返回的字符串只会是
// "count" 或 "bytes"，由 store.QueryTopDomains 再做一次白名单映射到受控列表达式（双保险防注入）。
func parseTopDomainSort(c *gin.Context) string {
	if c.Query("sort") == "bytes" {
		return "bytes"
	}
	return "count" // 缺省/非法/"count" → 按命中数排序
}
