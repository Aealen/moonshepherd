package server

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// handleLogin POST /auth/login（docs/06 §2.1；限流见 docs/05 §3.2）。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "用户名与密码必填")
		return
	}
	ip := clientIP(r)
	ipKey, userKey := "ip:"+ip, "user:"+req.Username
	// 双维度锁检查：任一命中即拒绝（锁定期间正确密码也拒绝，docs/05 §6）
	if locked, remain := s.limiter.Locked(ipKey, userKey); locked {
		w.Header().Set("Retry-After", strconv.Itoa(remain))
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，已锁定")
		return
	}
	u, err := s.st.VerifyLogin(r.Context(), req.Username, req.Password)
	if err != nil || u == nil {
		s.limiter.RecordFailure(ipKey, userKey)
		s.st.AddAudit(r.Context(), 0, req.Username, "auth.login_failed", "", "", ip)
		writeErr(w, http.StatusUnauthorized, "unauthorized", "用户名或密码错误")
		return
	}
	s.limiter.RecordSuccess(userKey)
	sess := s.sessions.Create(u.ID, u.Username)
	http.SetCookie(w, s.sessionCookie(sess.ID, int(sessionAbsTTL.Seconds())))
	s.st.AddAudit(r.Context(), u.ID, u.Username, "auth.login", "", "", ip)
	writeJSON(w, http.StatusOK, map[string]any{"username": u.Username, "role": u.Role, "expires_at": sess.ExpiresAt.Unix()})
}

// handleLogout POST /auth/logout：会话即焚。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.Destroy(ck.Value)
	}
	http.SetCookie(w, s.sessionCookie("", -1))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMe GET /auth/me。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"username": sess.Username, "role": "admin", "session_expires_at": sess.ExpiresAt.Unix()})
}

// handleChangePassword PUT /auth/password。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "请求体非法")
		return
	}
	if len(req.NewPassword) < 10 {
		writeErr(w, http.StatusBadRequest, "validation_failed", "新密码最短 10 位（docs/05 §3.2）")
		return
	}
	sess := sessionFrom(r)
	ctx := r.Context()
	u, err := s.st.VerifyLogin(ctx, sess.Username, req.OldPassword)
	if err != nil || u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "原密码错误")
		return
	}
	if err := s.st.UpdatePassword(ctx, u.ID, req.NewPassword); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "更新失败")
		return
	}
	// 其余会话全部失效（docs/05）
	s.sessions.DestroyOthers(sess.ID, u.ID)
	s.st.AddAudit(ctx, u.ID, u.Username, "auth.password_changed", "", "", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
