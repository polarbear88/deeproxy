package auth

import (
	"testing"

	"deeproxy/domain"
)

// fakeSnapshot 是 SnapshotView 的测试替身，用内存 map 模拟快照查询。
// 仅用于 auth 包单测，不依赖真实 snapshot/SQLite，保持 auth 测试纯粹快速。
type fakeSnapshot struct {
	users  map[string]UserInfo   // username → UserInfo
	groups map[string]GroupInfo  // groupName → GroupInfo
	authz  map[[2]int64]struct{} // {groupID, userID} 已授权集合
}

func (f *fakeSnapshot) LookupUser(name string) (UserInfo, bool) {
	u, ok := f.users[name]
	return u, ok
}
func (f *fakeSnapshot) LookupGroup(name string) (GroupInfo, bool) {
	g, ok := f.groups[name]
	return g, ok
}
func (f *fakeSnapshot) IsAuthorized(groupID, userID int64) bool {
	_, ok := f.authz[[2]int64{groupID, userID}]
	return ok
}

// newFakeSnap 构造一个含 alice（明文密码 correct-pwd）+ 两个分组（gA=TypeA, gB=TypeB）
// + alice 对两组均授权 的测试快照。ProxyUser 密码明文存储（决策 #2），故直接放明文。
func newFakeSnap() *fakeSnapshot {
	return &fakeSnapshot{
		users: map[string]UserInfo{
			"alice": {ID: 1, Pwd: "correct-pwd"},
		},
		groups: map[string]GroupInfo{
			"gA": {ID: 10, Type: domain.TypeA},
			"gB": {ID: 20, Type: domain.TypeB},
		},
		authz: map[[2]int64]struct{}{
			{10, 1}: {}, // gA ↔ alice
			{20, 1}: {}, // gB ↔ alice
		},
	}
}

// TestVerify_Success_TypeA 覆盖 Type A：尾段 base64 解出动态上游（AC-3）。
func TestVerify_Success_TypeA(t *testing.T) {
	snap := newFakeSnap()
	tail := EncodeUpstream(Upstream{Host: "up.com", Port: 1080, User: "uu", Pwd: "pp"})
	d, err := Verify(snap, "alice-gA-"+tail, "correct-pwd")
	if err != nil {
		t.Fatalf("应鉴权通过: %v", err)
	}
	if d.GroupType != domain.TypeA || !d.HasDynamicUpstream {
		t.Fatalf("Type A 应解出动态上游: %+v", d)
	}
	if d.DynamicUpstream.Host != "up.com" || d.DynamicUpstream.Port != 1080 ||
		d.DynamicUpstream.User != "uu" || d.DynamicUpstream.Pwd != "pp" {
		t.Fatalf("动态上游解析错误: %+v", d.DynamicUpstream)
	}
	if d.User != "alice" || d.UserID != 1 || d.Group != "gA" || d.GroupID != 10 {
		t.Fatalf("身份字段错误: %+v", d)
	}
}

// TestVerify_TypeA_NoTail 覆盖 G1 前置：Type A 无尾段鉴权仍通过、无动态上游
// （是否拒连由 T6 规则阶段按动作决定，鉴权阶段不拒）。
func TestVerify_TypeA_NoTail(t *testing.T) {
	snap := newFakeSnap()
	d, err := Verify(snap, "alice-gA", "correct-pwd")
	if err != nil {
		t.Fatalf("Type A 无尾段应鉴权通过: %v", err)
	}
	if d.HasDynamicUpstream {
		t.Fatalf("无尾段不应有动态上游: %+v", d)
	}
}

// TestVerify_TypeA_BadBase64 覆盖：Type A 尾段存在却非法 → 拒连。
func TestVerify_TypeA_BadBase64(t *testing.T) {
	snap := newFakeSnap()
	if _, err := Verify(snap, "alice-gA-@@@notbase64", "correct-pwd"); err == nil {
		t.Fatal("Type A 非法尾段应拒连")
	}
}

