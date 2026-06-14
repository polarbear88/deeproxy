// 本文件实现规则组与规则的 CRUD，以及「规则组应用到分组」的多对多关联（AC-22/29）。
//
// 端点对齐前端契约：/rule-groups（连字符）、规则嵌套 /rule-groups/:id/rules/:rid、
// 规则组维度关联 PUT /rule-groups/:id/groups。规则字段对外用 order（内部 OrderIdx）。
//
// 规则非法（如 ip-cidr 格式错）在 rebuildAndSwap 的预编译阶段被拦截，触发 G4 回滚。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"deeproxy/store"
)

// —— 规则组 ——

// ruleGroupReq 是规则组创建/更新请求体。
type ruleGroupReq struct {
	Name  string `json:"name"`
	Scope string `json:"scope"` // global / group
}

// ruleGroupResp 是规则组响应（含应用到的分组 + 规则数，对齐前端）。
type ruleGroupResp struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Scope     string     `json:"scope"`
	GroupIDs  []int64    `json:"groupIds"`
	Groups    []groupRef `json:"groups"`
	RuleCount int        `json:"ruleCount"`
}

// groupRef 是规则组响应里引用的分组简要信息。
type groupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func validateScope(s string) bool {
	return s == string(store.ScopeGlobal) || s == string(store.ScopeGroup)
}

func (a *App) handleListRuleGroups(c *gin.Context) {
	rgs, err := a.store.ListRuleGroups()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取规则组失败: "+err.Error())
		return
	}
	// 反查：规则组 → 关联分组；规则组 → 规则数（一次性读全量，避免 N+1）。
	grgs, err := a.store.ListGroupRuleGroups()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取规则组关联失败: "+err.Error())
		return
	}
	rules, err := a.store.ListAllRules()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取规则失败: "+err.Error())
		return
	}
	groups, err := a.store.ListGroups()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取分组失败: "+err.Error())
		return
	}
	groupName := make(map[int64]string, len(groups))
	for _, g := range groups {
		groupName[g.ID] = g.Name
	}
	rgToGroups := make(map[int64][]int64)
	for _, grg := range grgs {
		rgToGroups[grg.RuleGroupID] = append(rgToGroups[grg.RuleGroupID], grg.GroupID)
	}
	ruleCount := make(map[int64]int)
	for _, r := range rules {
		ruleCount[r.RuleGroupID]++
	}

	out := make([]ruleGroupResp, 0, len(rgs))
	for _, rg := range rgs {
		gids := rgToGroups[rg.ID]
		if gids == nil {
			gids = []int64{}
		}
		refs := make([]groupRef, 0, len(gids))
		for _, gid := range gids {
			refs = append(refs, groupRef{ID: gid, Name: groupName[gid]})
		}
		out = append(out, ruleGroupResp{
			ID: rg.ID, Name: rg.Name, Scope: string(rg.Scope),
			GroupIDs: gids, Groups: refs, RuleCount: ruleCount[rg.ID],
		})
	}
	respondOK(c, out)
}

