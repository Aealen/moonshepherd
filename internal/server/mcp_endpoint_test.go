// MCP 端点回归:New() 必须构造 mcpSrv,否则 POST /mcp 触发 nil pointer panic
// (线上曾表现为 "Empty reply from server")。
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMcpEndpointMounted /mcp 已挂载且无认证请求返回 401 JSON-RPC 错误(非 panic)。
func TestMcpEndpointMounted(t *testing.T) {
	srv, _ := newTestServer(t)
	if srv.mcpSrv == nil {
		t.Fatal("mcpSrv 未构造:New() 漏调 mcp.NewServer(PanelDeps)")
	}

	// 无 Authorization → 401(限速未触发时)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expect 401/429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "jsonrpc") {
		t.Fatalf("expect JSON-RPC error body, got %s", rec.Body.String())
	}
}

// TestMcpEndpointRejectsGet GET /mcp → 405(方法限制,同样不应 panic)。
func TestMcpEndpointRejectsGet(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expect 405, got %d", rec.Code)
	}
}
