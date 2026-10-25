// Package web 将前端构建产物（web/dist）embed 进面板二进制（ADR-5）。
// 构建流程：先 npm run build 产出 web/dist，再 go build（见 Makefile）。
package web

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
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
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		http.Error(w, "前端资源不可用", http.StatusServiceUnavailable)
		return
	}
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "前端资源不可用", http.StatusServiceUnavailable)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") || !strings.Contains(r.URL.Path, ".") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Write(b)
}

// Favicon 返回内嵌图标（SVG）。
func Favicon() ([]byte, error) {
	return distFS.ReadFile("dist/favicon.svg")
}