func (a *App) handleCreateRuleGroup(c *gin.Context) {
	var req ruleGroupReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "规则组名不能为空")
		return
	}
	if !validateScope(req.Scope) {
		respondError(c, http.StatusBadRequest, "规则组作用域非法（应为 global 或 group）")
		return
	}
	// 重名预校验：规则组名同样是快照索引键，重名会静默覆盖导致选错规则集。
	// DB 层已加 UNIQUE 兜底，这里先查给出清晰 409。
	if existing, err := a.store.GetRuleGroupByName(req.Name); err != nil {
		respondError(c, http.StatusInternalServerError, "校验规则组名失败: "+err.Error())
		return
	} else if existing != nil {
		respondError(c, http.StatusConflict, "规则组名已存在: "+req.Name)
		return
	}
	rg := store.RuleGroup{Name: req.Name, Scope: store.RuleScope(req.Scope)}
	if err := a.store.CreateRuleGroup(&rg); err != nil {
		respondError(c, http.StatusInternalServerError, "新增规则组失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("创建规则组", "id", rg.ID, "name", rg.Name, "scope", string(rg.Scope))
	respondOK(c, ruleGroupResp{ID: rg.ID, Name: rg.Name, Scope: string(rg.Scope), GroupIDs: []int64{}, Groups: []groupRef{}})
}

func (a *App) handleUpdateRuleGroup(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req ruleGroupReq
	if !bindJSON(c, &req) {
		return
	}
	if !validateScope(req.Scope) {
		respondError(c, http.StatusBadRequest, "规则组作用域非法（应为 global 或 group）")
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "规则组名不能为空")
		return
	}
	// 改名重名校验：允许保留自身名字，但不得与「其它」规则组撞名。
	if existing, err := a.store.GetRuleGroupByName(req.Name); err != nil {
		respondError(c, http.StatusInternalServerError, "校验规则组名失败: "+err.Error())
		return
	} else if existing != nil && existing.ID != id {
		respondError(c, http.StatusConflict, "规则组名已存在: "+req.Name)
		return
	}
	rg := store.RuleGroup{ID: id, Name: req.Name, Scope: store.RuleScope(req.Scope)}
	if err := a.store.UpdateRuleGroup(&rg); err != nil {
		respondError(c, http.StatusInternalServerError, "更新规则组失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("更新规则组", "id", rg.ID, "name", rg.Name)
	respondOK(c, nil)
}

func (a *App) handleDeleteRuleGroup(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := a.store.DeleteRuleGroup(id); err != nil {
		respondError(c, http.StatusInternalServerError, "删除规则组失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("删除规则组", "id", id)
	respondOK(c, nil)
}

// handleSetRuleGroupGroups 覆盖式设置某规则组应用到的分组（AC-29，规则组维度）。
func (a *App) handleSetRuleGroupGroups(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		GroupIDs []int64 `json:"groupIds"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if err := a.store.SetRuleGroupGroups(id, req.GroupIDs); err != nil {
		respondError(c, http.StatusInternalServerError, "设置规则组关联失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("设置规则组应用分组", "ruleGroupId", id, "groupIds", req.GroupIDs)
	respondOK(c, nil)
}

// —— 规则 ——

// ruleReq 是规则创建/更新请求体（对外字段 order）。
type ruleReq struct {
	Match  string `json:"match"`
	Action string `json:"action"`
	Order  int    `json:"order"`
}

// ruleResp 是规则响应（对外字段 order）。
type ruleResp struct {
	ID     int64  `json:"id"`
	Match  string `json:"match"`
	Action string `json:"action"`
	Order  int    `json:"order"`
}

func toRuleResp(r store.Rule) ruleResp {
	return ruleResp{ID: r.ID, Match: r.Match, Action: r.Action, Order: r.OrderIdx}
}

func (a *App) handleListRules(c *gin.Context) {
	rgid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	rules, err := a.store.ListRulesByGroup(rgid)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取规则失败: "+err.Error())
		return
	}
	out := make([]ruleResp, 0, len(rules))
	for _, r := range rules {
		out = append(out, toRuleResp(r))
	}
	respondOK(c, out)
}

func (a *App) handleCreateRule(c *gin.Context) {
	rgid, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req ruleReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Match == "" || req.Action == "" {
		respondError(c, http.StatusBadRequest, "规则 match/action 不能为空")
		return
	}
	r := store.Rule{RuleGroupID: rgid, Match: req.Match, Action: req.Action, OrderIdx: req.Order}
	// DEC-A1 写前校验：先用「现有规则 + 本条新规则」候选编译，非法则挡在落库前（DB 不写、不分裂）。
	if !a.validateRuleUpsert(c, r) {
		return
	}
	if err := a.store.CreateRule(&r); err != nil {
		respondError(c, http.StatusInternalServerError, "新增规则失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("创建规则", "id", r.ID, "ruleGroupId", r.RuleGroupID, "match", r.Match, "action", r.Action)
	respondOK(c, toRuleResp(r))
}

func (a *App) handleUpdateRule(c *gin.Context) {
	rid, ok := parseIDParam(c, "rid")
	if !ok {
		return
	}
	var req ruleReq
	if !bindJSON(c, &req) {
		return
	}
	r := store.Rule{ID: rid, Match: req.Match, Action: req.Action, OrderIdx: req.Order}
	// DEC-A1 写前校验：用「现有规则集（本条按 ID 覆盖）」候选编译，非法则挡在落库前。
	if !a.validateRuleUpsert(c, r) {
		return
	}
	if err := a.store.UpdateRule(&r); err != nil {
		respondError(c, http.StatusInternalServerError, "更新规则失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("更新规则", "id", r.ID, "match", r.Match, "action", r.Action)
	respondOK(c, toRuleResp(r))
}

func (a *App) handleDeleteRule(c *gin.Context) {
	rid, ok := parseIDParam(c, "rid")
	if !ok {
		return
	}
	if err := a.store.DeleteRule(rid); err != nil {
		respondError(c, http.StatusInternalServerError, "删除规则失败: "+err.Error())
		return
	}
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Info("删除规则", "id", rid)
	respondOK(c, nil)
}
