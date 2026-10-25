package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/maxonphong/herd/internal/credenc"
	"github.com/maxonphong/herd/internal/store"
	"github.com/maxonphong/herd/web"
)

// Options 面板启动配置。
type Options struct {
	DataDir     string // 数据目录（herd.db + master.key）
	Addr        string // 监听地址，默认 :8080
	TLSCertFile string // 手动证书（可选；默认建议前置反代）
	TLSKeyFile  string
	Dev         bool // 开发模式：不校验 Cookie Secure（本地 http 调试）
}

// Server 牧云面板。
type Server struct {
	opt      Options
	st       *store.Store
	box      *credenc.Box
	hub      *agentHub
	sessions *sessionStore
	limiter  *loginLimiter
	router   http.Handler
}

// New 组装面板。
func New(opt Options, st *store.Store, box *credenc.Box) (*Server, error) {
	if opt.Addr == "" {
		opt.Addr = ":8080"
	}
	s := &Server{
		opt:      opt,
		st:       st,
		box:      box,
		hub:      newHub(),
		sessions: newSessionStore(),
		limiter:  newLoginLimiter(),
	}
	s.router = s.buildRouter()
	return s, nil
}

// Handler 供测试与自定义 server 使用。
func (s *Server) Handler() http.Handler { return s.router }

// ListenAndServe 启动 HTTP(S) 服务。
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.opt.Addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if s.opt.TLSCertFile != "" && s.opt.TLSKeyFile != "" {
		slog.Info("面板启动（TLS）", "addr", s.opt.Addr)
		return srv.ListenAndServeTLS(s.opt.TLSCertFile, s.opt.TLSKeyFile)
	}
	if !s.opt.Dev {
		slog.Warn("面板以明文 HTTP 启动——仅建议本地调试；公网部署请启用 TLS 或前置反代（docs/05 §3.1）", "addr", s.opt.Addr)
	} else {
		slog.Info("面板启动（开发模式）", "addr", s.opt.Addr)
	}
	return srv.ListenAndServe()
}

// StartBackground 后台维护任务：过期指令清理 + metrics 保留策略。
func (s *Server) StartBackground(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.st.ExpireStaleCommands(ctx, 600)
				s.st.PurgeOldMetrics(ctx, 7*24*time.Hour)
			}
		}
	}()
}

func (s *Server) buildRouter() http.Handler {
	r := chi.NewRouter()

	r.Use(s.securityHeaders)
	r.Use(s.requestLogger)

	// Agent WSS 网关（docs/04；路径 /agent）
	r.Get("/agent", newGateway(s).handler)

	// REST API（docs/06）
	r.Route("/api/v1", func(api chi.Router) {
		api.Post("/auth/login", s.handleLogin)
		api.Group(func(auth chi.Router) {
			auth.Use(s.requireSession)
			auth.Post("/auth/logout", s.handleLogout)
			auth.Get("/auth/me", s.handleMe)
			auth.Put("/auth/password", s.handleChangePassword)

			auth.Get("/nodes", s.handleListNodes)
			auth.Post("/nodes/registration-tokens", s.handleCreateToken)
			auth.Get("/nodes/registration-tokens", s.handleListTokens)
			auth.Delete("/nodes/registration-tokens/{id}", s.handleRevokeToken)

			auth.Get("/nodes/{id}", s.handleGetNode)
			auth.Patch("/nodes/{id}", s.handlePatchNode)
			auth.Post("/nodes/{id}/disable", s.handleDisableNode)
			auth.Post("/nodes/{id}/enable", s.handleEnableNode)
			auth.Delete("/nodes/{id}", s.handleDeleteNode)
			auth.Post("/nodes/{id}/probe", s.handleProbeNode)
			auth.Get("/nodes/{id}/apps", s.handleNodeApps)

			auth.Post("/nodes/{id}/commands", s.handleCreateCommand)
			auth.Get("/nodes/{id}/configs", s.handleListConfigs)
			auth.Post("/nodes/{id}/configs", s.handleCreateConfig)

			auth.Get("/commands", s.handleListCommands)
			auth.Get("/commands/{id}", s.handleGetCommand)
			auth.Post("/commands/{id}/retry", s.handleRetryCommand)

			auth.Get("/configs/versions", s.handleListVersions)
			auth.Get("/configs/{versionID}", s.handleGetVersion)
			auth.Put("/configs/{versionID}", s.handleEditVersion)
			auth.Post("/configs/{versionID}/apply", s.handleApplyVersion)
			auth.Post("/configs/{versionID}/rollback", s.handleRollbackVersion)

			auth.Get("/apps", s.handleListApps)
			auth.Get("/audit-logs", s.handleAuditLogs)
			auth.Get("/system/info", s.handleSystemInfo)
			auth.Get("/events", s.handleEvents)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		// 非 API 路径 → 前端 SPA 路由兜底
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			web.ServeIndex(w, r)
			return
		}
		writeErr(w, http.StatusNotFound, "not_found", "接口不存在")
	})

	// 静态资源与 SPA（embed，docs/02 §1.2）
	fs, err := web.Assets()
	if err == nil {
		r.Handle("/assets/*", http.StripPrefix("/", fs))
		r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
			if b, err := web.Favicon(); err == nil {
				w.Header().Set("Content-Type", "image/svg+xml")
				_, _ = w.Write(b)
			}
		})
	}
	return r
}

// sessionCookie 构造会话 Cookie（HttpOnly/Secure/SameSite=Strict，docs/05 §3.2）。
// Secure 由部署形态决定：开发模式（本地 http）关闭，其余一律开启。
func (s *Server) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.opt.Dev,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

// ---------- 中间件 ----------

// securityHeaders 安全响应头（docs/05 §3.2）。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/assets") {
			slog.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
		}
	})
}

// requireSession 会话认证中间件（docs/05 §3.2）。
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(sessionCookie)
		if err != nil || s.sessions.Get(ck.Value) == nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		sess := s.sessions.Get(ck.Value)
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		ctx = context.WithValue(ctx, ctxClientIP, clientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------- 响应辅助 ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

type pageResp struct {
	Data     any   `json:"data"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int   `json:"total"`
}

func writePage(w http.ResponseWriter, page, pageSize, total int, data any) {
	if data == nil {
		data = []any{}
	}
	writeJSON(w, http.StatusOK, pageResp{Data: data, Page: page, PageSize: pageSize, Total: total})
}
