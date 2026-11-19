package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Node 节点记录。
type Node struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	AgentVersion string   `json:"agent_version"`
	ProtoVersion string   `json:"proto_version"`
	OSName       string   `json:"os_name"`
	OSVersion    string   `json:"os_version"`
	OSArch       string   `json:"os_arch"`
	Status       string   `json:"status"`
	Enabled      bool     `json:"enabled"`
	LastSeen     int64    `json:"last_seen"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
	CreatedAt    int64    `json:"created_at"`
}

// NodeFilter 列表过滤。
type NodeFilter struct {
	Status string
	Tag    string
	Q      string // 名称/主机名模糊
}

// ListNodes 分页查询节点。
func (s *Store) ListNodes(ctx context.Context, f NodeFilter, page, pageSize int) ([]Node, int, error) {
	where, args := []string{"1=1"}, []any{}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Q != "" {
		where = append(where, "(name LIKE ? OR hostname LIKE ?)")
		q := "%" + f.Q + "%"
		args = append(args, q, q)
	}
	if f.Tag != "" {
		where = append(where, "tags LIKE ?")
		args = append(args, `%"`+f.Tag+`"%`)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes WHERE "+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.DB.QueryContext(ctx, nodeCols+" WHERE "+w+" ORDER BY created_at DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *n)
	}
	return out, total, rows.Err()
}

const nodeCols = "SELECT id, name, hostname, agent_version, proto_version, os_name, os_version, os_arch, status, enabled, last_seen, tags, capabilities, created_at FROM nodes"

func scanNode(r *sql.Rows) (*Node, error) {
	var n Node
	var tags, caps string
	if err := r.Scan(&n.ID, &n.Name, &n.Hostname, &n.AgentVersion, &n.ProtoVersion, &n.OSName, &n.OSVersion, &n.OSArch, &n.Status, &n.Enabled, &n.LastSeen, &tags, &caps, &n.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tags), &n.Tags)
	_ = json.Unmarshal([]byte(caps), &n.Capabilities)
	return &n, nil
}

// GetNode 按 ID 查节点。
func (s *Store) GetNode(ctx context.Context, id string) (*Node, error) {
	row := s.DB.QueryRowContext(ctx, nodeCols+" WHERE id = ?", id)
	n, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("node %s not found", id)
	}
	return n, err
}

func scanRow(r *sql.Row) (*Node, error) {
	var n Node
	var tags, caps string
	if err := r.Scan(&n.ID, &n.Name, &n.Hostname, &n.AgentVersion, &n.ProtoVersion, &n.OSName, &n.OSVersion, &n.OSArch, &n.Status, &n.Enabled, &n.LastSeen, &tags, &caps, &n.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tags), &n.Tags)
	_ = json.Unmarshal([]byte(caps), &n.Capabilities)
	return &n, nil
}

// CountNodesByStatus 返回 online/offline 数（顶栏统计）。
func (s *Store) CountNodesByStatus(ctx context.Context) (online, offline int, err error) {
	err = s.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(status='online'),0), COALESCE(SUM(status<>'online'),0) FROM nodes").Scan(&online, &offline)
	return
}

// CreateNode 注册成功时写入节点。
func (s *Store) CreateNode(ctx context.Context, n *Node, credentialEnc string) error {
	tags, _ := json.Marshal(n.Tags)
	caps, _ := json.Marshal(n.Capabilities)
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO nodes (id, name, hostname, agent_version, proto_version, os_name, os_version, os_arch, status, enabled, last_seen, tags, capabilities, credential_enc, created_at)
		 VALUES (?,?,?,?,?,?,?,?, 'online', 1, ?, ?, ?, ?, ?)`,
		n.ID, n.Name, n.Hostname, n.AgentVersion, n.ProtoVersion, n.OSName, n.OSVersion, n.OSArch,
		now(), string(tags), string(caps), credentialEnc, now())
	return err
}

// UpdateNodeStatus 更新在线状态与最后心跳时间。
func (s *Store) UpdateNodeStatus(ctx context.Context, id, status string, seen int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE nodes SET status = ?, last_seen = ? WHERE id = ?", status, seen, id)
	return err
}

// RefreshNodeAgent hello 携带的版本/能力落库(Agent 换二进制重连后调用);
// 空字段不覆盖,老 Agent 上报缺字段时保留库中原值。
func (s *Store) RefreshNodeAgent(ctx context.Context, id string, agentVersion, protoVersion string, capabilities []string) error {
	if agentVersion != "" {
		if _, err := s.DB.ExecContext(ctx, "UPDATE nodes SET agent_version = ? WHERE id = ?", agentVersion, id); err != nil {
			return err
		}
	}
	if protoVersion != "" {
		if _, err := s.DB.ExecContext(ctx, "UPDATE nodes SET proto_version = ? WHERE id = ?", protoVersion, id); err != nil {
			return err
		}
	}
	if len(capabilities) > 0 {
		b, _ := json.Marshal(capabilities)
		if _, err := s.DB.ExecContext(ctx, "UPDATE nodes SET capabilities = ? WHERE id = ?", string(b), id); err != nil {
			return err
		}
	}
	return nil
}


