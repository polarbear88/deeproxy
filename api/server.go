// Package api 是 deeproxy v2 的管理后端（Gin HTTP 服务 + 前端 embed）。
//
// 职责（阶段7）：
//   - 提供独立端口（默认 0.0.0.0，AC-41）的 Gin HTTP 服务；
//   - 管理员登录/会话（内存会话 + HttpOnly Cookie，D2-A）与登录限流（AC-40）；
//   - 分组/规则/用户 等各模块 CRUD（AC-21~23）；
//   - 仪表盘聚合（实时读内存 stats + 今日读 SQLite，AC-24）；
//   - 系统日志 SSE 实时推送 + 级别筛选（AC-33/34）；
//   - 配置导入导出（AC-37）、代理测试连接（AC-38）、规则测试器（AC-39）。
//
// 与转发热路径的关系：本包完全旁路，任何写操作 commit 后调用 snapshot.Holder
// 的 RebuildAndSwap 刷新转发侧只读快照（G4：Rebuild 失败不 Swap、保留旧快照、返回错误）。
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"deeproxy/config"
	"deeproxy/connreg"
	"deeproxy/pool"
	"deeproxy/pool/health"
	"deeproxy/snapbuild"
	"deeproxy/snapshot"
	"deeproxy/stats"
	"deeproxy/store"
	"deeproxy/syslog"
)

// App 是管理后端的依赖容器，集中持有 handler 所需的全部协作者。
//
// 之所以用一个容器而非全局变量：便于测试用 httptest 注入 mock/内存实现，
// 且让各 handler 文件通过方法接收者共享依赖（DRY）。
type App struct {
	store  *store.Store          // SQLite 存储层（CRUD + 统计查询）
	holder *snapshot.Holder      // 运行期快照持有者（写后 RebuildAndSwap）
	cfg    *config.Config        // 启动引导配置（Rebuild 需要默认动作等）
	stats  *stats.Counter        // 内存实时计数器（仪表盘实时区）
	logs   *syslog.LogBuffer     // 系统日志环形缓冲（SSE 推送）
	audit  *syslog.AuditBuffer   // 连接审计环形缓冲（AC-36）
	health *health.HealthChecker // 健康检查器（代理测试连接 AC-38 / 启停）

	sessions *sessionStore // 内存会话表（D2-A）
	limiter  *loginLimiter // 登录失败限流（AC-40）
	registry *pool.Registry  // per-group SWRR 选择器注册表（M5：删分组时回收对应 Selector）
	connReg  *connreg.Registry // 活跃连接登记表（实时连接功能）：只读快照。注意与上面 registry *pool.Registry 区分

	logger    *slog.Logger   // 日志器（接入 syslog 缓冲，关键写操作埋点 → 系统日志页可见）
	levelVar  *slog.LevelVar // 日志级别变量：后台改 log_level 时对它 Set 原子热生效（不重启）
	startedAt time.Time      // 进程启动时间（仪表盘 uptime 计算）
	version   string         // 构建版本号（由 cmd 注入，仪表盘健康卡片展示）

	httpSrv *http.Server // 底层 HTTP 服务（H1：支持优雅关闭 Shutdown）
}

// NewApp 构建管理后端容器。各依赖由 cmd 装配阶段（阶段9）注入。
func NewApp(
	st *store.Store,
	holder *snapshot.Holder,
	cfg *config.Config,
	counter *stats.Counter,
	logs *syslog.LogBuffer,
	audit *syslog.AuditBuffer,
	health *health.HealthChecker,
	registry *pool.Registry,
	connReg *connreg.Registry,
	logger *slog.Logger,
	levelVar *slog.LevelVar,
	version string,
) *App {
	if logger == nil {
		logger = slog.Default()
	}
	if levelVar == nil {
		// 测试或调用方未注入时兜底，避免后台改日志级别时空指针。
		levelVar = new(slog.LevelVar)
	}
	if version == "" {
		// 测试或未注入时兜底为 dev，与 main 包默认值语义一致。
		version = "dev"
	}
	if registry == nil {
		// 测试或未注入时兜底，避免删分组回收时空指针。
		registry = pool.NewRegistry()
	}
	if connReg == nil {
		// 测试或未注入时兜底，避免读取活跃连接时空指针。
		connReg = connreg.New()
	}
	return &App{
		store:     st,
		holder:    holder,
		cfg:       cfg,
		stats:     counter,
		logs:      logs,
		audit:     audit,
		health:    health,
		registry:  registry,
		connReg:   connReg,
		sessions:  newSessionStore(),
		limiter:   newLoginLimiter(maxLoginFails, loginLockDuration),
		logger:    logger,
		levelVar:  levelVar,
		startedAt: time.Now(),
		version:   version,
	}
}

