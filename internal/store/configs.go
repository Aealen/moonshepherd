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

// ConfigTarget 配置目标：node × app_type × target_path。
type ConfigTarget struct {
	ID         int64  `json:"id"`
	NodeID     string `json:"node_id"`
	AppType    string `json:"app_type"`
	TargetPath string `json:"target_path"`
	CreatedAt  int64  `json:"created_at"`
}

// ConfigVersion 配置版本（不可变版本链）。
type ConfigVersion struct {
	ID         int64           `json:"id"`
	TargetID   int64           `json:"target_id"`
	Content    string          `json:"content"`
	Checksum   string          `json:"checksum"`
	Status     string          `json:"status"` // draft/applied/rolled_back
	CommandID  string          `json:"command_id,omitempty"`
	ApplyResult json.RawMessage `json:"apply_result,omitempty"`
	CreatedBy  string          `json:"created_by"`
	CreatedAt  int64           `json:"created_at"`
}

// ConfigTargetWithMeta 列表附加信息：当前生效版本与最近下发结果。
type ConfigTargetWithMeta struct {
	ConfigTarget
	CurrentVersionID int64  `json:"current_version_id"`
	LastApplyStatus  string `json:"last_apply_status,omitempty"`
}

// ListConfigTargets 节点的配置目标列表。
func (s *Store) ListConfigTargets(ctx context.Context, nodeID string) ([]ConfigTargetWithMeta, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT t.id, t.node_id, t.app_type, t.target_path, t.created_at,
		        COALESCE((SELECT v.id FROM config_versions v WHERE v.target_id = t.id AND v.status = 'applied' ORDER BY v.created_at DESC LIMIT 1), 0),
		        COALESCE((SELECT v.status FROM config_versions v WHERE v.target_id = t.id AND v.command_id <> '' ORDER BY v.created_at DESC LIMIT 1), '')
		 FROM config_targets t WHERE t.node_id = ? ORDER BY t.created_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigTargetWithMeta
	for rows.Next() {
		var t ConfigTargetWithMeta
		if err := rows.Scan(&t.ID, &t.NodeID, &t.AppType, &t.TargetPath, &t.CreatedAt, &t.CurrentVersionID, &t.LastApplyStatus); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateConfigTarget 新建目标 + 首版 draft（一个事务）。
func (s *Store) CreateConfigTarget(ctx context.Context, t *ConfigTarget, firstContent, createdBy string) (int64, int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		"INSERT INTO config_targets (node_id, app_type, target_path, created_at) VALUES (?,?,?,?)",
		t.NodeID, t.AppType, t.TargetPath, now())
	if err != nil {
		return 0, 0, err
	}
	targetID, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	vres, err := tx.ExecContext(ctx,
		"INSERT INTO config_versions (target_id, content, checksum, status, created_by, created_at) VALUES (?,?,?, 'draft', ?, ?)",
		targetID, firstContent, contentChecksum(firstContent), createdBy, now())
	if err != nil {
		return 0, 0, err
	}
	versionID, err := vres.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	return targetID, versionID, tx.Commit()
}

// GetConfigTarget 取目标（校验归属节点）。
func (s *Store) GetConfigTarget(ctx context.Context, nodeID string, targetID int64) (*ConfigTarget, error) {
	row := s.DB.QueryRowContext(ctx,
		"SELECT id, node_id, app_type, target_path, created_at FROM config_targets WHERE id = ? AND node_id = ?", targetID, nodeID)
	var t ConfigTarget
	err := row.Scan(&t.ID, &t.NodeID, &t.AppType, &t.TargetPath, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("config target not found")
	}
	return &t, err
}

// GetConfigTargetByID 按 ID 取目标（回执联动用）。
func (s *Store) GetConfigTargetByID(ctx context.Context, targetID int64) (*ConfigTarget, error) {
	row := s.DB.QueryRowContext(ctx,
		"SELECT id, node_id, app_type, target_path, created_at FROM config_targets WHERE id = ?", targetID)
	var t ConfigTarget
	err := row.Scan(&t.ID, &t.NodeID, &t.AppType, &t.TargetPath, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("config target not found")
	}
	return &t, err
}

// GetConfigVersion 版本详情。
func (s *Store) GetConfigVersion(ctx context.Context, versionID int64) (*ConfigVersion, error) {
	row := s.DB.QueryRowContext(ctx,
		"SELECT id, target_id, content, checksum, status, command_id, apply_result, created_by, created_at FROM config_versions WHERE id = ?", versionID)
	var v ConfigVersion
	var cmdID string
	var applyRes string
	err := row.Scan(&v.ID, &v.TargetID, &v.Content, &v.Checksum, &v.Status, &cmdID, &applyRes, &v.CreatedBy, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("config version not found")
	}
	v.CommandID = cmdID
	v.ApplyResult = json.RawMessage(applyRes)
	return &v, err
}

// AppendDraftVersion 编辑 draft = 产生新版本号（不可变版本链，docs/06 §2.5）。
func (s *Store) AppendDraftVersion(ctx context.Context, targetID int64, content, createdBy string) (int64, error) {
	res, err := s.DB.ExecContext(ctx,
		"INSERT INTO config_versions (target_id, content, checksum, status, created_by, created_at) VALUES (?,?,?, 'draft', ?, ?)",
		targetID, content, contentChecksum(content), createdBy, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MarkVersionApplied 版本下发成功。
func (s *Store) MarkVersionApplied(ctx context.Context, versionID int64, commandID string, applyResult string) error {
	return s.setVersionStatus(ctx, versionID, "applied", commandID, applyResult)
}

// MarkVersionFailed 下发失败保持 draft 可改。
func (s *Store) MarkVersionFailed(ctx context.Context, versionID int64, commandID string, applyResult string) error {
	return s.setVersionStatus(ctx, versionID, "draft", commandID, applyResult)
}

// MarkVersionRolledBack 该版本被作为回滚源应用过。
func (s *Store) MarkVersionRolledBack(ctx context.Context, versionID int64, commandID string, applyResult string) error {
	return s.setVersionStatus(ctx, versionID, "rolled_back", commandID, applyResult)
}

func (s *Store) setVersionStatus(ctx context.Context, versionID int64, status, commandID, applyResult string) error {
	_, err := s.DB.ExecContext(ctx,
		"UPDATE config_versions SET status = ?, command_id = ?, apply_result = ? WHERE id = ?", status, commandID, applyResult, versionID)
	return err
}

// ListConfigVersions 目标的版本历史（时间线）。
func (s *Store) ListConfigVersions(ctx context.Context, targetID int64) ([]ConfigVersion, error) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, target_id, content, checksum, status, command_id, apply_result, created_by, created_at FROM config_versions WHERE target_id = ? ORDER BY created_at DESC, id DESC",
		targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigVersion
	for rows.Next() {
		var v ConfigVersion
		var cmdID, applyRes string
		if err := rows.Scan(&v.ID, &v.TargetID, &v.Content, &v.Checksum, &v.Status, &cmdID, &applyRes, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.CommandID = cmdID
		v.ApplyResult = json.RawMessage(applyRes)
		out = append(out, v)
	}
	return out, rows.Err()
}

func contentChecksum(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}
