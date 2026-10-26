package server

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// fakeAudit 收集审计调用(测试断言用)。
type fakeAudit struct {
	actions []string
	details []string
}

func (f *fakeAudit) AddAudit(ctx context.Context, userID int64, username, action, target, detail, ip string) {
	f.actions = append(f.actions, action)
	f.details = append(f.details, action+"|"+target+"|"+detail)
}

// newFakeAgentConn 在 hub 里登记一个假 Agent 连接,读出面板下发消息。
func newFakeAgentConn(t *testing.T, h *agentHub, nodeID string) chan *protocol.Envelope {
	t.Helper()
	ch := make(chan *protocol.Envelope, 8)
	c, old := h.register(nodeID, ch)
	if old != nil {
		old.once.Do(func() { close(old.close) })
	}
	t.Cleanup(func() { h.unregister(c) })
	return ch
}

// TestPanelSessionsOpenAck 全流程:Open 下发 session.open → 模拟 Agent ack → 会话建立 + 审计。
func TestPanelSessionsOpenAck(t *testing.T) {
	h := newHub()
	st := &fakeAudit{}
	ps := newPanelSessions()
	agentCh := newFakeAgentConn(t, h, "n_1")

	opened := make(chan *panelSession, 1)
	go func() {
		s, err := ps.Open(context.Background(), h, st, "n_1", 7, "admin", "1.2.3.4", 24, 80)
		if err != nil {
			t.Errorf("Open: %v", err)
			return
		}
		opened <- s
	}()

	// 面板应向 Agent 下发 session.open
	select {
	case env := <-agentCh:
		if env.Type != protocol.TypeSessionOpen {
			t.Fatalf("应下发 session.open,实得 %s", env.Type)
		}
		p, _ := parseEnv[protocol.SessionOpenPayload](env)
		if p.SessionID == "" || p.Rows != 24 {
			t.Fatalf("open payload 异常: %+v", p)
		}
		// 模拟 Agent 成功回执
		ps.OnOpenAck(protocol.SessionOpenAckPayload{SessionID: p.SessionID, OK: true})
	case <-time.After(2 * time.Second):
		t.Fatal("未收到下发的 session.open")
	}

	select {
	case s := <-opened:
		if s.NodeID != "n_1" || s.Username != "admin" {
			t.Fatalf("会话字段异常: %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open 未在 ack 后返回")
	}
	if len(st.actions) != 1 || st.actions[0] != "session.open" {
		t.Fatalf("应落一条 session.open 审计,实得 %v", st.actions)
	}
}

// TestPanelSessionsInputAudit 输入按行审计:跨分片续行、空行跳过、归属校验。
func TestPanelSessionsInputAudit(t *testing.T) {
	h := newHub()
	st := &fakeAudit{}
	ps := newPanelSessions()
	agentCh := newFakeAgentConn(t, h, "n_1")

	// 直接植入一个已建立的会话(绕过 Open 的 ack 流程)
	ps.mu.Lock()
	ps.sessions["t_x"] = &panelSession{ID: "t_x", NodeID: "n_1", UserID: 7, Username: "admin"}
	ps.mu.Unlock()

	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	// 跨分片的一行: "ec" + "ho hi\r" + "\ndate\r\n"
	if !ps.ForwardInput(context.Background(), h, st, "t_x", b64("ec"), 7, "admin", "1.2.3.4") {
		t.Fatal("第一条 input 应被接受")
	}
	ps.ForwardInput(context.Background(), h, st, "t_x", b64("ho hi\r"), 7, "admin", "1.2.3.4")
	ps.ForwardInput(context.Background(), h, st, "t_x", b64("\ndate\r\n"), 7, "admin", "1.2.3.4")

	// Agent 侧应收到 3 条 input 转发
	if len(agentCh) != 3 {
		t.Fatalf("Agent 应收到 3 条 input,实得 %d", len(agentCh))
	}

	// 审计:两行命令(echo hi / date),空行不记
	if len(st.actions) != 2 {
		t.Fatalf("应落 2 条 session.input 审计,实得 %d: %v", len(st.actions), st.actions)
	}
	if want := `{"session_id":"t_x","cmd":"echo hi"}`; st.details[0] != "session.input|n_1|"+want {
		t.Fatalf("第一行审计异常: %s", st.details[0])
	}
	if !contains(st.details[1], `"cmd":"date"`) {
		t.Fatalf("第二行审计异常: %s", st.details[1])
	}

	// 非会话所有者:拒绝
	if ps.ForwardInput(context.Background(), h, st, "t_x", b64("rm -rf /"), 99, "intruder", "6.6.6.6") {
		t.Fatal("非所有者 input 应被拒绝")
	}
	// 不存在的会话:拒绝
	if ps.ForwardInput(context.Background(), h, st, "t_ghost", b64("x"), 7, "admin", "1.2.3.4") {
		t.Fatal("不存在会话的 input 应被拒绝")
	}
}

// TestPanelSessionsNodeOffline 节点离线:全会话终结 + 浏览器事件。
func TestPanelSessionsNodeOffline(t *testing.T) {
	h := newHub()
	st := &fakeAudit{}
	ps := newPanelSessions()
	newFakeAgentConn(t, h, "n_1")

	ps.mu.Lock()
	ps.sessions["t_a"] = &panelSession{ID: "t_a", NodeID: "n_1", UserID: 7, Username: "admin"}
	ps.sessions["t_b"] = &panelSession{ID: "t_b", NodeID: "n_1", UserID: 7, Username: "admin"}
	ps.sessions["t_c"] = &panelSession{ID: "t_c", NodeID: "n_2", UserID: 7, Username: "admin"}
	ps.mu.Unlock()

	// 订阅浏览器事件
	evCh := h.subscribe()
	defer h.unsubscribe(evCh)

	ps.NodeOffline(context.Background(), h, st, "n_1")

	ps.mu.Lock()
	n := len(ps.sessions)
	ps.mu.Unlock()
	if n != 1 {
		t.Fatalf("n_1 离线后应只剩 1 个会话(n_2 的),实得 %d", n)
	}
	// 两条 session.close 审计 + 两个浏览器事件
	if len(st.actions) != 2 || st.actions[0] != "session.close" {
		t.Fatalf("应落 2 条 session.close 审计,实得 %v", st.actions)
	}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-evCh:
			if ev.Topic != "session.close" || ev.NodeID != "n_1" {
				t.Fatalf("浏览器事件异常: %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("未收到 session.close 浏览器事件")
		}
	}
}

// TestPanelSessionsOpenTimeout Agent 不回 ack:Open 超时失败。
func TestPanelSessionsOpenTimeout(t *testing.T) {
	h := newHub()
	st := &fakeAudit{}
	ps := newPanelSessions()
	newFakeAgentConn(t, h, "n_1") // 假 Agent 只收不回

	done := make(chan error, 1)
	go func() {
		_, err := ps.Open(context.Background(), h, st, "n_1", 7, "admin", "1.2.3.4", 24, 80)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("无 ack 时 Open 应失败")
		}
		if !contains(err.Error(), "超时") {
			t.Fatalf("应为超时错误,实得: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("Open 未按预期超时")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
