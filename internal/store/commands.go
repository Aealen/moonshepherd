package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Command 指令记录（审计主体，不可删除）。
// Stdout/Stderr 仅详情接口返回正文；列表查询在 SQL 层置空（docs/06 §2.4）。
type Command struct {
	ID         string          `json:"command_id"`
	NodeID     string          `json:"node_id"`
	ParentID   string          `json:"parent_id,omitempty"` // 批量下发组 ID(同批次共享;单发为空)
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Status     string          `json:"status"`
	Stdout     string          `json:"stdout,omitempty"`
	Stderr     string          `json:"stderr,omitempty"`
	Truncated  bool            `json:"truncated"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	Stages     json.RawMessage `json:"stages,omitempty"`
	Error      string          `json:"error,omitempty"`
	TimeoutS   int             `json:"timeout_s"`
	CreatedBy  string          `json:"created_by"`
	CreatedAt  int64           `json:"created_at"`
	SentAt     int64           `json:"sent_at,omitempty"`
	FinishedAt int64           `json:"finished_at,omitempty"`
}

// CommandFilter 指令历史过滤。
type CommandFilter struct {
	NodeID   string
	ParentID string
	Type     string
	Status   string
}

// CreateCommand 新建指令（status=pending）。
func (s *Store) CreateCommand(ctx context.Context, c *Command) error {
	if c.TimeoutS <= 0 {
		c.TimeoutS = 120
	}
	c.CreatedAt = now()
	stages := "[]"
	if len(c.Stages) > 0 {
		stages = string(c.Stages)
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO commands (id, node_id, parent_id, type, payload, status, timeout_s, stages, created_by, created_at)
		 VALUES (?,?,?,?,?, 'pending', ?, ?, ?, ?)`,
		c.ID, c.NodeID, c.ParentID, c.Type, string(c.Payload), c.TimeoutS, stages, c.CreatedBy, c.CreatedAt)
	return err
}

const cmdCols = "SELECT id, node_id, parent_id, type, payload, status, stdout, stderr, truncated, exit_code, stages, error, timeout_s, created_by, created_at, sent_at, finished_at FROM commands"

func scanCommand(scan func(...any) error) (*Command, error) {
	var c Command
	var exit sql.NullInt64
	var errStr, stages, payload, parent sql.NullString
	if err := scan(&c.ID, &c.NodeID, &parent, &c.Type, &payload, &c.Status, &c.Stdout, &c.Stderr, &c.Truncated, &exit, &stages, &errStr, &c.TimeoutS, &c.CreatedBy, &c.CreatedAt, &c.SentAt, &c.FinishedAt); err != nil {
		return nil, err
	}
	if exit.Valid {
		v := int(exit.Int64)
		c.ExitCode = &v
	}
	c.Payload = json.RawMessage(payload.String)
	c.Stages = json.RawMessage(stages.String)
	if errStr.Valid {
		c.Error = errStr.String
	}
	if parent.Valid {
		c.ParentID = parent.String
	}
	return &c, nil
}

// GetCommand 指令详情（含输出正文）。
func (s *Store) GetCommand(ctx context.Context, id string) (*Command, error) {
	row := s.DB.QueryRowContext(ctx, cmdCols+" WHERE id = ?", id)
	c, err := scanCommand(row.Scan)
	if err == sql.ErrNoRows {
		return nil, err
	}
	return c, err
}

// ListCommands 分页查询（列表不含 stdout/stderr 正文，见 docs/06 §2.4）。
func (s *Store) ListCommands(ctx context.Context, f CommandFilter, page, pageSize int) ([]*Command, int, error) {
	where, args := []string{"1=1"}, []any{}
	if f.NodeID != "" {
		where = append(where, "node_id = ?")
		args = append(args, f.NodeID)
	}
	if f.ParentID != "" {
		where = append(where, "parent_id = ?")
		args = append(args, f.ParentID)
	}
	if f.Type != "" {
		where = append(where, "type = ?")
		args = append(args, f.Type)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM commands WHERE "+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, node_id, parent_id, type, payload, status, '', '', truncated, exit_code, stages, error, timeout_s, created_by, created_at, sent_at, finished_at FROM commands WHERE "+w+" ORDER BY created_at DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Command
	for rows.Next() {
		c, err := scanCommand(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		c.Stdout, c.Stderr = "", ""
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// UpdateCommandStatus 状态机迁移（docs/04 §4.1）。
func (s *Store) UpdateCommandStatus(ctx context.Context, id, status string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE commands SET status = ? WHERE id = ?", status, id)
	return err
}

// MarkCommandSent 已下发。
func (s *Store) MarkCommandSent(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE commands SET status = 'sent', sent_at = ? WHERE id = ?", now(), id)
	return err
}

// FinishCommand 回执落库（成功或失败终态）。
func (s *Store) FinishCommand(ctx context.Context, r *CommandResultIn) error {
	stages := "[]"
	if len(r.Stages) > 0 {
		stages = string(r.Stages)
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE commands SET status = ?, stdout = ?, stderr = ?, truncated = ?, exit_code = ?, stages = ?, error = ?, finished_at = ? WHERE id = ?`,
		r.Status, r.Stdout, r.Stderr, boolInt(r.Truncated), r.ExitCode, stages, r.Error, now(), r.CommandID)
	return err
}

// CommandResultIn 回执入库参数。
type CommandResultIn struct {
	CommandID string
	Status    string
	Stdout    string
	Stderr    string
	Truncated bool
	ExitCode  int
	Stages    json.RawMessage
	Error     string
}

// PendingCommandsForNode 节点重连后补投：未过期 pending 指令按创建序（docs/04 §4.2）。
func (s *Store) PendingCommandsForNode(ctx context.Context, nodeID string, ttlS int) ([]*Command, error) {
	rows, err := s.DB.QueryContext(ctx, cmdCols+" WHERE node_id = ? AND status = 'pending' AND created_at > ? ORDER BY created_at ASC", nodeID, now()-int64(ttlS))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Command
	for rows.Next() {
		c, err := scanCommand(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkLostCommands 连接断开：sent/running 且超 TTL 的指令标 lost（不自动重发，docs/04 §4.2）。
func (s *Store) MarkLostCommands(ctx context.Context, nodeID string) error {
	_, err := s.DB.ExecContext(ctx,
		"UPDATE commands SET status = 'lost', finished_at = ? WHERE node_id = ? AND status IN ('sent','running')", now(), nodeID)
	return err
}

// ExpireStaleCommands 周期清理：过期 pending 标 expired。
func (s *Store) ExpireStaleCommands(ctx context.Context, ttlS int) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE commands SET status = 'expired', finished_at = ? WHERE status = 'pending' AND created_at <= ?", now(), now()-int64(ttlS))
}

// NewCommandID c_ 前缀指令 ID。
func NewCommandID() string {
	return "c_" + randHex(16)
}

// BatchCommandStats 批量组内各状态计数(聚合条展示)。
func (s *Store) BatchCommandStats(ctx context.Context, parentID string) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT status, COUNT(*) FROM commands WHERE parent_id = ? GROUP BY status", parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
