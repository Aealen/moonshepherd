// 文件管理器面板侧(docs/04 §11):
//   POST /nodes/{id}/file/op           转发 file.op(list/write/mkdir/delete/move/copy/rename)
//   POST /nodes/{id}/file/upload       multipart 上传 → 切分片经 WSS 转发 Agent → done 校验
// 结果经 events WSS topic=file.op 按 request_id/session_id 转推浏览器;
// 全部变更类操作落审计(内容/文件树不落库)。
package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// handleFileOp POST /nodes/{id}/file/op {op,path,dest,content,recursive}
func (s *Server) handleFileOp(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	var body protocol.FileOpPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Op == "" || body.Path == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "需要 op 与 path")
		return
	}
	if !s.hub.online(nodeID) {
		writeErr(w, http.StatusConflict, "node_offline", "节点不在线")
		return
	}
	body.RequestID = "fo_" + randFileID()
	if !s.hub.sendTo(nodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeFileOp, TS: time.Now().Unix(), Payload: body}) {
		writeErr(w, http.StatusConflict, "node_offline", "节点不在线")
		return
	}
	if body.Op != "list" { // 变更类落审计(list 是浏览,不记)
		s.audit(r, "file."+body.Op, nodeID, map[string]string{"path": body.Path, "dest": body.Dest})
	}
	writeJSON(w, http.StatusOK, map[string]string{"request_id": body.RequestID})
}

// handleFileUpload POST /nodes/{id}/file/upload?path=/abs/dir/name (multipart file)
// 浏览器单请求 → 面板流式切 240KB 分片转发 → sha256 → done。全程内存零拷盘。
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	target := r.URL.Query().Get("path")
	if target == "" || !strings.HasPrefix(target, "/") || strings.Contains(target, "..") {
		writeErr(w, http.StatusBadRequest, "validation_failed", "path 须为绝对目标文件路径")
		return
	}
	if !s.hub.online(nodeID) {
		writeErr(w, http.StatusConflict, "node_offline", "节点不在线")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "multipart 非法")
		return
	}
	// 找到第一个 file part
	var src io.Reader
	for {
		part, err := mr.NextPart()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "validation_failed", "缺少文件字段")
			return
		}
		if part.FormName() == "file" {
			src = part
			break
		}
	}
	sessionID := "fu_" + randFileID()
	h := sha256.New()
	buf := make([]byte, protocol.FileUploadChunkBytes)
	seq := 0
	var total int64
	send := func(payload any) bool {
		return s.hub.sendTo(nodeID, &protocol.Envelope{ID: newMsgID(), Type: payloadType(payload), TS: time.Now().Unix(), Payload: payload})
	}
	for {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			total += int64(n)
			if total > protocol.MaxFileUploadBytes {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large",
					"超过 256MB 上限")
				return
			}
			h.Write(buf[:n])
			chunk := protocol.FileUploadChunkPayload{SessionID: sessionID, Seq: seq, Data: base64.StdEncoding.EncodeToString(buf[:n])}
			if seq == 0 {
				chunk.Path = filepath.Clean(target)
			}
			if !send(chunk) {
				writeErr(w, http.StatusConflict, "node_offline", "节点连接中断")
				return
			}
			seq++
		}
		if rerr != nil {
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			writeErr(w, http.StatusBadRequest, "read_error", rerr.Error())
			return
		}
	}
	if !send(protocol.FileUploadDonePayload{
		SessionID: sessionID, Path: filepath.Clean(target),
		SHA256: hex.EncodeToString(h.Sum(nil)), Size: total,
	}) {
		writeErr(w, http.StatusConflict, "node_offline", "节点连接中断")
		return
	}
	s.audit(r, "file.upload", nodeID, map[string]string{"path": target, "size": fmtI64(total)})
	writeJSON(w, http.StatusOK, map[string]string{"session_id": sessionID, "size": fmtI64(total)})
}

func payloadType(p any) string {
	switch p.(type) {
	case protocol.FileUploadChunkPayload:
		return protocol.TypeFileUploadChunk
	case protocol.FileUploadDonePayload:
		return protocol.TypeFileUploadDone
	}
	return protocol.TypeFileUploadChunk
}

func fmtI64(n int64) string { return jsonNumber(n) }

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
