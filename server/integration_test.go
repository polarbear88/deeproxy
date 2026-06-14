package server

import (
	"runtime"
	"testing"
	"time"

	"deeproxy/snapbuild"
	"deeproxy/store"
)

// integration_test.go：v2 server 集成测试（AC-4/6/7/9 + Type A/B 转发、direct、reject、
// 故障转移、整组全挂回 RepHostUnreachable、CONNECT only、鉴权三拒）。
// 公共脚手架见 harness_test.go。

// TestTypeAForward 覆盖 AC-3：Type A 组，尾段 base64 动态上游 → forward 成功。
func TestTypeAForward(t *testing.T) {
	_, targetAddr := newTarget(t)
	fu := &fakeUpstream{realTarget: targetAddr}
	upAddr := fu.start(t, map[string]string{"uu": "pp"})

	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "domain-suffix:forward.test", Action: "forward"}},
		defAction: "reject",
		user:      "alice", pwd: "secret",
	})

	// 用户名 = alice-ga-<base64(uu:pp@upAddr)>，密码 = secret。
	username := "alice-ga-" + encBase64("uu", "pp", upAddr)
	rep, conn := socks5Connect(t, env.addr, username, "secret", 0x01, atypDomain, "x.forward.test", 1234)
	if rep != 0x00 {
		t.Fatalf("Type A forward 期望 REP=0x00，实际 0x%02x", rep)
	}
	body := httpGetOver(t, conn, "x.forward.test")
	if !contains(body, "HELLO-DEEPROXY") {
		t.Fatalf("响应体不含目标内容: %q", body)
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1", fu.count.Load())
	}
	if got, _ := fu.lastFQDN.Load().(string); got != "x.forward.test" {
		t.Fatalf("上游收到 FQDN=%q 期望 x.forward.test（无本地 DNS）", got)
	}
}

// TestTypeANoTailForward 覆盖 G1/AC-45：Type A 无尾段但规则命中 forward → 无上游来源 → 拒连。
func TestTypeANoTailForward(t *testing.T) {
	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "domain-suffix:forward.test", Action: "forward"}},
		defAction: "reject",
		user:      "alice", pwd: "secret",
	})

	// 无尾段：alice-ga。无上游来源拨号失败 → RepHostUnreachable(0x04)。
	rep, conn := socks5Connect(t, env.addr, "alice-ga", "secret", 0x01, atypDomain, "x.forward.test", 80)
	if conn != nil {
		_ = conn.Close()
	}
	if rep != 0x04 {
		t.Fatalf("G1 无尾段 forward 期望 REP=0x04(HostUnreachable)，实际 0x%02x", rep)
	}
}

// TestTypeBForwardSWRR 覆盖 AC-4/AC-5：Type B 池选健康上游 + 模板变量替换 → forward 成功。
func TestTypeBForwardSWRR(t *testing.T) {
	_, targetAddr := newTarget(t)
	// 上游要求认证：用户名必须是模板替换后的 "acct-us"。
	fu := &fakeUpstream{realTarget: targetAddr}
	upAddr := fu.start(t, map[string]string{"acct-us": "pp"})
	host, port := splitHostPort(t, upAddr)

	env := startDeeproxyV2(t, seedSpec{
		groupName: "gb", groupType: store.TypeB,
		rules:     []store.Rule{{Match: "domain-suffix:forward.test", Action: "forward"}},
		defAction: "reject",
		user:      "bob", pwd: "secret",
		upstreams: []store.UpstreamProxy{
			{Host: host, Port: port, User: "acct-{region}", Pwd: "pp", Weight: 1},
		},
	})

	// 尾段命名变量 region_us → 模板 {region} 替换为 us → 上游用户名 acct-us。
	rep, conn := socks5Connect(t, env.addr, "bob-gb-region_us", "secret", 0x01, atypDomain, "x.forward.test", 1234)
	if rep != 0x00 {
		t.Fatalf("Type B forward 期望 REP=0x00，实际 0x%02x", rep)
	}
	body := httpGetOver(t, conn, "x.forward.test")
	if !contains(body, "HELLO-DEEPROXY") {
		t.Fatalf("响应体不含目标内容: %q", body)
	}
	if fu.count.Load() != 1 {
		t.Fatalf("上游计数=%d 期望 1", fu.count.Load())
	}
}

