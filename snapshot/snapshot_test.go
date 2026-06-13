package snapshot

import "testing"

// snapshot_test.go：snapshot 包纯逻辑测试（不依赖 store）。
// 依赖 store 的 Rebuild/Holder 热替换/G4 回滚测试见 snapbuild 包（AC-43：snapshot 零依赖 store）。

// TestUpstreamTemplateSubstitution 验证 UpstreamView 用命名变量替换模板。
func TestUpstreamTemplateSubstitution(t *testing.T) {
	uv := UpstreamView{User: "fallback", UsernameTemplate: "acct-{region}-{session}", Host: "h", Port: 1, Pwd: "p"}
	got := uv.ToAuthUpstream(map[string]string{"region": "us", "session": "abc"})
	if got.User != "acct-us-abc" {
		t.Fatalf("模板替换错误: %q", got.User)
	}
	// 缺值补空。
	got2 := uv.ToAuthUpstream(map[string]string{"region": "us"})
	if got2.User != "acct-us-" {
		t.Fatalf("缺值补空错误: %q", got2.User)
	}
	// 无模板用定值 User。
	uv2 := UpstreamView{User: "fixed"}
	if uv2.ResolveUser(nil) != "fixed" {
		t.Fatalf("无模板应用定值")
	}
}

// TestHolderLoadSwap 验证 Holder 的基本 Load/Swap（不涉及 store）。
func TestHolderLoadSwap(t *testing.T) {
	s1 := NewSnapshot(nil, nil, nil, nil, "forward", Settings{})
	h := NewHolder(s1)
	if h.Load() != s1 {
		t.Fatal("Load 应返回初始快照")
	}
	s2 := NewSnapshot(nil, nil, nil, nil, "direct", Settings{})
	h.Swap(s2)
	if h.Load() != s2 {
		t.Fatal("Swap 后 Load 应返回新快照")
	}
	if h.Load().DefaultAction() != "direct" {
		t.Fatal("默认动作应随快照切换")
	}
}
