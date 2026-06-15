package api

import (
	"fmt"
	"net/http"
	"testing"

	"deeproxy/syslog"
)

// 本文件测试连接审计分页 + 四维筛选接口：GET /api/syslog/audit。
// 用 testApp + 登录态，向 app.audit 注入已知审计记录后断言分页结构、排序与边界安全。

// seedAudit 向 app 的审计缓冲注入 n 条记录（user/group/action/target 按规律生成，便于筛选断言）。
func seedAudit(app *App, n int) {
	for i := 0; i < n; i++ {
		action := "forward"
		if i%3 == 1 {
			action = "direct"
		} else if i%3 == 2 {
			action = "reject"
		}
		app.audit.Record(syslog.AuditEntry{
			User:   fmt.Sprintf("user%d", i%2),       // user0 / user1
			Group:  fmt.Sprintf("grp%d", i%2),         // grp0 / grp1
			Target: fmt.Sprintf("host%d.example.com:443", i),
			Action: action,
		})
	}
}

// TestAuditPaginationBasic 验证基本分页结构与最新→最旧排序。
func TestAuditPaginationBasic(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)
	app.audit = syslog.NewAuditBuffer(5000)
	seedAudit(app, 120) // 注入 120 条（最后写入的 host119 最新）

	w := doJSON(t, app, "GET", "/api/syslog/audit?page=1&pageSize=10", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("审计接口失败: %d %s", w.Code, w.Body.String())
	}
	var resp auditPage
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	if resp.Total != 120 {
		t.Fatalf("total 应为 120，得 %d", resp.Total)
	}
	if resp.Page != 1 || resp.PageSize != 10 {
		t.Fatalf("分页回显异常: page=%d pageSize=%d", resp.Page, resp.PageSize)
	}
	if len(resp.Items) != 10 {
		t.Fatalf("应返回 10 条，得 %d", len(resp.Items))
	}
	// 最新→最旧：第 1 条应为最后写入的 host119。
	if resp.Items[0].Target != "host119.example.com:443" {
		t.Fatalf("首条应为最新 host119，得 %q", resp.Items[0].Target)
	}
}

// TestAuditPaginationOutOfRange 验证越界 page（防 panic 守门 AC）。
func TestAuditPaginationOutOfRange(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)
	app.audit = syslog.NewAuditBuffer(5000)
	seedAudit(app, 30)

	// page=99999 远超总页数：应返回空 items + 正确 total，绝不 panic / 报错。
	w := doJSON(t, app, "GET", "/api/syslog/audit?page=99999&pageSize=10", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("越界 page 应正常返回 200，得 %d %s", w.Code, w.Body.String())
	}
	var resp auditPage
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.Total != 30 {
		t.Fatalf("越界时 total 仍应为 30，得 %d", resp.Total)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("越界 page 应返回空 items，得 %d 条", len(resp.Items))
	}
}

// TestAuditPageSizeClamp 验证 pageSize 上限钳到 200、下限归默认。
func TestAuditPageSizeClamp(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)
	app.audit = syslog.NewAuditBuffer(5000)
	seedAudit(app, 300)

	// pageSize=9999 应被钳到 200。
	w := doJSON(t, app, "GET", "/api/syslog/audit?page=1&pageSize=9999", nil, cookies)
	var resp auditPage
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.PageSize != 200 {
		t.Fatalf("pageSize 应钳到 200，得 %d", resp.PageSize)
	}
	if len(resp.Items) != 200 {
		t.Fatalf("应返回 200 条，得 %d", len(resp.Items))
	}

	// pageSize=0 应归默认 50；page=0 应钳到 1。
	w = doJSON(t, app, "GET", "/api/syslog/audit?page=0&pageSize=0", nil, cookies)
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.PageSize != 50 || resp.Page != 1 {
		t.Fatalf("page/pageSize 默认/钳制异常: page=%d pageSize=%d", resp.Page, resp.PageSize)
	}
}

// TestAuditFilter 验证四维筛选（user 精确、action 精确、target 子串、group 精确）。
func TestAuditFilter(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)
	app.audit = syslog.NewAuditBuffer(5000)
	seedAudit(app, 90) // user0/user1 各半，action 三类轮转

	// action=forward 精确筛选：i%3==0 的为 forward，90 条里应有 30 条。
	w := doJSON(t, app, "GET", "/api/syslog/audit?action=forward&pageSize=200", nil, cookies)
	var resp auditPage
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.Total != 30 {
		t.Fatalf("action=forward 应有 30 条，得 %d", resp.Total)
	}
	for _, e := range resp.Items {
		if e.Action != "forward" {
			t.Fatalf("筛选后混入非 forward 记录: %q", e.Action)
		}
	}

	// user=user0 精确筛选：i%2==0 的为 user0，90 条里 45 条。
	w = doJSON(t, app, "GET", "/api/syslog/audit?user=user0&pageSize=200", nil, cookies)
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.Total != 45 {
		t.Fatalf("user=user0 应有 45 条，得 %d", resp.Total)
	}

	// target 子串：host5.example.com 只精确命中 host5（host50-59 也含 "host5"，故用更具区分度的子串）。
	w = doJSON(t, app, "GET", "/api/syslog/audit?target=host5.example.com:443&pageSize=200", nil, cookies)
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Fatalf("target 子串 host5.example.com:443 应命中 1 条，得 %d", resp.Total)
	}

	// group + action 组合精确筛选：grp0(i%2==0) 且 forward(i%3==0) → i 同时被 2、3 整除 = i%6==0，90 条里 15 条。
	w = doJSON(t, app, "GET", "/api/syslog/audit?group=grp0&action=forward&pageSize=200", nil, cookies)
	mustUnmarshal(t, w.Body.Bytes(), &resp)
	if resp.Total != 15 {
		t.Fatalf("group=grp0 且 action=forward 应有 15 条，得 %d", resp.Total)
	}
}
