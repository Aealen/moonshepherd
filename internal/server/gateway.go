package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/aealen/moonshepherd/internal/protocol"
	"github.com/aealen/moonshepherd/internal/store"
)

// gateway Agent WSS 接入网关（docs/04 §2/§3；路径 /agent）。
type gateway struct {
	srv *Server
	up  websocket.Upgrader

	nonceMu sync.Mutex
	nonces  map[string]map[string]time.Time // node_id -> 近期 nonce -> 时间
}

func newGateway(s *Server) *gateway {
	return &gateway{
		srv: s,
		up: websocket.Upgrader{
			ReadBufferSize:  32 << 10,
			WriteBufferSize: 32 << 10,
		},
		nonces: map[string]map[string]time.Time{},
	}
}

func (g *gateway) handler(w http.ResponseWriter, r *http.Request) {
	// 明文 ws:// 仅限调试场景：开发模式（本地/局域网联调）或本机回环（docs/04 §1）。
	// 生产部署必须 TLS 或前置反代（docs/05 §3.1）；反代终结 TLS 后明文回源
	// 以 X-Forwarded-Proto: https 声明（需 --trust-proxy 开启信任）。
	if !g.srv.isSecureRequest(r) && !g.srv.opt.Dev {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !isLoopback(host) {
			http.Error(w, "TLS required", http.StatusUpgradeRequired)
			return
		}
	}
	conn, err := g.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	go g.serve(conn)
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (g *gateway) serve(conn *websocket.Conn) {
	defer conn.Close()
	slog.Info("agent 接入", "remote", conn.RemoteAddr())

	// 第一条消息必须是 register 或 hello（5s 内）
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	env, perr := g.readEnvelope(conn)
	if perr != nil {
		return
	}

	switch env.Type {
	case protocol.TypeRegister:
		g.handleRegister(conn, env)
	case protocol.TypeHello:
		g.handleHello(conn, env)
	default:
		g.sendError(conn, protocol.ErrUnknownType, "首条消息必须是 register 或 hello", env.ID)
	}
}

// handleRegister 一次性 Token 注册换发节点身份（docs/04 §2.2）。
func (g *gateway) handleRegister(conn *websocket.Conn, env *protocol.Envelope) {
	raw, _ := json.Marshal(env.Payload)
	var p protocol.RegisterPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.RegistrationToken == "" || p.Hostname == "" {
		g.sendError(conn, protocol.ErrBadPayload, "register payload 非法", env.ID)
		return
	}
	if p.ProtoVersion != "" {
		major := strings.SplitN(p.ProtoVersion, ".", 2)[0]
		if major != fmt.Sprintf("%d", protocol.ProtoMajor) {
			g.sendError(conn, protocol.ErrProtoUnsupported, "协议主版本不支持", env.ID)
			conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(protocol.CloseProtoVersion, "proto major unsupported"), time.Now().Add(time.Second))
			return
		}
	}
	ctx := context.Background()
	if err := g.srv.st.ConsumeRegistrationToken(ctx, p.RegistrationToken); err != nil {
		code := protocol.ErrBadToken
		msg := "registration_token 无效"
		if err.Error() == "token_expired" {
			code = protocol.ErrTokenExpired
			msg = "registration_token 已过期"
		}
		g.sendError(conn, code, msg, env.ID)
		return
	}

	nodeID := store.NewNodeID()
	secret := store.NewNodeSecret()
	enc, err := g.srv.box.Encrypt([]byte(secret))
	if err != nil {
		slog.Error("加密节点凭据失败", "err", err)
		g.sendError(conn, protocol.ErrBadPayload, "内部错误", env.ID)
		return
	}
	name := p.Hostname
	node := &store.Node{
		ID: nodeID, Name: name, Hostname: p.Hostname,
		AgentVersion: p.AgentVersion, ProtoVersion: p.ProtoVersion,
		OSName: p.OS.Name, OSVersion: p.OS.Version, OSArch: p.OS.Arch,
		Status: "online", Tags: []string{}, Capabilities: p.Capabilities,
	}
	if err := g.srv.st.CreateNode(ctx, node, enc); err != nil {
		slog.Error("写入节点失败", "err", err)
		g.sendError(conn, protocol.ErrBadPayload, "内部错误", env.ID)
		return
	}
	g.srv.st.BindTokenToNode(ctx, p.RegistrationToken, nodeID)
	g.srv.st.AddAudit(ctx, 0, "system", "node.registered", nodeID, `{"hostname":"`+p.Hostname+`"}`, conn.RemoteAddr().String())
	g.srv.hub.publish("node.registered", nodeID, map[string]any{"node_id": nodeID, "name": name})

	ack := &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeRegisterAck, TS: time.Now().Unix(),
		Payload: protocol.RegisterAckPayload{NodeID: nodeID, NodeSecret: secret, Config: protocol.DefaultAgentConfig()}}
	if err := g.writeEnvelope(conn, ack); err != nil {
		return
	}
	g.enterSteady(conn, nodeID, protocol.DefaultAgentConfig())
}

