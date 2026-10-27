// Agent 自更新触发(docs/01 §3.5 一期:手动触发)。
// POST /nodes/{id}/update:面板计算同源下载 URL 与 sha256,下发 agent.update;
// 成败以 Agent 重连后的版本号为准(更新流程会终止旧进程,原指令可能 lost——预期语义)。
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/aealen/moonshepherd/internal/protocol"
	"github.com/aealen/moonshepherd/version"
)

// handleUpdateAgent POST /nodes/{id}/update
func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	node, err := s.st.GetNode(r.Context(), nodeID)
	if err != nil || node == nil {
		writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
		return
	}
	if !s.hub.online(nodeID) {
		writeErr(w, http.StatusConflict, "node_offline", "节点不在线")
		return
	}
	if node.AgentVersion == version.Version {
		writeErr(w, http.StatusConflict, "same_version", "节点已是面板版本 "+version.Version)
		return
	}
	// 按节点架构选二进制;非 linux 架构拒绝(自更新仅 Linux)
	arch := node.OSArch
	if arch != "amd64" && arch != "arm64" && arch != "armv7" && arch != "arm" {
		writeErr(w, http.StatusConflict, "arch_unsupported", "不支持架构: "+arch)
		return
	}
	if arch == "arm" {
		arch = "armv7"
	}
	binPath := filepath.Join(s.opt.DistDir, "herd-linux-"+arch)
	data, err := os.ReadFile(binPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no_binary",
			"面板未内嵌该架构二进制;Docker 部署自带,源码部署请 make distro-bin")
		return
	}
	sum := sha256.Sum256(data)

	// 同源下载 URL:复用请求的 scheme/host(反代场景即公网地址)
	scheme := "https"
	if !s.isSecureRequest(r) {
		scheme = "http"
	}
	downloadURL := scheme + "://" + r.Host + "/dist/herd-linux-" + arch

	cmd, err := s.createCommandFor(r, nodeID, protocol.CmdAgentUpdate, protocol.AgentUpdatePayload{
		Version:     version.Version,
		DownloadURL: downloadURL,
		SHA256:      hex.EncodeToString(sum[:]),
	}, 600)
	if err != nil {
		writeCmdErr(w, err)
		return
	}
	s.audit(r, "agent.update", nodeID, map[string]string{
		"from": node.AgentVersion, "to": version.Version, "command_id": cmd.ID,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"command_id": cmd.ID, "from": node.AgentVersion, "to": version.Version,
		"note": "更新中连接会中断;以节点重连后的版本号为准",
	})
}