// TestVerify_Success_TypeB 覆盖 Type B：尾段解析为命名变量 map，不在此替换模板（AC-5）。
func TestVerify_Success_TypeB(t *testing.T) {
	snap := newFakeSnap()
	d, err := Verify(snap, "alice-gB-region_us#session_abc123", "correct-pwd")
	if err != nil {
		t.Fatalf("应鉴权通过: %v", err)
	}
	if d.GroupType != domain.TypeB {
		t.Fatalf("应为 Type B: %+v", d)
	}
	if d.Variables["region"] != "us" || d.Variables["session"] != "abc123" {
		t.Fatalf("命名变量解析错误: %+v", d.Variables)
	}
	if d.HasDynamicUpstream {
		t.Fatal("Type B 不应有动态上游")
	}
}

// TestVerify_TypeB_NoTail 覆盖 Type B 无尾段：变量为空 map，鉴权通过。
func TestVerify_TypeB_NoTail(t *testing.T) {
	snap := newFakeSnap()
	d, err := Verify(snap, "alice-gB", "correct-pwd")
	if err != nil {
		t.Fatalf("Type B 无尾段应鉴权通过: %v", err)
	}
	if len(d.Variables) != 0 {
		t.Fatalf("无尾段变量应为空: %+v", d.Variables)
	}
}

// TestVerify_AuthFailures 覆盖 AC-6 三拒：用户不存在 / 密码错 / 未授权。
func TestVerify_AuthFailures(t *testing.T) {
	snap := newFakeSnap()
	// 用户不存在
	if _, err := Verify(snap, "nobody-gA", "correct-pwd"); err == nil {
		t.Fatal("用户不存在应拒连")
	}
	// 密码错误（明文比对不匹配）
	if _, err := Verify(snap, "alice-gA", "wrong-pwd"); err == nil {
		t.Fatal("密码错误应拒连")
	}
	// 未授权该分组：构造一个 alice 未授权的组 gC
	snap.groups["gC"] = GroupInfo{ID: 30, Type: domain.TypeA}
	if _, err := Verify(snap, "alice-gC", "correct-pwd"); err == nil {
		t.Fatal("未授权分组应拒连")
	}
	// 分组不存在
	if _, err := Verify(snap, "alice-nogroup", "correct-pwd"); err == nil {
		t.Fatal("分组不存在应拒连")
	}
}

// TestVerify_PasswordCompare 专项覆盖密码比对（FIX-H3 改为 crypto/subtle.ConstantTimeCompare）。
// 验证目标是【功能正确性】：常量时间比较只改变耗时特征、不改变 相等/不等 的判定语义，
// 故这里断言各类密码输入的 通过/拒绝 结果与预期一致（正确通过、其余均拒）。
func TestVerify_PasswordCompare(t *testing.T) {
	snap := newFakeSnap() // alice 明文密码为 "correct-pwd"

	cases := []struct {
		name    string
		pwd     string
		wantOK  bool
		comment string
	}{
		{"正确密码应通过", "correct-pwd", true, "明文完全相等 → ConstantTimeCompare 返回 1"},
		{"错误密码应拒", "wrong-pwd", false, "内容不同但长度相同 → 返回 0"},
		{"空密码应拒", "", false, "空串与非空长度不同 → 返回 0"},
		{"较短密码应拒", "correct", false, "前缀相同但更短，不同长度 → 返回 0（防前缀时序猜测）"},
		{"较长密码应拒", "correct-pwd-extra", false, "前缀相同但更长，不同长度 → 返回 0"},
		{"大小写不同应拒", "CORRECT-PWD", false, "等长内容不同 → 返回 0，比对大小写敏感"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Verify(snap, "alice-gB", c.pwd)
			if c.wantOK && err != nil {
				t.Fatalf("%s：期望通过却被拒(%s): %v", c.name, c.comment, err)
			}
			if !c.wantOK && err == nil {
				t.Fatalf("%s：期望拒绝却通过(%s)", c.name, c.comment)
			}
		})
	}
}

