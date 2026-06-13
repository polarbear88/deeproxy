// 本文件验证 FIX-CFG：取消配置文件后，5 项运行期设置（default_action / log_level /
// idle_timeout_sec / sniff_domain / sniff_timeout_ms）经后台 API 动态读写，并物化进转发快照。
package api

import (
	"log/slog"
	"net/http"
	"testing"
)

// TestSettings_DynamicRoundTrip 覆盖：GET 返回 5 项默认值 → PUT 改值 → 再 GET 反映新值，
// 且默认动作 / 空闲 / 嗅探 已物化进转发快照（holder.Load），log_level 经 LevelVar 热生效。
func TestSettings_DynamicRoundTrip(t *testing.T) {
	app := testApp(t)
	// 注入一个 LevelVar，断言后台改 log_level 后它被原子更新（热生效）。
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	app.levelVar = lv

	cookies := setupAndLogin(t, app)

	// —— 初始 GET：应为建库默认值 forward/info/300/true/300 ——
	var got settingsResp
	w := doJSON(t, app, "GET", "/api/settings", nil, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /settings 状态码=%d", w.Code)
	}
	mustUnmarshal(t, w.Body.Bytes(), &got)
	if got.DefaultAction != "forward" || got.LogLevel != "info" ||
		got.IdleTimeoutSec != 300 || !got.SniffDomain || got.SniffTimeoutMs != 300 {
		t.Fatalf("初始默认值不符: %+v", got)
	}

	// —— PUT 改这 5 项 ——
	sniffOff := false
	put := settingsReq{
		DefaultAction:  "direct",
		LogLevel:       "debug",
		IdleTimeoutSec: 120,
		SniffDomain:    &sniffOff,
		SniffTimeoutMs: 500,
	}
	w = doJSON(t, app, "PUT", "/api/settings", put, cookies)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /settings 状态码=%d body=%s", w.Code, w.Body.String())
	}

	// —— 再 GET：应反映新值 ——
	w = doJSON(t, app, "GET", "/api/settings", nil, cookies)
	mustUnmarshal(t, w.Body.Bytes(), &got)
	if got.DefaultAction != "direct" || got.LogLevel != "debug" ||
		got.IdleTimeoutSec != 120 || got.SniffDomain || got.SniffTimeoutMs != 500 {
		t.Fatalf("PUT 后值未更新: %+v", got)
	}

	// —— 转发快照已物化新设置（PUT 内部触发 RebuildAndSwap）——
	snap := app.holder.Load()
	if snap.DefaultAction() != "direct" {
		t.Fatalf("快照默认动作=%q 期望 direct", snap.DefaultAction())
	}
	st := snap.Settings()
	if st.IdleTimeout.Seconds() != 120 {
		t.Fatalf("快照空闲超时=%v 期望 120s", st.IdleTimeout)
	}
	if st.SniffDomain {
		t.Fatal("快照嗅探开关应为关闭")
	}
	if st.SniffTimeout.Milliseconds() != 500 {
		t.Fatalf("快照嗅探超时=%v 期望 500ms", st.SniffTimeout)
	}

	// —— log_level 热生效：LevelVar 应被原子更新为 debug ——
	if lv.Level() != slog.LevelDebug {
		t.Fatalf("LevelVar=%v 期望 debug（log_level 应热生效）", lv.Level())
	}
}

// TestSettings_RejectInvalid 覆盖非法值（默认动作 / 日志级别）被拒、不写库。
func TestSettings_RejectInvalid(t *testing.T) {
	app := testApp(t)
	cookies := setupAndLogin(t, app)

	w := doJSON(t, app, "PUT", "/api/settings", settingsReq{DefaultAction: "bogus"}, cookies)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法默认动作应 400，实际=%d", w.Code)
	}
	w = doJSON(t, app, "PUT", "/api/settings", settingsReq{LogLevel: "trace"}, cookies)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法日志级别应 400，实际=%d", w.Code)
	}
}
