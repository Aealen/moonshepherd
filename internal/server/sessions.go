// 面板侧终端会话管理(方案 B:Web 终端,docs/04 §8)。
// 职责:会话生命周期追踪、REST↔WSS 桥接、输出转推浏览器、
// 全量审计(会话开关 + 输入按行落 audit_logs)。
package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// panelSessions 面板侧活跃终端会话表 + open 等待区。
type panelSessions struct {
	mu       sync.Mutex
	sessions map[string]*panelSession
	pending  map[string]chan *protocol.SessionOpenAckPayload // session_id → REST 等待 ack
}

// panelSession 单个终端会话的面板侧视图。
type panelSession struct {
	ID       string
	NodeID   string
	UserID   int64 // 会话所有者(仅本人可输入/关闭)
	Username string
	OpenedAt time.Time

	inputMu  sync.Mutex
	inputBuf []byte // 审计行缓冲:遇 \r/\n 切分落一行(docs/04 §8.4)
}

func newPanelSessions() *panelSessions {
	return &panelSessions{sessions: map[string]*panelSession{}, pending: map[string]chan *protocol.SessionOpenAckPayload{}}
}

// ListByUserAndNode 列出指定用户在指定节点的活跃会话 id。
// 用途:面板重启会清空内存会话表,而浏览器侧终端组件保活存活——前端在
// events WS 重连成功后调本接口校验会话存活,失联的自动标记失效。
func (ps *panelSessions) ListByUserAndNode(nodeID string, uid int64) []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []string
	for sid, s := range ps.sessions {
		if s.NodeID == nodeID && s.UserID == uid {
			out = append(out, sid)
		}
	}
	return out
}

// Open 打开会话:下发 session.open 并同步等待 ack(≤5s)。
// 审计:session.open(谁在哪个节点开了终端)。
func (ps *panelSessions) Open(ctx context.Context, h *agentHub, st auditStore,
	nodeID string, uid int64, username, ip string, rows, cols int) (*panelSession, error) {
	sid := "t_" + randHexID()
	ch := make(chan *protocol.SessionOpenAckPayload, 1)
	ps.mu.Lock()
	ps.pending[sid] = ch
	ps.mu.Unlock()
	defer func() {
		ps.mu.Lock()
		delete(ps.pending, sid)
		ps.mu.Unlock()
	}()
	if !h.sendTo(nodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeSessionOpen, TS: time.Now().Unix(),
		Payload: protocol.SessionOpenPayload{SessionID: sid, Rows: rows, Cols: cols}}) {
		return nil, fmt.Errorf("节点不在线")
	}
	select {
	case ack := <-ch:
		if ack == nil || !ack.OK {
			errMsg := "节点拒绝打开终端"
			if ack != nil {
				errMsg = ack.Error
			}
			return nil, fmt.Errorf("%s", errMsg)
		}
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("打开终端超时(节点未响应)")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s := &panelSession{ID: sid, NodeID: nodeID, UserID: uid, Username: username, OpenedAt: time.Now()}
	ps.mu.Lock()
	ps.sessions[sid] = s
	ps.mu.Unlock()
	st.AddAudit(ctx, uid, username, "session.open", nodeID,
		`{"session_id":"`+sid+`","rows":`+fmt.Sprint(rows)+`,"cols":`+fmt.Sprint(cols)+`}`, ip)
	return s, nil
}

// OnOpenAck Agent 回执到达,唤醒等待中的 REST。
func (ps *panelSessions) OnOpenAck(payload protocol.SessionOpenAckPayload) {
	ps.mu.Lock()
	ch := ps.pending[payload.SessionID]
	ps.mu.Unlock()
	if ch != nil {
		ch <- &payload
	}
}

// OnOutput 终端输出分片 → 转推浏览器(按节点订阅过滤,docs/06 §3)。
func (ps *panelSessions) OnOutput(h *agentHub, nodeID string, payload protocol.SessionIOPayload) {
	h.publish("session.output", nodeID, payload)
}

// ForwardInput 浏览器输入 → 校验会话归属 → 转发 Agent + 按行审计。
// 返回 false 表示会话不存在/非本人(调用方回错误)。
func (ps *panelSessions) ForwardInput(ctx context.Context, h *agentHub, st auditStore,
	sessionID string, dataB64 string, uid int64, username, ip string) bool {
	ps.mu.Lock()
	s := ps.sessions[sessionID]
	ps.mu.Unlock()
	if s == nil || s.UserID != uid {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return true // 解码失败丢弃,不断会话
	}
	if !h.sendTo(s.NodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeSessionInput, TS: time.Now().Unix(),
		Payload: protocol.SessionIOPayload{SessionID: sessionID, Data: dataB64}}) {
		return true // 节点瞬断:丢弃,前端会收到 session.close
	}
	s.auditLines(ctx, st, raw, username, ip)
	return true
}

