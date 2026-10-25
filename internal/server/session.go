// Package server 实现牧云面板：HTTP API、Agent WSS 网关、浏览器事件通道。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Session 服务端会话（docs/05 §3.2：绝对过期 12h、空闲过期 2h、登出即焚）。
type Session struct {
	ID        string
	UserID    int64
	Username  string
	LastSeen  time.Time
	ExpiresAt time.Time
}

// sessionStore 内存会话表；面板重启 = 全体会话失效（可接受的安全增益）。
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]*Session{}}
}

const (
	sessionIdleTTL  = 2 * time.Hour
	sessionAbsTTL   = 12 * time.Hour
	sessionCookie   = "herd_session"
)

func (ss *sessionStore) Create(userID int64, username string) *Session {
	s := &Session{
		ID:        "s_" + randHex(32),
		UserID:    userID,
		Username:  username,
		LastSeen:  time.Now(),
		ExpiresAt: time.Now().Add(sessionAbsTTL),
	}
	ss.mu.Lock()
	ss.sessions[s.ID] = s
	ss.mu.Unlock()
	return s
}

// Get 返回会话并校验空闲/绝对过期；命中即续空闲期。
func (ss *sessionStore) Get(id string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.sessions[id]
	if !ok {
		return nil
	}
	now := time.Now()
	if now.After(s.ExpiresAt) || now.Sub(s.LastSeen) > sessionIdleTTL {
		delete(ss.sessions, id)
		return nil
	}
	s.LastSeen = now
	return s
}

func (ss *sessionStore) Destroy(id string) {
	ss.mu.Lock()
	delete(ss.sessions, id)
	ss.mu.Unlock()
}

// DestroyOthers 改密码后：除当前会话外全部失效。
func (ss *sessionStore) DestroyOthers(keepID string, userID int64) {
	ss.mu.Lock()
	for id, s := range ss.sessions {
		if s.UserID == userID && id != keepID {
			delete(ss.sessions, id)
		}
	}
	ss.mu.Unlock()
}

// ---------- 登录防爆破限流（docs/05 §3.2：IP + 账号双维度，P0） ----------

// loginLimiter 15 分钟窗口 5 次失败 → 锁定 15 分钟，连续触发指数升级。
type loginLimiter struct {
	mu sync.Mutex
	// key: "ip:1.2.3.4" 或 "user:admin"
	fails    map[string][]int64 // 窗口内失败时间戳
	lockedAt map[string]int64   // 锁定开始时间
	lockDur  map[string]time.Duration
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		fails:    map[string][]int64{},
		lockedAt: map[string]int64{},
		lockDur:  map[string]time.Duration{},
	}
}

const (
	loginWindow      = 15 * time.Minute
	loginMaxFails    = 5
	loginBaseLock    = 15 * time.Minute
	loginMaxLock     = 24 * time.Hour
	loginLockMultiplier = 2
)

// Locked 返回 (是否锁定, 剩余秒)。
func (l *loginLimiter) Locked(keys ...string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	for _, k := range keys {
		start, ok := l.lockedAt[k]
		if !ok {
			continue
		}
		dur := l.lockDur[k]
		remain := int(dur.Seconds()) - (int(now) - int(start))
		if remain > 0 {
			return true, remain
		}
		delete(l.lockedAt, k)
		delete(l.lockDur, k)
	}
	return false, 0
}

// RecordFailure 记失败；触发阈值则锁定（指数升级）。
func (l *loginLimiter) RecordFailure(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	for _, k := range keys {
		l.fails[k] = append(l.fails[k], now)
		// 只留窗口内
		var win []int64
		for _, t := range l.fails[k] {
			if now-t < int64(loginWindow.Seconds()) {
				win = append(win, t)
			}
		}
		l.fails[k] = win
		if len(win) >= loginMaxFails && l.lockedAt[k] == 0 {
			dur := loginBaseLock
			for i := 0; i < len(win)-loginMaxFails && dur < loginMaxLock; i++ {
				dur *= loginLockMultiplier
			}
			l.lockedAt[k] = now
			l.lockDur[k] = minDur(dur, loginMaxLock)
			l.fails[k] = nil
		}
	}
}

// RecordSuccess 登录成功清零该账号失败计数。
func (l *loginLimiter) RecordSuccess(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.fails, k)
	}
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// ---------- HTTP 上下文辅助 ----------

type ctxKey int

const (
	ctxSession ctxKey = iota
	ctxClientIP
)

func sessionFrom(r *http.Request) *Session {
	s, _ := r.Context().Value(ctxSession).(*Session)
	return s
}

func clientIP(r *http.Request) string {
	// 面板常前置反代（决策 docs/03 §6.2 主推反代）：优先 X-Forwarded-For 首个（最原始客户端）
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return trimSpace(xff[:i])
			}
		}
		return trimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	host := r.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
