// 本文件实现配置导入/导出与规则测试器（AC-37/39）。
//
// 端点对齐前端契约：
//   - GET  /settings/export → { schemaVersion, data:{...} }
//   - POST /settings/import ← { schemaVersion, data, strategy:'overwrite' }（G4：版本校验+导入前备份+整体覆盖）
//   - POST /rule-groups/test ← { target, groupId, sniffDomain? } → { matchedRule, fromGroup, action }（G3）
//
// 代理测试连接（AC-38）见 group_handler.go 的 handleTestUpstream（嵌套路由 .../test）。
package api

import (
	"net"
	"net/http"

	"github.com/gin-gonic/gin"

	"deeproxy/store"
)

// configSchemaVersion 是导出配置的结构版本号（G4：导入时校验兼容）。
const configSchemaVersion = 1

// configData 是配置实体集合（导出/导入的 data 字段内容）。
type configData struct {
	Groups          []store.Group          `json:"groups"`
	Upstreams       []store.UpstreamProxy  `json:"upstreams"`
	RuleGroups      []store.RuleGroup      `json:"ruleGroups"`
	Rules           []store.Rule           `json:"rules"`
	Users           []store.ProxyUser      `json:"users"`
	GroupUsers      []store.GroupUser      `json:"groupUsers"`
	GroupRuleGroups []store.GroupRuleGroup `json:"groupRuleGroups"`
}

// exportBundle 是导入/导出的整体包（schemaVersion + data 包一层，对齐前端）。
type exportBundle struct {
	SchemaVersion int        `json:"schemaVersion"`
	Data          configData `json:"data"`
}

// handleExportConfig 导出当前配置为 JSON（AC-37）。
func (a *App) handleExportConfig(c *gin.Context) {
	data, err := a.collectConfigData()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "导出配置失败: "+err.Error())
		return
	}
	a.logger.Info("导出配置", "groups", len(data.Groups), "rules", len(data.Rules), "users", len(data.Users))
	respondOK(c, exportBundle{SchemaVersion: configSchemaVersion, Data: *data})
}

// collectConfigData 从 store 读取全部配置实体（DRY：导出与导入前备份共用）。
func (a *App) collectConfigData() (*configData, error) {
	groups, err := a.store.ListGroups()
	if err != nil {
		return nil, err
	}
	ups, err := a.store.ListAllUpstreams()
	if err != nil {
		return nil, err
	}
	rgs, err := a.store.ListRuleGroups()
	if err != nil {
		return nil, err
	}
	rules, err := a.store.ListAllRules()
	if err != nil {
		return nil, err
	}
	users, err := a.store.ListProxyUsers()
	if err != nil {
		return nil, err
	}
	gus, err := a.store.ListGroupUsers()
	if err != nil {
		return nil, err
	}
	grgs, err := a.store.ListGroupRuleGroups()
	if err != nil {
		return nil, err
	}
	return &configData{
		Groups: groups, Upstreams: ups, RuleGroups: rgs, Rules: rules,
		Users: users, GroupUsers: gus, GroupRuleGroups: grgs,
	}, nil
}

// importReq 是导入请求体（含 strategy；首版仅 overwrite）。
type importReq struct {
	SchemaVersion int        `json:"schemaVersion"`
	Data          configData `json:"data"`
	Strategy      string     `json:"strategy"`
}

// importResp 回传导入前备份供前端留存（G4）。
type importResp struct {
	OK     bool        `json:"ok"`
	Backup *configData `json:"backup"`
}

// handleImportConfig 导入配置（G4：版本校验 → 备份旧配置 → 整体覆盖 → Rebuild+Swap）。
func (a *App) handleImportConfig(c *gin.Context) {
	var req importReq
	if !bindJSON(c, &req) {
		return
	}
	if req.SchemaVersion != configSchemaVersion {
		respondError(c, http.StatusBadRequest, "配置版本不兼容：导入文件 schemaVersion 与当前不符")
		return
	}
	// 首版仅支持整体覆盖策略。
	if req.Strategy != "" && req.Strategy != "overwrite" {
		respondError(c, http.StatusBadRequest, "暂仅支持 strategy=overwrite（整体覆盖）")
		return
	}

	// 导入前备份当前配置（G4）。
	backup, err := a.collectConfigData()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "导入前备份失败: "+err.Error())
		return
	}

	// 整体覆盖（事务内）。
	if err := a.store.ImportBundle(toStoreBundle(req.Data)); err != nil {
		respondError(c, http.StatusInternalServerError, "导入覆盖失败（配置未变更）: "+err.Error())
		return
	}
	// 刷新转发快照（失败回滚到旧快照并返回错误，G4）。
	if !a.rebuildAndSwap(c) {
		return
	}
	a.logger.Warn("导入配置（整体覆盖已生效）", "groups", len(req.Data.Groups), "rules", len(req.Data.Rules), "users", len(req.Data.Users))
	respondOK(c, importResp{OK: true, Backup: backup})
}

