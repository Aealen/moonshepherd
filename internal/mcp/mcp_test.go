package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aealen/moonshepherd/internal/store"
)

// fakeDeps 测试注入。
type fakeDeps struct {
	store *store.Store
	audit []string // action 记录
}

func (f *fakeDeps) Store() *store.Store { return f.store }
func (f *fakeDeps) Audit(action, target, detailJSON, ip, keyName string) {
	f.audit = append(f.audit, action)
}
func (f *fakeDeps) NodeOnline(string) bool                                  { return true }
func (f *fakeDeps) PanelInfo(context.Context) (string, error)               { return "panel", nil }
func (f *fakeDeps) ListConfigsText(context.Context, string) (string, error) { return "", nil }
func (f *fakeDeps) GetConfigText(context.Context, int64, int64) (string, error) {
	return "", nil
}
func (f *fakeDeps) RunDockerOp(context.Context, string, string, string, int) (string, error) {
	return "ok", nil
}
func (f *fakeDeps) RunExec(context.Context, string, string, int) (string, error) {
	return "done", nil
}
func (f *fakeDeps) FileOp(context.Context, string, string, string, string, string) (string, error) {
	return "ok", nil
}

func newTest(t *testing.T) (*Server, *store.Store, *fakeDeps, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	deps := &fakeDeps{store: st}
	srv := NewServer(deps)
	plain, hash, _ := GenerateKey()
	if _, err := st.CreateMcpKey(context.Background(), "test-key", hash, []string{ScopeRead}, "all"); err != nil {
		t.Fatal(err)
	}
	return srv, st, deps, plain
}

func call(srv *Server, key string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	srv.HandleHTTP(w, req)
	return w
}

// TestMcpAuth 认证:无 Key/错 Key/禁用 → 401;正确 → 200。
func TestMcpAuth(t *testing.T) {
	srv, st, _, plain := newTest(t)

	if w := call(srv, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); w.Code != 401 {
		t.Fatalf("无 Key 应 401,实得 %d", w.Code)
	}
	if w := call(srv, "mcp_wrongkeywrongkeywrongkeywrongkeywrongkey0", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); w.Code != 401 {
		t.Fatalf("错 Key 应 401,实得 %d", w.Code)
	}
	// 禁用
	keys, _ := st.ListMcpKeys(context.Background())
	off := false
	_ = st.UpdateMcpKey(context.Background(), keys[0].ID, nil, nil, nil, &off)
	if w := call(srv, plain, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); w.Code != 401 {
		t.Fatalf("禁用 Key 应 401,实得 %d", w.Code)
	}
	on := true
	_ = st.UpdateMcpKey(context.Background(), keys[0].ID, nil, nil, nil, &on)
	if w := call(srv, plain, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); w.Code != 200 {
		t.Fatalf("正确 Key 应 200,实得 %d", w.Code)
	}
}

