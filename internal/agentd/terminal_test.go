package agentd

import (
	"encoding/base64"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// collectEnv 构造收集型 emit。
func collectEmitted(mu *sync.Mutex, out *[]*protocol.Envelope) func(*protocol.Envelope) {
	return func(env *protocol.Envelope) {
		mu.Lock()
		defer mu.Unlock()
		*out = append(*out, env)
	}
}

func envPayload[P any](t *testing.T, env *protocol.Envelope) P {
	t.Helper()
	raw, _ := json.Marshal(env.Payload)
	var p P
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("payload 解析失败: %v", err)
	}
	return p
}

// TestTerminalSessionLifecycle 全链路:open → 输入回显 → 输出上报 → close 摘除。
// PTY 行为依赖真实终端子系统,非 Linux 平台跳过(CI 为 Linux)。
func TestTerminalSessionLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("PTY 交互测试仅 Linux,当前 %s 跳过", runtime.GOOS)
	}
	var mu sync.Mutex
	var out []*protocol.Envelope
	tm := newTerminalManager()
	tm.emit = collectEmitted(&mu, &out)
	defer tm.CloseAll("test_end")

	if err := tm.Open("t_test1", 24, 80); err != nil {
		t.Fatalf("open: %v", err)
	}
	if tm.Count() != 1 {
		t.Fatalf("会话数应为 1,实得 %d", tm.Count())
	}

	// 输入命令;shell 回显 + 命令输出都经 PTY 上报
	s := tm.get("t_test1")
	if s == nil {
		t.Fatal("会话不存在")
	}
	deadline := time.Now().Add(8 * time.Second)
	var gotMarker bool
	_ = s.Write([]byte("echo herd_marker_7f3d\r"))
	for time.Now().Before(deadline) {
		mu.Lock()
		concat := ""
		for _, env := range out {
			if env.Type == protocol.TypeSessionOutput {
				p := envPayload[protocol.SessionIOPayload](t, env)
				b, err := base64.StdEncoding.DecodeString(p.Data)
				if err != nil {
					continue
				}
				concat += string(b)
			}
		}
		mu.Unlock()
		if strings.Contains(concat, "herd_marker_7f3d") {
			gotMarker = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gotMarker {
		t.Fatal("8s 内未在终端输出中观察到命令回显")
	}

	// 关闭:摘除 + close 上报
	s.close("user_close")
	if tm.Count() != 0 {
		t.Fatalf("close 后会话数应为 0,实得 %d", tm.Count())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, env := range out {
		if env.Type == protocol.TypeSessionClose {
			if p := envPayload[protocol.SessionClosePayload](t, env); p.SessionID == "t_test1" && p.Reason == "user_close" {
				return
			}
		}
	}
	t.Fatal("未收到 user_close 的 session.close 上报")
}

// TestTerminalSessionLimit 并发上限:第 MaxSessionsPerAgent+1 个被拒。
func TestTerminalSessionLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("PTY 测试仅 Linux,当前 %s 跳过", runtime.GOOS)
	}
	var mu sync.Mutex
	var out []*protocol.Envelope
	tm := newTerminalManager()
	tm.emit = collectEmitted(&mu, &out)
	defer tm.CloseAll("test_end")

	for i := 0; i < protocol.MaxSessionsPerAgent; i++ {
		if err := tm.Open("t_lim"+string(rune('a'+i)), 24, 80); err != nil {
			t.Fatalf("第 %d 个会话应成功: %v", i+1, err)
		}
	}
	if err := tm.Open("t_over", 24, 80); err == nil {
		t.Fatal("超限会话应被拒绝")
	}
}

// TestTerminalOpenIdempotent 重复 open 同一 session_id 幂等忽略。
func TestTerminalOpenIdempotent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("PTY 测试仅 Linux,当前 %s 跳过", runtime.GOOS)
	}
	var mu sync.Mutex
	var out []*protocol.Envelope
	tm := newTerminalManager()
	tm.emit = collectEmitted(&mu, &out)
	defer tm.CloseAll("test_end")

	if err := tm.Open("t_dup", 24, 80); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := tm.Open("t_dup", 24, 80); err != nil {
		t.Fatalf("重复 open 应幂等成功: %v", err)
	}
	if tm.Count() != 1 {
		t.Fatalf("重复 open 后会话数应为 1,实得 %d", tm.Count())
	}
}
