// 本文件实现会话校验中间件（D2-A）。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// requireAuth 返回会话校验中间件：从 HttpOnly Cookie 读 sessionID 并校验，
// 无效则回 401 中止请求链。受保护的 /api/* 路由统一挂此中间件。
func (a *App) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		sid, err := c.Cookie(sessionCookieName)
		if err != nil || !a.sessions.Validate(sid) {
			respondError(c, http.StatusUnauthorized, "未登录或会话已过期")
			c.Abort()
			return
		}
		c.Next()
	}
}

// setSessionCookie 把会话 ID 写入 HttpOnly Cookie。
//
// 安全属性：HttpOnly 阻止 JS 读取（防 XSS 窃取）；SameSite=Lax 防 CSRF；
// path=/ 全站有效。首版后台默认 HTTP（spec 接受，HTTPS 列后续），故 Secure=false；
// 启用 HTTPS 后应置 Secure=true。
func setSessionCookie(c *gin.Context, sid string) {
	c.SetSameSite(http.SameSiteLaxMode)
	// maxAge 单位秒；与 sessionTTL 对齐。
	c.SetCookie(sessionCookieName, sid, int(sessionTTL.Seconds()), "/", "", false, true)
}

// clearSessionCookie 清除会话 Cookie（登出）。
func clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, "", -1, "/", "", false, true)
}
