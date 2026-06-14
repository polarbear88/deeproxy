// Package snapbuild 负责从 SQLite【物化】出不可变运行期快照（snapshot.Snapshot）并热替换。
//
// 为什么独立于 snapshot 包（AC-43 关键）：物化必须读 store（database/sql + SQLite 驱动），
// 若把 Rebuild 放在 snapshot 包，转发链 server→snapshot 就会静态拉入 store→database/sql，
// 违反“转发热路径不沾 store/database”硬约束。故把【需要 store 的物化逻辑】隔离到本上层包：
// 转发侧只依赖纯净的 snapshot（Holder.Load/Swap、只读视图），物化由 cmd/api 在管理 goroutine
// 调用本包完成。snapshot 包因此零依赖 store，server 依赖闭包脱离 database/sql。
package snapbuild

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"deeproxy/config"
	"deeproxy/domain"
	"deeproxy/rule"
	"deeproxy/snapshot"
	"deeproxy/store"
)

// rebuildMu 是【全局重建互斥锁】，序列化所有 RebuildAndSwap 调用（CRITICAL #2 修复）。
//
// 为什么需要：健康检查 worker 的 Refresh() 与所有 API 写 handler 都会调 RebuildAndSwap，
// 彼此原本无互斥。两个并发 Rebuild 各自读 DB → 各自 holder.Swap，后完成者会覆盖先完成者
// 的结果（lost-update），且存在「快照与 DB 不一致」的窗口期。本锁把「读 DB 物化 + Swap」
// 整个过程串行化，保证：任一时刻只有一个 Rebuild 在跑，Swap 的顺序与 Rebuild 完成顺序一致，
// 消除 lost-update。
//
// 为什么放包级、所有调用方共用一把：api(20+ 写 handler)、health.Refresh、cmd 启动后的
// refresher 全部经由 RebuildAndSwap 这一个入口，故锁加在此入口即对全体调用方生效，无需
// 各调用方自带锁（避免“多把锁锁不住同一临界区”）。
//
// ⚠️ 不在转发热路径上：本锁只在【后台写 / 健康翻转】触发的 RebuildAndSwap 中持有；
// 转发侧读取快照走 holder.Load()（atomic.Value，无锁），完全不碰本锁，零锁热路径不受影响。
var rebuildMu sync.Mutex

// rebuildCritHook 是【仅测试用】的临界区探针：在持有 rebuildMu 期间被调用（若非 nil）。
// 生产环境恒为 nil（一次 nil 判断，零开销），并发测试用它注入「进入临界区/制造交叠窗口」
// 的探测逻辑，从而【确定性】验证 RebuildAndSwap 是否真正互斥（而非依赖偶发竞争窗口）。
var rebuildCritHook func()

// RebuildAndSwap 从 store 物化新快照并在成功后原子替换到 holder（G4 回滚的统一入口）。
//
// 语义保证（G4）：Rebuild 失败 → 不调用 holder.Swap、保留旧快照、返回错误；
// 调用方（api 写 handler）据此把错误回给前端，转发侧仍读旧的一致快照。
//
// 并发保证（CRITICAL #2）：全程持 rebuildMu，所有并发调用被串行化，Rebuild+Swap 原子，
// 不会出现两个重建交叉导致后者覆盖前者（lost-update）。
func RebuildAndSwap(holder *snapshot.Holder, st *store.Store, cfg *config.Config) error {
	// 串行化「读 DB 物化 + Swap」整个临界区：直到本次 Swap 完成才释放，下一个 Rebuild 才能开始，
	// 保证它一定读到「包含本次写入」的 DB 状态，从而消除 lost-update。
	rebuildMu.Lock()
	defer rebuildMu.Unlock()

	if rebuildCritHook != nil {
		rebuildCritHook() // 仅测试注入：用于确定性验证临界区互斥（生产为 nil）。
	}

	snap, err := Rebuild(st, cfg)
	if err != nil {
		return fmt.Errorf("重建配置快照失败（已保留旧快照）: %w", err)
	}
	holder.Swap(snap)
	return nil
}

