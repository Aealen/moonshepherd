package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aealen/moonshepherd/internal/protocol"
	"github.com/aealen/moonshepherd/internal/store"
)

// handleListNodes GET /nodes（docs/06 §2.2）。
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, pageSize := pageParams(r)
	nodes, total, err := s.st.ListNodes(r.Context(), store.NodeFilter{
		Status: q.Get("status"), Tag: q.Get("tag"), Q: q.Get("q"),
	}, page, pageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	online, offline, _ := s.st.CountNodesByStatus(r.Context())
	// 附加实时在线状态（DB 状态由网关维护，这里仅透传）
	w.Header().Set("X-Total-Online", strconv.Itoa(online))
	w.Header().Set("X-Total-Offline", strconv.Itoa(offline))
	writePage(w, page, pageSize, total, nodes)
}

// handleGetNode GET /nodes/{id}：详情含最近快照与统计。
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	node, err := s.st.GetNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "node_not_found", "节点不存在")
		return
	}
	metric, _, _ := s.st.LatestMetric(r.Context(), node.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"node":          node,
		"online":        s.hub.online(node.ID),
		"latest_metric": metric,
	})
}

// handlePatchNode PATCH /nodes/{id}：改名/标签。
func (s *Server) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string   `json:"name"`
		Tags *[]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Name == nil && req.Tags == nil) {
		writeErr(w, http.StatusBadRequest, "validation_failed", "name 或 tags 至少一项")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.st.GetNode(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, "node_not_found", "节点不存在")
		return
	}
	if err := s.st.UpdateNodeMeta(r.Context(), id, req.Name, deref(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "node.update", id, req)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func deref[T any](p *[]T) []T {
	if p == nil {
		return nil
	}
	return *p
}

// handleDisableNode POST /nodes/{id}/disable：断开连接 + 凭据失效。
func (s *Server) handleDisableNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.st.SetNodeEnabled(r.Context(), id, false); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 已有活跃连接按 4002 踢掉（docs/04 §2.4）
	s.hub.mu.Lock()
	c := s.hub.conns[id]
	s.hub.mu.Unlock()
	if c != nil {
		c.once.Do(func() { close(c.close) })
	}
	s.audit(r, "node.disable", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleEnableNode POST /nodes/{id}/enable。
func (s *Server) handleEnableNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.st.SetNodeEnabled(r.Context(), id, true); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "node.enable", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteNode DELETE /nodes/{id}?confirm=<节点名>（防误删，docs/06 §2.2）。
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, err := s.st.GetNode(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "node_not_found", "节点不存在")
		return
	}
	if r.URL.Query().Get("confirm") != node.Name {
		writeErr(w, http.StatusBadRequest, "validation_failed", "需携带 confirm=<节点名> 确认删除")
		return
	}
	s.hub.mu.Lock()
	c := s.hub.conns[id]
	s.hub.mu.Unlock()
	if c != nil {
		c.once.Do(func() { close(c.close) })
	}
	if err := s.st.DeleteNode(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.hub.publish("node.status", id, map[string]string{"status": "deleted"})
	s.audit(r, "node.delete", id, map[string]string{"name": node.Name})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleProbeNode POST /nodes/{id}/probe：触发即时探测。
func (s *Server) handleProbeNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProbeNames []string `json:"probe_names"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	cmd, err := s.createCommandFor(r, chi.URLParam(r, "id"), protocol.CmdProbe,
		protocol.ProbePayload{ProbeNames: req.ProbeNames}, 120, "")
	if err != nil {
		writeCmdErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"command_id": cmd.ID, "status": cmd.Status})
}

// ---------- 注册 Token ----------

// handleCreateToken POST /nodes/registration-tokens：明文仅返回一次。
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note         string `json:"note"`
		ExpiresHours int    `json:"expires_hours"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ttl := 24 * time.Hour
	if req.ExpiresHours > 0 {
		ttl = time.Duration(req.ExpiresHours) * time.Hour
	}
	t, plaintext, err := s.st.NewRegistrationToken(r.Context(), req.Note, ttl)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "token.create", strconv.FormatInt(t.ID, 10), map[string]string{"note": req.Note})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": t.ID, "token": plaintext, "expires_at": t.ExpiresAt, "note": t.Note,
	})
}

// handleListTokens GET /nodes/registration-tokens（不含明文）。
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.st.ListRegistrationTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if tokens == nil {
		tokens = []store.RegistrationToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": tokens})
}

