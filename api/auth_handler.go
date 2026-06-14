// 本文件实现管理员鉴权相关 handler：首次设置(AC-19)、登录(AC-20)、登出。
//
// 安全（AC-40/G5）：密码用 bcrypt（store.HashPassword，DefaultCost）；
// 登录失败按来源限流（5 次锁 5 分钟）；为减小计时侧信道，无论用户名是否匹配
// 都执行一次 bcrypt 校验路径（见 handleLogin 说明）。
package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"deeproxy/store"
)

// setupStatusResp 是首次设置状态响应（字段 initialized 对齐前端 auth.js）。
type setupStatusResp struct {
	Initialized bool `json:"initialized"` // 管理员是否已初始化（false → 前端跳首次设置页）
}

// handleSetupStatus 返回管理员是否已配置（AC-19 首次设置引导）。
func (a *App) handleSetupStatus(c *gin.Context) {
	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return
	}
	respondOK(c, setupStatusResp{Initialized: ss.IsAdminConfigured()})
}

// credentialReq 是设置/登录共用的账号密码请求体。
type credentialReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetup 首次设置管理员账号密码（AC-19）。
// 仅当尚未配置管理员时允许；已配置后再调用一律拒绝（改密走系统设置 AC-31）。
func (a *App) handleSetup(c *gin.Context) {
	var req credentialReq
	if !bindJSON(c, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		respondError(c, http.StatusBadRequest, "用户名与密码不能为空")
		return
	}

	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return
	}
	// 已配置则禁止重复首次设置（避免越权改密）。
	if ss.IsAdminConfigured() {
		respondError(c, http.StatusConflict, "管理员已配置，请通过登录后在系统设置中修改")
		return
	}

	hash, err := store.HashPassword(req.Password)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "密码哈希失败: "+err.Error())
		return
	}
	if err := a.store.SetAdminCredential(req.Username, hash); err != nil {
		respondError(c, http.StatusInternalServerError, "保存管理员凭据失败: "+err.Error())
		return
	}
	a.logger.Info("完成管理员首次设置", "user", req.Username)
	respondOK(c, nil)
}

// handleLogin 管理员登录（AC-20/40）。
//
// 流程：先查限流锁定 → 读管理员配置 → bcrypt 校验 → 成功签发会话+清限流，
// 失败计一次限流并回 401。校验密码恒走一次 bcrypt 比较以减弱「用户名是否存在」
// 的计时侧信道（G5）。
func (a *App) handleLogin(c *gin.Context) {
	clientKey := c.ClientIP()

	// 1) 限流锁定检查。
	if locked, remain := a.limiter.Locked(clientKey); locked {
		respondError(c, http.StatusTooManyRequests,
			"登录失败次数过多，请 "+remain.Round(time.Second).String()+" 后重试")
		return
	}

	var req credentialReq
	if !bindJSON(c, &req) {
		return
	}

	ss, err := a.store.GetSystemSetting()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "读取系统设置失败: "+err.Error())
		return
	}
	if !ss.IsAdminConfigured() {
		respondError(c, http.StatusBadRequest, "管理员尚未配置，请先完成首次设置")
		return
	}

	// 2) 校验：用户名匹配 且 bcrypt 密码匹配。
	// 即便用户名不匹配也执行一次 bcrypt 校验（用已存哈希），抹平计时差异（G5）。
	userOK := req.Username == ss.AdminUser
	pwdOK := store.VerifyPassword(ss.AdminPwdHash, req.Password)
	if !userOK || !pwdOK {
		a.limiter.Fail(clientKey)
		a.logger.Warn("管理员登录失败", "ip", clientKey, "user", req.Username)
		respondError(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	// 3) 成功：清限流、签发会话。
	a.limiter.Reset(clientKey)
	sid, err := a.sessions.Create()
	if err != nil {
		// 随机源失败：绝不签发弱会话，回 500（极罕见，仅熵源异常时触发）。
		a.logger.Error("签发会话失败（随机源异常）", "ip", clientKey, "err", err.Error())
		respondError(c, http.StatusInternalServerError, "签发会话失败，请重试")
		return
	}
	setSessionCookie(c, sid)
	a.logger.Info("管理员登录成功", "ip", clientKey, "user", req.Username)
	respondOK(c, nil)
}

// handleLogout 登出：删除会话并清 Cookie。
func (a *App) handleLogout(c *gin.Context) {
	if sid, err := c.Cookie(sessionCookieName); err == nil {
		a.sessions.Delete(sid)
	}
	clearSessionCookie(c)
	a.logger.Info("管理员登出")
	respondOK(c, nil)
}