// Router 构建并返回配置好全部路由的 Gin 引擎。
//
// 路由分层：
//   - /api/setup、/api/login、/api/logout：免会话校验（首次设置与登录入口本身）；
//   - /api/* 其余：经 requireAuth 中间件做会话校验；
//   - 静态前端 + SPA fallback：挂在根路径（embed，见 static.go）。
func (a *App) Router() *gin.Engine {
	// 生产模式：关闭 gin 调试输出（日志走 slog → syslog 缓冲）。
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery()) // panic 兜底，避免单请求 panic 拖垮后台

	// 安全（FIX-H5a）：不信任 X-Forwarded-For / X-Real-IP。
	// gin 默认会信任这些头，c.ClientIP() 取其中的伪造 IP。后台默认 0.0.0.0 监听、
	// 通常无受控反代，攻击者每次伪造不同 XFF 即可让登录限流的 key 每次变化 →
	// 完全绕过「5 次锁 5 分钟」暴力破解；或伪造受害者 IP 触发针对性锁定。
	// 设为 nil 后 ClientIP() 退化为直接用 RemoteAddr 的对端 IP，无法被请求头伪造。
	// 若将来部署在受信反代后，再改为 SetTrustedProxies([]string{反代IP}) 即可。
	_ = r.SetTrustedProxies(nil)

	api := r.Group("/api")
	{
		// —— 免鉴权入口（认证前缀 /auth/*，对齐前端契约）——
		api.GET("/auth/init-status", a.handleSetupStatus) // 是否已初始化管理员（首次设置引导 AC-19）
		api.POST("/auth/setup", a.handleSetup)            // 首次设置管理员账密 AC-19
		api.POST("/auth/login", a.handleLogin)            // 登录签发会话 AC-20/40
		api.POST("/auth/logout", a.handleLogout)          // 登出（清会话）

		// —— 需鉴权区 ——
		auth := api.Group("")
		auth.Use(a.requireAuth())
		{
			// 仪表盘聚合 AC-24/27
			auth.GET("/connections", a.handleListConnections)           // 实时活跃连接列表（Top-N + 总数 + 截断标志）
			auth.GET("/dashboard/overview", a.handleDashboardOverview) // 实时+今日扁平概览
			auth.GET("/dashboard/timeseries", a.handleTimeSeries)      // 时序图 1h/24h/7d
			auth.GET("/dashboard/action-dist", a.handleActionDist)     // 动作分布饼图
			auth.GET("/dashboard/top", a.handleTop)                    // Top N group/user/domain
			auth.GET("/dashboard/runtime", a.handleRuntime)            // 运行健康区(内存/goroutine/各组健康)
			auth.GET("/feature-status", a.handleFeatureStatus)         // 权威功能状态表 T6.3

			// 分组 CRUD AC-21
			auth.GET("/groups", a.handleListGroups)
			auth.POST("/groups", a.handleCreateGroup)
			auth.PUT("/groups/:id", a.handleUpdateGroup)
			auth.DELETE("/groups/:id", a.handleDeleteGroup)
			// 分组下的上游 CRUD（嵌套路径，Type B）AC-21/28
			auth.GET("/groups/:id/upstreams", a.handleListUpstreams)
			auth.POST("/groups/:id/upstreams", a.handleCreateUpstream)
			auth.POST("/groups/:id/upstreams/batch", a.handleBatchCreateUpstreams)      // 批量添加 AC-3.1
			auth.POST("/groups/:id/upstreams/bulk", a.handleBulkUpdateUpstreams)        // 批量改权重/启停 AC-3.4
			auth.POST("/groups/:id/upstreams/bulk-delete", a.handleBulkDeleteUpstreams) // 批量删除上游
			auth.PUT("/groups/:id/upstreams/:uid", a.handleUpdateUpstream)
			auth.DELETE("/groups/:id/upstreams/:uid", a.handleDeleteUpstream)
			auth.POST("/groups/:id/upstreams/:uid/toggle", a.handleSetUpstreamEnabled) // 手动启停 AC-18
			auth.POST("/groups/:id/upstreams/:uid/test", a.handleTestUpstream)         // 测试连接 AC-38

			// 规则组 + 规则 CRUD + 关联 + 测试器 AC-22/29/39
			auth.GET("/rule-groups", a.handleListRuleGroups)
			auth.POST("/rule-groups", a.handleCreateRuleGroup)
			auth.PUT("/rule-groups/:id", a.handleUpdateRuleGroup)
			auth.DELETE("/rule-groups/:id", a.handleDeleteRuleGroup)
			auth.PUT("/rule-groups/:id/groups", a.handleSetRuleGroupGroups) // 规则组应用到分组（规则组维度）
			auth.GET("/rule-groups/:id/rules", a.handleListRules)
			auth.POST("/rule-groups/:id/rules", a.handleCreateRule)
			auth.PUT("/rule-groups/:id/rules/:rid", a.handleUpdateRule)
			auth.DELETE("/rule-groups/:id/rules/:rid", a.handleDeleteRule)
			auth.POST("/rule-groups/test", a.handleTestRule) // 规则测试器 AC-39/G3

			// 代理用户 CRUD + 授权 AC-23/30
			auth.GET("/proxy-users", a.handleListUsers)
			auth.POST("/proxy-users", a.handleCreateUser)
			auth.PUT("/proxy-users/:id", a.handleUpdateUser)
			auth.DELETE("/proxy-users/:id", a.handleDeleteUser)
			auth.POST("/proxy-users/:id/groups", a.handleSetUserGroups) // 设置用户授权分组（用户维度）

			// 系统设置 + 改密 + 导入导出 AC-31/37/40
			auth.GET("/settings", a.handleGetSettings)
			auth.PUT("/settings", a.handleUpdateSettings)
			auth.GET("/settings/server-info", a.handleServerInfo)              // 监听端口+服务器地址（连接提示 T4.2）
			auth.POST("/settings/admin-password", a.handleChangeAdminPassword) // 改密(校验旧密码)
			auth.GET("/settings/export", a.handleExportConfig)
			auth.POST("/settings/import", a.handleImportConfig)

			// 系统日志 SSE + 审计 AC-33/34/36
			auth.GET("/syslog", a.handleLogsSnapshot)        // 缓冲区快照 + 级别筛选
			auth.GET("/syslog/stream", a.handleLogsSSE)      // SSE 实时推送（默认事件，对齐前端 onmessage）
			auth.GET("/syslog/audit", a.handleAuditSnapshot) // 连接审计快照
		}
	}

	// 静态前端 + SPA history fallback（见 static.go）。
	a.registerStatic(r)
	return r
}