// toStoreBundle 把 API 配置数据转换为 store 导入结构。
func toStoreBundle(d configData) store.ConfigBundle {
	return store.ConfigBundle{
		Groups: d.Groups, Upstreams: d.Upstreams, RuleGroups: d.RuleGroups, Rules: d.Rules,
		Users: d.Users, GroupUsers: d.GroupUsers, GroupRuleGroups: d.GroupRuleGroups,
	}
}

// —— 规则测试器（AC-39 / G3）——

// testRuleReq 是规则测试请求：目标（域名或 IP）+ 分组 ID（对齐前端 groupId）。
type testRuleReq struct {
	Target      string `json:"target"`
	GroupID     int64  `json:"groupId"`
	SniffDomain string `json:"sniffDomain"` // 可选：用户提供的模拟嗅探域名（G3）
}

// testRuleResp 是规则测试结果（对齐前端 matchedRule/fromGroup/action）。
type testRuleResp struct {
	Action      string `json:"action"`      // 命中动作 forward/direct/reject
	MatchedRule string `json:"matchedRule"` // 命中的规则表达式（未命中为空，走默认）
	FromGroup   string `json:"fromGroup"`   // 测试所用分组名
	Matched     bool   `json:"matched"`     // 是否命中显式规则
	SniffNote   string `json:"sniffNote"`   // 嗅探路径限制说明（G3）
}

// handleTestRule 模拟跑某分组的合并规则序列（AC-39 / G3）。
//
// G3：仅模拟域名/IP 直接匹配。IP 未命中 ip-cidr 时真实路径会嗅探还原域名，测试器不可模拟；
// 若用户提供 sniffDomain，则改用该域名再跑一次匹配（模拟嗅探后的判定）。
func (a *App) handleTestRule(c *gin.Context) {
	var req testRuleReq
	if !bindJSON(c, &req) {
		return
	}
	if req.Target == "" || req.GroupID <= 0 {
		respondError(c, http.StatusBadRequest, "target 与 groupId 不能为空")
		return
	}

	snap := a.holder.Load()
	gv, ok := snap.GroupByID(req.GroupID)
	if !ok {
		respondError(c, http.StatusNotFound, "分组不存在")
		return
	}

	target := req.Target
	isIP := net.ParseIP(target) != nil
	sniffNote := ""

	// G3：IP 未命中且用户给了模拟嗅探域名 → 改用域名匹配。
	if isIP {
		if _, matched := gv.Engine.MatchRule(target); !matched && req.SniffDomain != "" {
			target = req.SniffDomain
			isIP = false
			sniffNote = "已用提供的模拟嗅探域名 " + req.SniffDomain + " 替代 IP 进行匹配。"
		}
	}

	action, matched := gv.Engine.MatchRule(target)
	resp := testRuleResp{
		Action:    string(action),
		FromGroup: gv.Name,
		Matched:   matched,
		SniffNote: sniffNote,
	}
	// matchedRule：rule.Engine 仅返回 (action,matched) 不含命中表达式；
	// 命中时以 "目标→动作" 概述供前端展示，未命中留空表示走默认动作。
	if matched {
		resp.MatchedRule = target + " → " + string(action)
	}
	if isIP && !matched && sniffNote == "" {
		resp.SniffNote = "目标为 IP 且未命中 ip-cidr 规则：真实转发会嗅探首包(TLS SNI/HTTP Host)还原域名再匹配，测试器无法模拟；可在 sniffDomain 传入模拟域名重测，或直接以域名作为 target。"
	}
	respondOK(c, resp)
}
