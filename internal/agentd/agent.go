// Package agentd 是 MoonShepherd Agent 核心：连接管理、心跳、探测、指令执行（docs/01 §3）。
package agentd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/aealen/moonshepherd/internal/executor"
	"github.com/aealen/moonshepherd/internal/probe"
	"github.com/aealen/moonshepherd/internal/protocol"
	"github.com/aealen/moonshepherd/version"
)

// Options Agent 启动配置。
type Options struct {
	Server   string // 面板地址，如 wss://panel.example.com 或 ws://127.0.0.1:8080（调试）
	Token    string // 一次性注册 Token（仅首连注册用）
	CredFile string // 凭据文件路径（默认 ~/.herd/credentials.json）
}

// credentials 本地凭据（0600，docs/04 §2.2）。
type credentials struct {
	Server     string `json:"server"`
	NodeID     string `json:"node_id"`
	NodeSecret string `json:"node_secret"`
}

// Agent 运行体。
type Agent struct {
	opt  Options
	exec *executor.E
	tm   *terminalManager
	lgm  *logManager

	sendMu sync.Mutex
	conn   *websocket.Conn

	cfg protocol.AgentConfig

	// 指令幂等缓存（docs/04 §4.3）：command_id → 上次回执
	idemMu    sync.Mutex
	idem      map[string]protocol.CommandResultPayload
	idemOrder []string
}

// New 创建 Agent。
func New(opt Options) *Agent {
	if opt.CredFile == "" {
		home, _ := os.UserHomeDir()
		opt.CredFile = filepath.Join(home, ".herd", "credentials.json")
	}
	a := &Agent{opt: opt, exec: &executor.E{}, cfg: protocol.DefaultAgentConfig(), idem: map[string]protocol.CommandResultPayload{}}
	a.tm = newTerminalManager()
	a.lgm = newLogManager()
	a.lgm.emit = a.send
	a.tm.emit = a.send
	return a
}

// Run 主循环：加载凭据/注册 → 连接 → 认证 → 常驻；断线指数退避重连（docs/04 §3）。
func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	authFails := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		creds, err := a.loadOrRegister(ctx)
		if err != nil {
			slog.Error("获取凭据失败", "err", err)
			return err
		}
		closeCode, err := a.runOnce(ctx, creds)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch closeCode {
		case protocol.CloseNodeDisabled:
			slog.Error("节点已被面板禁用/删除，Agent 停止（docs/04 §2.5）")
			return fmt.Errorf("节点被禁用")
		case protocol.CloseProtoVersion:
			slog.Error("协议主版本不支持，请升级 Agent")
			return fmt.Errorf("协议版本不兼容")
		case protocol.CloseAuthFailed:
			authFails++
			if authFails >= 10 {
				slog.Error("连续认证失败超限，停止重试；请检查凭据或重新注册", "fails", authFails)
				return fmt.Errorf("认证失败超限")
			}
		default:
			authFails = 0
		}
		// 指数退避 + ±20% 抖动（防重连风暴，docs/04 §3）
		jitter := time.Duration(float64(backoff) * (0.8 + jitterRand()*0.4))
		slog.Warn("连接断开，退避重连", "code", closeCode, "err", err, "wait", jitter.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter):
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

func jitterRand() float64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1000))
	if err != nil {
		return 1
	}
	return float64(n.Int64()) / 1000
}