// ForwardResize 浏览器窗口尺寸变化 → 转发 Agent。
func (ps *panelSessions) ForwardResize(h *agentHub, sessionID string, rows, cols int, uid int64) bool {
	ps.mu.Lock()
	s := ps.sessions[sessionID]
	ps.mu.Unlock()
	if s == nil || s.UserID != uid {
		return false
	}
	h.sendTo(s.NodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeSessionResize, TS: time.Now().Unix(),
		Payload: protocol.SessionResizePayload{SessionID: sessionID, Rows: rows, Cols: cols}})
	return true
}

// Close 用户主动关闭:下发 session.close(本地立即摘除,Agent 侧 close 回执幂等)。
func (ps *panelSessions) Close(ctx context.Context, h *agentHub, st auditStore,
	sessionID string, uid int64, username, ip string) bool {
	ps.mu.Lock()
	s := ps.sessions[sessionID]
	delete(ps.sessions, sessionID)
	ps.mu.Unlock()
	if s == nil || s.UserID != uid {
		return false
	}
	h.sendTo(s.NodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeSessionClose, TS: time.Now().Unix(),
		Payload: protocol.SessionClosePayload{SessionID: sessionID, Reason: "user_close"}})
	st.AddAudit(ctx, uid, username, "session.close", s.NodeID, sessionJSON(sessionID, "user_close"), ip)
	h.publish("session.close", s.NodeID, map[string]string{"session_id": sessionID, "reason": "user_close"})
	return true
}

// OnClose Agent 侧会话终结(exit/idle_timeout/conn_closed/user_close):
// 摘除 + 审计 + 通知浏览器。
func (ps *panelSessions) OnClose(ctx context.Context, h *agentHub, st auditStore,
	nodeID string, payload protocol.SessionClosePayload) {
	ps.mu.Lock()
	s := ps.sessions[payload.SessionID]
	delete(ps.sessions, payload.SessionID)
	ps.mu.Unlock()
	if s == nil {
		return
	}
	st.AddAudit(ctx, s.UserID, s.Username, "session.close", nodeID, sessionJSON(payload.SessionID, payload.Reason), "")
	h.publish("session.close", nodeID, map[string]string{"session_id": payload.SessionID, "reason": payload.Reason})
}

// NodeOffline 节点连接断开:该节点全部会话终结(docs/04 §8.1 会话绑定连接)。
func (ps *panelSessions) NodeOffline(ctx context.Context, h *agentHub, st auditStore, nodeID string) {
	ps.mu.Lock()
	doomed := make([]*panelSession, 0, 4)
	for sid, s := range ps.sessions {
		if s.NodeID == nodeID {
			doomed = append(doomed, s)
			delete(ps.sessions, sid)
		}
	}
	ps.mu.Unlock()
	for _, s := range doomed {
		slog.Info("节点离线,终端会话终结", "node", nodeID, "session", s.ID)
		st.AddAudit(ctx, s.UserID, s.Username, "session.close", nodeID, sessionJSON(s.ID, "conn_closed"), "")
		h.publish("session.close", nodeID, map[string]string{"session_id": s.ID, "reason": "conn_closed"})
	}
}

// auditLines 输入流按行切分落审计:行缓冲跨分片续接;控制字符(Ctrl-C 等)
// 以可见转义记录(用户操作全量可审计,docs/05 §3.3)。
func (s *panelSession) auditLines(ctx context.Context, st auditStore, raw []byte, username, ip string) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.inputBuf = append(s.inputBuf, raw...)
	for {
		idx := -1
		for i, b := range s.inputBuf {
			if b == '\n' || b == '\r' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(s.inputBuf[:idx]))
		s.inputBuf = s.inputBuf[idx+1:]
		if line == "" {
			continue
		}
		st.AddAudit(ctx, s.UserID, username, "session.input", s.NodeID,
			`{"session_id":"`+s.ID+`","cmd":`+jsonString(line)+`}`, ip)
	}
	// 行缓冲上限:未回车却狂写(如 cat 大文件粘贴)防膨胀,超限按行强制落
	if len(s.inputBuf) > 4096 {
		st.AddAudit(ctx, s.UserID, username, "session.input", s.NodeID,
			`{"session_id":"`+s.ID+`","cmd":`+jsonString(string(s.inputBuf))+`,"unterminated":true}`, ip)
		s.inputBuf = nil
	}
}

// auditStore AddAudit 的最小接口(解耦 store,便于测试)。
type auditStore interface {
	AddAudit(ctx context.Context, userID int64, username, action, target, detail, ip string)
}

func sessionJSON(sessionID, reason string) string {
	return `{"session_id":"` + sessionID + `","reason":"` + reason + `"}`
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func randHexID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
