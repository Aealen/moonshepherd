// 交互式终端会话管理(方案 B:Web 终端,docs/04 §7)。
// 生命周期:面板 session.open → PTY shell → 双向流(input/output)→
// 关闭(用户关闭/空闲超时/shell 退出/连接断开),全程审计由面板侧落库。
package agentd

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// terminalManager 本 Agent 全部 PTY 会话:并发上限、空闲回收、连接断开全清。
type terminalManager struct {
	mu       sync.Mutex
	sessions map[string]*termSession

	// emit 经 Agent.send 串行发往面板;测试注入假发送器。
	emit func(*protocol.Envelope)
}

func newTerminalManager() *terminalManager {
	tm := &terminalManager{sessions: map[string]*termSession{}}
	go tm.idleSweeper()
	return tm
}

// Open 打开 PTY 会话(rows/cols 为初始终端尺寸);重复 session_id 幂等忽略。
func (tm *terminalManager) Open(sessionID string, rows, cols int) error {
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if _, dup := tm.sessions[sessionID]; dup {
		return nil
	}
	if len(tm.sessions) >= protocol.MaxSessionsPerAgent {
		return fmt.Errorf("并发会话已达上限 %d", protocol.MaxSessionsPerAgent)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// 不预设 Setpgid:creack/pty 会追加 Setsid+Setctty,setsid 后再 setpgid 必 EPERM;
	// 新会话本身就是新进程组,关闭 ptmx 时 SIGHUP 即终止整个会话
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return fmt.Errorf("打开 PTY 失败: %w", err)
	}
	s := &termSession{id: sessionID, ptmx: ptmx, cmd: cmd, tm: tm}
	s.lastActive.Store(time.Now().Unix())
	tm.sessions[sessionID] = s
	go s.readLoop()
	slog.Info("终端会话已打开", "session", sessionID, "pid", cmd.Process.Pid)
	return nil
}

func (tm *terminalManager) get(sessionID string) *termSession {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.sessions[sessionID]
}

func (tm *terminalManager) remove(s *termSession) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.sessions[s.id] == s {
		delete(tm.sessions, s.id)
	}
}

// CloseAll 关闭全部会话(Agent 与面板连接断开时会话随之终止,docs/04 §7.1)。
func (tm *terminalManager) CloseAll(reason string) {
	tm.mu.Lock()
	list := make([]*termSession, 0, len(tm.sessions))
	for _, s := range tm.sessions {
		list = append(list, s)
	}
	tm.mu.Unlock()
	for _, s := range list {
		s.close(reason)
	}
}

// Count 当前会话数。
func (tm *terminalManager) Count() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return len(tm.sessions)
}

// idleSweeper 周期回收空闲会话(无输入且无输出超时,docs/04 §7.1)。
func (tm *terminalManager) idleSweeper() {
	for range time.Tick(30 * time.Second) {
		deadline := time.Now().Unix() - int64(protocol.SessionIdleTimeoutS)
		tm.mu.Lock()
		var stale []*termSession
		for _, s := range tm.sessions {
			if s.lastActive.Load() < deadline {
				stale = append(stale, s)
			}
		}
		tm.mu.Unlock()
		for _, s := range stale {
			slog.Warn("终端会话空闲超时", "session", s.id)
			s.close("idle_timeout")
		}
	}
}

func (tm *terminalManager) sendEnvelope(env *protocol.Envelope) {
	if tm.emit != nil {
		tm.emit(env)
	}
}

// termSession 单个 PTY 会话。
type termSession struct {
	id         string
	ptmx       *os.File
	cmd        *exec.Cmd
	tm         *terminalManager
	lastActive atomic.Int64 // unix 秒;输入与输出都刷新活跃
	writeMu    sync.Mutex   // ptmx 并发写保护(input 与 resize)
	closeOnce  sync.Once
}

// Write 写入终端输入(键盘字节流)。
func (s *termSession) Write(b []byte) error {
	s.lastActive.Store(time.Now().Unix())
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.ptmx.Write(b)
	return err
}

// Resize 终端窗口尺寸变化(SIGWINCH)。
func (s *termSession) Resize(rows, cols int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// readLoop 持续读 PTY 输出并分片上报(base64,docs/04 §7.2);
// shell 退出(EOF)即会话终结。
func (s *termSession) readLoop() {
	defer s.close("exit")
	buf := make([]byte, 8192)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.lastActive.Store(time.Now().Unix())
			s.tm.sendEnvelope(&protocol.Envelope{
				ID: newID(), Type: protocol.TypeSessionOutput, TS: time.Now().Unix(),
				Payload: protocol.SessionIOPayload{SessionID: s.id, Data: base64.StdEncoding.EncodeToString(buf[:n])},
			})
		}
		if err != nil {
			return
		}
	}
}

// close 幂等关闭:关 PTY(SIGHUP 通知会话)→ 宽限等待退出 → 超时强杀 → 回收。
// shell 不随 SIGHUP 退出的场景(非交互/忽略信号)不能让 Wait 无限阻塞。
func (s *termSession) close(reason string) {
	s.closeOnce.Do(func() {
		_ = s.ptmx.Close()
		if s.cmd.Process != nil {
			done := make(chan struct{})
			go func() { _ = s.cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = s.cmd.Process.Kill() // SIGKILL 后 Wait 必然返回
				<-done
			}
		}
		s.tm.remove(s)
		s.tm.sendEnvelope(&protocol.Envelope{
			ID: newID(), Type: protocol.TypeSessionClose, TS: time.Now().Unix(),
			Payload: protocol.SessionClosePayload{SessionID: s.id, Reason: reason},
		})
		slog.Info("终端会话已关闭", "session", s.id, "reason", reason)
	})
}
