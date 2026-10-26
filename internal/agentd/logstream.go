// 容器日志实时跟随(docs/04 §9):docker logs -f 语义。
// 生命周期:面板 log.stream.start → 先推 tail 历史 → 持续增量 chunk →
// 停止(用户关闭/超时/连接断开/容器消失),全程资源有上限防泄漏。
package agentd

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aealen/moonshepherd/internal/executor"
	"github.com/aealen/moonshepherd/internal/protocol"
)

// logManager 本 Agent 全部日志跟随器。
type logManager struct {
	mu      sync.Mutex
	streams map[string]*logStream
	emit    func(*protocol.Envelope)
}

func newLogManager() *logManager {
	return &logManager{streams: map[string]*logStream{}}
}

// Start 开启跟随:docker logs --tail N -f <container>(Engine API 无 follow 流式
// 简便接口,CLI 的 -f 稳定且各平台行为一致;stdout+stderr 合流)。
func (lm *logManager) Start(streamID, container string, tailLines int) error {
	if tailLines <= 0 {
		tailLines = 100
	}
	if tailLines > protocol.LogStreamTailMax {
		tailLines = protocol.LogStreamTailMax
	}
	lm.mu.Lock()
	if _, dup := lm.streams[streamID]; dup {
		lm.mu.Unlock()
		return nil
	}
	if len(lm.streams) >= protocol.MaxLogStreamsPerAgent {
		lm.mu.Unlock()
		return fmt.Errorf("并发日志流已达上限 %d", protocol.MaxLogStreamsPerAgent)
	}
	lm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), protocol.LogStreamTimeoutMin*time.Minute)
	cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", fmt.Sprint(tailLines), "-f", container)
	executor.SetSysProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("打开日志管道失败: %w", err)
	}
	cmd.Stderr = cmd.Stdout // 合流(Exec 的 StdoutPipe 已占 Stdout,共享写入端)
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("docker logs 启动失败: %w", err)
	}

	s := &logStream{id: streamID, container: container, cmd: cmd, cancel: cancel, lm: lm}
	s.lastActive.Store(time.Now().Unix())
	lm.mu.Lock()
	lm.streams[streamID] = s
	lm.mu.Unlock()
	go s.pump(stdout)
	slog.Info("日志跟随已开启", "stream", streamID, "container", container)
	return nil
}

func (lm *logManager) get(id string) *logStream {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	return lm.streams[id]
}

func (lm *logManager) remove(s *logStream) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if lm.streams[s.id] == s {
		delete(lm.streams, s.id)
	}
}

// StopAll 连接断开时全停(reason=conn_closed)。
func (lm *logManager) StopAll(reason string) {
	lm.mu.Lock()
	list := make([]*logStream, 0, len(lm.streams))
	for _, s := range lm.streams {
		list = append(list, s)
	}
	lm.mu.Unlock()
	for _, s := range list {
		s.stop(reason)
	}
}

// Count 当前流数。
func (lm *logManager) Count() int {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	return len(lm.streams)
}

// logStream 单个日志跟随。
type logStream struct {
	id         string
	container  string
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	lm         *logManager
	lastActive atomic.Int64
	stopOnce   sync.Once
}

// pump 读管道 → chunk 上报;管道关闭(停止/容器退出/docker 死)即终结。
func (s *logStream) pump(f io.ReadCloser) {
	buf := make([]byte, 16*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			s.lastActive.Store(time.Now().Unix())
			s.lm.emit(&protocol.Envelope{
				ID: newID(), Type: protocol.TypeLogStreamChunk, TS: time.Now().Unix(),
				Payload: protocol.LogStreamChunkPayload{
					StreamID: s.id,
					Data:     base64.StdEncoding.EncodeToString(buf[:n]),
				},
			})
		}
		if err != nil {
			s.stop("end")
			return
		}
	}
}

// stop 幂等终结:杀进程组 → 摘除 → stop 上报。
func (s *logStream) stop(reason string) {
	s.stopOnce.Do(func() {
		s.cancel()
		_ = s.cmd.Wait()
		s.lm.remove(s)
		s.lm.emit(&protocol.Envelope{
			ID: newID(), Type: protocol.TypeLogStreamStop, TS: time.Now().Unix(),
			Payload: protocol.LogStreamStopPayload{StreamID: s.id, Reason: reason},
		})
		slog.Info("日志跟随已停止", "stream", s.id, "container", s.container, "reason", reason)
	})
}
