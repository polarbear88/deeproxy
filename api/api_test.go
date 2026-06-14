// 本文件是管理后端 API 的 handler 测试（httptest + 内存 SQLite）。
// 覆盖：首次设置/登录/会话/限流、各 CRUD、仪表盘聚合、配置导入导出、规则测试器、代理测试。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"deeproxy/config"
	"deeproxy/pool/health"
	"deeproxy/snapbuild"
	"deeproxy/snapshot"
	"deeproxy/stats"
	"deeproxy/store"
	"deeproxy/syslog"
)

// mockProber 是可控的探测桩，用于测试代理测试连接（AC-38），不发真实网络请求。
type mockProber struct {
	result health.ProbeResult
}

func (m mockProber) Probe(_ context.Context, _ store.UpstreamProxy, _ store.HealthMode, _ string) health.ProbeResult {
	return m.result
}

// noopRefresher 是测试用的快照刷新桩（健康检查 worker 状态变化时回调，本测试不关心）。
type noopRefresher struct{}

func (noopRefresher) Refresh() error { return nil }

// testApp 构建一套用于测试的 App（内存 SQLite + 内存依赖）。
func testApp(t *testing.T) *App {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{Listen: "0.0.0.0:1768", AdminListen: "0.0.0.0:1769"}
	snap, err := snapbuild.Rebuild(st, cfg)
	if err != nil {
		t.Fatalf("初次 Rebuild 失败: %v", err)
	}
	holder := snapshot.NewHolder(snap)

	counter := stats.NewCounter()
	logs := syslog.NewLogBuffer(5000)
	logger := slog.New(logs.Handler())
	audit := syslog.NewAuditBuffer(5000)
	// 健康检查器注入 mock 探测桩（默认连通、10ms）。
	health := health.NewHealthChecker(st, mockProber{result: health.ProbeResult{OK: true, Latency: 10 * time.Millisecond}}, noopRefresher{}, nil)

	return NewApp(st, holder, cfg, counter, logs, audit, health, nil, logger, nil, "")
}

// doJSON 向 app.Router() 发一个 JSON 请求，返回响应。cookies 透传会话。
func doJSON(t *testing.T, app *App, method, path string, body any, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	return w
}