// loadOrRegister 读本地凭据；无凭据且有 Token 时完成一次性注册并落盘。
func (a *Agent) loadOrRegister(ctx context.Context) (*credentials, error) {
	b, err := os.ReadFile(a.opt.CredFile)
	if err == nil {
		var c credentials
		if json.Unmarshal(b, &c) == nil && c.NodeID != "" && c.NodeSecret != "" {
			if a.opt.Server != "" {
				c.Server = a.opt.Server // 环境变量优先：面板挪位置改一个变量即可（docs/01 §3.1）
			}
			return &c, nil
		}
	}
	if a.opt.Token == "" {
		return nil, fmt.Errorf("无凭据且未提供注册 Token（HERD_TOKEN）")
	}
	if a.opt.Server == "" {
		return nil, fmt.Errorf("注册需要面板地址（HERD_SERVER）")
	}
	c, err := a.register(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.saveCreds(c); err != nil {
		return nil, err
	}
	slog.Info("注册成功，凭据已写入", "file", a.opt.CredFile, "node_id", c.NodeID)
	return c, nil
}

// register 一次性 Token 注册（docs/04 §2.2）。
func (a *Agent) register(ctx context.Context) (*credentials, error) {
	conn, err := a.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	host, _ := os.Hostname()
	reg := &protocol.Envelope{ID: newID(), Type: protocol.TypeRegister, TS: time.Now().Unix(),
		Payload: protocol.RegisterPayload{
			RegistrationToken: a.opt.Token, Hostname: host,
			OS: osInfo(), AgentVersion: version.Version,
			ProtoVersion: protocol.ProtoVersion,
			// 能力 + 拉起者形态(supervisor:xxx 供面板判断可否安全自更新,docs/01 §3.5)
			Capabilities: append(probe.Available(), protocol.CapPty, protocol.CapDocker,
				"supervisor:"+executor.DetectSupervisor()),
		}}
	if err := conn.WriteJSON(reg); err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var ack protocol.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		return nil, err
	}
	if ack.Type == protocol.TypeError {
		var ep protocol.ErrorPayload
		b, _ := json.Marshal(ack.Payload)
		_ = json.Unmarshal(b, &ep)
		return nil, fmt.Errorf("注册被拒: [%d] %s", ep.Code, ep.Message)
	}
	if ack.Type != protocol.TypeRegisterAck {
		return nil, fmt.Errorf("注册响应类型异常: %s", ack.Type)
	}
	b, _ := json.Marshal(ack.Payload)
	var rap protocol.RegisterAckPayload
	if err := json.Unmarshal(b, &rap); err != nil {
		return nil, err
	}
	a.cfg = rap.Config
	return &credentials{Server: a.opt.Server, NodeID: rap.NodeID, NodeSecret: rap.NodeSecret}, nil
}

func (a *Agent) saveCreds(c *credentials) error {
	if err := os.MkdirAll(filepath.Dir(a.opt.CredFile), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(a.opt.CredFile, b, 0o600)
}

func (a *Agent) dial(ctx context.Context) (*websocket.Conn, error) {
	url := strings.TrimSuffix(a.opt.Server, "/") + "/agent"
	d := &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := d.DialContext(ctx, url, http.Header{"User-Agent": {"herd/" + version.Version}})
	return conn, err
}

// runOnce 单次完整连接周期：hello 认证 → 常驻（读写循环 + 心跳/探测 ticker）。
// 返回连接关闭码（供重连策略判断）。
func (a *Agent) runOnce(ctx context.Context, creds *credentials) (int, error) {
	conn, err := a.dial(ctx)
	if err != nil {
		return 0, err
	}
	// hello：HMAC 挑战（docs/04 §2.3）
	nonce := randHex(16)
	ts := time.Now().Unix()
	hello := &protocol.Envelope{ID: newID(), Type: protocol.TypeHello, TS: ts,
		Payload: protocol.HelloPayload{
			NodeID: creds.NodeID, TS: ts, Nonce: nonce,
			Sig: protocol.HelloSig(creds.NodeSecret, creds.NodeID, ts, nonce),
		}}
	if err := conn.WriteJSON(hello); err != nil {
		conn.Close()
		return 0, err
	}
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var ack protocol.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		conn.Close()
		return 0, err
	}
	conn.SetReadDeadline(time.Time{})
	if ack.Type == protocol.TypeError {
		conn.Close()
		return protocol.CloseAuthFailed, fmt.Errorf("hello 被拒")
	}
	if ack.Type != protocol.TypeHelloAck {
		conn.Close()
		return 0, fmt.Errorf("hello 响应类型异常: %s", ack.Type)
	}
	if b, err := json.Marshal(ack.Payload); err == nil {
		var hap protocol.HelloAckPayload
		if json.Unmarshal(b, &hap) == nil && hap.Config.HeartbeatIntervalS > 0 {
			a.cfg = hap.Config // 运行配置热更新（docs/04 §2.3）
		}
	}
	slog.Info("已连接面板", "node_id", creds.NodeID, "server", creds.Server)

	a.sendMu.Lock()
	a.conn = conn
	a.sendMu.Unlock()
	defer func() {
		a.sendMu.Lock()
		a.conn = nil
		a.sendMu.Unlock()
		// 终端会话绑定连接:连接断开会话随之终止(docs/04 §7.1),
		// 先于 conn 关闭尽力发出 close 通知(多数场景连接已死,发送静默失败属预期)
		a.tm.CloseAll("conn_closed")
		a.lgm.StopAll("conn_closed")
		conn.Close()
	}()

	sendCtx, sendCancel := context.WithCancel(ctx)
	defer sendCancel()
	var writeWG sync.WaitGroup
	writeWG.Add(1)
	go func() { // 心跳 + 探测两个 ticker 都经 send() 走串行写
		defer writeWG.Done()
		a.runTickers(sendCtx)
	}()

	// 读循环
	dead := make(chan int, 1)
	go func() {
		for {
			var env protocol.Envelope
			if err := conn.ReadJSON(&env); err != nil {
				if ce, ok := err.(*websocket.CloseError); ok {
					dead <- ce.Code
				} else {
					dead <- 0
				}
				return
			}
			a.handleEnvelope(ctx, env)
		}
	}()

	select {
	case <-ctx.Done():
		sendCancel()
		writeWG.Wait()
		return 0, ctx.Err()
	case code := <-dead:
		sendCancel()
		writeWG.Wait()
		return code, nil
	}
}