// TouchNode 心跳只刷新 last_seen（status 已是 online）。
func (s *Store) TouchNode(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE nodes SET last_seen = ?, status = 'online' WHERE id = ?", now(), id)
	return err
}

// UpdateNodeMeta 更名/标签。
func (s *Store) UpdateNodeMeta(ctx context.Context, id string, name *string, tags []string) error {
	if name != nil {
		if _, err := s.DB.ExecContext(ctx, "UPDATE nodes SET name = ? WHERE id = ?", *name, id); err != nil {
			return err
		}
	}
	if tags != nil {
		b, _ := json.Marshal(tags)
		if _, err := s.DB.ExecContext(ctx, "UPDATE nodes SET tags = ? WHERE id = ?", string(b), id); err != nil {
			return err
		}
	}
	return nil
}

// SetNodeEnabled 禁用/解禁；禁用时节点下线。
func (s *Store) SetNodeEnabled(ctx context.Context, id string, enabled bool) error {
	st := "online"
	if !enabled {
		st = "offline"
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE nodes SET enabled = ?, status = ? WHERE id = ?", boolInt(enabled), st, id)
	return err
}

// DeleteNode 删除节点（指令审计保留，node_id 字符串自然保留为墓碑标记）。
func (s *Store) DeleteNode(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"DELETE FROM nodes WHERE id = ?",
		"DELETE FROM app_inventory WHERE node_id = ?",
		"DELETE FROM metrics WHERE node_id = ?",
		"DELETE FROM config_targets WHERE node_id = ?",
		"UPDATE registration_tokens SET status = 'revoked' WHERE node_id = ? AND status = 'active'",
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NodeCredential 取出并解密前的密文（解密在调用方用 credenc 完成）。
func (s *Store) NodeCredential(ctx context.Context, id string) (string, error) {
	var enc string
	err := s.DB.QueryRowContext(ctx, "SELECT credential_enc FROM nodes WHERE id = ?", id).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("node %s not found", id)
	}
	return enc, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- 注册 Token（docs/04 §2.2） ----------

// RegistrationToken Token 记录（明文 token 仅在创建时返回一次，库中只有哈希）。
type RegistrationToken struct {
	ID        int64  `json:"id"`
	TokenHash string `json:"-"`
	Note      string `json:"note"`
	Status    string `json:"status"`
	NodeID    string `json:"node_id,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
}

// NewRegistrationToken 生成一次性注册 Token（rt_ 前缀，256bit 随机）。
func (s *Store) NewRegistrationToken(ctx context.Context, note string, ttl time.Duration) (*RegistrationToken, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	plaintext := "rt_" + hex.EncodeToString(raw)
	t := &RegistrationToken{
		TokenHash: hashToken(plaintext),
		Note:      note,
		Status:    "active",
		ExpiresAt: now() + int64(ttl.Seconds()),
		CreatedAt: now(),
	}
	res, err := s.DB.ExecContext(ctx,
		"INSERT INTO registration_tokens (token_hash, note, status, expires_at, created_at) VALUES (?,?, 'active', ?, ?)",
		t.TokenHash, t.Note, t.ExpiresAt, t.CreatedAt)
	if err != nil {
		return nil, "", err
	}
	t.ID, _ = res.LastInsertId()
	return t, plaintext, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ConsumeRegistrationToken 校验并消费一次性 Token（active + 未过期 → used）。
// 返回错误信息供网关回 error(1001/1002)。
func (s *Store) ConsumeRegistrationToken(ctx context.Context, plaintext string) error {
	row := s.DB.QueryRowContext(ctx,
		"SELECT id, status, expires_at FROM registration_tokens WHERE token_hash = ?", hashToken(plaintext))
	var id int64
	var status string
	var expires int64
	if err := row.Scan(&id, &status, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("token_invalid")
		}
		return err
	}
	if status == "used" {
		return fmt.Errorf("token_invalid")
	}
	if status == "revoked" {
		return fmt.Errorf("token_invalid")
	}
	if now() > expires {
		return fmt.Errorf("token_expired")
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE registration_tokens SET status = 'used' WHERE id = ?", id)
	return err
}

// BindTokenToNode 注册成功后回填 node_id。
func (s *Store) BindTokenToNode(ctx context.Context, token, nodeID string) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE registration_tokens SET node_id = ? WHERE token_hash = ?", nodeID, hashToken(token))
}

// ListRegistrationTokens Token 列表（不含明文）。
func (s *Store) ListRegistrationTokens(ctx context.Context) ([]RegistrationToken, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, token_hash, note, status, COALESCE(node_id, ''), expires_at, created_at FROM registration_tokens ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegistrationToken
	for rows.Next() {
		var t RegistrationToken
		if err := rows.Scan(&t.ID, &t.TokenHash, &t.Note, &t.Status, &t.NodeID, &t.ExpiresAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeRegistrationToken 撤销 Token。
func (s *Store) RevokeRegistrationToken(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE registration_tokens SET status = 'revoked' WHERE id = ? AND status = 'active'", id)
	return err
}

// NewNodeID 生成 n_ 前缀的节点 ID。
func NewNodeID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "n_" + hex.EncodeToString(b)
}

// NewNodeSecret 生成 256bit node_secret（sec_ 前缀）。
func NewNodeSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "sec_" + hex.EncodeToString(b)
}