// handleHello 每次连接的 HMAC 挑战认证（docs/04 §2.3）。
func (g *gateway) handleHello(conn *websocket.Conn, env *protocol.Envelope) {
	raw, _ := json.Marshal(env.Payload)
	var p protocol.HelloPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.NodeID == "" || p.Sig == "" {
		g.sendError(conn, protocol.ErrBadPayload, "hello payload 非法", env.ID)
		return
	}
	reject := func(code int, msg string, wsCode int) {
		g.sendError(conn, code, msg, env.ID)
		conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(wsCode, msg), time.Now().Add(time.Second))
	}
	ctx := context.Background()
	node, err := g.srv.st.GetNode(ctx, p.NodeID)
	if err != nil {
		reject(protocol.ErrAuthFailed, "节点不存在或已禁用", protocol.CloseAuthFailed)
		return
	}
	if !node.Enabled {
		reject(protocol.ErrNodeDisabled, "节点已禁用", protocol.CloseNodeDisabled)
		return
	}
	// 时钟偏移窗口 ±300s（docs/04 §2.3）
	if diff := time.Now().Unix() - p.TS; diff > 300 || diff < -300 {
		reject(protocol.ErrAuthFailed, "时间戳超出容忍窗口", protocol.CloseAuthFailed)
		return
	}
	if g.nonceSeen(p.NodeID, p.Nonce) {
		reject(protocol.ErrNonceReplay, "nonce 重放", protocol.CloseAuthFailed)
		return
	}
	enc, err := g.srv.st.NodeCredential(ctx, p.NodeID)
	if err != nil {
		reject(protocol.ErrAuthFailed, "凭据读取失败", protocol.CloseAuthFailed)
		return
	}
	secret, err := g.srv.box.Decrypt(enc)
	if err != nil {
		slog.Error("解密节点凭据失败", "node", p.NodeID, "err", err)
		reject(protocol.ErrAuthFailed, "认证失败", protocol.CloseAuthFailed)
		return
	}
	if !protocol.VerifyHelloSig(string(secret), p) {
		reject(protocol.ErrAuthFailed, "签名校验失败", protocol.CloseAuthFailed)
		return
	}
	g.recordNonce(p.NodeID, p.Nonce)

	_ = g.srv.st.UpdateNodeStatus(ctx, p.NodeID, "online", time.Now().Unix())
	cfg := protocol.DefaultAgentConfig()
	ack := &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeHelloAck, TS: time.Now().Unix(),
		Payload: protocol.HelloAckPayload{ServerTime: time.Now().Unix(), Config: cfg}}
	if err := g.writeEnvelope(conn, ack); err != nil {
		return
	}
	g.enterSteady(conn, p.NodeID, cfg)
}

