// 本文件实现分组（代理组）与其上游代理的 CRUD（AC-21/28/18/38）。
//
// 端点/载荷对齐前端契约（web/README.md）：
//   - 分组响应含嵌套 healthCheck 对象 + 今日流量(today*)。
//   - 上游响应 healthState 为三态字符串(healthy/unhealthy/unknown) + latencyMs。
//   - 上游为嵌套路由 /groups/:id/upstreams/:uid，含 toggle 与 test。
//
// 写操作 commit 后统一调用 rebuildAndSwap 刷新转发侧快照（G4 回滚封装）。
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"deeproxy/store"
)

// —— 请求体 ——

// healthCheckReq 是分组健康检查配置（嵌套对象，对齐前端 group.healthCheck）。
type healthCheckReq struct {
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"` // ping / url
	URL              string `json:"url"`
	IntervalSec      int    `json:"intervalSec"`
	FailThreshold    int    `json:"failThreshold"`
	RecoverThreshold int    `json:"recoverThreshold"`
}

// groupReq 是分组创建/更新请求体。
type groupReq struct {
	Name        string         `json:"name"`
	Remark      string         `json:"remark"`
	Type        string         `json:"type"` // A / B
	HealthCheck healthCheckReq `json:"healthCheck"`
}

// toModel 把请求体映射为 store.Group（id 单独由调用方设置）。
func (r groupReq) toModel() store.Group {
	return store.Group{
		Name:       r.Name,
		Remark:     r.Remark,
		Type:       store.GroupType(r.Type),
		HCEnabled:  r.HealthCheck.Enabled,
		HCMode:     store.HealthMode(r.HealthCheck.Mode),
		HCURL:      r.HealthCheck.URL,
		HCInterval: r.HealthCheck.IntervalSec,
		HCFailThld: r.HealthCheck.FailThreshold,
		HCRecvThld: r.HealthCheck.RecoverThreshold,
	}
}

func validateGroupType(t string) bool {
	return t == string(store.TypeA) || t == string(store.TypeB)
}

// —— 响应体 ——

// groupResp 是分组响应（嵌套 healthCheck + 今日流量）。
type groupResp struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Remark      string         `json:"remark"`
	Type        string         `json:"type"`
	HealthCheck healthCheckReq `json:"healthCheck"`
	TodayUp     int64          `json:"todayUp"`
	TodayDown   int64          `json:"todayDown"`
	TodayReq    int64          `json:"todayReq"`
}

// toGroupResp 把 store.Group 映射为响应（today* 由调用方按需查 store 填充）。
func toGroupResp(g store.Group, todayUp, todayDown, todayReq int64) groupResp {
	return groupResp{
		ID:     g.ID,
		Name:   g.Name,
		Remark: g.Remark,
		Type:   string(g.Type),
		HealthCheck: healthCheckReq{
			Enabled:          g.HCEnabled,
			Mode:             string(g.HCMode),
			URL:              g.HCURL,
			IntervalSec:      g.HCInterval,
			FailThreshold:    g.HCFailThld,
			RecoverThreshold: g.HCRecvThld,
		},
		TodayUp:   todayUp,
		TodayDown: todayDown,
		TodayReq:  todayReq,
	}
}

func (a *App) handleListGroups(c *gin.Context) {
	groups, err := a.store.ListGroups()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取分组失败: "+err.Error())
		return
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	out := make([]groupResp, 0, len(groups))
	for _, g := range groups {
		// 各分组今日流量（按 groupId 维度聚合）。
		tot, err := a.store.QueryTotals(dayStart, now, g.ID, 0)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "读取分组今日统计失败: "+err.Error())
			return
		}
		out = append(out, toGroupResp(g, tot.UpBytes, tot.DownBytes, tot.ReqCount))
	}
	respondOK(c, out)
}