// TestMcpScopeDenied 越权:read-only Key 调 exec → tool isError + .denied 审计。
func TestMcpScopeDenied(t *testing.T) {
	srv, _, deps, plain := newTest(t)
	w := call(srv, plain, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"exec_command","arguments":{"node_id":"n_1","cmd":"id"}}}`)
	if w.Code != 200 {
		t.Fatalf("tool 级拒绝应为 HTTP 200,实得 %d", w.Code)
	}
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Result.IsError {
		t.Fatal("scope 拒绝应为 tool isError")
	}
	if len(deps.audit) == 0 || !strings.HasSuffix(deps.audit[0], ".denied") {
		t.Fatalf("应落 .denied 审计,实得 %v", deps.audit)
	}
}

// TestMcpNodeScopeDenied 节点范围:限定 Key 调范围外节点 → 拒。
func TestMcpNodeScopeDenied(t *testing.T) {
	st2, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	deps := &fakeDeps{store: st2}
	srv := NewServer(deps)
	plain, hash, _ := GenerateKey()
	if _, err := st2.CreateMcpKey(context.Background(), "limited", hash, []string{ScopeExec}, `["n_allowed"]`); err != nil {
		t.Fatal(err)
	}
	w := call(srv, plain, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"exec_command","arguments":{"node_id":"n_forbidden","cmd":"id"}}}`)
	var resp struct {
		Result struct {
			IsError bool                    `json:"isError"`
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Result.IsError || !strings.Contains(resp.Result.Content[0].Text, "授权范围") {
		t.Fatalf("节点范围外应拒: %s", w.Body.String())
	}
}

// TestMcpJsonRpc JSON-RPC 协议面:initialize/tools list/未知方法/坏 JSON。
func TestMcpJsonRpc(t *testing.T) {
	srv, _, _, plain := newTest(t)

	// initialize
	w := call(srv, plain, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)
	if !strings.Contains(w.Body.String(), "moonshepherd") {
		t.Fatalf("initialize 应含 serverInfo: %s", w.Body.String())
	}
	// tools/list
	w = call(srv, plain, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if !strings.Contains(w.Body.String(), "exec_command") || !strings.Contains(w.Body.String(), "file_read") {
		t.Fatalf("tools/list 应含全部工具: %s", w.Body.String()[:200])
	}
	// 未知方法
	w = call(srv, plain, `{"jsonrpc":"2.0","id":3,"method":"resources/list"}`)
	if !strings.Contains(w.Body.String(), "-32601") {
		t.Fatalf("未知方法应 -32601: %s", w.Body.String())
	}
	// 坏 JSON
	if w := call(srv, plain, `{bad`); !strings.Contains(w.Body.String(), "-32700") {
		t.Fatalf("坏 JSON 应 -32700: %s", w.Body.String())
	}
}

// TestMcpAllowedCall 授权调用放行:read Key 调 list_nodes 成功。
func TestMcpAllowedCall(t *testing.T) {
	srv, _, deps, plain := newTest(t)
	w := call(srv, plain, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_nodes"}}`)
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result.IsError {
		t.Fatalf("read Key 调 list_nodes 应成功: %s", w.Body.String())
	}
	if len(deps.audit) == 0 || !strings.HasPrefix(deps.audit[0], "mcp.list_nodes") {
		t.Fatalf("应落 mcp.list_nodes 审计: %v", deps.audit)
	}
}

// TestGenerateKey 格式与确定性 hash。
func TestGenerateKey(t *testing.T) {
	plain, hash, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "mcp_") || len(plain) != 47 {
		t.Fatalf("格式异常: %s(len=%d)", plain, len(plain))
	}
	if hash != HashKey(plain) || HashKey("other") == hash {
		t.Fatal("hash 不稳定或碰撞")
	}
}

// TestAllowTargetTwoLevel 两级授权语义(docs/07 §3.2 细化):
// 勾节点 = 全部应用;仅勾应用 = 仅限这些;应用级只约束容器操作/日志。
func TestAllowTargetTwoLevel(t *testing.T) {
	// 新格式:允许 n_1 全部 + n_2 的 gitea
	ns := `{"mode":"custom","nodes":[{"id":"n_1"},{"id":"n_2","apps":["gitea"]}]}`
	cases := []struct {
		node, app string
		want      bool
	}{
		{"n_1", "anything", true},  // 节点全选
		{"n_1", "", true},          // 节点级操作
		{"n_2", "gitea", true},     // 应用在列
		{"n_2", "postgres", false}, // 应用不在列
		{"n_2", "", true},          // 节点在列(节点级操作如 exec/files)
		{"n_9", "", false},         // 节点不在范围
	}
	for _, c := range cases {
		if got := AllowTarget(ns, c.node, c.app); got != c.want {
			t.Errorf("AllowTarget(%s,%s,%s)=%v want %v", ns, c.node, c.app, got, c.want)
		}
	}
	// 旧格式(字符串数组)兼容:等价节点全选
	old := `["n_1","n_2"]`
	if !AllowTarget(old, "n_2", "postgres") {
		t.Error("旧格式应等价节点全选")
	}
	// all
	if !AllowTarget("all", "n_x", "any") {
		t.Error("all 应放行一切")
	}
}
