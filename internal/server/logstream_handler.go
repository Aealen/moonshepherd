// 容器日志流 REST(docs/04 §9):POST 开流 / DELETE 停流;数据面走 events WSS。
package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleOpenLogStream POST /nodes/{id}/logstream {container, tail_lines}
func (s *Server) handleOpenLogStream(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	nodeID := chi.URLParam(r, "id")
	var body struct {
		Container string `json:"container"`
		TailLines int    `json:"tail_lines"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Container == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "需要 container")
		return
	}
	if body.TailLines <= 0 || body.TailLines > 1000 {
		body.TailLines = 100
	}
	sid, err := s.logstream.Start(r.Context(), s.hub, s.st, nodeID, sess.UserID, sess.Username, clientIP(r), body.Container, body.TailLines)
	if err != nil {
		writeErr(w, http.StatusConflict, "logstream_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stream_id": sid})
}

// handleCloseLogStream DELETE /nodes/{id}/logstream/{sid}(仅开流人)
func (s *Server) handleCloseLogStream(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	sid := chi.URLParam(r, "sid")
	if !s.logstream.Stop(r.Context(), s.hub, s.st, sid, sess.UserID, clientIP(r)) {
		writeErr(w, http.StatusNotFound, "not_found", "日志流不存在或非本人开启")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}
