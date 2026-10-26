// 终端会话 REST:打开/关闭(docs/04 §8;输入输出经 events WSS 双向通道)。
package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aealen/moonshepherd/internal/protocol"
	"github.com/aealen/moonshepherd/internal/store"
)

// handleOpenTerminal POST /nodes/{id}/sessions {rows, cols}
// 校验节点在线且声明 pty 能力(旧 Agent 优雅降级),同步等 Agent open_ack。
func (s *Server) handleOpenTerminal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	nodeID := chi.URLParam(r, "id")

	var body struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Rows <= 0 || body.Rows > 200 || body.Cols <= 0 || body.Cols > 500 {
		body.Rows, body.Cols = 24, 80
	}

	ctx := r.Context()
	node, err := s.st.GetNode(ctx, nodeID)
	if err != nil || node == nil {
		writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
		return
	}
	if !s.hub.online(nodeID) {
		writeErr(w, http.StatusConflict, "node_offline", "节点不在线")
		return
	}
	if !nodeHasCapability(node, protocol.CapPty) {
		writeErr(w, http.StatusConflict, "pty_unsupported", "节点 Agent 不支持终端(需升级到支持 pty 的版本)")
		return
	}

	term, err := s.terms.Open(ctx, s.hub, s.st, nodeID, sess.UserID, sess.Username, clientIP(r), body.Rows, body.Cols)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "open_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": term.ID})
}

// handleCloseTerminal DELETE /nodes/{id}/sessions/{sid}(仅会话所有者)。
func (s *Server) handleCloseTerminal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	sid := chi.URLParam(r, "sid")
	if !s.terms.Close(r.Context(), s.hub, s.st, sid, sess.UserID, sess.Username, clientIP(r)) {
		writeErr(w, http.StatusNotFound, "not_found", "会话不存在或非本人开启")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}

func nodeHasCapability(node *store.Node, cap string) bool {
	for _, c := range node.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}