// TestTypeBFailover 覆盖 AC-4 故障转移：首选上游拨号失败 → 自动重试下一个健康上游。
func TestTypeBFailover(t *testing.T) {
	_, targetAddr := newTarget(t)
	good := &fakeUpstream{realTarget: targetAddr}
	goodAddr := good.start(t, nil)
	gh, gp := splitHostPort(t, goodAddr)

	env := startDeeproxyV2(t, seedSpec{
		groupName: "gb", groupType: store.TypeB,
		rules:     []store.Rule{{Match: "domain-suffix:forward.test", Action: "forward"}},
		defAction: "reject",
		user:      "bob", pwd: "secret",
		upstreams: []store.UpstreamProxy{
			{Host: "127.0.0.1", Port: 1, Weight: 100}, // 死节点，高权重优先被选
			{Host: gh, Port: gp, Weight: 1},           // 健康节点
		},
	})

	rep, conn := socks5Connect(t, env.addr, "bob-gb", "secret", 0x01, atypDomain, "x.forward.test", 1234)
	if rep != 0x00 {
		t.Fatalf("故障转移期望最终 REP=0x00，实际 0x%02x", rep)
	}
	body := httpGetOver(t, conn, "x.forward.test")
	if !contains(body, "HELLO-DEEPROXY") {
		t.Fatalf("故障转移后响应体不含目标内容: %q", body)
	}
	if good.count.Load() != 1 {
		t.Fatalf("健康上游计数=%d 期望 1（故障转移命中）", good.count.Load())
	}
}

// TestTypeBAllDown 覆盖 G6/AC-17/AC-46：Type B 整组全挂 → 拒连且回 RepHostUnreachable(0x04)。
func TestTypeBAllDown(t *testing.T) {
	env := startDeeproxyV2(t, seedSpec{
		groupName: "gb", groupType: store.TypeB,
		rules:     []store.Rule{{Match: "domain-suffix:forward.test", Action: "forward"}},
		defAction: "reject",
		user:      "bob", pwd: "secret",
		upstreams: nil, // 无上游 → 健康池为空（模拟整组全挂）
	})

	rep, conn := socks5Connect(t, env.addr, "bob-gb", "secret", 0x01, atypDomain, "x.forward.test", 80)
	if conn != nil {
		_ = conn.Close()
	}
	if rep != 0x04 {
		t.Fatalf("整组全挂期望 REP=0x04(HostUnreachable)，实际 0x%02x", rep)
	}
}

// TestDirect 覆盖 AC-7 direct：ip-cidr 命中 direct → 本机直连，上游计数不变。
func TestDirect(t *testing.T) {
	_, targetAddr := newTarget(t)
	host, port := splitHostPort(t, targetAddr)

	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "ip-cidr:127.0.0.0/8", Action: "direct"}},
		defAction: "reject",
		user:      "alice", pwd: "secret",
	})

	rep, conn := socks5Connect(t, env.addr, "alice-ga", "secret", 0x01, atypIPv4, host, port)
	if rep != 0x00 {
		t.Fatalf("direct 期望 REP=0x00，实际 0x%02x", rep)
	}
	body := httpGetOver(t, conn, host)
	if !contains(body, "HELLO-DEEPROXY") {
		t.Fatalf("direct 响应体不含目标内容: %q", body)
	}
}

// TestReject 覆盖 AC-7 reject：规则 reject → REP=0x02 (RuleFailure)。
func TestReject(t *testing.T) {
	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "domain-suffix:reject.test", Action: "reject"}},
		defAction: "forward",
		user:      "alice", pwd: "secret",
	})

	rep, conn := socks5Connect(t, env.addr, "alice-ga", "secret", 0x01, atypDomain, "x.reject.test", 80)
	if conn != nil {
		_ = conn.Close()
	}
	if rep != 0x02 {
		t.Fatalf("reject 期望 REP=0x02，实际 0x%02x", rep)
	}
}