// handleRevokeToken DELETE /nodes/registration-tokens/{id}。
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "id 非法")
		return
	}
	if err := s.st.RevokeRegistrationToken(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "token.revoke", strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- 指令 ----------

// validateCommandReq 单发/批量共用的指令校验(type 白名单 + payload 结构)。
func validateCommandReq(cmdType string, raw json.RawMessage, timeoutS int) error {
	switch cmdType {
	case protocol.CmdExec, protocol.CmdProbe, protocol.CmdConfigApply, protocol.CmdDockerOp:
	default:
		return errBadRequest("不支持的指令类型")
	}
	if timeoutS > 600 {
		return errBadRequest("超时上限 600s（docs/04 §5.3）")
	}
	switch cmdType {
	case protocol.CmdExec:
		var p protocol.ExecPayload
		if json.Unmarshal(raw, &p) != nil || p.Cmd == "" {
			return errBadRequest("exec 指令需要 payload.cmd")
		}
	case protocol.CmdDockerOp:
		var p protocol.DockerOpPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return errBadRequest("docker.op payload 非法")
		}
		allowed := false
		for _, op := range protocol.DockerOpsAllowed {
			if p.Op == op {
				allowed = true
				break
			}
		}
		if !allowed {
			return errBadRequest("op 必须是 " + strings.Join(protocol.DockerOpsAllowed, "/"))
		}
		if p.Container == "" && !(p.Op == protocol.DockerOpPull && p.Image != "") {
			return errBadRequest("缺少 container")
		}
	}
	return nil
}

// handleCreateCommand POST /nodes/{id}/commands（docs/06 §2.4）。
func (s *Server) handleCreateCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type     string          `json:"type"`
		Payload  json.RawMessage `json:"payload"`
		TimeoutS int             `json:"timeout_s"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "请求体非法")
		return
	}
	if err := validateCommandReq(req.Type, req.Payload, req.TimeoutS); err != nil {
		writeCmdErr(w, err)
		return
	}
	cmd, err := s.createCommandFor(r, chi.URLParam(r, "id"), req.Type, json.RawMessage(req.Payload), req.TimeoutS, "")
	if err != nil {
		writeCmdErr(w, err)
		return
	}
	s.audit(r, "command.create", cmd.ID, map[string]string{"type": req.Type})
	writeJSON(w, http.StatusOK, map[string]any{"command_id": cmd.ID, "status": cmd.Status})
}

// handleCreateCommandBatch POST /commands/batch：同一命令批量下发多节点。
// 拆为 N 条独立指令(共享 parent_id)走既有链路——每节点独立审计/超时/结果；
// exec 对离线节点拒投递,计入 skipped,不阻塞其余节点。
func (s *Server) handleCreateCommandBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeIDs  []string        `json:"node_ids"`
		Type     string          `json:"type"`
		Payload  json.RawMessage `json:"payload"`
		TimeoutS int             `json:"timeout_s"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "请求体非法")
		return
	}
	if len(req.NodeIDs) == 0 || len(req.NodeIDs) > 50 {
		writeErr(w, http.StatusBadRequest, "validation_failed", "node_ids 需 1~50 个")
		return
	}
	if err := validateCommandReq(req.Type, req.Payload, req.TimeoutS); err != nil {
		writeCmdErr(w, err)
		return
	}
	// 去重保持顺序
	seen := map[string]bool{}
	ids := req.NodeIDs[:0]
	for _, id := range req.NodeIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	batchID := store.NewCommandID()
	created := make([]map[string]any, 0, len(ids))
	skipped := make([]map[string]any, 0)
	for _, nodeID := range ids {
		cmd, err := s.createCommandFor(r, nodeID, req.Type, json.RawMessage(req.Payload), req.TimeoutS, batchID)
		if err != nil {
			skipped = append(skipped, map[string]any{"node_id": nodeID, "reason": err.Error()})
			continue
		}
		s.audit(r, "command.create", cmd.ID, map[string]string{"type": req.Type, "batch": batchID})
		created = append(created, map[string]any{"command_id": cmd.ID, "node_id": nodeID, "status": cmd.Status})
	}
	s.audit(r, "command.batch", batchID, map[string]any{
		"type": req.Type, "requested": len(ids), "created": len(created), "skipped": len(skipped)})
	writeJSON(w, http.StatusOK, map[string]any{
		"batch_id": batchID, "created": created, "skipped": skipped,
	})
}

