package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aealen/moonshepherd/internal/store"
)

// withTestSession 注入哑会话(handler 经 sessionFrom 取操作者用户名)。
func withTestSession(req *http.Request) *http.Request {
	sess := &Session{ID: "test", UserID: 1, Username: "tester"}
	return req.WithContext(context.WithValue(req.Context(), ctxSession, sess))
}

func batchReq(srv *Server, body string) *httptest.ResponseRecorder {
	req := withTestSession(httptest.NewRequest(http.MethodPost, "/commands/batch", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleCreateCommandBatch(rec, req)
	return rec
}

// TestBatchValidation 批量端点校验闸:数量/类型/payload。
func TestBatchValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	cases := []struct {
		name, body, wantErr string
	}{
		{"空列表拒绝", `{"node_ids":[],"type":"exec","payload":{"cmd":"df -h"}}`, "node_ids 需 1~50"},
		{"超 50 拒绝", batchBody(51), "node_ids 需 1~50"},
		{"exec 缺命令拒绝", `{"node_ids":["n_1"],"type":"exec","payload":{}}`, "payload.cmd"},
		{"非法类型拒绝", `{"node_ids":["n_1"],"type":"exec2","payload":{"cmd":"x"}}`, "不支持的指令类型"},
		{"docker.op 非白名单拒绝", `{"node_ids":["n_1"],"type":"docker.op","payload":{"op":"rm","container":"x"}}`, "op 必须是"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := batchReq(srv, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应 400,实得 %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.wantErr) {
				t.Fatalf("错误应含 %q,实得: %s", c.wantErr, rec.Body.String())
			}
		})
	}
}

func batchBody(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = `"n_` + strings.Repeat("x", i/26) + string(rune('a'+i%26)) + `"`
	}
	return `{"node_ids":[` + strings.Join(ids, ",") + `],"type":"exec","payload":{"cmd":"uptime"}}`
}

// TestBatchAllOfflineSkipped 全离线:exec 全部计入 skipped,不产生指令。
func TestBatchAllOfflineSkipped(t *testing.T) {
	srv, st := newTestServer(t)
	rec := batchReq(srv, `{"node_ids":["n_ghost1","n_ghost2"],"type":"exec","payload":{"cmd":"uptime"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200(离线=跳过而非报错),实得 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		BatchID string `json:"batch_id"`
		Created []any  `json:"created"`
		Skipped []struct {
			NodeID string `json:"node_id"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.BatchID == "" || len(out.Created) != 0 || len(out.Skipped) != 2 {
		t.Fatalf("应 batch_id 非空/created=0/skipped=2,实得: %+v", out)
	}
	cmds, _, err := st.ListCommands(context.Background(), store.CommandFilter{ParentID: out.BatchID}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 0 {
		t.Fatalf("全跳过批次不应有子指令,实得 %d", len(cmds))
	}
}

// TestBatchParentFilter 批量创建成功路径(probe 离线可排队) + parent 过滤回查 + 状态统计。
func TestBatchParentFilter(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	// 造两个节点(probe 非 exec,离线可排队创建)
	for _, id := range []string{"n_b1", "n_b2"} {
		n := &store.Node{ID: id, Name: id, Hostname: id, Status: "offline", Enabled: true}
		if err := st.CreateNode(ctx, n, "enc"); err != nil {
			t.Fatalf("造节点: %v", err)
		}
	}
	// node_ids 含重复:去重后 2 条(n_b1 + n_b2),重复节点只发一次
	rec := batchReq(srv, `{"node_ids":["n_b1","n_b2","n_b1"],"type":"probe","payload":{"probe_names":[]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200,实得 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		BatchID string `json:"batch_id"`
		Created []struct {
			CommandID string `json:"command_id"`
			NodeID    string `json:"node_id"`
		} `json:"created"`
		Skipped []any `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Created) != 2 || len(out.Skipped) != 0 {
		t.Fatalf("去重后应创建 2 条,实得 created=%d skipped=%d", len(out.Created), len(out.Skipped))
	}
	cmds, total, err := st.ListCommands(ctx, store.CommandFilter{ParentID: out.BatchID}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("批次应有 2 条子指令,实得 %d", total)
	}
	for _, c := range cmds {
		if c.ParentID != out.BatchID {
			t.Fatalf("子指令 parent_id 应为 %q,实得 %q", out.BatchID, c.ParentID)
		}
	}
	stats, err := st.BatchCommandStats(ctx, out.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if stats["pending"] != 2 {
		t.Fatalf("2 条 probe 应全部 pending,实得 %v", stats)
	}
}
