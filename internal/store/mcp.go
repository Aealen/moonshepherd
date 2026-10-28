// MCP Keys 存储(docs/07 §8)。
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// McpKey MCP Key 记录(明文 Key 永不入库;key_hash = sha256 hex)。
type McpKey struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Scopes    []string `json:"scopes"`
	NodeScope string  `json:"node_scope"` // "all" 或 JSON 数组字符串
	Enabled   bool    `json:"enabled"`
	LastUsed  int64   `json:"last_used"`
	CreatedAt int64   `json:"created_at"`
}

// HashKey 明文 → sha256 hex。
func HashMcpKey(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}

// CreateMcpKey 创建(name 唯一);返回记录。
func (s *Store) CreateMcpKey(ctx context.Context, name, keyHash string, scopes []string, nodeScope string) (*McpKey, error) {
	sc, _ := json.Marshal(scopes)
	if nodeScope == "" {
		nodeScope = "all"
	}
	r, err := s.DB.ExecContext(ctx,
		"INSERT INTO mcp_keys (name, key_hash, scopes, node_scope, enabled, created_at) VALUES (?,?,?,?,1,?)",
		name, keyHash, string(sc), nodeScope, now())
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	return &McpKey{ID: id, Name: name, Scopes: scopes, NodeScope: nodeScope, Enabled: true, CreatedAt: now()}, nil
}

// ListMcpKeys 全量(不含 hash)。
func (s *Store) ListMcpKeys(ctx context.Context) ([]McpKey, error) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, name, scopes, node_scope, enabled, last_used, created_at FROM mcp_keys ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []McpKey
	for rows.Next() {
		var k McpKey
		var sc, ns string
		var en int
		if err := rows.Scan(&k.ID, &k.Name, &sc, &ns, &en, &k.LastUsed, &k.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(sc), &k.Scopes)
		k.NodeScope = ns
		k.Enabled = en == 1
		out = append(out, k)
	}
	return out, rows.Err()
}

// McpKeyByHash 按 hash 查(认证路径;含 enabled)。
func (s *Store) McpKeyByHash(ctx context.Context, keyHash string) (*McpKey, error) {
	row := s.DB.QueryRowContext(ctx,
		"SELECT id, name, scopes, node_scope, enabled, last_used, created_at FROM mcp_keys WHERE key_hash = ?", keyHash)
	var k McpKey
	var sc, ns string
	var en int
	if err := row.Scan(&k.ID, &k.Name, &sc, &ns, &en, &k.LastUsed, &k.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(sc), &k.Scopes)
	k.NodeScope = ns
	k.Enabled = en == 1
	return &k, nil
}

// CountMcpKeys 数量(上限校验用)。
func (s *Store) CountMcpKeys(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_keys").Scan(&n)
	return n, err
}

// UpdateMcpKey 改名/scope/节点范围/启停(key_hash 不可改)。
func (s *Store) UpdateMcpKey(ctx context.Context, id int64, name *string, scopes *[]string, nodeScope *string, enabled *bool) error {
	sets, args := []string{}, []any{}
	if name != nil {
		sets = append(sets, "name=?")
		args = append(args, *name)
	}
	if scopes != nil {
		sc, _ := json.Marshal(*scopes)
		sets = append(sets, "scopes=?")
		args = append(args, string(sc))
	}
	if nodeScope != nil {
		sets = append(sets, "node_scope=?")
		args = append(args, *nodeScope)
	}
	if enabled != nil {
		sets = append(sets, "enabled=?")
		v := 0
		if *enabled {
			v = 1
		}
		args = append(args, v)
	}
	if len(sets) == 0 {
		return fmt.Errorf("无可更新字段")
	}
	args = append(args, id)
	_, err := s.DB.ExecContext(ctx, "UPDATE mcp_keys SET "+joinSets(sets)+" WHERE id=?", args...)
	return err
}

// TouchMcpKey 更新最后使用时间(异步调用,失败忽略)。
func (s *Store) TouchMcpKey(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE mcp_keys SET last_used=? WHERE id=?", now(), id)
}

// DeleteMcpKey 吊销(硬删;审计不依赖此行)。
func (s *Store) DeleteMcpKey(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM mcp_keys WHERE id=?", id)
	return err
}

func joinSets(sets []string) string {
	out := ""
	for i, s := range sets {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
