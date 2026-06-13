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

	cfg := &config.Config{}
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

	return NewApp(st, holder, cfg, counter, logs, audit, health, logger, nil)
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

// TestBadRuleRollback 验证非法规则导致快照重建失败时 G4 回滚（写入但返回错误）。
func TestBadRuleRollback(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 需至少一个分组，全局规则才会在该组的合并引擎中被预编译校验。
	doJSON(t, app, "POST", "/api/groups", groupReq{Name: "poolA", Type: "B"}, cookies)

	w := doJSON(t, app, "POST", "/api/rule-groups", ruleGroupReq{Name: "g", Scope: "global"}, cookies)
	var rg ruleGroupResp
	mustUnmarshal(t, w.Body.Bytes(), &rg)

	// 非法 ip-cidr → 预编译失败 → 500（G4：返回错误）。
	w = doJSON(t, app, "POST", "/api/rule-groups/"+strconv.FormatInt(rg.ID, 10)+"/rules",
		ruleReq{Match: "ip-cidr:not-a-cidr", Action: "direct", Order: 1}, cookies)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("非法规则应 500（G4 回滚）, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestImportExportRoundTrip 验证配置导入导出回环 + schemaVersion 校验（AC-37/G4）。
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

// TestDashboardTop 验证 Top 排行：group/user 落地，domain 显式占位（X-Feature-Status）。
func TestDashboardTop(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// kind=group / kind=user：无流量时返回空数组(200，非 not-implemented)。
	for _, kind := range []string{"group", "user"} {
		w := doJSON(t, app, "GET", "/api/dashboard/top?kind="+kind, nil, cookies)
		if w.Code != http.StatusOK {
			t.Fatalf("top kind=%s 应 200, got %d %s", kind, w.Code, w.Body.String())
		}
		if w.Header().Get("X-Feature-Status") == "not-implemented" {
			t.Fatalf("top kind=%s 已落地，不应标 not-implemented", kind)
		}
	}

	// kind=domain：首版占位，应带 X-Feature-Status: not-implemented。
	w := doJSON(t, app, "GET", "/api/dashboard/top?kind=domain", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("top kind=domain 应 200, got %d", w.Code)
	}
	if w.Header().Get("X-Feature-Status") != "not-implemented" {
		t.Fatal("top kind=domain 应标 X-Feature-Status: not-implemented")
	}

	// 非法 kind → 400。
	w = doJSON(t, app, "GET", "/api/dashboard/top?kind=bogus", nil, cookies)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 kind 应 400, got %d", w.Code)
	}
}
