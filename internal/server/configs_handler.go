package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/maxonphong/herd/internal/protocol"
	"github.com/maxonphong/herd/internal/store"
)

// supportedAppTypes 已注册 validate/reload 的应用（docs/01 §6.2：未知应用不允许跳过校验下发）。
// 命令模板由 Agent 侧 executor 执行；面板只做注册表校验。
var supportedAppTypes = map[string]bool{"nginx": true, "caddy": true}

// handleListConfigs GET /nodes/{id}/configs：目标列表 + 当前生效版本。
func (s *Server) handleListConfigs(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if _, err := s.st.GetNode(r.Context(), nodeID); err != nil {
		writeErr(w, http.StatusNotFound, "node_not_found", "节点不存在")
		return
	}
	targets, err := s.st.ListConfigTargets(r.Context(), nodeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if targets == nil {
		targets = []store.ConfigTargetWithMeta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": targets})
}

// handleCreateConfig POST /nodes/{id}/configs：新建目标 + 首版 draft。
func (s *Server) handleCreateConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if _, err := s.st.GetNode(r.Context(), nodeID); err != nil {
		writeErr(w, http.StatusNotFound, "node_not_found", "节点不存在")
		return
	}
	var req struct {
		AppType    string `json:"app_type"`
		TargetPath string `json:"target_path"`
		Content    string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetPath == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "target_path 必填")
		return
	}
	if !supportedAppTypes[req.AppType] {
		writeErr(w, http.StatusBadRequest, "validation_failed", "app_type 未注册 validate/reload（当前支持 nginx/caddy）")
		return
	}
	if !isSafeTargetPath(req.TargetPath) {
		writeErr(w, http.StatusBadRequest, "validation_failed", "target_path 必须为绝对路径且不得包含 ..")
		return
	}
	targetID, versionID, err := s.st.CreateConfigTarget(r.Context(), &store.ConfigTarget{
		NodeID: nodeID, AppType: req.AppType, TargetPath: req.TargetPath,
	}, req.Content, sessionFrom(r).Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "config.create", nodeID, map[string]any{"target_id": targetID, "target_path": req.TargetPath})
	writeJSON(w, http.StatusCreated, map[string]any{"target_id": targetID, "version_id": versionID, "status": "draft"})
}

// handleGetVersion GET /configs/{versionID}。
func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	vid, err := parseVersionID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "version id 非法")
		return
	}
	v, err := s.st.GetConfigVersion(r.Context(), vid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "config_version_not_found", "版本不存在")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleEditVersion PUT /configs/{versionID}：仅 draft 可编辑 → 产生新 draft（不可变版本链）。
func (s *Server) handleEditVersion(w http.ResponseWriter, r *http.Request) {
	vid, err := parseVersionID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "version id 非法")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "请求体非法")
		return
	}
	v, err := s.st.GetConfigVersion(r.Context(), vid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "config_version_not_found", "版本不存在")
		return
	}
	if v.Status != "draft" {
		writeErr(w, http.StatusConflict, "conflict", "仅 draft 版本可编辑；请基于它创建新版本")
		return
	}
	newID, err := s.st.AppendDraftVersion(r.Context(), v.TargetID, req.Content, sessionFrom(r).Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.audit(r, "config.edit", strconv.FormatInt(vid, 10), map[string]any{"new_version_id": newID})
	writeJSON(w, http.StatusOK, map[string]any{"version_id": newID, "status": "draft"})
}

// handleApplyVersion POST /configs/{versionID}/apply：下发 = 创建 config.apply 指令。
func (s *Server) handleApplyVersion(w http.ResponseWriter, r *http.Request) {
	s.applyOrRollback(w, r, false)
}

// handleRollbackVersion POST /configs/{versionID}/rollback：回滚即下发（docs/01 §6.2）。
func (s *Server) handleRollbackVersion(w http.ResponseWriter, r *http.Request) {
	s.applyOrRollback(w, r, true)
}

func (s *Server) applyOrRollback(w http.ResponseWriter, r *http.Request, rollback bool) {
	vid, err := parseVersionID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "version id 非法")
		return
	}
	v, err := s.st.GetConfigVersion(r.Context(), vid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "config_version_not_found", "版本不存在")
		return
	}
	// 找目标取 node_id/app_type/target_path
	tgt, err := s.st.GetConfigTargetByID(r.Context(), v.TargetID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "config_target_not_found", "配置目标不存在")
		return
	}
	if rollback && v.Status != "applied" && v.Status != "rolled_back" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "仅历史生效版本可作为回滚源")
		return
	}
	cmd, err := s.createCommandFor(r, tgt.NodeID, protocol.CmdConfigApply, protocol.ConfigApplyPayload{
		AppType: tgt.AppType, TargetPath: tgt.TargetPath, Content: v.Content, VersionID: v.ID,
	}, 300)
	if err != nil {
		writeCmdErr(w, err)
		return
	}
	if rollback {
		_ = s.st.MarkVersionRolledBack(r.Context(), v.ID, cmd.ID, "")
	}
	action := "config.apply"
	if rollback {
		action = "config.rollback"
	}
	s.audit(r, action, tgt.NodeID, map[string]any{"version_id": v.ID, "target_path": tgt.TargetPath, "command_id": cmd.ID})
	writeJSON(w, http.StatusOK, map[string]any{"command_id": cmd.ID, "status": cmd.Status})
}

// handleListVersions GET /configs/versions?target_id=。
func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	targetID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("target_id")), 10, 64)
	if err != nil || targetID <= 0 {
		writeErr(w, http.StatusBadRequest, "validation_failed", "target_id 必填且为正整数")
		return
	}
	versions, err := s.st.ListConfigVersions(r.Context(), targetID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if versions == nil {
		versions = []store.ConfigVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": versions})
}

func parseVersionID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "versionID"), 10, 64)
}

// isSafeTargetPath 绝对路径 + 无 .. 遍历（docs/05 §3.3 危险操作约束）。
func isSafeTargetPath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
