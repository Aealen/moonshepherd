package executor

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// TestSelfUpdateGuards 自更新安全闸:非法 payload 一律拒绝(不触达下载)。
// 校验顺序:payload 完整性 → https 限制 → 平台限制 → 定位自身 → 下载…
func TestSelfUpdateGuards(t *testing.T) {
	cases := []struct {
		name    string
		payload protocol.AgentUpdatePayload
		wantErr string
	}{
		{"缺 version", protocol.AgentUpdatePayload{DownloadURL: "https://x/herd", SHA256: strings.Repeat("a", 64)}, "payload 非法"},
		{"缺 sha256", protocol.AgentUpdatePayload{Version: "v9", DownloadURL: "https://x/herd"}, "payload 非法"},
		{"sha256 长度错", protocol.AgentUpdatePayload{Version: "v9", DownloadURL: "https://x/herd", SHA256: "abc"}, "payload 非法"},
		{"http 明文拒绝", protocol.AgentUpdatePayload{Version: "v9", DownloadURL: "http://evil.example/herd", SHA256: strings.Repeat("a", 64)}, "https"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := SelfUpdate(context.Background(), c.payload, 30)
			if err == nil {
				t.Fatal("应被拒绝")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("错误应含 %q,实得: %v", c.wantErr, err)
			}
		})
	}
}

// TestDetectSupervisor 拉起者探测:非 Linux 下不 panic,返回合法值。
func TestDetectSupervisor(t *testing.T) {
	s := DetectSupervisor()
	if s != protocol.UpdateSupervisorSystemd && s != protocol.UpdateSupervisorBare {
		t.Fatalf("非法形态值: %s", s)
	}
}

// TestSelfUpdatePlatform 非 Linux 平台直接拒绝(不执行下载)。
func TestSelfUpdatePlatform(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux 平台不适用")
	}
	err := SelfUpdate(context.Background(), protocol.AgentUpdatePayload{
		Version: "v9", DownloadURL: "https://x/herd", SHA256: strings.Repeat("a", 64),
	}, 30)
	if err == nil || !strings.Contains(err.Error(), "仅支持 Linux") {
		t.Fatalf("非 Linux 应拒绝,实得: %v", err)
	}
}