// enterSteady 认证通过后的常驻阶段：登 hub、补投 pending、进入读写循环。
func (g *gateway) enterSteady(conn *websocket.Conn, nodeID string, cfg protocol.AgentConfig) {
	ctx := context.Background()
	send := make(chan *protocol.Envelope, 64)
	c, old := g.srv.hub.register(nodeID, send)
	if old != nil {
		// 同节点旧连接被踢（docs/04：每节点单连接）
		old.once.Do(func() { close(old.close) })
		oldSend := old.send
		go func() {
			// 通知旧连接退出
			select {
			case oldSend <- &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeError, TS: time.Now().Unix(),
				Payload: protocol.ErrorPayload{Code: 3001, Message: "被新连接替换"}}:
			default:
			}
		}()
	}
	defer func() {
		g.srv.hub.unregister(c)
		// 断开：sent/running 标 lost（不自动重发，docs/04 §4.2）
		if g.srv.hub.online(nodeID) {
			// 已有新连接接管，不置离线
		} else {
			_ = g.srv.st.UpdateNodeStatus(ctx, nodeID, "offline", time.Now().Unix())
			_ = g.srv.st.MarkLostCommands(ctx, nodeID)
			g.srv.hub.publish("node.status", nodeID, map[string]string{"status": "offline"})
		}
	}()

	// 补投 pending（docs/04 §4.2）
	pending, err := g.srv.st.PendingCommandsForNode(ctx, nodeID, cfg.CommandTTLs)
	if err == nil {
		for _, cmd := range pending {
			if g.srv.hub.sendTo(nodeID, commandEnvelope(cmd)) {
				_ = g.srv.st.MarkCommandSent(ctx, cmd.ID)
			}
		}
	}

	g.srv.hub.publish("node.status", nodeID, map[string]string{"status": "online"})

	// 写循环：send 通道 + keepalive ping（退出由 c.close 触发，见 unregister）
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case env := <-send:
				if err := g.writeEnvelope(conn, env); err != nil {
					return
				}
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			case <-c.close:
				return
			}
		}
	}()

	// 读循环：deadline = 3×心跳间隔（docs/04 §3）
	readDeadline := time.Duration(cfg.HeartbeatIntervalS*3) * time.Second
	if readDeadline < 30*time.Second {
		readDeadline = 30 * time.Second
	}
	conn.SetReadLimit(protocol.MaxPayloadBytes + 4096)
	for {
		conn.SetReadDeadline(time.Now().Add(readDeadline))
		env, err := g.readEnvelope(conn)
		if err != nil {
			return
		}
		switch env.Type {
		case protocol.TypeHeartbeat:
			g.onHeartbeat(ctx, nodeID, env)
		case protocol.TypeReport:
			g.onReport(ctx, nodeID, env)
		case protocol.TypeCommandResult:
			g.onCommandResult(ctx, nodeID, env)
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if b, _ := json.Marshal(env.Payload); json.Unmarshal(b, &ep) == nil {
				slog.Warn("agent 上报错误", "node", nodeID, "code", ep.Code, "msg", ep.Message)
			}
		default:
			// 未知类型不断连（前向兼容，docs/04 §1）
			g.sendEnvelope(send, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeError, TS: time.Now().Unix(),
				Payload: protocol.ErrorPayload{Code: protocol.ErrUnknownType, Message: "unknown type: " + env.Type, RefID: env.ID}})
		}
	}
}

func (g *gateway) onHeartbeat(ctx context.Context, nodeID string, env *protocol.Envelope) {
	raw, _ := json.Marshal(env.Payload)
	var p protocol.HeartbeatPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	_ = g.srv.st.TouchNode(ctx, nodeID)
	m := store.MetricIn{NodeID: nodeID, CPU: p.CPUPercent, Load1: p.Load1}
	if p.Net != nil {
		m.NetRx, m.NetTx = p.Net.RxKBps, p.Net.TxKBps
	}
	if p.Mem != nil {
		m.MemUsed, m.MemTotal = p.Mem.UsedMB, p.Mem.TotalMB
	}
	if p.Disk != nil {
		m.DiskUsed, m.DiskTotal = p.Disk.UsedGB, p.Disk.TotalGB
	}
	_ = g.srv.st.InsertMetric(ctx, m)
	g.srv.hub.publish("node.snapshot", nodeID, p)
}

func (g *gateway) onReport(ctx context.Context, nodeID string, env *protocol.Envelope) {
	raw, _ := json.Marshal(env.Payload)
	var p protocol.ReportPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	rows := make([]store.AppRow, 0, len(p.Apps))
	for _, a := range p.Apps {
		detail := []byte("{}")
		if a.Detail != nil {
			if b, err := json.Marshal(a.Detail); err == nil {
				detail = b
			}
		}
		rows = append(rows, store.AppRow{
			NodeID: nodeID, ProbeName: p.ProbeName, AppName: a.Name, AppType: a.AppType,
			Version: a.Version, Status: a.Status, Detail: detail, DetectedAt: p.DetectedAt,
		})
	}
	if err := g.srv.st.UpsertAppInventory(ctx, nodeID, p.ProbeName, rows); err != nil {
		slog.Error("应用清单入库失败", "node", nodeID, "probe", p.ProbeName, "err", err)
		return
	}
	g.srv.hub.publish("report.updated", nodeID, map[string]string{"probe_name": p.ProbeName})
}

