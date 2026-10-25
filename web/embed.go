// Package web 将前端构建产物（web/dist）embed 进面板二进制（ADR-5）。
// 构建流程：先 npm run build 产出 web/dist，再 go build（见 Makefile）。
package web

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Assets 返回处理静态资源的 handler；dist 缺失时返回 nil（调用方跳过挂载）。
func Assets() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, errors.New("dist/index.html 不存在")
	}
	return http.FileServer(http.FS(sub)), nil
}

// ServeIndex 输出 SPA 入口（前端路由兜底）。
func ServeIndex(w http.ResponseWriter, r *http.Request) {
	b, err := distFS.ReadFile("dist/index.html")
	if err != nil {
		http.Error(w, "前端资源不可用", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// TryServeStatic 尝试按路径服务 dist 根级静态文件（如 /logo-64.png）；
// 命中返回 true。路径穿越防护：拒绝 ".." 与绝对路径逃逸。
func TryServeStatic(w http.ResponseWriter, r *http.Request) bool {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || strings.Contains(r.URL.Path, "..") {
		return false
	}
	if _, err := fs.Stat(distFS, "dist/"+name); err != nil {
		return false
	}
	b, err := distFS.ReadFile("dist/" + name)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", mimeByExt(path.Ext(name)))
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write(b)
	return true
}

func mimeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".html":
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// Favicon 返回内嵌图标（logo-64.png）。
func Favicon() ([]byte, error) {
	return distFS.ReadFile("dist/logo-64.png")
}
