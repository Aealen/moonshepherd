package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// TestDockerOpWhitelist 白名单是安全闸:清单外操作一律拒绝(不触达任何执行层)。
func TestDockerOpWhitelist(t *testing.T) {
	cases := []struct {
		name    string
		payload protocol.DockerOpPayload
		wantErr string // 空 = 应成功(结果层;本地无 docker 时为失败但非白名单错误)
	}{
		{"exec 逃逸拒绝", protocol.DockerOpPayload{Op: "exec", Container: "x", Image: "alpine"}, "白名单"},
		{"rm 逃逸拒绝", protocol.DockerOpPayload{Op: "rm", Container: "x"}, "白名单"},
		{"run 逃逸拒绝", protocol.DockerOpPayload{Op: "run", Image: "alpine"}, "白名单"},
		{"空 op 拒绝", protocol.DockerOpPayload{Container: "x"}, "白名单"},
		{"restart 缺容器拒绝", protocol.DockerOpPayload{Op: protocol.DockerOpRestart}, "缺少 container"},
		{"logs 缺容器拒绝", protocol.DockerOpPayload{Op: protocol.DockerOpLogs}, "缺少 container"},
		{"pull 缺容器缺镜像拒绝", protocol.DockerOpPayload{Op: protocol.DockerOpPull}, "container 或 image"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(c.payload)
			res := (&E{}).dockerOp(context.Background(), raw, 30)
			if res.Reply.Status != protocol.ResultFailed {
				t.Fatalf("应失败,实得 %+v", res.Reply)
			}
			if !strings.Contains(res.Reply.Error, c.wantErr) {
				t.Fatalf("错误应含 %q,实得: %s", c.wantErr, res.Reply.Error)
			}
		})
	}
}

// TestDockerOpPayloadValid 合法 payload 走到执行层(本地无 docker → 报执行层错误而非校验错误)。
func TestDockerOpPayloadValid(t *testing.T) {
	raw, _ := json.Marshal(protocol.DockerOpPayload{Op: protocol.DockerOpRestart, Container: "gitea"})
	res := (&E{}).dockerOp(context.Background(), raw, 30)
	if res.Reply.Status != protocol.ResultFailed {
		return // 本机有 docker 且真有 gitea 容器:成功也合理
	}
	// 错误必须来自执行层(API/CLI),不能是白名单/payload 校验
	for _, bad := range []string{"白名单", "缺少 container", "非法"} {
		if strings.Contains(res.Reply.Error, bad) {
			t.Fatalf("合法 payload 不应被校验拦截,实得: %s", res.Reply.Error)
		}
	}
}

// TestDemuxDockerLogs Engine API 日志流帧头剥离。
func TestDemuxDockerLogs(t *testing.T) {
	frame := func(stream byte, payload string) []byte {
		n := len(payload)
		f := []byte{stream, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
		return append(f, payload...)
	}
	var raw []byte
	raw = append(raw, frame(1, "hello ")...)
	raw = append(raw, frame(2, "world")...)
	raw = append(raw, frame(1, "!\n")...)
	if got := string(demuxDockerLogs(raw)); got != "hello world!\n" {
		t.Fatalf("demux 结果异常: %q", got)
	}
	// 帧异常(裸文本):原样返回
	if got := string(demuxDockerLogs([]byte("plain text"))); got != "plain text" {
		t.Fatalf("裸文本应原样返回,实得 %q", got)
	}
}