// runTickers 心跳与定时探测（默认 30s / 300s，面板可调）。
func (a *Agent) runTickers(ctx context.Context) {
	hb := time.NewTicker(time.Duration(a.cfg.HeartbeatIntervalS) * time.Second)
	defer hb.Stop()
	// 首个心跳/探测立即执行一次（面板秒级看到状态）
	a.sendHeartbeat()
	a.sendReports(nil)
	probeT := time.NewTicker(time.Duration(a.cfg.ProbeIntervalS) * time.Second)
	defer probeT.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			a.sendHeartbeat()
		case <-probeT.C:
			a.sendReports(nil)
		}
	}
}

// send 串行写（gorilla/websocket 不允许并发写）。
func (a *Agent) send(env *protocol.Envelope) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if a.conn == nil {
		return
	}
	a.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err := a.conn.WriteJSON(env); err != nil {
		slog.Debug("发送失败", "type", env.Type, "err", err)
	}
	a.conn.SetWriteDeadline(time.Time{})
}

// sendHeartbeat 采集本机快照并上报；字段缺失时省略（docs/04 §5.1）。
func (a *Agent) sendHeartbeat() {
	p := collectHeartbeat()
	a.send(&protocol.Envelope{ID: newID(), Type: protocol.TypeHeartbeat, TS: time.Now().Unix(), Payload: p})
}

// sendReports 定时/按需探测上报。
func (a *Agent) sendReports(names []string) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	all := len(want) == 0
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for name, ctor := range probe.Registry {
		if !all && !want[name] {
			continue
		}
		apps, err := ctor().Detect(ctx)
		if err != nil {
			apps = []protocol.App{}
		}
		a.send(&protocol.Envelope{ID: newID(), Type: protocol.TypeReport, TS: time.Now().Unix(),
			Payload: protocol.ReportPayload{ProbeName: name, DetectedAt: time.Now().Unix(), Apps: apps}})
	}
}

// handleEnvelope 处理面板消息：command / session.* / error。
func (a *Agent) handleEnvelope(ctx context.Context, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeCommand:
		b, _ := json.Marshal(env.Payload)
		var cp protocol.CommandPayload
		if json.Unmarshal(b, &cp) != nil || cp.CommandID == "" {
			return
		}
		go a.executeCommand(ctx, cp)
	case protocol.TypeSessionOpen:
		a.handleSessionOpen(env)
	case protocol.TypeSessionInput:
		a.handleSessionInput(env)
	case protocol.TypeSessionResize:
		a.handleSessionResize(env)
	case protocol.TypeSessionClose:
		if p, ok := parseSessionPayload[protocol.SessionClosePayload](env); ok {
			if s := a.tm.get(p.SessionID); s != nil {
				s.close("user_close")
			}
		}
	case protocol.TypeFileRead:
		go handleFileRead(env, a.send)
	case protocol.TypeFileOp:
		go handleFileOp(env, a.send) // 异步:大目录拷贝/递归删除耗时不阻塞读循环
	case protocol.TypeFileUploadChunk:
		if p, ok := parseSessionPayload[protocol.FileUploadChunkPayload](env); ok {
			if p.Seq == 0 && p.Path != "" {
				if _, err := ensureUploadSession(p.SessionID, p.Path); err != nil {
					a.send(&protocol.Envelope{ID: newID(), Type: protocol.TypeFileOpAck, TS: time.Now().Unix(),
						Payload: opErr(p.SessionID, err)})
					return
				}
			}
			handleUploadChunk(env)
		}
	case protocol.TypeFileUploadDone:
		go handleUploadDone(env, a.send)
	case protocol.TypeLogStreamStart:
		if p, ok := parseSessionPayload[protocol.LogStreamStartPayload](env); ok && p.StreamID != "" && p.Container != "" {
			_ = a.lgm.Start(p.StreamID, p.Container, p.TailLines)
		}
	case protocol.TypeLogStreamStop:
		if p, ok := parseSessionPayload[protocol.LogStreamStopPayload](env); ok {
			if st := a.lgm.get(p.StreamID); st != nil {
				st.stop("user_close")
			}
		}
	case protocol.TypeError:
		slog.Warn("面板错误消息", "envelope", env.ID)
	}
}

