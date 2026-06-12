// Command deeproxy 是一个跨平台 SOCKS5 中继转发工具的入口。
//
// 启动流程：解析 -c 配置路径 → 加载并校验配置 → 构建规则引擎 →
// 初始化日志 → 装配 SOCKS5 服务 → 监听并服务。任一步失败都会打印中文错误并退出。
package main

import (
	"flag"
	"fmt"
	"os"

	"deeproxy/config"
	"deeproxy/internal/logging"
	"deeproxy/rule"
	"deeproxy/server"
)

func main() {
	// -c 指定配置文件路径，默认当前目录下的 config.yaml。
	confPath := flag.String("c", "config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置（含默认值填充与合法性校验）。
	cfg, err := config.Load(*confPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 构建规则引擎（预编译 CIDR、校验动作）。
	engine, err := rule.NewEngine(cfg.Rules, rule.Action(cfg.DefaultAction))
	if err != nil {
		fmt.Fprintf(os.Stderr, "构建规则引擎失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化结构化日志器。
	logger := logging.New(cfg.LogLevel)

	// 装配并启动 SOCKS5 中继服务。
	srv := server.New(cfg, engine, logger)
	logger.Info("deeproxy 启动", "listen", cfg.Listen, "default_action", cfg.DefaultAction)

	if err := srv.ListenAndServe("tcp", cfg.Listen); err != nil {
		fmt.Fprintf(os.Stderr, "服务运行失败: %v\n", err)
		os.Exit(1)
	}
}