// setupAndLogin 完成首次设置 + 登录，返回登录会话 cookie（AC-19/20）。
func setupAndLogin(t *testing.T, app *App) []*http.Cookie {
	t.Helper()
	// 首次设置。
	w := doJSON(t, app, "POST", "/api/auth/setup", credentialReq{Username: "admin", Password: "secret123"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("首次设置失败: code=%d body=%s", w.Code, w.Body.String())
	}
	// 登录。
	w = doJSON(t, app, "POST", "/api/auth/login", credentialReq{Username: "admin", Password: "secret123"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: code=%d body=%s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()
}

// TestSetupStatusAndFirstSetup 验证首次设置引导（AC-19）。
func TestSetupStatusAndFirstSetup(t *testing.T) {
	app := testApp(t)

	// 初始未配置。
	w := doJSON(t, app, "GET", "/api/auth/init-status", nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"initialized":false`) {
		t.Fatalf("初始应未配置: code=%d body=%s", w.Code, w.Body.String())
	}

	// 首次设置成功。
	w = doJSON(t, app, "POST", "/api/auth/setup", credentialReq{Username: "admin", Password: "secret123"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("首次设置失败: %d %s", w.Code, w.Body.String())
	}

	// 再查已配置。
	w = doJSON(t, app, "GET", "/api/auth/init-status", nil, nil)
	if !strings.Contains(w.Body.String(), `"initialized":true`) {
		t.Fatalf("设置后应已配置: %s", w.Body.String())
	}

	// 重复首次设置应被拒绝（409）。
	w = doJSON(t, app, "POST", "/api/auth/setup", credentialReq{Username: "x", Password: "y"}, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("重复首次设置应 409, got %d", w.Code)
	}
}

// TestLoginAndSession 验证登录签发会话 + 受保护接口需会话（AC-20）。
func TestLoginAndSession(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 无 cookie 访问受保护接口 → 401。
	w := doJSON(t, app, "GET", "/api/groups", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无会话应 401, got %d", w.Code)
	}

	// 带 cookie → 200。
	w = doJSON(t, app, "GET", "/api/groups", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("有会话应 200, got %d body=%s", w.Code, w.Body.String())
	}

	// 登出后 cookie 失效。
	w = doJSON(t, app, "POST", "/api/auth/logout", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应 200, got %d", w.Code)
	}
	w = doJSON(t, app, "GET", "/api/groups", nil, cookies)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("登出后应 401, got %d", w.Code)
	}
}

// TestLoginRateLimit 验证登录失败限流锁定（AC-40）。
func TestLoginRateLimit(t *testing.T) {
	app := testApp(t)
	// 先完成首次设置。
	doJSON(t, app, "POST", "/api/auth/setup", credentialReq{Username: "admin", Password: "secret123"}, nil)

	// 连续 5 次错误密码。
	for i := 0; i < maxLoginFails; i++ {
		w := doJSON(t, app, "POST", "/api/auth/login", credentialReq{Username: "admin", Password: "wrong"}, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401, got %d", i+1, w.Code)
		}
	}
	// 第 6 次（即便密码正确）应被锁定 429。
	w := doJSON(t, app, "POST", "/api/auth/login", credentialReq{Username: "admin", Password: "secret123"}, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("超过阈值应 429, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestGroupAndRuleCRUD 验证分组/规则组/规则 CRUD 及快照热替换（AC-21/22）。
func TestGroupAndRuleCRUD(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 建一个 Type B 分组。
	w := doJSON(t, app, "POST", "/api/groups", groupReq{Name: "poolA", Type: "B"}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("建分组失败: %d %s", w.Code, w.Body.String())
	}
	var g groupResp
	mustUnmarshal(t, w.Body.Bytes(), &g)
	if g.ID == 0 {
		t.Fatal("分组 ID 应非零")
	}

	// 建规则组(global) + 一条规则。
	w = doJSON(t, app, "POST", "/api/rule-groups", ruleGroupReq{Name: "g", Scope: "global"}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("建规则组失败: %d %s", w.Code, w.Body.String())
	}
	var rg ruleGroupResp
	mustUnmarshal(t, w.Body.Bytes(), &rg)
	w = doJSON(t, app, "POST", "/api/rule-groups/"+strconv.FormatInt(rg.ID, 10)+"/rules",
		ruleReq{Match: "domain-suffix:example.com", Action: "reject", Order: 1}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("建规则失败: %d %s", w.Code, w.Body.String())
	}

	// 快照已热替换：该分组合并引擎应命中 example.com → reject（用规则测试器验证）。
	w = doJSON(t, app, "POST", "/api/rule-groups/test", testRuleReq{Target: "www.example.com", GroupID: g.ID}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("规则测试失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"action":"reject"`) {
		t.Fatalf("应命中 reject: %s", w.Body.String())
	}
}

// TestBadRuleRollback 验证 DEC-A1（AC-5.1）写前候选校验：非法规则在【落库之前】被拦截，
// 返回 400 且 DB 不写入坏规则（DB 与转发快照不分裂，Principle 1）。
func TestBadRuleRollback(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 需至少一个分组，全局规则才会在该组的合并引擎中被预编译校验。
	doJSON(t, app, "POST", "/api/groups", groupReq{Name: "poolA", Type: "B"}, cookies)

	w := doJSON(t, app, "POST", "/api/rule-groups", ruleGroupReq{Name: "g", Scope: "global"}, cookies)
	var rg ruleGroupResp
	mustUnmarshal(t, w.Body.Bytes(), &rg)
	rgPath := "/api/rule-groups/" + strconv.FormatInt(rg.ID, 10) + "/rules"

	// 非法 ip-cidr → 写前候选编译失败 → 400（挡在落库前，配置未改动）。
	w = doJSON(t, app, "POST", rgPath,
		ruleReq{Match: "ip-cidr:not-a-cidr", Action: "direct", Order: 1}, cookies)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法规则应 400（写前拦截），got %d body=%s", w.Code, w.Body.String())
	}

	// 关键断言：坏规则【未落库】——列表应为空，证明 DB 与快照未被坏配置污染。
	w = doJSON(t, app, "GET", rgPath, nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("读取规则列表失败: %d %s", w.Code, w.Body.String())
	}
	var rules []ruleResp
	mustUnmarshal(t, w.Body.Bytes(), &rules)
	if len(rules) != 0 {
		t.Fatalf("坏规则不应落库，列表却含 %d 条: %+v", len(rules), rules)
	}

	// 合法规则随后应正常写入并生效（证明拦截不影响正常写）。
	w = doJSON(t, app, "POST", rgPath,
		ruleReq{Match: "domain-suffix:example.com", Action: "reject", Order: 1}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("合法规则应写入成功, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestImportExportRoundTrip 验证配置导入导出回环 + schemaVersion 校验（AC-37/G4）。
// TestUserAuthzAllGroupsCoexist 验证 DEC-B1（T1.1）「并存」语义：
// all_groups 开→存→关→存，用户原有逐组精细授权完整保留（不被 all_groups 切换清除）。
func TestUserAuthzAllGroupsCoexist(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 造两个分组与一个用户。
	w := doJSON(t, app, "POST", "/api/groups", groupReq{Name: "g1", Type: "B"}, cookies)
	var g1 groupResp
	mustUnmarshal(t, w.Body.Bytes(), &g1)
	w = doJSON(t, app, "POST", "/api/groups", groupReq{Name: "g2", Type: "B"}, cookies)
	var g2 groupResp
	mustUnmarshal(t, w.Body.Bytes(), &g2)

	w = doJSON(t, app, "POST", "/api/proxy-users", userReq{Username: "bob", Password: "pw"}, cookies)
	var u userResp
	mustUnmarshal(t, w.Body.Bytes(), &u)
	uPath := "/api/proxy-users/" + strconv.FormatInt(u.ID, 10) + "/groups"

	// D3：用户列表/详情应回传明文 pwd，供「复制代理地址」拼可用 URL。
	got0 := getUserByID(t, app, cookies, u.ID)
	if got0.Pwd != "pw" {
		t.Fatalf("用户列表应回传明文 pwd=pw（供复制代理地址），得到 %q", got0.Pwd)
	}

	on := true
	off := false

	// 1) 先逐组授权 g1（精细），并开 all_groups。
	w = doJSON(t, app, "POST", uPath, setUserGroupsReq{AllGroups: &on, GroupIDs: []int64{g1.ID}}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("设置授权失败: %d %s", w.Code, w.Body.String())
	}
	// 回显：all_groups=true 且 g1 在精细列表中。
	got := getUserByID(t, app, cookies, u.ID)
	if !got.AllGroups {
		t.Fatal("应回显 allGroups=true")
	}
	if !containsID(got.GroupIDs, g1.ID) {
		t.Fatalf("精细授权应含 g1，得到 %+v", got.GroupIDs)
	}

	// 2) 关掉 all_groups，但【不传 groupIds】（只改通配维度）。精细授权必须完整保留。
	w = doJSON(t, app, "POST", uPath, setUserGroupsReq{AllGroups: &off}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("关闭 all_groups 失败: %d %s", w.Code, w.Body.String())
	}
	got = getUserByID(t, app, cookies, u.ID)
	if got.AllGroups {
		t.Fatal("应回显 allGroups=false")
	}
	if !containsID(got.GroupIDs, g1.ID) {
		t.Fatalf("关闭 all_groups 后精细授权 g1 不应丢失，得到 %+v", got.GroupIDs)
	}
}

// getUserByID 从用户列表取指定 ID 的用户视图（测试辅助）。
func getUserByID(t *testing.T, app *App, cookies []*http.Cookie, id int64) userResp {
	t.Helper()
	w := doJSON(t, app, "GET", "/api/proxy-users", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("读取用户列表失败: %d %s", w.Code, w.Body.String())
	}
	var users []userResp
	mustUnmarshal(t, w.Body.Bytes(), &users)
	for _, u := range users {
		if u.ID == id {
			return u
		}
	}
	t.Fatalf("未找到用户 id=%d", id)
	return userResp{}
}

// containsID 判断 id 是否在切片中（测试辅助）。
func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// TestBatchAndBulkUpstreams 验证 WP-3 批量添加（AC-3.1）、分页（AC-3.3）、批量改权重（AC-3.4）。
func TestBatchAndBulkUpstreams(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	w := doJSON(t, app, "POST", "/api/groups", groupReq{Name: "poolB", Type: "B"}, cookies)
	var g groupResp
	mustUnmarshal(t, w.Body.Bytes(), &g)
	base := "/api/groups/" + strconv.FormatInt(g.ID, 10) + "/upstreams"

	// 批量添加：2 行合法（@ 形 + colon 形）、1 行非法（裸 IPv6）。用 lines 数组（最终契约）。
	lines := []string{"u1:p1@h1.com:1080", "u2:p2:h2.com:2080", "2001:db8::1:3080"}
	w = doJSON(t, app, "POST", base+"/batch", batchUpstreamReq{Lines: lines, Weight: 3}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("批量添加失败: %d %s", w.Code, w.Body.String())
	}
	var br batchUpstreamResp
	mustUnmarshal(t, w.Body.Bytes(), &br)
	if br.OK != 2 || len(br.Failed) != 1 {
		t.Fatalf("批量结果应 ok=2 failed=1，得到 %+v", br)
	}
	if br.Failed[0].Line != 3 {
		t.Fatalf("失败明细应含第3行: %+v", br.Failed)
	}

	// 分页：page=1&pageSize=1 → 1 条，total=2。
	w = doJSON(t, app, "GET", base+"?page=1&pageSize=1", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("分页查询失败: %d %s", w.Code, w.Body.String())
	}
	var pr pagedUpstreamsResp
	mustUnmarshal(t, w.Body.Bytes(), &pr)
	if pr.Total != 2 || len(pr.Items) != 1 {
		t.Fatalf("分页应 total=2 items=1，得到 total=%d items=%d", pr.Total, len(pr.Items))
	}

	// 批量改权重（筛选模式，全部）：changes.weight→7。
	wt := 7
	w = doJSON(t, app, "POST", base+"/bulk",
		bulkUpdateUpstreamsReq{Filter: &bulkFilterDTO{}, Changes: bulkChangesDTO{Weight: &wt}}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("批量改权重失败: %d %s", w.Code, w.Body.String())
	}
	// 校验全部权重为 7。
	w = doJSON(t, app, "GET", base, nil, cookies)
	var ups []upstreamResp
	mustUnmarshal(t, w.Body.Bytes(), &ups)
	if len(ups) != 2 {
		t.Fatalf("应有 2 条上游，得到 %d", len(ups))
	}
	for _, u := range ups {
		if u.Weight != 7 {
			t.Fatalf("上游 %s 权重应 7，得到 %d", u.Host, u.Weight)
		}
	}

	// 批量改启停（id 列表模式）：禁用第一条。
	off := false
	w = doJSON(t, app, "POST", base+"/bulk",
		bulkUpdateUpstreamsReq{IDs: []int64{ups[0].ID}, Changes: bulkChangesDTO{Enabled: &off}}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("批量改启停失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(t, app, "GET", base, nil, cookies)
	var ups2 []upstreamResp
	mustUnmarshal(t, w.Body.Bytes(), &ups2)
	disabled := 0
	for _, u := range ups2 {
		if !u.Enabled {
			disabled++
		}
	}
	if disabled != 1 {
		t.Fatalf("应有 1 条被禁用，得到 %d", disabled)
	}
}

// TestSettingsServerAddrAndProbePool 验证 WP-4：serverAddr/probePoolSize 读写往返 + server-info 端点。
func TestSettingsServerAddrAndProbePool(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 设置 serverAddr 与 probePoolSize。
	addr := "proxy.example.com"
	w := doJSON(t, app, "PUT", "/api/settings", settingsReq{
		ServerAddr:    &addr,
		ProbePoolSize: 200,
	}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("更新设置失败: %d %s", w.Code, w.Body.String())
	}

	// GET 回显应反映新值。
	w = doJSON(t, app, "GET", "/api/settings", nil, cookies)
	var sr settingsResp
	mustUnmarshal(t, w.Body.Bytes(), &sr)
	if sr.ServerAddr != addr {
		t.Fatalf("serverAddr 往返失败: %q", sr.ServerAddr)
	}
	if sr.ProbePoolSize != 200 {
		t.Fatalf("probePoolSize 往返失败: %d", sr.ProbePoolSize)
	}
	// GET /settings 也应暴露监听端口（前端连接示例优先从 settings 取）。
	if sr.Socks5Port != 1768 || sr.WebPort != 1769 {
		t.Fatalf("settings 应含端口 socks5=1768/web=1769，得到 socks5=%d web=%d", sr.Socks5Port, sr.WebPort)
	}

	// server-info 端点应返回已设置的 serverAddr。
	w = doJSON(t, app, "GET", "/api/settings/server-info", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("server-info 失败: %d %s", w.Code, w.Body.String())
	}
	var info serverInfoResp
	mustUnmarshal(t, w.Body.Bytes(), &info)
	if info.ServerAddr != addr {
		t.Fatalf("server-info serverAddr 应为 %q，得到 %q", addr, info.ServerAddr)
	}
}

// TestFeatureStatus 验证 T6.3 权威功能状态表端点返回 dashboard.top.domain=implemented。
func TestFeatureStatus(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	w := doJSON(t, app, "GET", "/api/feature-status", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("feature-status 失败: %d %s", w.Code, w.Body.String())
	}
	var status map[string]string
	mustUnmarshal(t, w.Body.Bytes(), &status)
	if status["dashboard.top.domain"] != "implemented" {
		t.Fatalf("dashboard.top.domain 应为 implemented，得到 %q", status["dashboard.top.domain"])
	}
}

func TestImportExportRoundTrip(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 造一点配置。
	doJSON(t, app, "POST", "/api/groups", groupReq{Name: "poolA", Type: "B"}, cookies)
	doJSON(t, app, "POST", "/api/proxy-users", userReq{Username: "alice", Password: "pw"}, cookies)

	// 导出。
	w := doJSON(t, app, "GET", "/api/settings/export", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("导出失败: %d %s", w.Code, w.Body.String())
	}
	var bundle exportBundle
	mustUnmarshal(t, w.Body.Bytes(), &bundle)
	if bundle.SchemaVersion != configSchemaVersion || len(bundle.Data.Groups) != 1 || len(bundle.Data.Users) != 1 {
		t.Fatalf("导出内容不符: %+v", bundle)
	}

	// 导入（整体覆盖，回环）。
	impReq := importReq{SchemaVersion: bundle.SchemaVersion, Data: bundle.Data, Strategy: "overwrite"}
	w = doJSON(t, app, "POST", "/api/settings/import", impReq, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}

	// 版本不兼容应被拒。
	impReq.SchemaVersion = 999
	w = doJSON(t, app, "POST", "/api/settings/import", impReq, cookies)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("版本不兼容应 400, got %d", w.Code)
	}
}

// TestImportBadRuleRejected 验证 AC-5.1（task #13 修复）：导入含坏规则的 bundle 在落库前被拦截，
// 返回 400 且 DB 未变更（坏规则不落库、DB 与快照不分裂，修复 split-brain）。
func TestImportBadRuleRejected(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 先建一份合法基线配置并导出，作为「导入前的 DB 状态」。
	doJSON(t, app, "POST", "/api/groups", groupReq{Name: "baseGroup", Type: "B"}, cookies)
	w := doJSON(t, app, "GET", "/api/settings/export", nil, cookies)
	var baseline exportBundle
	mustUnmarshal(t, w.Body.Bytes(), &baseline)

	// 构造一个含【坏规则】的导入 bundle：一个全局规则组 + 一条非法 ip-cidr 规则。
	bad := exportBundle{
		SchemaVersion: configSchemaVersion,
		Data: configData{
			RuleGroups: []store.RuleGroup{{ID: 1, Name: "bad-glob", Scope: store.ScopeGlobal}},
			Rules:      []store.Rule{{ID: 1, RuleGroupID: 1, Match: "ip-cidr:not-a-cidr", Action: "direct", OrderIdx: 0}},
		},
	}
	impReq := importReq{SchemaVersion: bad.SchemaVersion, Data: bad.Data, Strategy: "overwrite"}
	w = doJSON(t, app, "POST", "/api/settings/import", impReq, cookies)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("含坏规则的导入应 400（写前拦截），got %d body=%s", w.Code, w.Body.String())
	}

	// 关键断言：DB 未变更——重新导出应与基线一致（坏规则未落库、基线分组仍在）。
	w = doJSON(t, app, "GET", "/api/settings/export", nil, cookies)
	var after exportBundle
	mustUnmarshal(t, w.Body.Bytes(), &after)
	if len(after.Data.Rules) != 0 {
		t.Fatalf("坏规则不应落库，导出却含 %d 条规则: %+v", len(after.Data.Rules), after.Data.Rules)
	}
	if len(after.Data.Groups) != len(baseline.Data.Groups) || len(after.Data.Groups) != 1 {
		t.Fatalf("基线分组应保持不变（1 个），得到 %d", len(after.Data.Groups))
	}
	if after.Data.Groups[0].Name != "baseGroup" {
		t.Fatalf("基线分组应仍为 baseGroup，得到 %q", after.Data.Groups[0].Name)
	}

	// 反证：把规则改成合法后，同结构导入应成功（证明拦截只针对坏规则、不误伤）。
	good := impReq
	good.Data.Rules = []store.Rule{{ID: 1, RuleGroupID: 1, Match: "ip-cidr:10.0.0.0/8", Action: "direct", OrderIdx: 0}}
	w = doJSON(t, app, "POST", "/api/settings/import", good, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("合法规则导入应成功, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestTestUpstream 验证代理测试连接经 mock 探测返回结果（AC-38，嵌套路由）。
func TestTestUpstream(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 建 Type B 分组 + 一条上游。
	w := doJSON(t, app, "POST", "/api/groups", groupReq{Name: "poolA", Type: "B"}, cookies)
	var g groupResp
	mustUnmarshal(t, w.Body.Bytes(), &g)
	w = doJSON(t, app, "POST", "/api/groups/"+strconv.FormatInt(g.ID, 10)+"/upstreams",
		upstreamReq{Host: "1.2.3.4", Port: 1080, Weight: 1, Enabled: true}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("建上游失败: %d %s", w.Code, w.Body.String())
	}
	var up upstreamResp
	mustUnmarshal(t, w.Body.Bytes(), &up)

	// 测试连接（mock 探测返回 OK）。
	w = doJSON(t, app, "POST",
		"/api/groups/"+strconv.FormatInt(g.ID, 10)+"/upstreams/"+strconv.FormatInt(up.ID, 10)+"/test",
		nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("测试上游失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("mock 探测应通: %s", w.Body.String())
	}
}

// TestDashboard 验证仪表盘聚合返回实时+今日+健康结构（AC-24）。
func TestDashboard(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	w := doJSON(t, app, "GET", "/api/dashboard/overview", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("仪表盘失败: %d %s", w.Code, w.Body.String())
	}
	var resp overviewResp
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	// 结构存在即可（无流量时今日全 0）。
	if resp.TodayUp != 0 {
		t.Fatalf("无流量今日上行应为 0, got %d", resp.TodayUp)
	}
	// 首页连接提示字段（AC-2.6/4.2）：端口来自 testApp 的 cfg.Listen/AdminListen。
	if resp.Socks5Port != 1768 {
		t.Fatalf("socks5Port 应为 1768, got %d", resp.Socks5Port)
	}
	if resp.WebPort != 1769 {
		t.Fatalf("webPort 应为 1769, got %d", resp.WebPort)
	}
	// serverAddr 未设置时回探测 IP，可能为空（无网卡环境），不强断言其值，仅确认字段存在不报错。
}

// TestGroupRuleGroupDuplicateName 验证分组名/规则组名重名校验（FIX-H4）：
// 重名会让快照路由键静默覆盖（潜在越权），故创建/改名撞名须回 409。
func TestGroupRuleGroupDuplicateName(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// —— 分组重名 ——
	// 首个分组创建成功。
	w := doJSON(t, app, "POST", "/api/groups", groupReq{Name: "dup", Type: "B"}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("首个分组应建成: %d %s", w.Code, w.Body.String())
	}
	var g1 groupResp
	mustUnmarshal(t, w.Body.Bytes(), &g1)

	// 同名再建 → 409。
	w = doJSON(t, app, "POST", "/api/groups", groupReq{Name: "dup", Type: "A"}, cookies)
	if w.Code != http.StatusConflict {
		t.Fatalf("重名分组应 409, got %d body=%s", w.Code, w.Body.String())
	}

	// 另建一个分组，再把它改名撞 "dup" → 409。
	w = doJSON(t, app, "POST", "/api/groups", groupReq{Name: "other", Type: "B"}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("第二个分组应建成: %d %s", w.Code, w.Body.String())
	}
	var g2 groupResp
	mustUnmarshal(t, w.Body.Bytes(), &g2)
	w = doJSON(t, app, "PUT", "/api/groups/"+strconv.FormatInt(g2.ID, 10),
		groupReq{Name: "dup", Type: "B"}, cookies)
	if w.Code != http.StatusConflict {
		t.Fatalf("改名撞名应 409, got %d body=%s", w.Code, w.Body.String())
	}

	// 用自身原名更新（未改名）应放行 → 200。
	w = doJSON(t, app, "PUT", "/api/groups/"+strconv.FormatInt(g1.ID, 10),
		groupReq{Name: "dup", Type: "B"}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("保留自身名更新应 200, got %d body=%s", w.Code, w.Body.String())
	}

	// —— 规则组重名 ——
	w = doJSON(t, app, "POST", "/api/rule-groups", ruleGroupReq{Name: "rgdup", Scope: "global"}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("首个规则组应建成: %d %s", w.Code, w.Body.String())
	}
	var rg1 ruleGroupResp
	mustUnmarshal(t, w.Body.Bytes(), &rg1)

	// 同名再建 → 409。
	w = doJSON(t, app, "POST", "/api/rule-groups", ruleGroupReq{Name: "rgdup", Scope: "group"}, cookies)
	if w.Code != http.StatusConflict {
		t.Fatalf("重名规则组应 409, got %d body=%s", w.Code, w.Body.String())
	}

	// 另建一个规则组，改名撞 "rgdup" → 409。
	w = doJSON(t, app, "POST", "/api/rule-groups", ruleGroupReq{Name: "rgother", Scope: "global"}, cookies)
	var rg2 ruleGroupResp
	mustUnmarshal(t, w.Body.Bytes(), &rg2)
	w = doJSON(t, app, "PUT", "/api/rule-groups/"+strconv.FormatInt(rg2.ID, 10),
		ruleGroupReq{Name: "rgdup", Scope: "global"}, cookies)
	if w.Code != http.StatusConflict {
		t.Fatalf("规则组改名撞名应 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestLoginRateLimitNoXFFBypass 验证限流键不信任 X-Forwarded-For（FIX-H5a）：
// 攻击者每次伪造不同 XFF 也不能绕过「5 次锁定」——因为 key 取真实 RemoteAddr。
func TestLoginRateLimitNoXFFBypass(t *testing.T) {
	app := testApp(t)
	doJSON(t, app, "POST", "/api/auth/setup", credentialReq{Username: "admin", Password: "secret123"}, nil)

	// doLoginXFF 用伪造的 X-Forwarded-For 发一次错误密码登录。
	doLoginXFF := func(xff string) int {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(credentialReq{Username: "admin", Password: "wrong"})
		req := httptest.NewRequest("POST", "/api/auth/login", &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", xff) // 每次伪造不同来源，试图绕过限流
		w := httptest.NewRecorder()
		app.Router().ServeHTTP(w, req)
		return w.Code
	}

	// 用 5 个不同伪造 XFF 连打 5 次错误密码。
	// 若信任 XFF，每次 key 不同 → 永远不会锁；不信任则共用同一 RemoteAddr key → 第 5 次后锁定。
	for i := 0; i < maxLoginFails; i++ {
		if code := doLoginXFF("9.9.9." + strconv.Itoa(i)); code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401, got %d", i+1, code)
		}
	}
	// 再来一次（即便又换 XFF、即便密码正确）应已被锁定 429，证明 XFF 未能绕过限流。
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(credentialReq{Username: "admin", Password: "secret123"})
	req := httptest.NewRequest("POST", "/api/auth/login", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("伪造 XFF 不应绕过限流，应 429, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestLoginLimiterLockNotExtended 直接对限流器单测锁定不累加 + 自然过期（FIX-H5b）。
// 用很短的锁定时长，避免依赖真实 5 分钟；不引入时间 mock。
func TestLoginLimiterLockNotExtended(t *testing.T) {
	const key = "1.2.3.4:55555"
	lim := newLoginLimiter(3, 80*time.Millisecond)

	// 连续 3 次失败 → 进入锁定。
	for i := 0; i < 3; i++ {
		lim.Fail(key)
	}
	locked, remain1 := lim.Locked(key)
	if !locked {
		t.Fatal("达到阈值后应锁定")
	}

	// 锁定期间继续失败：不应延长锁定（剩余时长不增大）。
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 10; i++ {
		lim.Fail(key) // 锁定期内应被冻结，不累加、不续期
	}
	locked2, remain2 := lim.Locked(key)
	if !locked2 {
		t.Fatal("锁定期内仍应处于锁定")
	}
	// 时间已流逝 20ms，若未续期 remain2 必然 < remain1；若被续期则会重新接近满额（80ms）。
	if remain2 >= remain1 {
		t.Fatalf("锁定期失败不应延长锁定: remain1=%v remain2=%v", remain1, remain2)
	}

	// 等待自然过期后应可重新登录（解锁且计数清零）。
	time.Sleep(remain2 + 30*time.Millisecond)
	if locked3, _ := lim.Locked(key); locked3 {
		t.Fatal("锁定自然过期后应解锁")
	}
	// 过期后再失败一次不应立刻又锁（计数已清零）。
	lim.Fail(key)
	if locked4, _ := lim.Locked(key); locked4 {
		t.Fatal("过期清零后单次失败不应立即锁定")
	}
}

// —— 测试辅助 ——

func mustUnmarshal(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, string(data))
	}
}

// TestWriteOpsProduceLogs 验证关键写操作产生 slog 日志写入缓冲（系统日志页有内容）。
// 这是本轮补强的核心：CRUD 等写操作须埋点，否则日志页平时空白。
func TestWriteOpsProduceLogs(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	before := app.logs.Len()
	// 一次写操作（建分组）应至少产生 1 条日志（"创建分组"）。
	w := doJSON(t, app, "POST", "/api/groups", groupReq{Name: "poolA", Type: "B"}, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("建分组失败: %d %s", w.Code, w.Body.String())
	}
	after := app.logs.Len()
	if after <= before {
		t.Fatalf("写操作应产生日志: before=%d after=%d", before, after)
	}
	// 校验确有"创建分组"消息。
	found := false
	for _, e := range app.logs.Snapshot("") {
		if strings.Contains(e.Message, "创建分组") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("未找到\"创建分组\"日志条目")
	}
}

// TestDashboardTop 验证 Top 排行：group/user/domain 均已落地（无流量时返回空数组，200，无占位头）。
func TestDashboardTop(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// kind=group / kind=user / kind=domain：无流量时返回空数组(200，非 not-implemented)。
	for _, kind := range []string{"group", "user", "domain"} {
		w := doJSON(t, app, "GET", "/api/dashboard/top?kind="+kind, nil, cookies)
		if w.Code != http.StatusOK {
			t.Fatalf("top kind=%s 应 200, got %d %s", kind, w.Code, w.Body.String())
		}
		if w.Header().Get("X-Feature-Status") == "not-implemented" {
			t.Fatalf("top kind=%s 已落地，不应标 not-implemented", kind)
		}
	}

	// kind=domain：已落地，返回 [{name,count}] 数组（空库下为空数组），且不带 X-Feature-Status。
	w := doJSON(t, app, "GET", "/api/dashboard/top?kind=domain", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("top kind=domain 应 200, got %d", w.Code)
	}
	if w.Header().Get("X-Feature-Status") == "not-implemented" {
		t.Fatal("top kind=domain 已落地，不应标 not-implemented")
	}
	var items []map[string]any
	mustUnmarshal(t, w.Body.Bytes(), &items) // 断言 body 为 JSON 数组（空库下可为空数组）

	// 非法 kind → 400。
	w = doJSON(t, app, "GET", "/api/dashboard/top?kind=bogus", nil, cookies)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 kind 应 400, got %d", w.Code)
	}
}
