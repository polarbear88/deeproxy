// 本文件集中放置 handler 共用的 HTTP 响应与参数解析辅助（DRY）。
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// errorResponse 是统一的错误响应体。
// 字段名 msg 对齐前端 axios 拦截器读取的 error.response.data.msg。
type errorResponse struct {
	Msg string `json:"msg"` // 中文错误说明，前端可直接展示
}

// respondError 写入统一的 JSON 错误响应。
func respondError(c *gin.Context, status int, msg string) {
	c.JSON(status, errorResponse{Msg: msg})
}

// respondOK 写入 200 + 数据体；data 为 nil 时回 {"ok":true}。
func respondOK(c *gin.Context, data any) {
	if data == nil {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	c.JSON(http.StatusOK, data)
}

// parseIDParam 解析路径参数中的 int64 ID；非法时写入 400 并返回 ok=false。
func parseIDParam(c *gin.Context, name string) (int64, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "路径参数 "+name+" 非法: "+raw)
		return 0, false
	}
	return id, true
}

// parseQueryID 解析查询参数中的 int64 ID；非法时写入 400 并返回 ok=false。
func parseQueryID(c *gin.Context, name string) (int64, bool) {
	raw := c.Query(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		respondError(c, http.StatusBadRequest, "查询参数 "+name+" 非法: "+raw)
		return 0, false
	}
	return id, true
}

// bindJSON 把请求体解析到 dst；失败时写入 400 并返回 ok=false。
// 统一封装避免每个 handler 重复写错误处理（DRY）。
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		respondError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return false
	}
	return true
}
