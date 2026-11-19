package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/aealen/moonshepherd/internal/protocol"
	"github.com/aealen/moonshepherd/internal/store"
	"github.com/aealen/moonshepherd/version"
)

// handleListApps GET /apps：应用中心主查询（docs/06 §2.3）。
func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, pageSize := pageParams(r)
	apps, total, err := s.st.ListApps(r.Context(), store.AppFilter{
		NodeID: q.Get("node_id"), Probe: q.Get("probe"), AppType: q.Get("app_type"),
		Status: q.Get("status"), Q: q.Get("q"),
	}, page, pageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writePage(w, page, pageSize, total, apps)
}

// handleNodeApps GET /nodes/{id}/apps：单节点应用清单。
func (s *Server) handleNodeApps(w http.ResponseWriter, r *http.Request) {
	s.handleListApps(w, withNodeID(r, routeParam(r, "id")))
}

// handleAuditLogs GET /audit-logs：只读审计（docs/06 §2.6）。
func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	page, pageSize := pageParams(r)
	logs, total, err := s.st.ListAuditLogs(r.Context(), page, pageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writePage(w, page, pageSize, total, logs)
}

// handleSystemInfo GET /system/info。
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.st.GetSystemInfo(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	info.PanelVersion = version.Version
	writeJSON(w, http.StatusOK, info)
}

// ---------- 浏览器实时事件通道（docs/06 §3） ----------

var eventsUpgrader = websocket.Upgrader{ReadBufferSize: 4 << 10, WriteBufferSize: 4 << 10}

type eventSub struct {
	NodeIDs []string `json:"node_ids"` // ["*"] 或节点列表
	Topics  []string `json:"topics"`   // 空 = 全部
}

// wsClientMsg 浏览器经 events WSS 上行的客户端消息(docs/04 §8:
// 终端输入/窗口尺寸;其余上行消息保留探活语义,未知类型忽略)。
type wsClientMsg struct {
	Type      string `json:"type"` // session.input / session.resize
	SessionID string `json:"session_id,omitempty"`
	DataB64   string `json:"data_b64,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Cols      int    `json:"cols,omitempty"`
}

// handleEvents GET /api/v1/events：订阅模型 + 按 sub 过滤推送。
// WS 只做增量提示，页面数据以 REST 为源（docs/06 §3）。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.isSecureRequest(r) && !s.opt.Dev {
		host := clientIP(r)
		if host != "127.0.0.1" && host != "::1" && host != "[::1]" {
			writeErr(w, http.StatusUpgradeRequired, "tls_required", "事件通道要求 TLS")
			return
		}
	}
	conn, err := eventsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 读第一条：订阅声明
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var sub eventSub
	if err := conn.ReadJSON(&sub); err != nil {
		return
	}
	wantsAllNodes := len(sub.NodeIDs) == 0
	for _, id := range sub.NodeIDs {
		if id == "*" {
			wantsAllNodes = true
		}
	}
	wantsAllTopics := len(sub.Topics) == 0
	topicSet := map[string]bool{}
	for _, t := range sub.Topics {
		topicSet[t] = true
	}
	nodeSet := map[string]bool{}
	for _, id := range sub.NodeIDs {
		nodeSet[id] = true
	}

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	// 保活:20s 服务端 WS ping(gateway 对 Agent 连接同款)——反代将 ping 帧视为
	// 活动字节,防空闲超时掐断长连(浏览器 events 通道此前无保活,公网反代
	// proxy_read_timeout 60s 必掐,终端输入/输出随之断流);读侧 60s deadline
	// 由 PongHandler 刷新,死连接及时清理。
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	keepaliveDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return // 写失败即连接死,读循环与写循环自行退出
				}
			case <-keepaliveDone:
				return
			}
		}
	}()

	// 读循环:探活/关闭检测 + 终端上行消息(input/resize 会话归属校验后转发;
	// 未知类型忽略——客户端 25s 应用层心跳 ping 也走这里维持字节流)
	sess := sessionFrom(r)
	done := make(chan struct{})
	go func() {
		defer func() {
			close(done)
			close(keepaliveDone)
		}()
		conn.SetReadLimit(protocol.MaxPayloadBytes + 4096)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg wsClientMsg
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			// 客户端心跳/终端消息都视为活动字节,刷新读 deadline
			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			switch msg.Type {
			case "session.input":
				s.terms.ForwardInput(context.Background(), s.hub, s.st,
					msg.SessionID, msg.DataB64, sess.UserID, sess.Username, clientIP(r))
			case "session.resize":
				s.terms.ForwardResize(s.hub, msg.SessionID, msg.Rows, msg.Cols, sess.UserID)
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case ev := <-ch:
			if !wantsAllTopics && !topicSet[ev.Topic] {
				continue
			}
			if !wantsAllNodes && ev.NodeID != "" && !nodeSet[ev.NodeID] {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(ev); err != nil {
				return
			}
		}
	}
}

// withNodeID 把路径参数注入查询参数（node_id），复用 ListApps 过滤。
func withNodeID(r *http.Request, nodeID string) *http.Request {
	q := r.URL.Query()
	q.Set("node_id", nodeID)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	return r2
}

func routeParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}
