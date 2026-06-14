package service

import (
	"reflect"
	"testing"
)

// TestFilterServiceArgs 覆盖 AC-6.4 契约：精确移除 6 个开关、其余 token 原样位置透传。
//
// 注：stdlib flag 不支持捆绑短选项（如 `-dv`），故不会出现需要拆解的 token，不予测试。
func TestFilterServiceArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		// —— 6 个精确开关均被移除 ——
		{"移除 --daemon", []string{"--daemon"}, []string{}},
		{"移除 -daemon", []string{"-daemon"}, []string{}},
		{"移除 --startup", []string{"--startup"}, []string{}},
		{"移除 -startup", []string{"-startup"}, []string{}},
		{"移除 --daemon=true", []string{"--daemon=true"}, []string{}},
		{"移除 --startup=true", []string{"--startup=true"}, []string{}},

		// —— 典型组合：端口参数原样保留、仅剔除开关 ——
		{
			"剔除 --daemon 保留端口参数",
			[]string{"--socks5", "1000", "--web", "1001", "--daemon"},
			[]string{"--socks5", "1000", "--web", "1001"},
		},
		{
			"同时剔除 --daemon --startup",
			[]string{"--socks5", "1000", "--daemon", "--web", "1001", "--startup"},
			[]string{"--socks5", "1000", "--web", "1001"},
		},

		// —— '=' 形式不拆分 ——
		{"--socks5=1000 整段保留不拆分", []string{"--socks5=1000"}, []string{"--socks5=1000"}},

		// —— 单破折号形式：--socks5 等价的 -socks5，两个 token 都保留 ——
		{"-socks5 1000 两 token 都保留", []string{"-socks5", "1000"}, []string{"-socks5", "1000"}},

		// —— -web=1001 原样保留（既非开关、也不拆分） ——
		{"-web=1001 原样保留", []string{"-web=1001"}, []string{"-web=1001"}},

		// —— 空输入 ——
		{"空输入返回空", []string{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterServiceArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("filterServiceArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
