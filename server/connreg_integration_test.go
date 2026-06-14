package server

import (
	"testing"
	"time"

	"deeproxy/store"
)

// 本文件覆盖「实时连接」功能的 never-reject 不变量（共识评审 C1）：
// 被拒绝的连接结构上不得出现在活跃连接登记表里——
//   - Allow 阶段 reject：在规则判定阶段即关闭，从不进入 connectHandle，故从不 Register；
//   - 嗅探阶段 reject：connectHandle 已回 success 才嗅探，会瞬时 Register，但嗅探解析出
//     reject 时在 SetAction 守卫之前 return，绝不写入 action="reject"，且连接关闭即 Deregister。
// 二者最终都保证：活跃快照中永不出现 action=="reject" 的条目。

// connRegSnapLimit 是测试快照用的上限（足够覆盖测试中的少量连接）。
const connRegSnapLimit = 100

// assertNoRejectAction 断言当前活跃快照里没有任何 action=="reject" 的条目。
func assertNoRejectAction(t *testing.T, env *deeproxyEnv) {
	t.Helper()
	items, _, _ := env.conns.Snapshot(connRegSnapLimit, "start")
	for _, it := range items {
		if it.Action == "reject" {
			t.Fatalf("活跃连接快照不应出现 action=reject，得 %+v", it)
		}
	}
}

// waitLenZero 轮询等待活跃连接数归零（连接关闭后 defer Deregister 异步生效）。
func waitLenZero(t *testing.T, env *deeproxyEnv) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if env.conns.Len() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("连接关闭后活跃数未归零，Len()=%d", env.conns.Len())
}

// TestRealtimeConns_AllowReject_NeverRegisters 覆盖 Allow 阶段 reject：从不登记。
func TestRealtimeConns_AllowReject_NeverRegisters(t *testing.T) {
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
	// Allow 阶段 reject 在 connectHandle 之前关闭，故永不登记：Len 恒为 0。
	if env.conns.Len() != 0 {
		t.Fatalf("Allow 阶段 reject 不应登记任何活跃连接，Len()=%d", env.conns.Len())
	}
	assertNoRejectAction(t, env)
}

// TestRealtimeConns_SniffReject_NeverShowsReject 覆盖嗅探阶段 reject：
// 瞬时登记但绝不写 action=reject，连接关闭后归零。
func TestRealtimeConns_SniffReject_NeverShowsReject(t *testing.T) {
	env, _, user := sniffEnv(t,
		[]store.Rule{{Match: "domain-suffix:block.test", Action: "reject"}},
		"forward")

	rep, conn := socks5Connect(t, env.addr, user, "secret", 0x01, atypIPv4, "1.2.3.4", 443)
	if rep != 0x00 {
		t.Fatalf("嗅探路径应先回 success(0x00)，实际 0x%02x", rep)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(makeClientHello(t, "block.test")); err != nil {
		// 写入即失败也算连接被拒——仍需校验归零与无 reject 动作。
		waitLenZero(t, env)
		assertNoRejectAction(t, env)
		return
	}
	// 嗅探 reject 后连接被关闭。
	_, _ = conn.Read(make([]byte, 16))
	_ = conn.Close()

	// 关闭后活跃数应归零（瞬时登记的条目已 Deregister）。
	waitLenZero(t, env)
	// 全程绝不出现 action=reject（SetAction 在 reject 守卫之后，嗅探 reject 不会触达）。
	assertNoRejectAction(t, env)
}