// TestVerify_BadUsername 覆盖用户名语法非法 → 拒连。
func TestVerify_BadUsername(t *testing.T) {
	snap := newFakeSnap()
	for _, bad := range []string{"", "noseparator", "-gA", "alice-"} {
		if _, err := Verify(snap, bad, "correct-pwd"); err == nil {
			t.Fatalf("非法用户名 %q 应拒连", bad)
		}
	}
}

// TestParseOnly_NoPasswordCheck 是 AC-43 修复的核心断言：
// ParseOnly 不校验密码——即便密码错误（甚至空密码），只要用户名/授权/尾段合法，
// 也返回成功的 *Decision（密码正确性由 Valid 阶段的 Verify 负责，Allow 不重复校验）。
// 这保证 Allow 阶段零密码校验、零 bcrypt（本项目 ProxyUser 明文，亦无 bcrypt）。
func TestParseOnly_NoPasswordCheck(t *testing.T) {
	snap := newFakeSnap()
	tail := EncodeUpstream(Upstream{Host: "up.com", Port: 1080, User: "uu", Pwd: "pp"})
	// 注意：ParseOnly 无 password 参数——它根本不接触密码。
	d, err := ParseOnly(snap, "alice-gA-"+tail)
	if err != nil {
		t.Fatalf("ParseOnly 应成功（不校验密码）: %v", err)
	}
	if d.User != "alice" || d.GroupType != domain.TypeA || !d.HasDynamicUpstream {
		t.Fatalf("ParseOnly 应产出完整 Decision: %+v", d)
	}
	// ParseOnly 仍做授权/存在性防御校验：用户不存在/未授权仍失败。
	if _, err := ParseOnly(snap, "nobody-gA-"+tail); err == nil {
		t.Fatal("ParseOnly 对不存在用户应失败（防御）")
	}
}

// TestVerify_ParseOnly_Consistency 断言 Verify 与 ParseOnly 在密码正确时产出一致的 Decision，
// 保证 Valid/Allow 两阶段对同一连接的判定一致（DRY，D0-0）。
func TestVerify_ParseOnly_Consistency(t *testing.T) {
	snap := newFakeSnap()
	u := "alice-gB-region_us#session_abc"
	dv, ev := Verify(snap, u, "correct-pwd")
	dp, ep := ParseOnly(snap, u)
	if ev != nil || ep != nil {
		t.Fatalf("两者均应成功: verify=%v parse=%v", ev, ep)
	}
	if dv.User != dp.User || dv.UserID != dp.UserID || dv.GroupID != dp.GroupID ||
		dv.GroupType != dp.GroupType || dv.Variables["region"] != dp.Variables["region"] {
		t.Fatalf("Verify 与 ParseOnly 产出不一致:\nverify=%+v\nparse=%+v", dv, dp)
	}
}

// TestCredential_Valid 覆盖 Credential.Valid 经 Provider 读快照鉴权的成败布尔。
func TestCredential_Valid(t *testing.T) {
	snap := newFakeSnap()
	c := NewCredential(func() SnapshotView { return snap })
	if !c.Valid("alice-gB-region_us", "correct-pwd", "1.2.3.4:5") {
		t.Fatal("合法连接应通过")
	}
	if c.Valid("alice-gB", "wrong-pwd", "1.2.3.4:5") {
		t.Fatal("错误密码应失败")
	}
	// Provider 返回 nil（快照未就绪）→ 保守拒连
	cNil := NewCredential(func() SnapshotView { return nil })
	if cNil.Valid("alice-gB", "correct-pwd", "1.2.3.4:5") {
		t.Fatal("快照未就绪应拒连")
	}
	// 零值 Credential（provider nil）→ 拒连不 panic
	var zero Credential
	if zero.Valid("alice-gB", "correct-pwd", "1.2.3.4:5") {
		t.Fatal("零值 Credential 应拒连")
	}
}
