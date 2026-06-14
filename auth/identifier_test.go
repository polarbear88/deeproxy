package auth

import "testing"

// TestValidIdentifier 覆盖合法与各类非法输入（AC-7.1/7.2）。
// 重点验证含 '-' 被拒（结构性根因：username.go SplitN 用 '-' 切段），
// 以及刻意更严：'_'、'#'、空串、中文均被拒。
func TestValidIdentifier(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"纯字母数字合法", "abc123", true},
		{"纯字母合法", "Group", true},
		{"纯数字合法", "12345", true},
		{"含连字符非法", "a-b", false},   // 会破坏 SplitN(username,"-",3) 切段
		{"含下划线非法", "a_b", false},   // 尾段变量串合法，但 user/group 段绝不允许
		{"含井号非法", "a#b", false},    // 同上，仅尾段允许
		{"含点号非法", "a.b", false},    // 排除展示/模板拼接歧义
		{"空串非法", "", false},        // + 要求至少一个字符
		{"中文非法", "分组", false},      // 仅 ASCII 字母数字
		{"含空格非法", "a b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidIdentifier(tc.in); got != tc.want {
				t.Errorf("ValidIdentifier(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
