package auth

import (
	"reflect"
	"testing"
)

// TestParseUsername 覆盖 v2 用户名位置语法解析（AC-1 / AC-2）。
func TestParseUsername(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		want     ParsedUsername
		wantErr  bool
		errHint  string
	}{
		{
			name:  "无尾段 user-group",
			input: "alice-poolA",
			want:  ParsedUsername{User: "alice", Group: "poolA", Tail: "", HasTail: false},
		},
		{
			name:  "含尾段 user-group-尾段",
			input: "alice-poolA-region_us",
			want:  ParsedUsername{User: "alice", Group: "poolA", Tail: "region_us", HasTail: true},
		},
		{
			// AC-2 关键：尾段整体不拆，即使尾段里还有多个 '-'（base64 或变量值可能含 '-'）。
			name:  "尾段含-不拆",
			input: "bob-grpB-aaa-bbb-ccc",
			want:  ParsedUsername{User: "bob", Group: "grpB", Tail: "aaa-bbb-ccc", HasTail: true},
		},
		{
			// base64 上游典型形态（Type A），尾段可能含 '=' '+' '/' 与 '-'，整体保留。
			name:  "Type A base64 尾段",
			input: "u1-gA-dXNlcjpwd2RAYWEuY29tOjg4OA==",
			want:  ParsedUsername{User: "u1", Group: "gA", Tail: "dXNlcjpwd2RAYWEuY29tOjg4OA==", HasTail: true},
		},
		{
			name:  "尾段为空字符串(user-group-)",
			input: "alice-poolA-",
			want:  ParsedUsername{User: "alice", Group: "poolA", Tail: "", HasTail: true},
		},
		{
			name:    "空串报错",
			input:   "",
			wantErr: true,
		},
		{
			name:    "缺分组段报错",
			input:   "aliceonly",
			wantErr: true,
		},
		{
			name:    "user段为空报错",
			input:   "-poolA",
			wantErr: true,
		},
		{
			name:    "group段为空报错(中间空)",
			input:   "alice--tail",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUsername(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际成功，got=%+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if got != tc.want {
				t.Fatalf("解析不符\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

// TestParseVariables 覆盖命名变量串解析（AC-5：name_value#name_value）。
func TestParseVariables(t *testing.T) {
	cases := []struct {
		name  string
		tail  string
		want  map[string]string
	}{
		{
			name: "空尾段返回空map",
			tail: "",
			want: map[string]string{},
		},
		{
			name: "单变量",
			tail: "region_us",
			want: map[string]string{"region": "us"},
		},
		{
			name: "多变量顺序无关",
			tail: "region_us#session_abc123",
			want: map[string]string{"region": "us", "session": "abc123"},
		},
		{
			name: "值含下划线只切第一个_",
			tail: "session_abc_def_ghi",
			want: map[string]string{"session": "abc_def_ghi"},
		},
		{
			name: "畸形段(无下划线)被忽略",
			tail: "region_us#garbage#session_x",
			want: map[string]string{"region": "us", "session": "x"},
		},
		{
			name: "连续#与首尾#产生空段被忽略",
			tail: "#region_us##session_x#",
			want: map[string]string{"region": "us", "session": "x"},
		},
		{
			name: "变量名为空被忽略",
			tail: "_noname#ok_v",
			want: map[string]string{"ok": "v"},
		},
		{
			name: "重名后者覆盖前者",
			tail: "region_us#region_eu",
			want: map[string]string{"region": "eu"},
		},
		{
			name: "值为空合法",
			tail: "session_",
			want: map[string]string{"session": ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseVariables(tc.tail)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("解析不符\n got=%v\nwant=%v", got, tc.want)
			}
		})
	}
}

// TestSubstituteTemplate 覆盖模板替换语义（AC-5：缺值补空/多余忽略/顺序无关/隐式定义）。
func TestSubstituteTemplate(t *testing.T) {
	cases := []struct {
		name     string
		template string
		vars     map[string]string
		want     string
	}{
		{
			name:     "无占位原样返回",
			template: "acct:pwd",
			vars:     map[string]string{"region": "us"},
			want:     "acct:pwd",
		},
		{
			name:     "单占位替换",
			template: "acct-{region}",
			vars:     map[string]string{"region": "us"},
			want:     "acct-us",
		},
		{
			// spec 示例：acct-{region}-{session} + region_us#session_abc123 → acct-us-abc123
			name:     "多占位替换(spec示例)",
			template: "acct-{region}-{session}",
			vars:     map[string]string{"region": "us", "session": "abc123"},
			want:     "acct-us-abc123",
		},
		{
			name:     "缺值补空字符串",
			template: "acct-{region}-{session}",
			vars:     map[string]string{"region": "us"}, // 缺 session
			want:     "acct-us-",
		},
		{
			name:     "多余变量忽略",
			template: "acct-{region}",
			vars:     map[string]string{"region": "us", "extra": "x"},
			want:     "acct-us",
		},
		{
			name:     "全部缺值全补空",
			template: "{a}{b}{c}",
			vars:     map[string]string{},
			want:     "",
		},
		{
			name:     "未闭合花括号原样保留",
			template: "acct-{region",
			vars:     map[string]string{"region": "us"},
			want:     "acct-{region",
		},
		{
			name:     "空占位{}原样保留",
			template: "acct-{}-{region}",
			vars:     map[string]string{"region": "us"},
			want:     "acct-{}-us",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SubstituteTemplate(tc.template, tc.vars)
			if got != tc.want {
				t.Fatalf("替换不符\n got=%q\nwant=%q", got, tc.want)
			}
		})
	}
}
