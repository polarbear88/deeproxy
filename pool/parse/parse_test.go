package parse

import "testing"

// parse_test.go 覆盖批量上游解析的消歧规则与边界（AC-3.1）。

func TestParseLine_AtForm(t *testing.T) {
	cases := []struct {
		in   string
		want Upstream
	}{
		// 标准 @ 形。
		{"user:pass@host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "user", Pwd: "pass"}},
		// pass 含 ':'（@ 形：@ 前第一个 ':' 分 user/pass，其余归 pass）。
		{"user:pa:ss@host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "user", Pwd: "pa:ss"}},
		// 无凭据冒号：整体为 user、pass 空。
		{"useronly@host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "useronly", Pwd: ""}},
		// 空凭据（@ 前为空）：user/pass 均空。
		{"@host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "", Pwd: ""}},
		// IPv6 方括号 @ 形。
		{"u:p@[2001:db8::1]:8080", Upstream{Host: "2001:db8::1", Port: 8080, User: "u", Pwd: "p"}},
	}
	for _, c := range cases {
		got, err := ParseLine(c.in)
		if err != nil {
			t.Fatalf("ParseLine(%q) 意外报错: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseLine(%q) = %+v, 期望 %+v", c.in, got, c.want)
		}
	}
}

func TestParseLine_ColonForm(t *testing.T) {
	cases := []struct {
		in   string
		want Upstream
	}{
		// 标准 user:pass:host:port（右起两段 host:port）。
		{"user:pass:host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "user", Pwd: "pass"}},
		// pass 含 ':'（左侧第一个 ':' 分 user/pass，其余归 pass）。
		{"user:pa:ss:host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "user", Pwd: "pa:ss"}},
		// 仅 host:port（无凭据）。
		{"host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "", Pwd: ""}},
		// user 无 pass（左侧仅一段）：user 命中、pass 空。
		{"user:host.com:1080", Upstream{Host: "host.com", Port: 1080, User: "user", Pwd: ""}},
	}
	for _, c := range cases {
		got, err := ParseLine(c.in)
		if err != nil {
			t.Fatalf("ParseLine(%q) 意外报错: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseLine(%q) = %+v, 期望 %+v", c.in, got, c.want)
		}
	}
}

func TestParseLine_Errors(t *testing.T) {
	bad := []string{
		"",                       // 空
		"justhost",               // 无端口
		"host.com:notaport",      // 端口非数字
		"host.com:99999",         // 端口越界
		"2001:db8::1:8080",       // 裸 IPv6 冒号形：无法消歧，应报错（提示用 @ 或方括号）
		"user:pass@",             // @ 后缺 host:port
		"user:pass@host.com",     // @ 后缺端口
	}
	for _, in := range bad {
		if _, err := ParseLine(in); err == nil {
			t.Errorf("ParseLine(%q) 应报错，却成功", in)
		}
	}
}

func TestParseLines_SkipAndCollect(t *testing.T) {
	text := "user:pass@host1.com:1080\n" +
		"\n" + // 空行跳过
		"# 这是注释\n" + // 注释跳过
		"badport:host.com:notaport\n" + // 非法行
		"host2.com:2080\n"
	res := ParseLines(text)
	if len(res) != 3 {
		t.Fatalf("应返回 3 条结果（跳过空行与注释），得到 %d: %+v", len(res), res)
	}
	if !res[0].OK || res[0].Up.Host != "host1.com" {
		t.Errorf("第1条应成功 host1.com: %+v", res[0])
	}
	if res[1].OK {
		t.Errorf("第2条应失败（非法端口）: %+v", res[1])
	}
	if !res[2].OK || res[2].Up.Host != "host2.com" {
		t.Errorf("第3条应成功 host2.com: %+v", res[2])
	}
	// 行号应对应原始输入位置（注释/空行不重排已有行号）。
	if res[1].LineNo != 4 {
		t.Errorf("非法行行号应为 4（原始位置），得到 %d", res[1].LineNo)
	}
}
