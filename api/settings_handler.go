// 本文件实现系统设置读写、管理员改密（AC-31/40）。
//
// 端点对齐前端契约：
//   - GET/PUT /settings：statRetentionDays + 嵌套 hcDefaults。
//   - POST /settings/admin-password：{oldPassword,newPassword}，校验旧密码后改密并清所有会话。
package api

import (
	"net"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"deeproxy/internal/logging"
	"deeproxy/internal/netutil"
	"deeproxy/store"
)

// hcDefaultsDTO 是健康检查默认值（嵌套对象，对齐前端 settings.hcDefaults）。
type hcDefaultsDTO struct {
	Mode             string `json:"mode"`
	URL              string `json:"url"`
	IntervalSec      int    `json:"intervalSec"`
	FailThreshold    int    `json:"failThreshold"`
	RecoverThreshold int    `json:"recoverThreshold"`
}

// settingsResp 是系统设置返回体（不含密码哈希；含管理员用户名供展示）。
type settingsResp struct {
	AdminUser         string        `json:"adminUser"`
	StatRetentionDays int           `json:"statRetentionDays"`
	HCDefaults        hcDefaultsDTO `json:"hcDefaults"`

	// —— v2 运行期动态设置（取消配置文件后迁入系统设置，可后台热改）——
	DefaultAction  string `json:"defaultAction"`  // 默认动作 forward/direct/reject
	LogLevel       string `json:"logLevel"`       // 日志级别 debug/info/warn/error
	IdleTimeoutSec int    `json:"idleTimeoutSec"` // 空闲超时（秒）
	SniffDomain    bool   `json:"sniffDomain"`    // 是否启用域名嗅探
	SniffTimeoutMs int    `json:"sniffTimeoutMs"` // 嗅探首包等待超时（毫秒）

	// —— v2 批量增强：连接提示与探测池 ——
	ServerAddr    string `json:"serverAddr"`    // 服务器域名/IP（连接示例/复制地址用；首次为空时回探测值）
	ProbePoolSize int    `json:"probePoolSize"` // 健康检查全局协程池大小（默认 150）
	Socks5Port    int    `json:"socks5Port"`    // 本地 SOCKS5 监听端口（前端连接示例优先从这里取）
	WebPort       int    `json:"webPort"`       // Web 后台监听端口
}

func (a *App) handleGetSettings(c *gin.Context) {
	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return
	}
	// 首次默认：server_addr 为空时回探测到的本机非回环 IPv4 作为提示（不落库，仅展示默认值；
	// 用户保存后才持久化。这样空库首屏即有可用提示，又不强行写入用户未确认的值）。
	serverAddr := ss.ServerAddr
	if serverAddr == "" {
		serverAddr = netutil.DetectLocalIP()
	}
	// probe_pool_size 兜底：旧库迁移后即为 150，但防御性兜底避免展示 0。
	probePoolSize := ss.ProbePoolSize
	if probePoolSize <= 0 {
		probePoolSize = 150
	}
	respondOK(c, settingsResp{
		AdminUser:         ss.AdminUser,
		StatRetentionDays: ss.StatRetentionDays,
		HCDefaults: hcDefaultsDTO{
			Mode:             string(ss.HCDefaultMode),
			URL:              ss.HCDefaultURL,
			IntervalSec:      ss.HCDefaultInterval,
			FailThreshold:    ss.HCDefaultFailThld,
			RecoverThreshold: ss.HCDefaultRecvThld,
		},
		DefaultAction:  ss.DefaultAction,
		LogLevel:       ss.LogLevel,
		IdleTimeoutSec: ss.IdleTimeoutSec,
		SniffDomain:    ss.SniffDomain,
		SniffTimeoutMs: ss.SniffTimeoutMs,
		ServerAddr:     serverAddr,
		ProbePoolSize:  probePoolSize,
		Socks5Port:     portOf(a.cfg.Listen),
		WebPort:        portOf(a.cfg.AdminListen),
	})
}