// Run 在 cfg.AdminListen（默认 0.0.0.0，AC-41）上启动管理后端 HTTP 服务（阻塞）。
// 由 cmd 装配阶段（T9）调用；独立于 SOCKS5 监听端口。
//
// H1：用显式 *http.Server 承载，以便 Shutdown 优雅关闭（等在途请求收尾）。
// 正常关闭（Shutdown 触发）时 ListenAndServe 返回 http.ErrServerClosed，本方法归一为 nil，
// 使 cmd 不把「优雅关闭」误判为致命错误。
//
// 仅设 ReadHeaderTimeout（防慢速请求头 slowloris，不波及 SSE 长连接的响应体写）；
// 不设 WriteTimeout，否则会切断 /syslog/stream 这类 SSE 长连接。
func (a *App) Run() error {
	a.httpSrv = &http.Server{
		Addr:              a.cfg.AdminListen,
		Handler:           a.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	err := a.httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil // 优雅关闭，非致命
	}
	return err
}

// Shutdown 优雅关闭管理后端：停止接受新连接并等待在途请求在 ctx 截止前收尾（H1）。
// httpSrv 尚未创建（Run 未调用）时为 no-op。
func (a *App) Shutdown(ctx context.Context) error {
	if a.httpSrv == nil {
		return nil
	}
	return a.httpSrv.Shutdown(ctx)
}

