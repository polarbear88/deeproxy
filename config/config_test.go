package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTemp 把内容写入临时文件并返回路径。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return p
}

// TestLoadDefaults 覆盖 AC8：缺省字段填充默认值。
func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, "rules: []\n"))
	if err != nil {
		t.Fatalf("Load 报错: %v", err)
	}
	if cfg.Listen != defaultListen {
		t.Errorf("Listen 默认值 = %q, 期望 %q", cfg.Listen, defaultListen)
	}
	if cfg.DefaultAction != defaultAction {
		t.Errorf("DefaultAction 默认值 = %q, 期望 %q", cfg.DefaultAction, defaultAction)
	}
	if cfg.LogLevel != defaultLogLevel {
		t.Errorf("LogLevel 默认值 = %q, 期望 %q", cfg.LogLevel, defaultLogLevel)
	}
	if cfg.IdleTimeoutSec != defaultIdleTimeout {
		t.Errorf("IdleTimeoutSec 默认值 = %d, 期望 %d", cfg.IdleTimeoutSec, defaultIdleTimeout)
	}
}

// TestLoadValid 覆盖 AC8：完整合法配置正常加载。
func TestLoadValid(t *testing.T) {
	content := `
listen: "0.0.0.0:1080"
default_action: direct
log_level: debug
idle_timeout_sec: 60
rules:
  - { match: "domain-suffix:google.com", action: forward }
  - { match: "ip-cidr:192.168.0.0/16", action: direct }
  - { match: "domain:ads.example.com", action: reject }
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load 合法配置报错: %v", err)
	}
	if len(cfg.Rules) != 3 || cfg.Listen != "0.0.0.0:1080" || cfg.DefaultAction != "direct" {
		t.Fatalf("配置解析结果不符: %+v", cfg)
	}
}

// TestLoadInvalid 覆盖 AC8：各类非法配置应返回 error。
func TestLoadInvalid(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"非法默认动作", "default_action: bogus\nrules: []\n"},
		{"未知match前缀", "rules:\n  - { match: \"geoip:CN\", action: direct }\n"},
		{"规则动作非法", "rules:\n  - { match: \"domain:a.com\", action: drop }\n"},
		{"match模式为空", "rules:\n  - { match: \"domain:\", action: direct }\n"},
		{"YAML语法错误", "listen: \"x\n  bad: [\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, c.content)); err == nil {
				t.Fatalf("期望报错，但 Load 成功了")
			}
		})
	}
}

// TestLoadMissingFile 覆盖 AC8：文件不存在应返回 error。
func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/path/config.yaml"); err == nil {
		t.Fatal("文件不存在时期望报错")
	}
}