// Rebuild 从 SQLite 物化一份全新的不可变快照，并为每个分组预编译规则引擎。
//
// 步骤：
//  1. 一次性批量读出所有实体（List*），避免 N+1 查询；
//  2. 按规则组分桶规则，区分 global / group 作用域；
//  3. 为每个分组：合并「全局规则组 → 该组关联的分组规则组」预编译扁平化 Engine（复用 rule 包）；
//     物化健康节点列表（仅 Type B：enabled && healthy）与全部上游列表；
//  4. 建立 group/user 索引与授权集合，经 snapshot.NewSnapshot 组装为不可变快照。
//
// 任一步出错（如规则非法导致预编译失败）→ 返回 error，调用方不 Swap（G4）。
func Rebuild(st *store.Store, cfg *config.Config) (*snapshot.Snapshot, error) {
	// —— 0. 读取运行期系统设置（取消配置文件后，default_action / idle / sniff 等迁入 system_setting）——
	// 这些值随快照物化、后台改后经 RebuildAndSwap 热生效；cfg 仅保留启动引导项（监听/DB 路径）。
	ss, err := st.GetSystemSetting()
	if err != nil {
		return nil, fmt.Errorf("读取系统设置失败: %w", err)
	}

	// 全局默认动作：以系统设置为权威来源（非法值兜底为 forward，避免坏数据让整库无引擎）。
	def := rule.Action(ss.DefaultAction)
	if def != rule.ActionForward && def != rule.ActionDirect && def != rule.ActionReject {
		def = rule.ActionForward
	}

	// 物化运行期全局动态设置（建连时由 server 从快照读取，零热路径开销）。
	settings := snapshot.Settings{
		IdleTimeout:  time.Duration(ss.IdleTimeoutSec) * time.Second,
		SniffDomain:  ss.SniffDomain,
		SniffTimeout: time.Duration(ss.SniffTimeoutMs) * time.Millisecond,
	}

	// —— 1. 批量读出所有实体 ——
	groups, err := st.ListGroups()
	if err != nil {
		return nil, fmt.Errorf("读取分组失败: %w", err)
	}
	allUpstreams, err := st.ListAllUpstreams()
	if err != nil {
		return nil, fmt.Errorf("读取上游失败: %w", err)
	}
	users, err := st.ListProxyUsers()
	if err != nil {
		return nil, fmt.Errorf("读取用户失败: %w", err)
	}
	ruleGroups, err := st.ListRuleGroups()
	if err != nil {
		return nil, fmt.Errorf("读取规则组失败: %w", err)
	}
	rules, err := st.ListAllRules()
	if err != nil {
		return nil, fmt.Errorf("读取规则失败: %w", err)
	}
	groupUsers, err := st.ListGroupUsers()
	if err != nil {
		return nil, fmt.Errorf("读取授权关系失败: %w", err)
	}
	groupRuleGroups, err := st.ListGroupRuleGroups()
	if err != nil {
		return nil, fmt.Errorf("读取分组规则组关联失败: %w", err)
	}

	// —— 2. 规则按规则组分桶（SQL 已按 order_idx 排序，保持顺序） ——
	rulesByRG := make(map[int64][]config.RuleSpec)
	for _, r := range rules {
		rulesByRG[r.RuleGroupID] = append(rulesByRG[r.RuleGroupID], config.RuleSpec{
			Match:  r.Match,
			Action: r.Action,
		})
	}

	rgByID := make(map[int64]store.RuleGroup, len(ruleGroups))
	for _, rg := range ruleGroups {
		rgByID[rg.ID] = rg
	}
	// 全局组按 ID 升序拼装，保证顺序稳定可预期。
	var globalSpecs []rule.RuleGroupSpec
	for _, rg := range sortedRuleGroupsByScope(ruleGroups, domain.ScopeGlobal) {
		globalSpecs = append(globalSpecs, rule.RuleGroupSpec{Name: rg.Name, Specs: rulesByRG[rg.ID]})
	}

	// 全局规则的【独立校验】：BuildGroupEngine 只在 for group 循环里被调用，
	// 当没有任何分组时，非法的全局规则（如坏 CIDR）将永远不会被编译、从而漏过校验。
	// 这违反 G4 快速失败原则（坏配置写入应让 Rebuild 失败、由后台回错给前端，
	// 而非静默 Swap 一个隐藏坏规则的快照）。故此处无论是否有分组，都先把全局规则
	// 单独编译一次以触发校验；编译产物丢弃（真正生效的引擎仍按分组各自编译）。
	if len(globalSpecs) > 0 {
		if _, err := rule.BuildGroupEngine(globalSpecs, nil, def); err != nil {
			return nil, fmt.Errorf("全局规则校验失败: %w", err)
		}
	}

	// 分组 → 其关联的规则组 ID 列表。
	groupToRGs := make(map[int64][]int64)
	for _, grg := range groupRuleGroups {
		groupToRGs[grg.GroupID] = append(groupToRGs[grg.GroupID], grg.RuleGroupID)
	}

	// 上游按分组分桶。
	upstreamsByGroup := make(map[int64][]store.UpstreamProxy)
	for _, u := range allUpstreams {
		upstreamsByGroup[u.GroupID] = append(upstreamsByGroup[u.GroupID], u)
	}

	// —— 3. 逐分组构建视图 + 预编译 Engine ——
	groupsByName := make(map[string]*snapshot.GroupView, len(groups))
	groupsByID := make(map[int64]*snapshot.GroupView, len(groups))
	for _, g := range groups {
		var groupSpecs []rule.RuleGroupSpec
		rgIDs := groupToRGs[g.ID]
		sort.Slice(rgIDs, func(i, j int) bool { return rgIDs[i] < rgIDs[j] })
		for _, rgID := range rgIDs {
			rg, ok := rgByID[rgID]
			if !ok || rg.Scope != domain.ScopeGroup {
				continue // 指向已删除规则组 或 非分组作用域（全局已统一处理），跳过
			}
			groupSpecs = append(groupSpecs, rule.RuleGroupSpec{Name: rg.Name, Specs: rulesByRG[rg.ID]})
		}

		eng, err := rule.BuildGroupEngine(globalSpecs, groupSpecs, def)
		if err != nil {
			return nil, fmt.Errorf("分组 %q(id=%d) 规则预编译失败: %w", g.Name, g.ID, err)
		}

		var allViews, healthyViews []snapshot.UpstreamView
		for _, u := range upstreamsByGroup[g.ID] {
			uv := snapshot.UpstreamView{
				ID:               u.ID,
				Host:             u.Host,
				Port:             u.Port,
				User:             u.User,
				UsernameTemplate: u.UsernameTemplate,
				Pwd:              u.Pwd,
				Weight:           u.Weight,
				Enabled:          u.Enabled,
				Healthy:          u.HealthState,
			}
			allViews = append(allViews, uv)
			if u.Enabled && u.HealthState {
				healthyViews = append(healthyViews, uv)
			}
		}

		gv := &snapshot.GroupView{
			ID:               g.ID,
			Name:             g.Name,
			Type:             g.Type,
			Remark:           g.Remark,
			Engine:           eng,
			HealthyUpstreams: healthyViews,
			AllUpstreams:     allViews,
		}
		groupsByName[g.Name] = gv
		groupsByID[g.ID] = gv
	}

	// —— 4. 用户索引 + 授权集合 ——
	usersByName := make(map[string]*snapshot.UserView, len(users))
	// allGroupsUsers：物化「授权全部分组」通配用户集合（DEC-B1）。与逐组 authz 并存，
	// IsAuthorized 命中此集合即对任意分组放行（覆盖未来新增分组）。
	allGroupsUsers := make(map[int64]struct{})
	for _, u := range users {
		usersByName[u.Username] = &snapshot.UserView{ID: u.ID, Username: u.Username, Pwd: u.Pwd}
		if u.AllGroups {
			allGroupsUsers[u.ID] = struct{}{}
		}
	}
	authz := make(map[snapshot.AuthzKey]struct{}, len(groupUsers))
	for _, gu := range groupUsers {
		authz[snapshot.NewAuthzKey(gu.GroupID, gu.UserID)] = struct{}{}
	}

	return snapshot.NewSnapshot(groupsByName, groupsByID, usersByName, authz, allGroupsUsers, def, settings), nil
}

// sortedRuleGroupsByScope 过滤出指定作用域的规则组并按 ID 升序返回（顺序稳定）。
func sortedRuleGroupsByScope(all []store.RuleGroup, scope domain.RuleScope) []store.RuleGroup {
	var out []store.RuleGroup
	for _, rg := range all {
		if rg.Scope == scope {
			out = append(out, rg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