// onCommandResult 指令回执：状态机迁移 + config.apply 版本联动 + 事件广播。
func (g *gateway) onCommandResult(ctx context.Context, nodeID string, env *protocol.Envelope) {
	raw, _ := json.Marshal(env.Payload)
	var p protocol.CommandResultPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	if p.Status == protocol.ResultAccepted {
		_ = g.srv.st.UpdateCommandStatus(ctx, p.CommandID, "running")
		g.srv.hub.publish("command.status", nodeID, map[string]string{"command_id": p.CommandID, "status": "running"})
		return
	}
	status := "failed"
	if p.Status == protocol.ResultSuccess {
		status = "success"
	}
	stages, _ := json.Marshal(p.Stages)
	_ = g.srv.st.FinishCommand(ctx, &store.CommandResultIn{
		CommandID: p.CommandID, Status: status, Stdout: p.Stdout, Stderr: p.Stderr,
		Truncated: p.Truncated, ExitCode: p.ExitCode, Stages: stages, Error: p.Error,
	})
	// config.apply 联动版本状态（docs/06 §2.5）
	cmd, err := g.srv.st.GetCommand(ctx, p.CommandID)
	if err == nil && cmd.Type == protocol.CmdConfigApply {
		var cp protocol.ConfigApplyPayload
		if json.Unmarshal(cmd.Payload, &cp) == nil && cp.VersionID > 0 {
			applyRes := string(stages)
			if status == "success" {
				_ = g.srv.st.MarkVersionApplied(ctx, cp.VersionID, cmd.ID, applyRes)
			} else {
				_ = g.srv.st.MarkVersionFailed(ctx, cp.VersionID, cmd.ID, applyRes)
			}
		}
	}
	g.srv.hub.publish("command.status", nodeID, map[string]string{"command_id": p.CommandID, "status": status})
}

// ---------- 读写信封辅助 ----------

type peerEnvelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	TS      int64           `json:"ts"`
	Payload json.RawMessage `json:"payload"`
}

func (g *gateway) readEnvelope(conn *websocket.Conn) (*protocol.Envelope, error) {
	var pe peerEnvelope
	if err := conn.ReadJSON(&pe); err != nil {
		return nil, err
	}
	return &protocol.Envelope{ID: pe.ID, Type: pe.Type, TS: pe.TS, Payload: pe.Payload}, nil
}

func (g *gateway) writeEnvelope(conn *websocket.Conn, env *protocol.Envelope) error {
	return conn.WriteJSON(env)
}

func (g *gateway) sendEnvelope(send chan *protocol.Envelope, env *protocol.Envelope) {
	select {
	case send <- env:
	default:
	}
}

func (g *gateway) sendError(conn *websocket.Conn, code int, msg, refID string) {
	_ = g.writeEnvelope(conn, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeError, TS: time.Now().Unix(),
		Payload: protocol.ErrorPayload{Code: code, Message: msg, RefID: refID}})
}

func commandEnvelope(c *store.Command) *protocol.Envelope {
	var payload any
	_ = json.Unmarshal(c.Payload, &payload)
	return &protocol.Envelope{
		ID: newMsgID(), Type: protocol.TypeCommand, TS: time.Now().Unix(),
		Payload: protocol.CommandPayload{CommandID: c.ID, Type: c.Type, TimeoutS: c.TimeoutS, Payload: payload},
	}
}

// ---------- nonce 防重放（docs/04 §2.3） ----------

func (g *gateway) recordNonce(nodeID, nonce string) {
	g.nonceMu.Lock()
	defer g.nonceMu.Unlock()
	if g.nonces[nodeID] == nil {
		g.nonces[nodeID] = map[string]time.Time{}
	}
	g.nonces[nodeID][nonce] = time.Now()
	// 顺手清理超过 10 分钟的旧 nonce（远大于 ±300s 窗口）
	cutoff := time.Now().Add(-10 * time.Minute)
	for n, t := range g.nonces[nodeID] {
		if t.Before(cutoff) {
			delete(g.nonces[nodeID], n)
		}
	}
}

func (g *gateway) nonceSeen(nodeID, nonce string) bool {
	g.nonceMu.Lock()
	defer g.nonceMu.Unlock()
	_, ok := g.nonces[nodeID][nonce]
	return ok
}

func newMsgID() string { return "m_" + randHex(16) }
