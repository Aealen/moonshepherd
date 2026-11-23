// MCP 指令桥接回归:createCommandFor 的哑请求没有登录 session,
// 曾因 sessionFrom(r).Username 直接解引用 panic,表现为 MCP 的
// exec_command/container_op 全部 502(Caddy 上游 abrupt close)。
package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// TestCreateCommandWithoutSession 无 session 的请求创建指令不得 panic,
// 且 CreatedBy 回退为 mcp(MCP 侧身份,精确审计在 mcp 层)。
func TestCreateCommandWithoutSession(t *testing.T) {
	srv, st := newTestServer(t)

	// 造一个 enabled 节点(离线也可排队 probe,流程会走到指令落库)
	now := time.Now().Unix()
	if _, err := st.DB.Exec(`INSERT INTO nodes (id,name,agent_version,proto_version,os_name,os_version,os_arch,hostname,status,enabled,last_seen,tags,capabilities,credential_enc,created_at)
		VALUES ('n_mcp_test','mcp-test','','','linux','','amd64','h','offline',1,?,'[]','["exec"]','x',?)`, now, now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/mcp-internal", nil) // 无 ctxSession
	cmd, err := srv.createCommandFor(req, "n_mcp_test", protocol.CmdProbe, map[string]any{}, 10, "")
	if err != nil {
		t.Fatalf("createCommandFor: %v", err)
	}
	if cmd == nil || cmd.ID == "" {
		t.Fatal("expect command created")
	}
	if cmd.CreatedBy != "mcp" {
		t.Fatalf("expect CreatedBy=mcp, got %q", cmd.CreatedBy)
	}
}
