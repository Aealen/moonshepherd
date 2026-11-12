package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
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

// mockDockerDaemon 起 unix socket mock Engine API,记录调用序列(socket/listener 经 t.Cleanup 收尾)。
// 路由:按 method+path 分发;failCreate=true 时 /containers/create 返回 500。
func mockDockerDaemon(t *testing.T, failCreate bool) *[]string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("监听 unix socket: %v", err)
	}
	calls := &[]string{}
	var createBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/containers/libsage/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Name":"/libsage","Image":"gitea/gitea:latest",
			"Config":{"Image":"gitea/gitea:latest","Env":["PATH=/usr/bin","TZ=Asia/Shanghai"],"Labels":{}},
			"HostConfig":{"RestartPolicy":{"Name":"unless-stopped"},"PortBindings":{"3000/tcp":[{"HostIp":"0.0.0.0","HostPort":"7111"}]}},
			"NetworkSettings":{"NetworkingConfig":{"EndpointsConfig":{"bridge":{"IPAMConfig":null}}}},
			"State":{"Running":true}}`)
	})
	mux.HandleFunc("/containers/libsage/rename", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "rename:"+r.URL.Query().Get("name"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("/containers/libsage/stop", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "stop")
		w.WriteHeader(204)
	})
	mux.HandleFunc("/containers/libsage/start", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "start")
		w.WriteHeader(204)
	})
	mux.HandleFunc("/images/create", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "pull")
		fmt.Fprintln(w, `{"status":"Status: Downloaded newer image"}`)
	})
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if failCreate {
			*calls = append(*calls, "create:500")
			w.WriteHeader(500)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
			t.Errorf("create body 非法: %v", err)
		}
		*calls = append(*calls, "create")
		w.WriteHeader(201)
		fmt.Fprintln(w, `{"Id":"newid123"}`)
	})
	mux.HandleFunc("/containers/libsage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.URL.Query().Get("force") == "1" {
				*calls = append(*calls, "delete:force")
			} else {
				*calls = append(*calls, "delete-old")
			}
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("/containers/libsage_old", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "delete-old-rollback")
		w.WriteHeader(204)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l) //nolint:errcheck — 测试 mock,进程退出即收尾

	orig := dockerSocks
	dockerSocks = []string{sock}
	t.Cleanup(func() {
		dockerSocks = orig
		srv.Close()
		l.Close()
	})
	return calls
}

// TestDockerUpdateRecreate recreate 全流程:改名让位→停旧→拉镜像→复用配置 create→起新→删旧。
func TestDockerUpdateRecreate(t *testing.T) {
	calls := mockDockerDaemon(t, false)

	raw, _ := json.Marshal(protocol.DockerOpPayload{Op: protocol.DockerOpUpdate, Container: "libsage"})
	res := (&E{}).dockerOp(context.Background(), raw, 30)
	if res.Reply.Status != protocol.ResultSuccess {
		t.Fatalf("应成功,实得: %+v", res.Reply)
	}
	want := []string{"rename:libsage_old", "stop", "pull", "create", "start", "delete-old"}
	if len(*calls) != len(want) {
		t.Fatalf("调用序列长度 %d ≠ %d: %v", len(*calls), len(want), *calls)
	}
	for i, w := range want {
		if !strings.HasPrefix((*calls)[i], w) {
			t.Fatalf("第 %d 步应为 %q,实得 %q;全序列 %v", i, w, (*calls)[i], *calls)
		}
	}
}

// TestDockerUpdateRollback create 失败时回滚:删同名新容器→旧容器名还原→按原运行态拉起。
func TestDockerUpdateRollback(t *testing.T) {
	calls := mockDockerDaemon(t, true)

	raw, _ := json.Marshal(protocol.DockerOpPayload{Op: protocol.DockerOpUpdate, Container: "libsage"})
	res := (&E{}).dockerOp(context.Background(), raw, 30)
	if res.Reply.Status != protocol.ResultFailed {
		t.Fatalf("create 失败应整体失败,实得: %+v", res.Reply)
	}
	if !strings.Contains(res.Reply.Error, "回滚") {
		t.Fatalf("错误应说明已回滚,实得: %s", res.Reply.Error)
	}
	joined := strings.Join(*calls, ",")
	for _, step := range []string{"delete:force", "rename:libsage", "start"} {
		if !containsCall(*calls, step) {
			t.Fatalf("回滚应包含 %q,全序列 %v", step, *calls)
		}
	}
	_ = joined
}

func containsCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
