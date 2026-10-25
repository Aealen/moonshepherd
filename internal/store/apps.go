package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AppRow 应用清单行。
type AppRow struct {
	ID         int64           `json:"id"`
	NodeID     string          `json:"node_id"`
	NodeName   string          `json:"node_name,omitempty"`
	ProbeName  string          `json:"probe_name"`
	AppName    string          `json:"app_name"`
	AppType    string          `json:"app_type"`
	Version    string          `json:"version,omitempty"`
	Status     string          `json:"status"`
	Detail     json.RawMessage `json:"detail,omitempty"`
	DetectedAt int64           `json:"detected_at"`
}

// UpsertAppInventory 覆盖式更新：先删该节点该探测器行，再插入本次全量结果（docs/04 §5.2）。
func (s *Store) UpsertAppInventory(ctx context.Context, nodeID, probeName string, apps []AppRow) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM app_inventory WHERE node_id = ? AND probe_name = ?", nodeID, probeName); err != nil {
		return err
	}
	for _, a := range apps {
		detail := "{}"
		if len(a.Detail) > 0 {
			detail = string(a.Detail)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO app_inventory (node_id, probe_name, app_name, app_type, version, status, detail, detected_at) VALUES (?,?,?,?,?,?,?,?)`,
			nodeID, probeName, a.AppName, a.AppType, a.Version, a.Status, detail, a.DetectedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppFilter 应用清单过滤。
type AppFilter struct {
	NodeID  string
	Probe   string
	AppType string
	Status  string
	Q       string
}

// ListApps 跨节点应用清单（应用中心主查询，带节点名冗余）。
func (s *Store) ListApps(ctx context.Context, f AppFilter, page, pageSize int) ([]AppRow, int, error) {
	where, args := []string{"1=1"}, []any{}
	if f.NodeID != "" {
		where = append(where, "a.node_id = ?")
		args = append(args, f.NodeID)
	}
	if f.Probe != "" {
		where = append(where, "a.probe_name = ?")
		args = append(args, f.Probe)
	}
	if f.AppType != "" {
		where = append(where, "a.app_type = ?")
		args = append(args, f.AppType)
	}
	if f.Status != "" {
		where = append(where, "a.status = ?")
		args = append(args, f.Status)
	}
	if f.Q != "" {
		where = append(where, "a.app_name LIKE ?")
		args = append(args, "%"+f.Q+"%")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_inventory a WHERE "+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.DB.QueryContext(ctx,
		`SELECT a.id, a.node_id, COALESCE(n.name, a.node_id), a.probe_name, a.app_name, a.app_type, a.version, a.status, a.detail, a.detected_at
		 FROM app_inventory a LEFT JOIN nodes n ON n.id = a.node_id WHERE `+w+` ORDER BY a.detected_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AppRow
	for rows.Next() {
		var a AppRow
		var detail string
		if err := rows.Scan(&a.ID, &a.NodeID, &a.NodeName, &a.ProbeName, &a.AppName, &a.AppType, &a.Version, &a.Status, &detail, &a.DetectedAt); err != nil {
			return nil, 0, err
		}
		a.Detail = json.RawMessage(detail)
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// MetricIn 心跳快照入库。
type MetricIn struct {
	NodeID    string
	CPU       float64
	MemUsed   int64
	MemTotal  int64
	DiskUsed  int64
	DiskTotal int64
	NetRx     float64
	NetTx     float64
	Load1     float64
}

// InsertMetric 心跳快照写时序表。
func (s *Store) InsertMetric(ctx context.Context, m MetricIn) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO metrics (node_id, ts, cpu, mem_used, mem_total, disk_used, disk_total, net_rx, net_tx, load1) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		m.NodeID, now(), m.CPU, m.MemUsed, m.MemTotal, m.DiskUsed, m.DiskTotal, m.NetRx, m.NetTx, m.Load1)
	return err
}

// LatestMetric 节点详情"概览"tab 的最近一次快照。
func (s *Store) LatestMetric(ctx context.Context, nodeID string) (*MetricIn, int64, error) {
	m := &MetricIn{NodeID: nodeID}
	var ts int64
	err := s.DB.QueryRowContext(ctx,
		"SELECT ts, cpu, mem_used, mem_total, disk_used, disk_total, net_rx, net_tx, load1 FROM metrics WHERE node_id = ? ORDER BY ts DESC LIMIT 1", nodeID).
		Scan(&ts, &m.CPU, &m.MemUsed, &m.MemTotal, &m.DiskUsed, &m.DiskTotal, &m.NetRx, &m.NetTx, &m.Load1)
	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	return m, ts, err
}

// PurgeOldMetrics 保留策略：默认 7 天（docs/01 §7）。
func (s *Store) PurgeOldMetrics(ctx context.Context, retention time.Duration) {
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM metrics WHERE ts <= ?", now()-int64(retention.Seconds()))
}
