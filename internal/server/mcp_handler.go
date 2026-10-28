// MCP Keys 管理 REST(docs/07 §6;管理员 Cookie 认证)+ /mcp 路由挂载。
package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aealen/moonshepherd/internal/mcp"
)

const maxMcpKeys = 10

// handleMcpListKeys GET /api/v1/mcp/keys
func (s *Server) handleMcpListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.st.ListMcpKeys(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": keys})
}

// handleMcpCreateKey POST /api/v1/mcp/keys {name, scopes, node_scope}
// 明文 Key 仅本响应出现一次(no-store)。
func (s *Server) handleMcpCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		NodeScope string   `json:"node_scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "name 必填")
		return
	}
	if !mcp.ValidScopes(req.Scopes) {
		writeErr(w, http.StatusBadRequest, "validation_failed", "scopes 含非法值")
		return
	}
	if !mcp.ValidNodeScope(req.NodeScope) {
		writeErr(w, http.StatusBadRequest, "validation_failed", "node_scope 须为 all 或节点 ID JSON 数组")
		return
	}
	if n, _ := s.st.CountMcpKeys(r.Context()); n >= maxMcpKeys {
		writeErr(w, http.StatusConflict, "limit", "Key 数量已达上限 10")
		return
	}
	plain, hash, err := mcp.GenerateKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	k, err := s.st.CreateMcpKey(r.Context(), req.Name, hash, req.Scopes, req.NodeScope)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "mcp.key.create", strconv.FormatInt(k.ID, 10), map[string]any{"name": req.Name, "scopes": req.Scopes})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": k.ID, "name": k.Name, "scopes": k.Scopes, "node_scope": k.NodeScope,
		"key": plain, // 一次性明文
	})
}

// handleMcpUpdateKey PATCH /api/v1/mcp/keys/{id}(key_hash 不可改)
func (s *Server) handleMcpUpdateKey(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req struct {
		Name      *string   `json:"name"`
		Scopes    *[]string `json:"scopes"`
		NodeScope *string   `json:"node_scope"`
		Enabled   *bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "请求体非法")
		return
	}
	if req.Scopes != nil && !mcp.ValidScopes(*req.Scopes) {
		writeErr(w, http.StatusBadRequest, "validation_failed", "scopes 含非法值")
		return
	}
	if req.NodeScope != nil && !mcp.ValidNodeScope(*req.NodeScope) {
		writeErr(w, http.StatusBadRequest, "validation_failed", "node_scope 非法")
		return
	}
	if err := s.st.UpdateMcpKey(r.Context(), id, req.Name, req.Scopes, req.NodeScope, req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "mcp.key.update", strconv.FormatInt(id, 10), req)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMcpDeleteKey DELETE /api/v1/mcp/keys/{id}(吊销,审计保留)
func (s *Server) handleMcpDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.st.DeleteMcpKey(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "mcp.key.delete", strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
