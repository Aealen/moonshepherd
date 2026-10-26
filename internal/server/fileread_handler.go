// 文件只读查看 REST(docs/04 §10):POST 发起读取,内容经 events WSS
// 以 file.read topic 转推浏览器(与 log.stream 同款转发形态,一问一答)。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// handleReadFile POST /nodes/{id}/file/read {path}
func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "需要 path")
		return
	}
	if !s.hub.online(nodeID) {
		writeErr(w, http.StatusConflict, "node_offline", "节点不在线")
		return
	}
	reqID := "fr_" + randFileID()
	if !s.hub.sendTo(nodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeFileRead, TS: time.Now().Unix(),
		Payload: protocol.FileReadPayload{RequestID: reqID, Path: body.Path}}) {
		writeErr(w, http.StatusConflict, "node_offline", "节点不在线")
		return
	}
	s.audit(r, "file.read", nodeID, map[string]string{"path": body.Path, "request_id": reqID})
	writeJSON(w, http.StatusOK, map[string]string{"request_id": reqID})
}

func randFileID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