// settingsReq 是系统设置更新请求体（不含改密，改密走独立端点）。
type settingsReq struct {
	StatRetentionDays int           `json:"statRetentionDays"`
	HCDefaults        hcDefaultsDTO `json:"hcDefaults"`

	// v2 运行期动态设置。SniffDomain 用 *bool 以区分「未传」与「显式 false」（避免漏传被当成关闭）。
	DefaultAction  string `json:"defaultAction"`
	LogLevel       string `json:"logLevel"`
	IdleTimeoutSec int    `json:"idleTimeoutSec"`
	SniffDomain    *bool  `json:"sniffDomain"`
	SniffTimeoutMs int    `json:"sniffTimeoutMs"`

	// v2 批量增强：ServerAddr 用 *string 区分「未传」与「显式清空」（允许用户清空回退探测）。
	ServerAddr    *string `json:"serverAddr"`
	ProbePoolSize int     `json:"probePoolSize"` // <=0 视为不改，保留原值
}

// validSettingActions 校验默认动作取值（非法即拒，避免坏数据进库/快照）。
// 日志级别复用 syslog_handler.go 的 validLogLevels（其含 "" 表示「不筛选」，
// 此处在调用前已用 req.LogLevel != "" 单独排除空值，故复用安全）。
var validSettingActions = map[string]bool{"forward": true, "direct": true, "reject": true}

func (a *App) handleUpdateSettings(c *gin.Context) {
	var req settingsReq
	if !bindJSON(c, &req) {
		return
	}
	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return
	}
	// <=0 / 空 视为不改，保留原值（避免前端漏传清零）。
	if req.StatRetentionDays > 0 {
		ss.StatRetentionDays = req.StatRetentionDays
	}
	if req.HCDefaults.Mode != "" {
		ss.HCDefaultMode = store.HealthMode(req.HCDefaults.Mode)
	}
	if req.HCDefaults.URL != "" {
		ss.HCDefaultURL = req.HCDefaults.URL
	}
	if req.HCDefaults.IntervalSec > 0 {
		ss.HCDefaultInterval = req.HCDefaults.IntervalSec
	}
	if req.HCDefaults.FailThreshold > 0 {
		ss.HCDefaultFailThld = req.HCDefaults.FailThreshold
	}
	if req.HCDefaults.RecoverThreshold > 0 {
		ss.HCDefaultRecvThld = req.HCDefaults.RecoverThreshold
	}

	// —— v2 运行期动态设置（带合法性校验，非法即拒，不写库）——
	if req.DefaultAction != "" {
		if !validSettingActions[req.DefaultAction] {
			respondError(c, http.StatusBadRequest, "默认动作非法（应为 forward/direct/reject）")
			return
		}
		ss.DefaultAction = req.DefaultAction
	}
	if req.LogLevel != "" {
		if !validLogLevels[req.LogLevel] {
			respondError(c, http.StatusBadRequest, "日志级别非法（应为 debug/info/warn/error）")
			return
		}
		ss.LogLevel = req.LogLevel
	}
	if req.IdleTimeoutSec > 0 {
		ss.IdleTimeoutSec = req.IdleTimeoutSec
	}
	if req.SniffTimeoutMs > 0 {
		ss.SniffTimeoutMs = req.SniffTimeoutMs
	}
	if req.SniffDomain != nil { // *bool：显式传了才改（含 false=关闭嗅探）
		ss.SniffDomain = *req.SniffDomain
	}

	// v2 批量增强字段。
	if req.ServerAddr != nil { // *string：显式传了才改（含 ""=清空回退探测）
		ss.ServerAddr = *req.ServerAddr
	}
	if req.ProbePoolSize > 0 { // <=0 视为不改，避免漏传清零（health 每轮读它，0 会让池退化）
		ss.ProbePoolSize = req.ProbePoolSize
	}

	if err := a.store.UpdateSystemSetting(ss); err != nil {
		respondError(c, http.StatusInternalServerError, "更新系统设置失败: "+err.Error())
		return
	}

	// 日志级别热生效：紧跟写库之后、rebuildAndSwap 之前 Set。
	// 为什么提前：log_level 与转发快照【解耦】（不进 Snapshot，由独立 LevelVar 承载），
	// 其生效不依赖 rebuild。若放在 rebuildAndSwap 之后，一旦 rebuild 失败提前 return，
	// 会出现「DB 里 log_level 已是新值、但运行时 LevelVar 仍是旧值」的脱节，直到下次成功设置。
	// 紧跟写库后 Set 可保证 DB 与运行时级别始终一致（即便随后 rebuild 失败也不影响日志级别正确性）。
	a.levelVar.Set(logging.ParseLevel(ss.LogLevel))

	// 默认动作 / 空闲 / 嗅探 已写库，需重建快照让转发侧（新连接）读到新值（这些项进快照，须 rebuild 成功才生效）。
	// 健康检查默认值变更不影响转发结构，但一并刷新保持一致。
	if !a.rebuildAndSwap(c) {
		return
	}

	a.logger.Info("更新系统设置", "default_action", ss.DefaultAction, "log_level", ss.LogLevel,
		"idle_timeout_sec", ss.IdleTimeoutSec, "sniff_domain", ss.SniffDomain, "sniff_timeout_ms", ss.SniffTimeoutMs)
	respondOK(c, nil)
}

