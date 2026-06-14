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
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"deeproxy/auth"
	"deeproxy/pool/parse"
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
	// 分组名字符校验（组件⑦ AC-7.1）：仅允许英文字母与数字。根因见 auth.ValidIdentifier
	// 文件头注释——名字含 '-' 会破坏连接鉴权的 user-group 切分。新增路径恒校验。
	if !auth.ValidIdentifier(req.Name) {
		respondError(c, http.StatusBadRequest, "分组名只能包含英文字母与数字")
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
	// 「仅变更才校验」（组件⑦ R-7 / AC-7.4）：按 ID 取旧记录，仅当分组名实际改变时才做
	// 字符校验，避免强制迁移存量含特殊字符的旧分组。注意此处必须用 GetGroup(id)（WHERE id=?）
	// 取旧值，而非 GetGroupByName(req.Name)（后者按【新名】查、rename 后会取到错记录或 nil）。
	old, err := a.store.GetGroup(id)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取分组失败: "+err.Error())
		return
	}
	if old == nil {
		respondError(c, http.StatusNotFound, "分组不存在")
		return
	}
	if req.Name != old.Name && !auth.ValidIdentifier(req.Name) {
		respondError(c, http.StatusBadRequest, "分组名只能包含英文字母与数字")
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

// pagedUpstreamsResp 是分页上游响应（AC-3.3）。
type pagedUpstreamsResp struct {
	Items    []upstreamResp `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}

func (a *App) handleListUpstreams(c *gin.Context) {
	gid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}

	// 分页参数（AC-3.3）：传了 page 或 pageSize 即走服务端分页，返回 {items,total,page,pageSize}；
	// 都不传则保持旧契约返回扁平数组（向后兼容快照/旧前端）。
	pageStr := c.Query("page")
	pageSizeStr := c.Query("pageSize")
	keyword := c.Query("keyword")
	healthState := c.Query("healthState")

	if pageStr == "" && pageSizeStr == "" && keyword == "" && healthState == "" {
		// 旧契约：全量扁平数组。
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
		return
	}

	page := atoiDefault(pageStr, 1)
	pageSize := atoiDefault(pageSizeStr, 100)
	filter := store.UpstreamFilter{GroupID: gid, Keyword: keyword, HealthState: healthState}
	ups, total, err := a.store.ListUpstreamsPaged(filter, page, pageSize)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "分页读取上游失败: "+err.Error())
		return
	}
	items := make([]upstreamResp, 0, len(ups))
	for _, u := range ups {
		items = append(items, a.toUpstreamResp(u))
	}
	respondOK(c, pagedUpstreamsResp{Items: items, Total: total, Page: page, PageSize: pageSize})
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

// batchUpstreamReq 是批量添加上游的请求体（AC-3.1/3.2）。
//
// 前端最终契约：lines 为字符串数组（每元素一行）。为兼容旧用法仍保留 text（多行文本）：
// 二者皆可，lines 优先；都为空则无新增。统一默认值（weight/enabled/usernameTemplate）为可选。
type batchUpstreamReq struct {
	Lines []string `json:"lines"` // 每元素一行待解析的上游（前端最终契约）
	Text  string   `json:"text"`  // 兼容：多行文本（lines 为空时回退用它）
	// 以下为可选的统一默认值：批量行通常只含 host:port[+凭据]，权重/模板等用这些默认值统一套用。
	Weight           int    `json:"weight"`           // 统一权重（<=0 兜底 1）
	Enabled          *bool  `json:"enabled"`          // 统一启停（nil 默认启用）
	UsernameTemplate string `json:"usernameTemplate"` // 统一用户名模板（可空）
}

// batchUpstreamResp 是批量添加结果（前端最终契约）：ok=成功数，failed=失败明细（行号 + 原因）。
type batchUpstreamResp struct {
	OK     int                  `json:"ok"`
	Failed []batchFailureDetail `json:"failed"`
}

// batchFailureDetail 是单条失败明细（前端最终契约字段名 line/reason）。
type batchFailureDetail struct {
	Line   int    `json:"line"`   // 行号（从 1 开始）
	Reason string `json:"reason"` // 失败原因
}

// handleBatchCreateUpstreams 批量添加上游（AC-3.1/3.2，路由 POST /groups/:id/upstreams/batch）。
//
// 逐行用 pool/parse 多格式解析，失败行不中断、累计明细返回；成功行一次性入库后只 rebuild 一次。
func (a *App) handleBatchCreateUpstreams(c *gin.Context) {
	gid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req batchUpstreamReq
	if !bindJSON(c, &req) {
		return
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// lines 优先（前端最终契约）；为空时回退 text（兼容）。把 lines 拼成多行交给同一解析器，
	// 保证行号语义一致（pool/parse 按 '\n' 切分并从 1 计行号）。
	source := req.Text
	if len(req.Lines) > 0 {
		source = strings.Join(req.Lines, "\n")
	}

	results := parse.ParseLines(source)
	resp := batchUpstreamResp{Failed: []batchFailureDetail{}}
	successAny := false
	for _, r := range results {
		if !r.OK {
			resp.Failed = append(resp.Failed, batchFailureDetail{Line: r.LineNo, Reason: r.Err})
			continue
		}
		u := store.UpstreamProxy{
			GroupID:          gid,
			Host:             r.Up.Host,
			Port:             r.Up.Port,
			User:             r.Up.User,
			UsernameTemplate: req.UsernameTemplate,
			Pwd:              r.Up.Pwd,
			Weight:           weight,
			Enabled:          enabled,
			HealthState:      true,
		}
		if err := a.store.CreateUpstream(&u); err != nil {
			// 单行入库失败（如外键：分组不存在）也逐行容错累计。
			resp.Failed = append(resp.Failed, batchFailureDetail{Line: r.LineNo, Reason: err.Error()})
			continue
		}
		resp.OK++
		successAny = true
	}

	// 有任何成功入库才需重建快照（一次重建覆盖本批全部新增，避免逐条 rebuild）。
	if successAny {
		if !a.rebuildAndSwap(c) {
			return
		}
	}
	a.logger.Info("批量添加上游", "groupId", gid, "ok", resp.OK, "failed", len(resp.Failed))
	respondOK(c, resp)
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

// bulkUpdateUpstreamsReq 是批量改权重/启停的请求体（AC-3.4，前端最终契约）。
//
// 选择二选一：ids 非空 → id 列表模式；否则用 filter（按 keyword/healthState 筛当前分组，支持跨页全选）。
// changes 携带要改的字段（weight / enabled 均为可选指针，可同时改）；至少需一项。
type bulkUpdateUpstreamsReq struct {
	IDs     []int64             `json:"ids"`     // id 列表模式：精确选中的上游 id
	Filter  *bulkFilterDTO      `json:"filter"`  // 筛选模式：keyword/healthState（ids 为空时用）
	Changes bulkChangesDTO      `json:"changes"` // 要应用的字段变更
}

// bulkFilterDTO 是筛选模式的条件。
type bulkFilterDTO struct {
	Keyword     string `json:"keyword"`
	HealthState string `json:"healthState"`
}

// bulkChangesDTO 是批量变更的字段（均可选；指针区分「未传」与「显式值」）。
type bulkChangesDTO struct {
	Weight  *int  `json:"weight"`
	Enabled *bool `json:"enabled"`
}

// handleBulkUpdateUpstreams 批量改权重/启停（AC-3.4，路由 POST /groups/:id/upstreams/bulk）。
//
// 执行策略：单写操作（筛选模式一条 UPDATE；id 列表模式分块 IN，同事务）。weight 与 enabled
// 若同时传，则各执行一次单 SQL（仍 O(1) 写、非 O(rows)）。返回受影响行数（取各字段最大值，
// 同一批匹配行数一致）。
func (a *App) handleBulkUpdateUpstreams(c *gin.Context) {
	gid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req bulkUpdateUpstreamsReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Changes.Weight == nil && req.Changes.Enabled == nil {
		respondError(c, http.StatusBadRequest, "changes 至少需含 weight 或 enabled 之一")
		return
	}
	useIDs := len(req.IDs) > 0

	// applyField 对一个字段执行一次批量更新（按 ids 或 filter 模式），返回受影响行数。
	applyField := func(field store.UpstreamBulkField, weight int, enabled bool) (int64, error) {
		if useIDs {
			return a.store.BulkUpdateUpstreamsByIDs(gid, req.IDs, field, weight, enabled)
		}
		f := store.UpstreamFilter{GroupID: gid}
		if req.Filter != nil {
			f.Keyword = req.Filter.Keyword
			f.HealthState = req.Filter.HealthState
		}
		return a.store.BulkUpdateUpstreamsByFilter(f, field, weight, enabled)
	}

	var affected int64
	// 改权重。
	if req.Changes.Weight != nil {
		w := *req.Changes.Weight
		if w <= 0 {
			w = 1 // 权重兜底，避免 SWRR 除零
		}
		n, err := applyField(store.BulkFieldWeight, w, false)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "批量改权重失败: "+err.Error())
			return
		}
		if n > affected {
			affected = n
		}
	}
	// 改启停。
	if req.Changes.Enabled != nil {
		n, err := applyField(store.BulkFieldEnabled, 0, *req.Changes.Enabled)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "批量改启停失败: "+err.Error())
			return
		}
		if n > affected {
			affected = n
		}
	}

	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("批量更新上游", "groupId", gid, "byIDs", useIDs, "affected", affected)
	respondOK(c, gin.H{"affected": affected})
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