// createCommandFor 建指令 + 在线即投递（节点 offline 时 exec 拒绝，docs/06 §2.4）。
// parentID 非空表示批量组子指令(共享批次 ID,单发传空串)。
func (s *Server) createCommandFor(r *http.Request, nodeID, cmdType string, payload any, timeoutS int, parentID string) (*store.Command, error) {
	ctx := r.Context()
	node, err := s.st.GetNode(ctx, nodeID)
	if err != nil {
		return nil, errNotFound("节点不存在")
	}
	if !node.Enabled {
		return nil, errBadRequest("节点已禁用")
	}
	online := s.hub.online(nodeID)
	if !online && cmdType == protocol.CmdExec {
		return nil, errConflict("节点离线，交互式命令拒绝创建（probe/config.apply 可排队）")
	}
	raw, _ := json.Marshal(payload)
	cmd := &store.Command{ID: store.NewCommandID(), NodeID: nodeID, ParentID: parentID, Type: cmdType,
		Payload: raw, TimeoutS: timeoutS, CreatedBy: sessionFrom(r).Username}
	if err := s.st.CreateCommand(ctx, cmd); err != nil {
		return nil, errInternal(err)
	}
	if online && s.hub.sendTo(nodeID, commandEnvelope(cmd)) {
		_ = s.st.MarkCommandSent(ctx, cmd.ID)
		cmd.Status = "sent"
	}
	return cmd, nil
}

// handleListCommands GET /commands。
func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, pageSize := pageParams(r)
	cmds, total, err := s.st.ListCommands(r.Context(), store.CommandFilter{
		NodeID: q.Get("node_id"), ParentID: q.Get("parent"), Type: q.Get("type"), Status: q.Get("status"),
	}, page, pageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writePage(w, page, pageSize, total, cmds)
}

// handleBatchStats GET /commands/batch/{id}/stats：批量组各状态计数(前端聚合条)。
func (s *Server) handleBatchStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.st.BatchCommandStats(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleGetCommand GET /commands/{id}：详情含输出正文与 stages。
func (s *Server) handleGetCommand(w http.ResponseWriter, r *http.Request) {
	cmd, err := s.st.GetCommand(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "command_not_found", "指令不存在")
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

// handleRetryCommand POST /commands/{id}/retry：仅 lost/failed，生成新 command_id（docs/06 §2.4）。
func (s *Server) handleRetryCommand(w http.ResponseWriter, r *http.Request) {
	old, err := s.st.GetCommand(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "command_not_found", "指令不存在")
		return
	}
	if old.Status != "lost" && old.Status != "failed" {
		writeErr(w, http.StatusConflict, "conflict", "仅 lost/failed 指令可重发")
		return
	}
	cmd := &store.Command{ID: store.NewCommandID(), NodeID: old.NodeID, ParentID: old.ParentID, Type: old.Type,
		Payload: old.Payload, TimeoutS: old.TimeoutS, CreatedBy: sessionFrom(r).Username}
	if err := s.st.CreateCommand(r.Context(), cmd); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if s.hub.online(old.NodeID) && s.hub.sendTo(old.NodeID, commandEnvelope(cmd)) {
		_ = s.st.MarkCommandSent(r.Context(), cmd.ID)
		cmd.Status = "sent"
	}
	s.audit(r, "command.retry", old.ID, map[string]string{"new_command_id": cmd.ID})
	writeJSON(w, http.StatusOK, map[string]any{"command_id": cmd.ID, "status": cmd.Status})
}

// ---------- 辅助 ----------

func pageParams(r *http.Request) (page, pageSize int) {
	page, pageSize = 1, 20
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && v > 0 && v <= 100 {
		pageSize = v
	}
	return
}

type httpError struct {
	status int
	code   string
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errNotFound(msg string) error   { return &httpError{404, "not_found", msg} }
func errBadRequest(msg string) error { return &httpError{400, "validation_failed", msg} }
func errConflict(msg string) error   { return &httpError{409, "conflict", msg} }
func errInternal(err error) error    { return &httpError{500, "internal", err.Error()} }

func writeCmdErr(w http.ResponseWriter, err error) {
	if he, ok := err.(*httpError); ok {
		writeErr(w, he.status, he.code, he.msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal", err.Error())
}

func (s *Server) audit(r *http.Request, action, target string, detail any) {
	sess := sessionFrom(r)
	d := ""
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			d = string(b)
		}
	}
	s.st.AddAudit(r.Context(), sess.UserID, sess.Username, action, target, d, clientIP(r))
}

// handleNetSeries GET /metrics/net-series?minutes=60：牧群网络吞吐时序（跨节点聚合，30s 桶）。
func (s *Server) handleNetSeries(w http.ResponseWriter, r *http.Request) {
	minutes := 60
	if v := r.URL.Query().Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*60 {
			minutes = n
		}
	}
	pts, err := s.st.NetSeries(r.Context(), int64(minutes)*60, 30)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": pts, "bucket": 30})
}