// changePwdReq 是管理员改密请求体（校验旧密码）。
type changePwdReq struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// handleChangeAdminPassword 管理员改密（AC-40）：校验旧密码 → 写新哈希 → 清所有会话强制重登。
func (a *App) handleChangeAdminPassword(c *gin.Context) {
	var req changePwdReq
	if !bindJSON(c, &req) {
		return
	}
	if req.NewPassword == "" {
		respondError(c, http.StatusBadRequest, "新密码不能为空")
		return
	}
	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return
	}
	// 校验旧密码（防会话被盗后直接改密）。
	if !store.VerifyPassword(ss.AdminPwdHash, req.OldPassword) {
		respondError(c, http.StatusUnauthorized, "旧密码不正确")
		return
	}
	hash, err := store.HashPassword(req.NewPassword)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "密码哈希失败: "+err.Error())
		return
	}
	if err := a.store.SetAdminCredential(ss.AdminUser, hash); err != nil {
		respondError(c, http.StatusInternalServerError, "保存新密码失败: "+err.Error())
		return
	}
	// 改密即踢出所有会话（含当前），强制重登。
	a.sessions.Clear()
	clearSessionCookie(c)
	a.logger.Warn("管理员密码已修改，所有会话已失效")
	respondOK(c, nil)
}

// serverInfoResp 是首页连接提示所需的服务器信息（AC-2.6/4.2/4.3）：
// 服务器域名/IP + 监听端口，供前端拼出真实连接示例与「复制代理地址」。
type serverInfoResp struct {
	ServerAddr string `json:"serverAddr"` // 服务器域名/IP（设置值优先，空则回探测的本机 IP）
	Socks5Port int    `json:"socks5Port"` // 本地 SOCKS5 监听端口
	WebPort    int    `json:"webPort"`    // Web 后台监听端口
}

// handleServerInfo 暴露监听端口与服务器地址给前端（T4.2，路由 GET /settings/server-info）。
//
// 端口来源于启动引导配置 cfg.Listen/AdminListen（非业务数据，进程级固定）；
// serverAddr 来源于系统设置（用户可改），为空时回探测本机非回环 IPv4 作默认提示。
func (a *App) handleServerInfo(c *gin.Context) {
	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return
	}
	serverAddr := ss.ServerAddr
	if serverAddr == "" {
		serverAddr = netutil.DetectLocalIP()
	}
	respondOK(c, serverInfoResp{
		ServerAddr: serverAddr,
		Socks5Port: portOf(a.cfg.Listen),
		WebPort:    portOf(a.cfg.AdminListen),
	})
}

// portOf 从 "host:port" 监听地址提取端口号；解析失败返回 0。
func portOf(listen string) int {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return port
}