// rebuildAndSwap 在写操作 commit 后刷新转发侧快照（G4 回滚封装）。
//
// 统一封装：所有写 handler commit 成功后必须调用本方法。若 Rebuild 失败（如规则非法
// 导致预编译出错），Holder.RebuildAndSwap 会保留旧快照并返回错误，本方法把错误
// 转为 500 回前端，转发侧仍读旧的一致快照（AC-44）。
//
// 返回 true 表示成功（调用方可继续回成功响应）；false 表示已写入 500，调用方应直接 return。
func (a *App) rebuildAndSwap(c *gin.Context) bool {
	if err := snapbuild.RebuildAndSwap(a.holder, a.store, a.cfg); err != nil {
		// G4 回滚：保留旧快照、返回错误。记 Warn（配置写入但热替换失败，需关注）。
		a.logger.Warn("配置快照重建失败，已回滚保留旧快照", "err", err.Error())
		respondError(c, http.StatusInternalServerError, "配置已写入但快照重建失败（已回滚到旧配置）: "+err.Error())
		return false
	}
	return true
}

// validateRuleUpsert 实现 DEC-A1（AC-5.1）写前候选校验：在把一条规则 upsert 落库【之前】，
// 用「当前已提交规则集 + 本次待写规则」构建候选全量并编译校验。校验通过返回 true（调用方继续写）；
// 非法（坏 match/CIDR/action）返回 false 并已写 400 响应，调用方应直接 return，DB 不写。
//
// 为什么挡在写前：规则类坏配置一旦落库再 Rebuild 失败，DB 与转发快照会分裂（坏规则留库）。
// 写前校验保证「规则编译类坏配置永不持久化」（Principle 1）。
func (a *App) validateRuleUpsert(c *gin.Context, pending store.Rule) bool {
	committed, err := a.store.ListAllRules()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取现有规则失败: "+err.Error())
		return false
	}
	ruleGroups, err := a.store.ListRuleGroups()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取规则组失败: "+err.Error())
		return false
	}
	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return false
	}
	cand := snapbuild.BuildCandidateRulesUpsert(committed, pending)
	if err := snapbuild.ValidateRuleset(cand, ruleGroups, ss.DefaultAction); err != nil {
		// 规则编译失败：挡在写前，返回 400（客户端输入非法），DB 不写、快照不分裂。
		respondError(c, http.StatusBadRequest, "规则非法，已拒绝写入（配置未改动）: "+err.Error())
		return false
	}
	return true
}

// validateDefaultAction 校验「把全局默认动作改为 next 后」现有规则集仍能编译。
// 默认动作非法时返回 false 并写 400。供系统设置写前调用，避免坏默认动作落库致分裂。
func (a *App) validateDefaultAction(c *gin.Context, next string) bool {
	committed, err := a.store.ListAllRules()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取现有规则失败: "+err.Error())
		return false
	}
	ruleGroups, err := a.store.ListRuleGroups()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取规则组失败: "+err.Error())
		return false
	}
	if err := snapbuild.ValidateRuleset(committed, ruleGroups, next); err != nil {
		respondError(c, http.StatusBadRequest, "默认动作或现有规则非法，已拒绝写入（配置未改动）: "+err.Error())
		return false
	}
	return true
}
