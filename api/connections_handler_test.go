package api

import (
	"net/http"
	"testing"
	"time"

	"deeproxy/connreg"
)

// 本文件测试「实时连接」列表 API：GET /api/connections。
// 用 testApp + 登录态，直接向 app.connReg 注入已知活跃连接后断言响应形态与排序/截断。

// TestListConnections 覆盖响应形态、截断、duration 排序（最旧/最长优先）。
func TestListConnections(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	// 注入一个可控的活跃连接登记表（覆盖 testApp 里 nil 兜底生成的空表）。
	reg := connreg.New()
	app.connReg = reg
	base := time.Now().Add(-1 * time.Hour)
	// 登记 3 条 Start 递增的连接（id 越大越新；duration 越大=越旧）。
	id1 := reg.Register(connreg.ConnMeta{Target: "a.test", Action: "forward", User: "u", Group: "g", Client: "c1", Start: base})
	_ = reg.Register(connreg.ConnMeta{Target: "b.test", Action: "direct", User: "u", Group: "g", Client: "c2", Start: base.Add(1 * time.Second)})
	_ = reg.Register(connreg.ConnMeta{Target: "c.test", Action: "forward", User: "u", Group: "g", Client: "c3", Start: base.Add(2 * time.Second)})
	reg.SetUpstream(id1, "1.2.3.4:1080")

	// limit=2 + sort=duration：应返回 2 条最旧者（最长时长），按最旧优先排序。
	w := doJSON(t, app, "GET", "/api/connections?limit=2&sort=duration", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("实时连接接口失败: %d %s", w.Code, w.Body.String())
	}
	var resp connListResp
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	if resp.Total != 3 {
		t.Fatalf("total 应为 3（精确活跃数），得 %d", resp.Total)
	}
	if resp.Limit != 2 {
		t.Fatalf("limit 应回显 2，得 %d", resp.Limit)
	}
	if !resp.Truncated {
		t.Fatalf("total>limit 时 truncated 应为 true")
	}
	if len(resp.Items) != 2 {
		t.Fatalf("应返回 2 条，得 %d", len(resp.Items))
	}
	// duration 模式最旧优先：第 1 条应是最早的 a.test，第 2 条 b.test。
	if resp.Items[0].Target != "a.test" || resp.Items[1].Target != "b.test" {
		t.Fatalf("duration 排序应为最旧优先 [a,b]，得 [%s,%s]", resp.Items[0].Target, resp.Items[1].Target)
	}
	// 上游回填生效，且字段形态正确。
	if resp.Items[0].Upstream != "1.2.3.4:1080" {
		t.Fatalf("a.test 上游应为 1.2.3.4:1080，得 %q", resp.Items[0].Upstream)
	}
	if resp.Items[0].Action != "forward" || resp.Items[1].Action != "direct" {
		t.Fatalf("动作字段异常：%q/%q", resp.Items[0].Action, resp.Items[1].Action)
	}
	if resp.Items[0].StartTs == 0 || resp.Items[0].DurationSec <= 0 {
		t.Fatalf("start_ts/duration_sec 应有有效值，得 ts=%d dur=%d", resp.Items[0].StartTs, resp.Items[0].DurationSec)
	}
}

// TestListConnectionsEmpty 覆盖空表：返回空 items、total=0、未截断。
func TestListConnectionsEmpty(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)
	app.connReg = connreg.New()

	w := doJSON(t, app, "GET", "/api/connections", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("空表请求失败: %d %s", w.Code, w.Body.String())
	}
	var resp connListResp
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.Total != 0 || len(resp.Items) != 0 || resp.Truncated {
		t.Fatalf("空表应 total=0/items=0/未截断，得 total=%d len=%d truncated=%v", resp.Total, len(resp.Items), resp.Truncated)
	}
}
