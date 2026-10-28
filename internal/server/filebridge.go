// fileBridge:MCP/内部调用方的 file.read / file.op 同步等待桥
// (复用 gateway 的 ack → events 转推路径,在此处多播一份给 pending)。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// hubSender 发送依赖注入(newFileBridge 后由 Server 设置)。
type fileBridgeT struct {
	send    func(nodeID string, env *protocol.Envelope) bool
	mu      sync.Mutex
	pending map[string]chan any
}

func newFileBridge() *fileBridgeT {
	return &fileBridgeT{pending: map[string]chan any{}}
}

func (f *fileBridgeT) newRID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return "fb_" + hex.EncodeToString(b)
}

func (f *fileBridgeT) sendRead(nodeID, path string) (string, error) {
	rid := f.newRID()
	if !f.send(nodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeFileRead, TS: time.Now().Unix(),
		Payload: protocol.FileReadPayload{RequestID: rid, Path: path}}) {
		return "", fmt.Errorf("节点不在线")
	}
	return rid, nil
}

func (f *fileBridgeT) sendOp(nodeID string, p protocol.FileOpPayload) (string, error) {
	rid := f.newRID()
	p.RequestID = rid
	if !f.send(nodeID, &protocol.Envelope{ID: newMsgID(), Type: protocol.TypeFileOp, TS: time.Now().Unix(), Payload: p}) {
		return "", fmt.Errorf("节点不在线")
	}
	return rid, nil
}

// wait 阻塞等 ack(resolve 由 deliver 驱动);nil = 超时。
func (f *fileBridgeT) wait(ctx context.Context, rid string, timeout time.Duration) any {
	ch := make(chan any, 1)
	f.mu.Lock()
	f.pending[rid] = ch
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.pending, rid)
		f.mu.Unlock()
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case v := <-ch:
		return v
	case <-t.C:
		return nil
	case <-ctx.Done():
		return nil
	}
}

// deliver gateway ack 转推时多播(request_id 匹配则唤醒等待方)。
func (f *fileBridgeT) deliver(rid string, payload any) {
	f.mu.Lock()
	ch := f.pending[rid]
	delete(f.pending, rid)
	f.mu.Unlock()
	if ch != nil {
		ch <- payload
	}
}
