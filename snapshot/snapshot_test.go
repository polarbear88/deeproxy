package snapshot

import "testing"

// snapshot_test.go：snapshot 包纯逻辑测试（不依赖 store）。
// 依赖 store 的 Rebuild/Holder 热替换/G4 回滚测试见 snapbuild 包（AC-43：snapshot 零依赖 store）。

// TestUpstreamTemplateSubstitution 验证 UpstreamView 用命名变量替换模板。
func TestUpstreamTemplateSubstitution(t *testing.T) {
	uv := UpstreamView{User: "acct-{region}-{session}", Host: "h", Port: 1, Pwd: "p"}
	got := uv.ToAuthUpstream(map[string]string{"region": "us", "session": "abc"})
	if got.User != "acct-us-abc" {
		t.Fatalf("模板替换错误: %q", got.User)
	}
	// 缺值补空。
	got2 := uv.ToAuthUpstream(map[string]string{"region": "us"})
	if got2.User != "acct-us-" {
		t.Fatalf("缺值补空错误: %q", got2.User)
	}
	// 无占位的 User 原样返回（等价定值）。
	uv2 := UpstreamView{User: "fixed"}
	if uv2.ResolveUser(nil) != "fixed" {
		t.Fatalf("无占位应原样返回 User")
	}
}

// TestHolderLoadSwap 验证 Holder 的基本 Load/Swap（不涉及 store）。
func TestHolderLoadSwap(t *testing.T) {
	s1 := NewSnapshot(nil, nil, nil, nil, nil, "forward", Settings{})
	h := NewHolder(s1)
	if h.Load() != s1 {
		t.Fatal("Load 应返回初始快照")
	}
	s2 := NewSnapshot(nil, nil, nil, nil, nil, "direct", Settings{})
	h.Swap(s2)
	if h.Load() != s2 {
		t.Fatal("Swap 后 Load 应返回新快照")
	}
	if h.Load().DefaultAction() != "direct" {
		t.Fatal("默认动作应随快照切换")
	}
}

// TestIsAuthorizedAllGroups 验证 DEC-B1：IsAuthorized = all_groups 命中 OR 精细行命中（并存）。
func TestIsAuthorizedAllGroups(t *testing.T) {
	// 用户 1：开 all_groups（通配）；用户 2：仅逐组授权分组 10；用户 3：无任何授权。
	authz := map[AuthzKey]struct{}{
		NewAuthzKey(10, 2): {}, // 用户2 ↔ 分组10
	}
	allGroups := map[int64]struct{}{
		1: {}, // 用户1 通配全部分组
	}
	s := NewSnapshot(nil, nil, nil, authz, allGroups, "forward", Settings{})

	cases := []struct {
		group, user int64
		want        bool
		why         string
	}{
		{10, 1, true, "用户1 all_groups → 任意分组放行"},
		{999, 1, true, "用户1 all_groups → 未来新分组也放行"},
		{10, 2, true, "用户2 逐组授权分组10命中"},
		{11, 2, false, "用户2 未授权分组11拒绝"},
		{10, 3, false, "用户3 无授权拒绝"},
	}
	for _, c := range cases {
		if got := s.IsAuthorized(c.group, c.user); got != c.want {
			t.Errorf("IsAuthorized(g=%d,u=%d)=%v 期望 %v（%s）", c.group, c.user, got, c.want, c.why)
		}
	}
}