func (a *App) handleCreateGroup(c *gin.Context) {
	var req groupReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "分组名不能为空")
		return
	}
	if !validateGroupType(req.Type) {
		respondError(c, http.StatusBadRequest, "分组类型非法（应为 A 或 B）")
		return
	}
	// 重名预校验：分组名在快照中作为路由键（LookupGroup(name)），重名会静默覆盖导致
	// 请求路由到非预期分组（潜在越权）。DB 层已加 UNIQUE 兜底，这里先查给出清晰 409。
	if existing, err := a.store.GetGroupByName(req.Name); err != nil {
		respondError(c, http.StatusInternalServerError, "校验分组名失败: "+err.Error())
		return
	} else if existing != nil {
		respondError(c, http.StatusConflict, "分组名已存在: "+req.Name)
		return
	}
	g := req.toModel()
	if err := a.store.CreateGroup(&g); err != nil {
		respondError(c, http.StatusInternalServerError, "新增分组失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("创建分组", "id", g.ID, "name", g.Name, "type", string(g.Type))
	respondOK(c, toGroupResp(g, 0, 0, 0))
}

func (a *App) handleUpdateGroup(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req groupReq
	if !bindJSON(c, &req) {
		return
	}
	if !validateGroupType(req.Type) {
		respondError(c, http.StatusBadRequest, "分组类型非法（应为 A 或 B）")
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "分组名不能为空")
		return
	}
	// 改名重名校验：允许保留自身名字，但不得与「其它」分组撞名（同名→路由键覆盖）。
	if existing, err := a.store.GetGroupByName(req.Name); err != nil {
		respondError(c, http.StatusInternalServerError, "校验分组名失败: "+err.Error())
		return
	} else if existing != nil && existing.ID != id {
		respondError(c, http.StatusConflict, "分组名已存在: "+req.Name)
		return
	}
	g := req.toModel()
	g.ID = id
	if err := a.store.UpdateGroup(&g); err != nil {
		respondError(c, http.StatusInternalServerError, "更新分组失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("更新分组", "id", g.ID, "name", g.Name)
	respondOK(c, toGroupResp(g, 0, 0, 0))
}

func (a *App) handleDeleteGroup(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := a.store.DeleteGroup(id); err != nil {
		respondError(c, http.StatusInternalServerError, "删除分组失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("删除分组", "id", id)
	respondOK(c, nil)
}

// —— 上游代理（Type B 分组下，嵌套路由）——

// upstreamReq 是上游代理创建/更新请求体。
type upstreamReq struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	User             string `json:"user"`
	UsernameTemplate string `json:"usernameTemplate"`
	Pwd              string `json:"pwd"`
	Weight           int    `json:"weight"`
	Enabled          bool   `json:"enabled"`
}

// upstreamResp 是上游响应（healthState 三态字符串 + latencyMs，对齐前端）。
type upstreamResp struct {
	ID               int64  `json:"id"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	User             string `json:"user"`
	UsernameTemplate string `json:"usernameTemplate"`
	Pwd              string `json:"pwd"`
	Weight           int    `json:"weight"`
	Enabled          bool   `json:"enabled"`
	HealthState      string `json:"healthState"` // healthy / unhealthy / unknown
	LatencyMs        int64  `json:"latencyMs"`
}

// toUpstreamResp 把 store.UpstreamProxy 映射为响应。
// healthState：未启用 → unknown；启用且 HealthState=true → healthy；否则 unhealthy。
func (a *App) toUpstreamResp(u store.UpstreamProxy) upstreamResp {
	state := "unknown"
	if u.Enabled {
		if u.HealthState {
			state = "healthy"
		} else {
			state = "unhealthy"
		}
	}
	return upstreamResp{
		ID:               u.ID,
		Host:             u.Host,
		Port:             u.Port,
		User:             u.User,
		UsernameTemplate: u.UsernameTemplate,
		Pwd:              u.Pwd,
		Weight:           u.Weight,
		Enabled:          u.Enabled,
		HealthState:      state,
		LatencyMs:        a.health.LatencyMs(u.ID),
	}
}

func (a *App) handleListUpstreams(c *gin.Context) {
	gid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	ups, err := a.store.ListUpstreamsByGroup(gid)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取上游失败: "+err.Error())
		return
	}
	out := make([]upstreamResp, 0, len(ups))
	for _, u := range ups {
		out = append(out, a.toUpstreamResp(u))
	}
	respondOK(c, out)
}

func (a *App) handleCreateUpstream(c *gin.Context) {
	gid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req upstreamReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Host == "" || req.Port <= 0 {
		respondError(c, http.StatusBadRequest, "上游 host/port 非法")
		return
	}
	if req.Weight <= 0 {
		req.Weight = 1 // 权重缺省为 1，避免 SWRR 除零/无份额
	}
	u := store.UpstreamProxy{
		GroupID:          gid,
		Host:             req.Host,
		Port:             req.Port,
		User:             req.User,
		UsernameTemplate: req.UsernameTemplate,
		Pwd:              req.Pwd,
		Weight:           req.Weight,
		Enabled:          req.Enabled,
		HealthState:      true, // 新增上游初值健康，待健康检查 worker 校正
	}
	if err := a.store.CreateUpstream(&u); err != nil {
		respondError(c, http.StatusInternalServerError, "新增上游失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("创建上游", "id", u.ID, "groupId", u.GroupID, "host", u.Host, "port", u.Port)
	respondOK(c, a.toUpstreamResp(u))
}

func (a *App) handleUpdateUpstream(c *gin.Context) {
	uid, ok := parseIDParam(c, "uid")
	if !ok {
		return
	}
	old, err := a.store.GetUpstream(uid)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取上游失败: "+err.Error())
		return
	}
	if old == nil {
		respondError(c, http.StatusNotFound, "上游不存在")
		return
	}
	var req upstreamReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Weight <= 0 {
		req.Weight = 1
	}
	old.Host = req.Host
	old.Port = req.Port
	old.User = req.User
	old.UsernameTemplate = req.UsernameTemplate
	old.Pwd = req.Pwd
	old.Weight = req.Weight
	old.Enabled = req.Enabled
	if err := a.store.UpdateUpstream(old); err != nil {
		respondError(c, http.StatusInternalServerError, "更新上游失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("更新上游", "id", old.ID, "host", old.Host, "port", old.Port)
	respondOK(c, a.toUpstreamResp(*old))
}

func (a *App) handleDeleteUpstream(c *gin.Context) {
	uid, ok := parseIDParam(c, "uid")
	if !ok {
		return
	}
	if err := a.store.DeleteUpstream(uid); err != nil {
		respondError(c, http.StatusInternalServerError, "删除上游失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("删除上游", "id", uid)
	respondOK(c, nil)
}

// setEnabledReq 是手动启停上游的请求体（AC-18）。
type setEnabledReq struct {
	Enabled bool `json:"enabled"`
}

// handleSetUpstreamEnabled 手动启用/禁用单条上游（AC-18，路由 .../toggle）。
func (a *App) handleSetUpstreamEnabled(c *gin.Context) {
	uid, ok := parseIDParam(c, "uid")
	if !ok {
		return
	}
	var req setEnabledReq
	if !bindJSON(c, &req) {
		return
	}
	if err := a.store.SetUpstreamEnabled(uid, req.Enabled); err != nil {
		respondError(c, http.StatusInternalServerError, "设置上游启停失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("手动启停上游", "id", uid, "enabled", req.Enabled)
	respondOK(c, nil)
}

// testUpstreamResp 是测试连接结果（AC-38）。
type testUpstreamResp struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

// handleTestUpstream 立即对单条已存在上游发起一次探测（AC-38，路由 .../test）。
// 探测方式取所属分组的健康检查配置（mode/url），无则用系统默认。
func (a *App) handleTestUpstream(c *gin.Context) {
	uid, ok := parseIDParam(c, "uid")
	if !ok {
		return
	}
	up, err := a.store.GetUpstream(uid)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取上游失败: "+err.Error())
		return
	}
	if up == nil {
		respondError(c, http.StatusNotFound, "上游不存在")
		return
	}

	// 探测参数：优先用所属分组的 HC 配置，缺省用系统默认。
	mode := store.HealthURL
	probeURL := ""
	if g, gerr := a.store.GetGroup(up.GroupID); gerr == nil && g != nil {
		if g.HCMode != "" {
			mode = g.HCMode
		}
		probeURL = g.HCURL
	}
	if probeURL == "" {
		if ss, serr := a.store.GetSystemSetting(); serr == nil {
			probeURL = ss.HCDefaultURL
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	res := a.health.TestProxy(ctx, *up, mode, probeURL)
	a.logger.Info("代理测试连接", "upstreamId", up.ID, "host", up.Host, "ok", res.OK, "latencyMs", res.Latency.Milliseconds())
	respondOK(c, testUpstreamResp{OK: res.OK, LatencyMs: res.Latency.Milliseconds(), Error: res.Err})
}
