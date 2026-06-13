// Package api 是 deeproxy v2 的管理后端（Gin HTTP 服务 + 前端 embed）。
//
// 本文件（阶段 0 spike）落地 embed 占位与静态资源挂载的最小骨架，保证：
//   - //go:embed dist/* 在前端未构建时也能编译（仓库已提供 dist/.gitkeep + index.html 占位）；
//   - gin 依赖被实际引用，go mod tidy 不会将其裁剪。
//
// 完整的路由、SPA history fallback 等在阶段 7/9 由对应 worker 完善。
package api

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

// distFS 嵌入前端构建产物目录 dist。
// embed 路径规范要求统一用正斜杠，跨平台（Windows）不依赖 OS 分隔符。
//
// 必须用 all: 前缀：Go embed 默认【忽略以 _ 或 . 开头的文件】，而 Vite 会产出
// 名为 _plugin-vue_export-helper-*.js/.css 的公共 chunk（下划线开头）。若用普通
// //go:embed dist，这些 chunk 不会被嵌入，运行时请求它们会落到 SPA fallback 返回
// index.html(text/html)，浏览器按 ES module 严格 MIME 检查报错、整页空白。
// all: 前缀强制包含下划线/点开头的文件，修复该问题。
//
//go:embed all:dist
var distFS embed.FS

// StaticFS 返回嵌入的前端静态文件系统（以 dist 为根）。
// 阶段 9 由 cmd 装配时挂到 Gin；此处先提供取用入口并锚定 embed 引用。
func StaticFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

// RegisterStatic 把嵌入的前端静态资源挂到 Gin 引擎（自由函数版，向后兼容 spike 调用）。
// 内部委托给 App 无依赖的 mountStatic（DRY）。
func RegisterStatic(r *gin.Engine) error {
	return mountStatic(r)
}

// registerStatic 是 App 在 Router() 中调用的静态资源挂载入口（含 SPA history fallback）。
func (a *App) registerStatic(r *gin.Engine) {
	// 挂载失败不致命（占位 dist 总能 Sub 成功）；记录后继续，保证 /api 仍可用。
	_ = mountStatic(r)
}

// mountStatic 挂载嵌入前端：静态文件 + SPA history fallback。
//
// SPA fallback：前端是单页应用（vue-router history 模式），刷新 /groups 等前端路由
// 时服务端无对应文件，需回退到 index.html 由前端路由接管。用 NoRoute 兜底，
// 但 /api 前缀的未命中应回 404 而非 index.html（避免把 API 404 当页面）。
func mountStatic(r *gin.Engine) error {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return err
	}
	fileServer := http.FileServer(http.FS(sub))

	// 静态资源：交给文件服务器（命中文件直接返回）。
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		// /api 未命中 → 真正的 404（不回退 index.html）。
		if len(path) >= 4 && path[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"msg": "接口不存在"})
			return
		}
		// 尝试作为静态文件返回；文件不存在时由下面的 SPA 回退处理。
		if f, ferr := sub.Open(trimLeadingSlash(path)); ferr == nil {
			_ = f.Close()
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// SPA history fallback：回 index.html。
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
	return nil
}

// trimLeadingSlash 去掉路径前导斜杠（fs.Open 不接受前导 "/"）。
func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	if p == "" {
		return "."
	}
	return p
}
