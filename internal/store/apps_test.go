package store

import (
	"context"
	"testing"
)

// NetSeries 聚合语义:同一时间桶内先按节点取均值,再跨节点求和——
// 心跳更密的节点不应在总量里占更大权重。
func TestNetSeries(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// 数据桶锚定一个完整的过去桶 [dataBase, dataBase+30):三条时间戳全在桶内,避免跨桶边界 flaky
	// n_a 两条 rx=100/200(均值 150),n_b 两条 rx=10/30(均值 20) → 桶和 rx=170/tx=12
	// 桶外(now-7200):旧数据,不应出现在窗口结果里
	dataBase := (now()/30)*30 - 60
	ins := `INSERT INTO metrics (node_id, ts, net_rx, net_tx) VALUES (?, ?, ?, ?)`
	rows := []struct {
		node   string
		ts     int64
		rx, tx float64
	}{
		{"n_a", dataBase + 5, 100, 5},
		{"n_a", dataBase + 20, 200, 15},
		{"n_b", dataBase + 8, 10, 1},
		{"n_b", dataBase + 22, 30, 3},
	}
	for _, r := range rows {
		if _, err := st.DB.ExecContext(ctx, ins, r.node, r.ts, r.rx, r.tx); err != nil {
			t.Fatalf("插入心跳: %v", err)
		}
	}
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO metrics (node_id, ts, net_rx, net_tx) VALUES ('n_old', ?, 9999, 9999)`, now()-7200); err != nil {
		t.Fatalf("插入旧数据: %v", err)
	}

	pts, err := st.NetSeries(ctx, 300, 30)
	if err != nil {
		t.Fatalf("NetSeries: %v", err)
	}
	if len(pts) == 0 {
		t.Fatal("应至少返回 1 个桶")
	}
	var hit *NetPoint
	for i := range pts {
		if pts[i].TS == dataBase {
			hit = &pts[i]
		}
	}
	if hit == nil {
		t.Fatalf("数据桶 %d 未出现在结果: %+v", dataBase, pts)
	}
	if hit.Rx != 170 {
		t.Errorf("桶内 rx 应为 170(节点均值 150+20 再求和),实得 %v", hit.Rx)
	}
	if hit.Tx != 12 {
		t.Errorf("桶内 tx 应为 12(n_a 均值 10 + n_b 均值 2),实得 %v", hit.Tx)
	}
	for _, p := range pts {
		if p.Rx > 9000 || p.Tx > 9000 {
			t.Errorf("窗口外的旧数据(n_old rx=9999)不应出现: %+v", p)
		}
	}
}

// 空库不炸,返回空切片。
func TestNetSeriesEmpty(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	defer st.Close()
	pts, err := st.NetSeries(t.Context(), 300, 30)
	if err != nil {
		t.Fatalf("NetSeries: %v", err)
	}
	if len(pts) != 0 {
		t.Errorf("空库应返回 0 桶,实得 %d", len(pts))
	}
}