// ---------- 终端会话消息处理(docs/04 §7) ----------

// parseSessionPayload 解析会话消息 payload 泛型辅助。
func parseSessionPayload[P any](env protocol.Envelope) (P, bool) {
	var p P
	b, err := json.Marshal(env.Payload)
	if err != nil {
		return p, false
	}
	if json.Unmarshal(b, &p) != nil {
		return p, false
	}
	return p, true
}

// handleSessionOpen 打开 PTY 会话并回执(ok=false 携带原因)。
func (a *Agent) handleSessionOpen(env protocol.Envelope) {
	p, ok := parseSessionPayload[protocol.SessionOpenPayload](env)
	if !ok || p.SessionID == "" {
		return
	}
	ack := protocol.SessionOpenAckPayload{SessionID: p.SessionID, OK: true}
	if err := a.tm.Open(p.SessionID, p.Rows, p.Cols); err != nil {
		ack.OK = false
		ack.Error = err.Error()
	}
	a.send(&protocol.Envelope{ID: newID(), Type: protocol.TypeSessionOpenAck, TS: time.Now().Unix(), Payload: ack})
}

// handleSessionInput 终端输入(base64 → PTY);会话不存在(刚关闭的竞态)静默丢弃。
func (a *Agent) handleSessionInput(env protocol.Envelope) {
	p, ok := parseSessionPayload[protocol.SessionIOPayload](env)
	if !ok {
		return
	}
	if s := a.tm.get(p.SessionID); s != nil {
		if b, err := base64.StdEncoding.DecodeString(p.Data); err == nil {
			_ = s.Write(b)
		}
	}
}

// handleSessionResize 窗口尺寸变化。
func (a *Agent) handleSessionResize(env protocol.Envelope) {
	p, ok := parseSessionPayload[protocol.SessionResizePayload](env)
	if !ok {
		return
	}
	if s := a.tm.get(p.SessionID); s != nil {
		_ = s.Resize(p.Rows, p.Cols)
	}
}

// executeCommand 幂等执行（docs/04 §4.3）+ 两段式回执。
func (a *Agent) executeCommand(ctx context.Context, cp protocol.CommandPayload) {
	// 幂等：重复投递直接重放上次回执
	a.idemMu.Lock()
	if last, ok := a.idem[cp.CommandID]; ok {
		a.idemMu.Unlock()
		a.sendResult(last)
		return
	}
	a.idemMu.Unlock()

	// 首段回执 accepted
	a.sendResult(protocol.CommandResultPayload{CommandID: cp.CommandID, Status: protocol.ResultAccepted})

	// Agent 侧超期拒执行（docs/04 §5.4 expired）
	if cp.TimeoutS <= 0 {
		cp.TimeoutS = 120
	}
	payload, _ := json.Marshal(cp.Payload)
	res := a.exec.Execute(ctx, cp.Type, payload, cp.TimeoutS, a.send)
	res.Reply.CommandID = cp.CommandID

	a.idemMu.Lock()
	a.idem[cp.CommandID] = res.Reply
	a.idemOrder = append(a.idemOrder, cp.CommandID)
	if len(a.idemOrder) > 1000 { // LRU 上限（docs/04 §4.3）
		delete(a.idem, a.idemOrder[0])
		a.idemOrder = a.idemOrder[1:]
	}
	a.idemMu.Unlock()

	a.sendResult(res.Reply)
}

func (a *Agent) sendResult(p protocol.CommandResultPayload) {
	a.send(&protocol.Envelope{ID: newID(), Type: protocol.TypeCommandResult, TS: time.Now().Unix(), Payload: p})
}

// ---------- 主机信息采集 ----------

func osInfo() protocol.OSInfo {
	name, ver := "linux", ""
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			switch {
			case strings.HasPrefix(line, "NAME="):
				name = strings.Trim(strings.TrimPrefix(line, "NAME="), `"`)
			case strings.HasPrefix(line, "VERSION_ID="):
				ver = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"`)
			}
		}
	} else if runtime.GOOS != "linux" {
		name = runtime.GOOS
	}
	return protocol.OSInfo{Name: name, Version: ver, Arch: runtime.GOARCH}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newID() string { return "m_" + randHex(16) }
