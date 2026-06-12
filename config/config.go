// Package config 负责加载、校验 YAML 配置文件，并为缺省字段填充默认值。
//
// 注意：上游 SOCKS5 代理不在配置文件中（由客户端用户名动态携带），
// 因此配置只承载“本地监听 / 默认动作 / 日志级别 / 空闲超时 / 分流规则”。
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// 默认值常量：当配置文件未显式给出对应字段时使用。
const (
	defaultListen      = "127.0.0.1:1080" // 默认仅监听本地回环，避免误暴露到公网
	defaultAction      = "forward"        // 规则都不命中时默认走上游
	defaultLogLevel    = "info"
	defaultIdleTimeout = 300 // 连接双向空闲超时（秒），用于回收半开连接
	defaultSniffTimeMs = 300 // 嗅探客户端首包的等待超时（毫秒）
)

// 合法的动作集合与规则匹配前缀集合，用于配置校验。
var (
	validActions     = map[string]bool{"forward": true, "direct": true, "reject": true}
	validMatchPrefix = map[string]bool{"domain": true, "domain-suffix": true, "ip-cidr": true}
)

// RuleSpec 是配置文件中单条规则的原始形态。
// Match 形如 "domain-suffix:google.com"；Action 为 forward/direct/reject。
type RuleSpec struct {
	Match  string `yaml:"match"`
	Action string `yaml:"action"`
}

// Config 是 deeproxy 的完整配置。
type Config struct {
	Listen         string     `yaml:"listen"`           // 本地 SOCKS5 监听地址
	DefaultAction  string     `yaml:"default_action"`   // 规则不命中时的默认动作
	LogLevel       string     `yaml:"log_level"`        // 日志级别 debug/info/warn/error
	IdleTimeoutSec int        `yaml:"idle_timeout_sec"` // 空闲超时（秒）
	Rules          []RuleSpec `yaml:"rules"`            // 分流规则（顺序首匹配）

	// SniffDomain 控制：当目标是 IP 且未命中 ip-cidr 规则时，是否嗅探
	// 客户端首包（TLS SNI / HTTP Host）还原域名再按域名规则选路。
	// 用 *bool 以区分“未配置”（默认 true）与“显式 false”（关闭）。
	SniffDomain    *bool `yaml:"sniff_domain"`
	SniffTimeoutMs int   `yaml:"sniff_timeout_ms"` // 嗅探首包等待超时（毫秒）
}

// SniffEnabled 返回是否启用域名嗅探（未配置时默认启用）。
func (c *Config) SniffEnabled() bool {
	return c.SniffDomain == nil || *c.SniffDomain
}

// Load 读取并解析指定路径的 YAML 配置，填充默认值后做合法性校验。
// 任一字段非法都会返回带中文说明的 error，调用方应据此终止启动。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败 %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 YAML 配置失败: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults 为未显式配置的字段填充默认值。
func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = defaultListen
	}
	if strings.TrimSpace(c.DefaultAction) == "" {
		c.DefaultAction = defaultAction
	}
	if strings.TrimSpace(c.LogLevel) == "" {
		c.LogLevel = defaultLogLevel
	}
	if c.IdleTimeoutSec <= 0 {
		c.IdleTimeoutSec = defaultIdleTimeout
	}
	if c.SniffTimeoutMs <= 0 {
		c.SniffTimeoutMs = defaultSniffTimeMs
	}
}

// validate 校验关键字段的合法性。
func (c *Config) validate() error {
	// listen 不可为空（applyDefaults 后理论上已有值，这里防御性再查一次）。
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("listen 不能为空")
	}
	// 默认动作必须是三枚举之一。
	if !validActions[c.DefaultAction] {
		return fmt.Errorf("default_action 非法: %q（应为 forward/direct/reject）", c.DefaultAction)
	}
	// 逐条校验规则的动作与匹配前缀。
	for i, r := range c.Rules {
		if !validActions[r.Action] {
			return fmt.Errorf("第 %d 条规则 action 非法: %q（应为 forward/direct/reject）", i+1, r.Action)
		}
		prefix, pattern, ok := strings.Cut(r.Match, ":")
		if !ok || !validMatchPrefix[prefix] {
			return fmt.Errorf("第 %d 条规则 match 前缀非法: %q（应为 domain:/domain-suffix:/ip-cidr:）", i+1, r.Match)
		}
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("第 %d 条规则 match 模式为空: %q", i+1, r.Match)
		}
	}
	return nil
}
