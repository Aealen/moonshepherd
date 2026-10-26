// 面板侧容器日志流管理(docs/04 §9):REST 开关 + chunk 经 events WSS 转推浏览器。
// 与终端会话(panelSessions)同款转发形态;审计只记开关(日志内容不落库)。
package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// panelLogStreams 面板侧活跃日志流。
type panelLogStreams struct {
	mu      sync.Mutex
	streams map[string]*logStreamView // stream_id → view
}

// logStreamView 单条日志流的面板侧视图(归属校验用)。
type logStreamView struct {
	NodeID   string
	UserID   int64
	Username string
}

func newPanelLogStreams() *panelLogStreams {
	return &panelLogStreams{streams: map[string]*logStreamView{}}
}

// Start 开流:登记视图 → 下发 log.stream.start。受理即返回
// (Agent 无 per-stream ack;容器不存在时其跟随器即刻终止并上报 stop,
// 浏览器经 session 同款转推收到 end 提示)。
func (ps *panelLogStreams) Start(ctx context.Context, h *agentHub, st auditStore,
	nodeID string, uid int64, username, ip, container string, tailLines int) (string, error) {
	if !h.online(nodeID) {
		return "", fmt.Errorf("节点不在线")
	}
	sid := "ls_" + randHexID()
	if !h.sendTo(nodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeLogStreamStart, TS: time.Now().Unix(),
		Payload: protocol.LogStreamStartPayload{StreamID: sid, Container: container, TailLines: tailLines}}) {
		return "", fmt.Errorf("节点不在线")
	}
	ps.mu.Lock()
	ps.streams[sid] = &logStreamView{NodeID: nodeID, UserID: uid, Username: username}
	ps.mu.Unlock()
	st.AddAudit(ctx, uid, username, "log.stream.open", nodeID,
		`{"stream_id":"`+sid+`","container":`+jsonString(container)+`}`, ip)
	return sid, nil
}

// isMine 会话归属校验(输入/停止用)。
func (ps *panelLogStreams) isMine(streamID string, uid int64) (*logStreamView, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	v := ps.streams[streamID]
	return v, v != nil && v.UserID == uid
}

// Stop 用户停止:摘除 + 下发 stop + 审计。Agent 侧 stop(任意 reason)到达走 OnStop。
func (ps *panelLogStreams) Stop(ctx context.Context, h *agentHub, st auditStore, streamID string, uid int64, ip string) bool {
	ps.mu.Lock()
	v := ps.streams[streamID]
	delete(ps.streams, streamID)
	ps.mu.Unlock()
	if v == nil || v.UserID != uid {
		return false
	}
	h.sendTo(v.NodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeLogStreamStop, TS: time.Now().Unix(),
		Payload: protocol.LogStreamStopPayload{StreamID: streamID, Reason: "user_close"}})
	st.AddAudit(ctx, uid, v.Username, "log.stream.close", v.NodeID,
		`{"stream_id":"`+streamID+`","reason":"user_close"}`, ip)
	return true
}

// OnChunk Agent 日志增量 → 按节点转推浏览器(订阅过滤由 hub 做)。
func (ps *panelLogStreams) OnChunk(h *agentHub, nodeID string, payload protocol.LogStreamChunkPayload) {
	h.publish("log.stream", nodeID, payload)
}

// OnStop Agent 侧终结(容器退出/超时/连接断开/超限):摘除 + 通知浏览器 + 审计。
func (ps *panelLogStreams) OnStop(ctx context.Context, h *agentHub, st auditStore, nodeID string, payload protocol.LogStreamStopPayload) {
	ps.mu.Lock()
	v := ps.streams[payload.StreamID]
	delete(ps.streams, payload.StreamID)
	ps.mu.Unlock()
	if v == nil {
		return
	}
	st.AddAudit(ctx, v.UserID, v.Username, "log.stream.close", nodeID,
		`{"stream_id":"`+payload.StreamID+`","reason":"`+payload.Reason+`"}`, "")
	h.publish("log.stream", nodeID, payload)
}

// NodeOffline 节点断连:全部日志流终结。
func (ps *panelLogStreams) NodeOffline(ctx context.Context, h *agentHub, st auditStore, nodeID string) {
	ps.mu.Lock()
	doomed := make([]*logStreamView, 0, 4)
	for sid, v := range ps.streams {
		if v.NodeID == nodeID {
			doomed = append(doomed, v)
			delete(ps.streams, sid)
		}
	}
	ps.mu.Unlock()
	for _, v := range doomed {
		st.AddAudit(ctx, v.UserID, v.Username, "log.stream.close", nodeID, `{"reason":"conn_closed"}`, "")
		h.publish("log.stream", nodeID, map[string]string{"reason": "conn_closed"})
	}
}
