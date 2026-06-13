// Command deeproxy 是一个跨平台 SOCKS5 中继转发工具 + Web 管理后台的入口（v2）。
//
// 启动流程（取消配置文件版）：
//  1. 解析命令行参数：仅 --socks5（默认 1768）/ --web（默认 1769）/ -v；
//     host 固定 0.0.0.0、db 固定 ./deeproxy.db（运行期设置都在库里，库路径无法做成设置）；
//  2. 打开 SQLite（WAL + 单写协程 + 建表迁移，首次建库写入设置默认值）；
//  3. 读取 system_setting 取日志级别，建 slog.LevelVar（后台改级别可原子热生效）；
//  4. 接入 syslog 内存环形缓冲 Handler（与 stderr 并联），构建 slog 日志器；
//  5. 物化首个不可变快照 Snapshot（含 system_setting 里的运行期动态设置），放入 atomic Holder；
//  6. 启动后台 worker：健康检查、统计 flush + 过期清理；
//  7. 启动 SOCKS5 中继服务（读 Holder 快照）+ Gin 管理后台（独立端口，embed 前端）；
//  8. 捕获信号优雅退出。
//
// 性能（一号硬约束）：转发链只读 atomic 快照、内存原子计数；SQLite/HTTP/健康检查
// 全部为旁路后台 goroutine，绝不进入字节中继热路径。运行期设置（默认动作/空闲/嗅探）
// 也已物化进快照，建连读一次（纳秒级），不进字节中继循环。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"deeproxy/api"
	"deeproxy/config"
	"deeproxy/internal/logging"
	"deeproxy/pool"
	"deeproxy/pool/health"
	"deeproxy/server"
	"deeproxy/snapbuild"
	"deeproxy/snapshot"
	"deeproxy/stats"
	"deeproxy/stats/flush"
	"deeproxy/store"
	"deeproxy/syslog"
)

// 内存缓冲容量默认值（spec：系统日志默认 5000，连接审计同机制）。
const (
	logBufferCap   = 5000
	auditBufferCap = 5000
)

// 固定引导项（不做成命令行参数）：
//   - bindHost：两个服务的监听地址固定全网卡，用户要求默认对外可访问（AC-41）；
//   - dbPath：SQLite 路径——所有运行期设置都存在库里，库路径本身是“鸡生蛋”，故固定不做设置。
const (
	bindHost = "0.0.0.0"
	dbPath   = "./deeproxy.db"

	defaultSocks5Port = 1768 // SOCKS5 中继默认端口
	defaultWebPort    = 1769 // Web 管理后台默认端口
)

// version 是构建版本号，由 -ldflags "-X main.version=..." 在编译期注入；
// 未注入时为 dev（本地 go build / go run 的默认值）。
var version = "dev"

func main() {
	// 命令行参数只保留两个端口 + 版本号；其余运行期设置均在后台系统设置页动态修改。
	socks5Port := flag.Int("socks5", defaultSocks5Port, "SOCKS5 中继监听端口")
	webPort := flag.Int("web", defaultWebPort, "Web 管理后台监听端口")
	showVer := flag.Bool("v", false, "打印版本号并退出")
	flag.Parse()

	// -v：打印版本号即退出（供 CI 冒烟与运维排查）。
	if *showVer {
		fmt.Println("deeproxy", version)
		return
	}

	// ① 由固定 host + 端口参数组装监听地址（引导配置仅此三项）。
	cfg := &config.Config{
		Listen:      net.JoinHostPort(bindHost, strconv.Itoa(*socks5Port)),
		AdminListen: net.JoinHostPort(bindHost, strconv.Itoa(*webPort)),
		DBPath:      dbPath,
	}

	// ② 打开 SQLite 存储层（WAL + 单写协程 + 建表迁移；首次建库写入 system_setting 默认值）。
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败 %q: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	// ③ 读取系统设置，取日志级别等运行期项（取消配置文件后这些来自库）。
	ss, err := st.GetSystemSetting()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取系统设置失败: %v\n", err)
		os.Exit(1)
	}

	// 用 LevelVar 持有日志级别：后台改级别时对它 Set 即原子热生效，无需重启。
	levelVar := new(slog.LevelVar)
	levelVar.Set(logging.ParseLevel(ss.LogLevel))

	// ④ 系统日志/审计内存环形缓冲 + slog 接入（控制台 + 内存缓冲并联，供后台 SSE 推送）。
	logBuf := syslog.NewLogBuffer(logBufferCap)
	auditBuf := syslog.NewAuditBuffer(auditBufferCap)
	logger := logging.NewWithLevelVar(levelVar, logBuf.Handler())

	// ⑤ 物化首个不可变快照（从 SQLite，含运行期动态设置），放入 atomic Holder（转发侧无锁读）。
	snap, err := snapbuild.Rebuild(st, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "物化初始配置快照失败: %v\n", err)
		os.Exit(1)
	}
	holder := snapshot.NewHolder(snap)

	// ⑥ 运行期协作者：SWRR 选择器注册表、内存统计计数器。
	registry := pool.NewRegistry()
	counter := stats.NewCounter()

	// 健康检查器：探测翻转后经 refresher 触发快照重建+原子替换，刷新 HealthyUpstreams。
	// refresher 是闭包适配 SnapshotRefresher（Refresh() error）→ snapbuild.RebuildAndSwap（DRY）。
	refresher := refresherFunc(func() error {
		return snapbuild.RebuildAndSwap(holder, st, cfg)
	})
	healthChecker := health.NewHealthChecker(st, health.NewNetProber(), refresher, logger)

	// 统计 flush + 过期清理 worker（保留期来自系统设置）。
	flusher := flush.NewFlusher(counter, st, logger, flush.WithRetentionDays(ss.StatRetentionDays))

	// ⑦ 根上下文：收到信号后取消，驱动所有后台 worker 退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 启动后台 worker（旁路，不阻塞转发）。
	go healthChecker.Run(ctx)
	go flusher.Run(ctx)

	// 装配管理后台（Gin + embed 前端，独立端口）。levelVar 传入以便后台改日志级别时热生效。
	app := api.NewApp(st, holder, cfg, counter, logBuf, auditBuf, healthChecker, logger, levelVar)
	adminErr := make(chan error, 1)
	go func() {
		logger.Info("管理后台启动", "listen", cfg.AdminListen)
		adminErr <- app.Run()
	}()

	// 装配 SOCKS5 中继服务（读 atomic 快照；空闲/嗅探/默认动作均从快照动态读）。
	srv := server.New(holder, registry, counter, auditBuf, logger)
	socksErr := make(chan error, 1)
	go func() {
		logger.Info("SOCKS5 服务启动", "listen", cfg.Listen)
		socksErr <- srv.ListenAndServe("tcp", cfg.Listen)
	}()

	// ⑧ 等待信号或任一服务致命错误。
	select {
	case <-ctx.Done():
		logger.Info("收到退出信号，正在关闭…")
	case err := <-socksErr:
		fmt.Fprintf(os.Stderr, "SOCKS5 服务运行失败: %v\n", err)
		os.Exit(1)
	case err := <-adminErr:
		fmt.Fprintf(os.Stderr, "管理后台运行失败: %v\n", err)
		os.Exit(1)
	}
}

// refresherFunc 把函数适配为 health.SnapshotRefresher（Refresh() error）。
type refresherFunc func() error

func (f refresherFunc) Refresh() error { return f() }