// TestAuthRejections 覆盖 AC-6：用户不存在 / 密码错误 / 未授权分组 三种鉴权失败均拒连。
func TestAuthRejections(t *testing.T) {
	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "domain-suffix:any.test", Action: "direct"}},
		defAction: "direct",
		user:      "alice", pwd: "secret",
	})
	// 额外建一个未授权 ga 的用户 carol（明文密码）。
	cu := &store.ProxyUser{Username: "carol", Pwd: "pw"}
	_ = env.store.CreateProxyUser(cu)
	_ = snapbuild.RebuildAndSwap(env.holder, env.store, env.cfg)

	cases := []struct{ name, user, pwd string }{
		{"用户不存在", "nobody-ga", "secret"},
		{"密码错误", "alice-ga", "WRONG"},
		{"未授权分组", "carol-ga", "pw"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep, conn := socks5Connect(t, env.addr, c.user, c.pwd, 0x01, atypDomain, "x.any.test", 80)
			if conn != nil {
				_ = conn.Close()
			}
			// 鉴权失败 → 库在认证子协商阶段返回失败，脚手架以 0xFF 标记。
			if rep != 0xFF {
				t.Fatalf("%s 期望鉴权失败(0xFF)，实际 0x%02x", c.name, rep)
			}
		})
	}
}

// TestNonConnectCommands 覆盖 AC-9：BIND / UDP ASSOCIATE 均被拒（REP=0x02）。
func TestNonConnectCommands(t *testing.T) {
	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "domain-suffix:any.test", Action: "forward"}},
		defAction: "forward",
		user:      "alice", pwd: "secret",
	})

	for _, cmd := range []byte{0x02 /*BIND*/, 0x03 /*ASSOCIATE*/} {
		rep, conn := socks5Connect(t, env.addr, "alice-ga", "secret", cmd, atypDomain, "x.any.test", 80)
		if conn != nil {
			_ = conn.Close()
		}
		if rep != 0x02 {
			t.Fatalf("命令 0x%02x 期望 REP=0x02，实际 0x%02x", cmd, rep)
		}
	}
}

// TestUpstreamUnreachableNoLeak：上游不可达回网络错误码且无 goroutine 泄漏。
func TestUpstreamUnreachableNoLeak(t *testing.T) {
	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "domain-suffix:forward.test", Action: "forward"}},
		defAction: "reject",
		user:      "alice", pwd: "secret",
	})

	before := runtime.NumGoroutine()
	username := "alice-ga-" + encBase64("uu", "pp", "127.0.0.1:1") // 死上游
	rep, conn := socks5Connect(t, env.addr, username, "secret", 0x01, atypDomain, "x.forward.test", 80)
	if conn != nil {
		_ = conn.Close()
	}
	if rep == 0x00 || rep == 0x02 {
		t.Fatalf("上游不可达期望网络类错误码，实际 0x%02x", rep)
	}
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("疑似 goroutine 泄漏：before=%d after=%d", before, after)
	}
}

// TestStatsAndAudit 覆盖埋点：一次成功 direct 连接后审计有记录、实时快照可读不 panic。
func TestStatsAndAudit(t *testing.T) {
	_, targetAddr := newTarget(t)
	host, port := splitHostPort(t, targetAddr)

	env := startDeeproxyV2(t, seedSpec{
		groupName: "ga", groupType: store.TypeA,
		rules:     []store.Rule{{Match: "ip-cidr:127.0.0.0/8", Action: "direct"}},
		defAction: "reject",
		user:      "alice", pwd: "secret",
	})

	rep, conn := socks5Connect(t, env.addr, "alice-ga", "secret", 0x01, atypIPv4, host, port)
	if rep != 0x00 {
		t.Fatalf("期望成功，实际 0x%02x", rep)
	}
	_ = httpGetOver(t, conn, host)
	time.Sleep(100 * time.Millisecond)

	if env.audit.Len() < 1 {
		t.Fatalf("审计应至少有 1 条记录，实际 %d", env.audit.Len())
	}
	_ = env.counter.RealtimeSnapshot() // 只断言可读不 panic
}
