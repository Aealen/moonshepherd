package server

import (
	"sync"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// agentConn 单个已认证 Agent 连接。
type agentConn struct {
	nodeID string
	send   chan *protocol.Envelope
	close  chan struct{}
	once   sync.Once
}

func (c *agentConn) done() chan struct{} { return c.close }

// agentHub 在线 Agent 连接注册表 + 浏览器事件总线。
type agentHub struct {
	mu    sync.Mutex
	conns map[string]*agentConn // node_id -> conn（每节点单连接）

	subMu sync.Mutex
	subs  map[chan hubEvent]struct{}
}

// hubEvent 浏览器事件通道推送的事件（docs/06 §3）。
type hubEvent struct {
	Topic   string `json:"topic"`
	NodeID  string `json:"node_id,omitempty"`
	Payload any    `json:"payload"`
}

func newHub() *agentHub {
	return &agentHub{conns: map[string]*agentConn{}, subs: map[chan hubEvent]struct{}{}}
}

// register 登记连接；同节点旧连接被踢（close 4000，docs/04 §2.5）。
// 返回被踢的旧连接（若存在），由调用方负责关闭。
func (h *agentHub) register(nodeID string, send chan *protocol.Envelope) (*agentConn, *agentConn) {
	c := &agentConn{nodeID: nodeID, send: send, close: make(chan struct{})}
	h.mu.Lock()
	old := h.conns[nodeID]
	h.conns[nodeID] = c
	h.mu.Unlock()
	return c, old
}

// unregister 注销（幂等：只注销自己）。
func (h *agentHub) unregister(c *agentConn) {
	c.once.Do(func() { close(c.close) })
	h.mu.Lock()
	if h.conns[c.nodeID] == c {
		delete(h.conns, c.nodeID)
	}
	h.mu.Unlock()
}

// online 节点是否在线。
func (h *agentHub) online(nodeID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[nodeID]
	return ok
}

// sendTo 向节点下发消息（非阻塞投递到发送通道；通道满则丢弃并返回 false——指令走补投兜底）。
func (h *agentHub) sendTo(nodeID string, env *protocol.Envelope) bool {
	h.mu.Lock()
	c := h.conns[nodeID]
	h.mu.Unlock()
	if c == nil {
		return false
	}
	select {
	case c.send <- env:
		return true
	case <-c.close:
		return false
	default:
		return false
	}
}

// ---------- 浏览器事件订阅 ----------

func (h *agentHub) subscribe() chan hubEvent {
	ch := make(chan hubEvent, 64)
	h.subMu.Lock()
	h.subs[ch] = struct{}{}
	h.subMu.Unlock()
	return ch
}

func (h *agentHub) unsubscribe(ch chan hubEvent) {
	h.subMu.Lock()
	delete(h.subs, ch)
	h.subMu.Unlock()
}

// publish 向所有浏览器订阅者广播事件。
func (h *agentHub) publish(topic, nodeID string, payload any) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- hubEvent{Topic: topic, NodeID: nodeID, Payload: payload}:
		default: // 慢消费者丢弃，前端以 REST 全量刷新兜底
		}
	}
}
