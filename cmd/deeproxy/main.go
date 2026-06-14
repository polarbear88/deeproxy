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
	"time"

	"deeproxy/api"
	"deeproxy/config"
	"deeproxy/internal/logging"
	"deeproxy/pool"
	"deeproxy/pool/health"
	"deeproxy/server"
	"deeproxy/service"
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
	// 系统服务相关开关（组件⑥）：--daemon 把自身安装为系统服务并启动；
	// --startup 仅与 --daemon 同用有效，表示设为开机自启。
	daemon := flag.Bool("daemon", false, "安装为系统服务并启动（Linux=systemd / Windows=SCM；macOS 不支持）")
	startup := flag.Bool("startup", false, "与 --daemon 同用：将服务设为开机自启")

	// 自定义 --help / 用法输出：中英双语，覆盖全部参数（AC-6.9）。
	// flag.Usage 由 flag.Parse() 在遇到 -h/--help 或解析错误时触发，早于 daemon 分支与平台分发，
	// 故 --help 在所有平台（含 macOS）均可正常打印，只有 --daemon 才会触发「macOS 不支持」。
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, `deeproxy —— 跨平台 SOCKS5 中继转发工具 + Web 管理后台

用法：
  deeproxy [参数]

参数：
  --socks5 <端口>   SOCKS5 中继监听端口（默认 1768）
  --web <端口>      Web 管理后台监听端口（默认 1769）
  -v                打印版本号并退出
  --daemon          安装为系统服务并启动（Linux=systemd / Windows=SCM；macOS 不支持）
  --startup         与 --daemon 同用：将服务设为开机自启
  --help            显示本帮助并退出

示例：
  deeproxy --socks5 1080 --web 8080
  deeproxy --socks5 1080 --web 8080 --daemon --startup

----------------------------------------------------------------------

deeproxy — cross-platform SOCKS5 relay + Web admin panel

Usage:
  deeproxy [options]

Options:
  --socks5 <port>   SOCKS5 relay listen port (default 1768)
  --web <port>      Web admin panel listen port (default 1769)
  -v                Print version and exit
  --daemon          Install as a system service and start it (Linux=systemd / Windows=SCM; macOS unsupported)
  --startup         Use with --daemon: enable start-on-boot
  --help            Show this help and exit

Examples:
  deeproxy --socks5 1080 --web 8080
  deeproxy --socks5 1080 --web 8080 --daemon --startup`)
	}
	flag.Parse()

	// -v：打印版本号即退出（供 CI 冒烟与运维排查）。
	if *showVer {
		fmt.Println("deeproxy", version)
		return
	}

	// --daemon：安装为系统服务并启动（组件⑥，AC-6.7）。必须在打开 DB / 绑定端口之前处理，
	// 完成后前台进程退出：绝不触碰 SQLite（避免 WAL 写争用）、不绑定端口（避免与后台服务进程冲突）。
	if *daemon {
		service.InstallAndStart(*startup) // 内部失败会自行 os.Exit(1)，成功则正常返回
		os.Exit(0)
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
	// P2：健康翻转的重建经【去抖刷新器】合并——一段静默窗口内的多次翻转只触发一次全量重建，
	// 避免大池/频繁抖动时相邻全量 Rebuild 造成的 CPU + rebuildMu 锁竞争。API 写仍走同步重建。
	debounced := newDebouncedRefresher(func() error {
		return snapbuild.RebuildAndSwap(holder, st, cfg)
	}, 2*time.Second, logger)
	healthChecker := health.NewHealthChecker(st, health.NewNetProber(), debounced, logger)

	// 统计 flush + 过期清理 worker（保留期来自系统设置）。
	flusher := flush.NewFlusher(counter, st, logger, flush.WithRetentionDays(ss.StatRetentionDays))

	// ⑦ 根上下文：收到信号后取消，驱动所有后台 worker 退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 启动后台 worker（旁路，不阻塞转发）。
	go healthChecker.Run(ctx)
	go flusher.Run(ctx)
	go debounced.run(ctx) // P2：去抖刷新循环（合并健康翻转触发的全量重建）

	// 装配管理后台（Gin + embed 前端，独立端口）。levelVar 传入以便后台改日志级别时热生效。
	app := api.NewApp(st, holder, cfg, counter, logBuf, auditBuf, healthChecker, registry, logger, levelVar, version)
	adminErr := make(chan error, 1)
	go func() {
		logger.Info("管理后台启动", "listen", cfg.AdminListen)
		adminErr <- app.Run()
	}()

	// 装配 SOCKS5 中继服务（读 atomic 快照；空闲/嗅探/默认动作均从快照动态读）。
	// C2：用 server.Listen 创建【带握手读超时】的监听器（防 slowloris 半开连接泄漏），
	// 同时持有该 listener 句柄以便优雅关闭时主动关闭、停止接受新连接（H1）。
	srv := server.New(holder, registry, counter, auditBuf, logger)
	socksLn, err := server.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SOCKS5 服务监听失败 %q: %v\n", cfg.Listen, err)
		os.Exit(1)
	}
	socksErr := make(chan error, 1)
	go func() {
		logger.Info("SOCKS5 服务启动", "listen", cfg.Listen)
		socksErr <- srv.Serve(socksLn)
	}()

	// ⑧ 等待信号或任一服务致命错误。
	select {
	case <-ctx.Done():
		logger.Info("收到退出信号，正在优雅关闭…")
	case err := <-socksErr:
		fmt.Fprintf(os.Stderr, "SOCKS5 服务运行失败: %v\n", err)
		os.Exit(1)
	case err := <-adminErr:
		fmt.Fprintf(os.Stderr, "管理后台运行失败: %v\n", err)
		os.Exit(1)
	}

	// ⑨ 优雅关闭（H1）：有序拆除，尽量不丢在途请求与尾部统计。
	//  1. 关 SOCKS 监听：停止接受新中继连接（已建立的中继由其自身 idle/连接结束自然收尾）。
	//  2. Shutdown 管理后台：等在途 HTTP 请求在超时内收尾（SSE 长连接会被关闭）。
	//  3. stop()：解除 signal 通知；ctx 已 Done，后台 worker（健康/flush/去抖）随之退出，
	//     flush worker 退出前会做最后一次 flush，落下尾部统计增量。
	//  4. 关 store：确定性排空单写协程队列后关库（见 store.Close），避免丢写/WAL 未收尾。
	_ = socksLn.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Shutdown(shutdownCtx); err != nil {
		logger.Warn("管理后台优雅关闭超时/出错", "err", err)
	}

	stop()                              // 解除信号捕获；ctx 已取消，后台 worker 开始退出
	time.Sleep(200 * time.Millisecond) // 给 flush worker 的「退出前最后一次 flush」一点落库窗口
	if err := st.Close(); err != nil {  // 确定性排空写队列后关库
		logger.Warn("关闭数据库出错", "err", err)
	}
	logger.Info("已优雅关闭")
}
