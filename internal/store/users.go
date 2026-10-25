package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// User 面板账户（MVP 单管理员，docs/01 §7）。
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
}

// CountUsers 是否已初始化管理员。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// CreateUser 创建管理员（密码 bcrypt 哈希，cost 10，docs/05 §3.2）。
func (s *Store) CreateUser(ctx context.Context, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		"INSERT INTO users (username, password_hash, role, created_at) VALUES (?,?,'admin',?)", username, string(hash), now())
	return err
}

// GetUser 按用户名查。
func (s *Store) GetUser(ctx context.Context, username string) (*User, error) {
	row := s.DB.QueryRowContext(ctx, "SELECT id, username, password_hash, role FROM users WHERE username = ?", username)
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// VerifyLogin 校验登录；成功返回用户。
func (s *Store) VerifyLogin(ctx context.Context, username, password string) (*User, error) {
	u, err := s.GetUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		// 对不存在用户也跑一次 bcrypt，避免时序侧信道区分用户名是否存在
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0S4M0Zzq6JXbCkQ0nQZ8uH0FJ6a"), []byte(password))
		return nil, nil
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, nil
	}
	return u, nil
}

// UpdatePassword 修改密码。
func (s *Store) UpdatePassword(ctx context.Context, userID int64, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 10)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", string(hash), userID)
	return err
}

// ---------- 审计（不可删除，docs/05 §3.3） ----------

// AuditLog 面板操作审计。
type AuditLog struct {
	ID        int64           `json:"id"`
	Username  string          `json:"username"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	IP        string          `json:"ip"`
	CreatedAt int64           `json:"created_at"`
}

// AddAudit 落一条审计。
func (s *Store) AddAudit(ctx context.Context, userID int64, username, action, target, detail, ip string) {
	if detail == "" {
		detail = "{}"
	}
	_, _ = s.DB.ExecContext(ctx,
		"INSERT INTO audit_logs (user_id, username, action, target, detail, ip, created_at) VALUES (?,?,?,?,?,?,?)",
		userID, username, action, target, detail, ip, now())
}

// ListAuditLogs 审计分页（只读）。
func (s *Store) ListAuditLogs(ctx context.Context, page, pageSize int) ([]AuditLog, int, error) {
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, username, action, target, detail, ip, created_at FROM audit_logs ORDER BY created_at DESC LIMIT ? OFFSET ?", pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		var l AuditLog
		var detail string
		if err := rows.Scan(&l.ID, &l.Username, &l.Action, &l.Target, &detail, &l.IP, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		l.Detail = json.RawMessage(detail)
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// SystemInfo 面板信息（/system/info）。
type SystemInfo struct {
	PanelVersion string `json:"panel_version"`
	NodeTotal    int    `json:"node_total"`
	NodeOnline   int    `json:"node_online"`
	CommandTotal int    `json:"command_total"`
}

// GetSystemInfo 汇总信息。
func (s *Store) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	info := &SystemInfo{}
	err := s.DB.QueryRowContext(ctx,
		"SELECT (SELECT COUNT(*) FROM nodes), (SELECT COUNT(*) FROM nodes WHERE status='online'), (SELECT COUNT(*) FROM commands)").
		Scan(&info.NodeTotal, &info.NodeOnline, &info.CommandTotal)
	return info, err
}
