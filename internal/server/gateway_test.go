package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maxonphong/herd/internal/credenc"
	"github.com/maxonphong/herd/internal/protocol"
	"github.com/maxonphong/herd/internal/store"
)

// newTestServer 组装内存面板（临时 data 目录）。
func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := credenc.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{DataDir: dir, Dev: true}, st, box)
	if err != nil {
		t.Fatal(err)
	}
	return srv, st
}

func dialAgent(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(url, "http") + "/agent"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func writeEnv(t *testing.T, conn *websocket.Conn, typ string, payload any) {
	t.Helper()
	if err := conn.WriteJSON(&protocol.Envelope{ID: "m_test", Type: typ, TS: time.Now().Unix(), Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

// rawEnv 测试侧解码的信封（Payload 保持原始 JSON）。
type rawEnv struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func readEnv(t *testing.T, conn *websocket.Conn) *rawEnv {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var env rawEnv
	if err := conn.ReadJSON(&env); err != nil {
		t.Fatal(err)
	}
	return &env
}

// M1 验收：注册 → 换发身份 → hello 认证 → 心跳 → 上报，全链路（docs/03 §5 M1）。
func TestGatewayRegisterHelloHeartbeat(t *testing.T) {
	srv, st := newTestServer(t)
	_ = srv

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. 发放注册 Token
	_, plaintext, err := st.NewRegistrationToken(t.Context(), "", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Agent 注册
	conn := dialAgent(t, ts.URL)
	writeEnv(t, conn, protocol.TypeRegister, protocol.RegisterPayload{
		RegistrationToken: plaintext, Hostname: "nas",
		OS: protocol.OSInfo{Name: "Unraid", Version: "7.1", Arch: "amd64"},
		AgentVersion: "0.1.0", ProtoVersion: protocol.ProtoVersion,
		Capabilities: []string{"docker"},
	})
	ack := readEnv(t, conn)
	if ack.Type != protocol.TypeRegisterAck {
		t.Fatalf("期望 register_ack，收到 %s（payload=%s）", ack.Type, ack.Payload)
	}
	var rap protocol.RegisterAckPayload
	if err := json.Unmarshal(ack.Payload, &rap); err != nil || rap.NodeSecret == "" {
		t.Fatalf("register_ack 应含 node_secret: %v %s", err, ack.Payload)
	}

	// 3. Token 一次性：重放注册必须被拒
	conn2 := dialAgent(t, ts.URL)
	writeEnv(t, conn2, protocol.TypeRegister, protocol.RegisterPayload{
		RegistrationToken: plaintext, Hostname: "evil",
		OS: protocol.OSInfo{Name: "linux"}, ProtoVersion: protocol.ProtoVersion,
	})
	errEnv := readEnv(t, conn2)
	if errEnv.Type != protocol.TypeError {
		t.Fatalf("重放注册 Token 必须收到 error，收到 %s", errEnv.Type)
	}

	// 4. 断开后以 node_secret hello 重连（正常路径）
	conn.Close()
	time.Sleep(100 * time.Millisecond)
	conn3 := dialAgent(t, ts.URL)
	nonce := "test-nonce-1"
	tsNow := time.Now().Unix()
	writeEnv(t, conn3, protocol.TypeHello, protocol.HelloPayload{
		NodeID: rap.NodeID, TS: tsNow, Nonce: nonce,
		Sig: protocol.HelloSig(rap.NodeSecret, rap.NodeID, tsNow, nonce),
	})
	hack := readEnv(t, conn3)
	if hack.Type != protocol.TypeHelloAck {
		t.Fatalf("期望 hello_ack，收到 %s", hack.Type)
	}

	// 5. 伪造凭据必须被拒（M1 验收：伪造 secret 被拒）
	conn4 := dialAgent(t, ts.URL)
	nonce2 := "test-nonce-2"
	writeEnv(t, conn4, protocol.TypeHello, protocol.HelloPayload{
		NodeID: rap.NodeID, TS: tsNow, Nonce: nonce2,
		Sig: protocol.HelloSig("sec_forged", rap.NodeID, tsNow, nonce2),
	})
	reject := readEnv(t, conn4)
	if reject.Type != protocol.TypeError {
		t.Fatalf("伪造 secret 必须收到 error，收到 %s", reject.Type)
	}
	var ep protocol.ErrorPayload
	_ = json.Unmarshal(reject.Payload, &ep)
	if ep.Code != protocol.ErrAuthFailed {
		t.Fatalf("错误码应为 1003，实得 %d", ep.Code)
	}

	// 6. 心跳入库
	writeEnv(t, conn3, protocol.TypeHeartbeat, protocol.HeartbeatPayload{
		CPUPercent: 42, Mem: &protocol.MemSnapshot{UsedMB: 100, TotalMB: 200},
	})
	time.Sleep(200 * time.Millisecond)
	node, err := st.GetNode(t.Context(), rap.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != "online" {
		t.Fatalf("节点应 online，实为 %s", node.Status)
	}
	if m, _, _ := st.LatestMetric(t.Context(), rap.NodeID); m == nil || m.CPU != 42 {
		t.Fatal("心跳快照应已入库")
	}

	// 7. 应用清单上报（覆盖式）
	writeEnv(t, conn3, protocol.TypeReport, protocol.ReportPayload{
		ProbeName: "docker", DetectedAt: time.Now().Unix(),
		Apps: []protocol.App{{AppType: "docker", Name: "blog", Status: protocol.AppRunning}},
	})
	time.Sleep(200 * time.Millisecond)
	apps, total, err := st.ListApps(t.Context(), store.AppFilter{NodeID: rap.NodeID}, 1, 20)
	if err != nil || total != 1 {
		t.Fatalf("应用清单应 1 条: %v total=%d", err, total)
	}
	if apps[0].AppName != "blog" || apps[0].Status != "running" {
		t.Fatalf("应用数据不符: %+v", apps[0])
	}
}

// nonce 重放必须被拒（docs/05 §6 认证用例）。
func TestGatewayNonceReplay(t *testing.T) {
	srv, st := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_, plaintext, err := st.NewRegistrationToken(t.Context(), "", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	conn := dialAgent(t, ts.URL)
	writeEnv(t, conn, protocol.TypeRegister, protocol.RegisterPayload{
		RegistrationToken: plaintext, Hostname: "n1", ProtoVersion: protocol.ProtoVersion,
	})
	ack := readEnv(t, conn)
	var rap protocol.RegisterAckPayload
	_ = json.Unmarshal(ack.Payload, &rap)
	if rap.NodeID == "" {
		t.Fatalf("nonce 测试前置失败：注册未成功 ack=%+v", ack)
	}
	conn.Close()

	time.Sleep(100 * time.Millisecond)
	nonce := "replay-nonce"
	tsNow := time.Now().Unix()
	hello := protocol.HelloPayload{NodeID: rap.NodeID, TS: tsNow, Nonce: nonce}
	hello.Sig = protocol.HelloSig(rap.NodeSecret, rap.NodeID, tsNow, nonce)

	c1 := dialAgent(t, ts.URL)
	writeEnv(t, c1, protocol.TypeHello, hello)
	if e := readEnv(t, c1); e.Type != protocol.TypeHelloAck {
		t.Fatalf("首次 hello 应成功，收到 %s", e.Type)
	}
	c1.Close()

	time.Sleep(100 * time.Millisecond)
	c2 := dialAgent(t, ts.URL)
	writeEnv(t, c2, protocol.TypeHello, hello) // 完全相同的 nonce + ts + sig
	rej := readEnv(t, c2)
	if rej.Type != protocol.TypeError {
		t.Fatalf("nonce 重放必须收到 error，收到 %s", rej.Type)
	}
	var ep protocol.ErrorPayload
	_ = json.Unmarshal(rej.Payload, &ep)
	if ep.Code != protocol.ErrNonceReplay {
		t.Fatalf("错误码应为 1004（nonce 重放），实得 %d", ep.Code)
	}
}

// 登录防爆破：5 次失败锁定（docs/05 §3.2/§6）。
func TestLoginRateLimit(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.CreateUser(t.Context(), "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	login := func(user, pass string) int {
		resp, err := ts.Client().Post(ts.URL+"/api/v1/auth/login", "application/json",
			strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 5; i++ {
		if code := login("admin", "wrong-password"); code != 401 {
			t.Fatalf("第 %d 次错误密码应 401，实得 %d", i+1, code)
		}
	}
	if code := login("admin", "password123"); code != 429 {
		t.Fatalf("锁定期间正确密码也应 429，实得 %d", code)
	}
}
