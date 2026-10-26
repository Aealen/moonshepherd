// docker.op 容器生命周期白名单操作(docs/01 §3.4;docs/03 P1)。
// 执行层优先 Docker Engine API(unix socket,与探测器共用降级探测链),
// 无 socket 时降级 docker CLI;白名单在此硬校验——面板传什么都越不过这道闸。
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// dockerSocks 探测顺序(与 probe/docker.go 一致:默认 socket → rootless → CLI)。
var dockerSocks = []string{
	"/var/run/docker.sock",
	"$XDG_RUNTIME_DIR/docker.sock",
	"$HOME/.docker/run/docker.sock",
}

// DockerOp 执行白名单容器操作。
func (e *E) dockerOp(ctx context.Context, payload json.RawMessage, timeoutS int) Result {
	var p protocol.DockerOpPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fail(payload, "docker.op payload 非法")
	}
	switch p.Op {
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart, protocol.DockerOpLogs:
		if p.Container == "" {
			return fail(payload, "docker.op 缺少 container")
		}
	case protocol.DockerOpPull:
		// pull 允许不带 container(直接拉 image)
	default:
		return fail(payload, "docker.op 操作不在白名单: "+p.Op)
	}
	if p.Op == protocol.DockerOpPull && p.Container == "" && p.Image == "" {
		return fail(payload, "docker.op pull 需要 container 或 image")
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	defer cancel()

	if c := newDockerClient(); c != nil {
		return c.op(ctx, p)
	}
	return dockerOpCLI(ctx, p)
}

// ---------- Engine API 客户端 ----------

type dockerClient struct {
	httpc *http.Client
	base  string
}

// newDockerClient 按 dockerSocks 顺序找可用 socket;找不到返回 nil(降级 CLI)。
func newDockerClient() *dockerClient {
	for _, s := range dockerSocks {
		path := expandEnv(s)
		if path == "" {
			continue
		}
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		}
		c := &dockerClient{httpc: &http.Client{Transport: tr, Timeout: 5 * time.Second}, base: "http://docker"}
		// 连通性探测(_ping):失败换下一个
		if resp, err := c.httpc.Get(c.base + "/_ping"); err == nil {
			resp.Body.Close()
			return c
		}
	}
	return nil
}

func expandEnv(s string) string {
	if strings.HasPrefix(s, "$XDG_RUNTIME_DIR") {
		return strings.Replace(s, "$XDG_RUNTIME_DIR", os.Getenv("XDG_RUNTIME_DIR"), 1)
	}
	if strings.HasPrefix(s, "$HOME") {
		return strings.Replace(s, "$HOME", os.Getenv("HOME"), 1)
	}
	return s
}

func (c *dockerClient) op(ctx context.Context, p protocol.DockerOpPayload) Result {
	switch p.Op {
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart:
		return c.lifecycle(ctx, p)
	case protocol.DockerOpLogs:
		return c.logs(ctx, p)
	case protocol.DockerOpPull:
		return c.pull(ctx, p)
	}
	return fail(nil, "unreachable")
}

// lifecycle POST /containers/{id}/{action}。stop 带 t=10 优雅等待。
func (c *dockerClient) lifecycle(ctx context.Context, p protocol.DockerOpPayload) Result {
	action := map[string]string{
		protocol.DockerOpStart: "start", protocol.DockerOpStop: "stop", protocol.DockerOpRestart: "restart",
	}[p.Op]
	u := fmt.Sprintf("%s/containers/%s/%s?t=%d", c.base, url.PathEscape(p.Container), action, 10)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fail(nil, "Docker API 请求失败: "+err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return fail(nil, fmt.Sprintf("Docker API %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultSuccess,
		Stdout: fmt.Sprintf("%s %s 完成(HTTP %d)", p.Container, action, resp.StatusCode)}}
}

// logs GET /containers/{id}/logs?tail=N(stdout+stderr 合流,非 TTY 格式)。
func (c *dockerClient) logs(ctx context.Context, p protocol.DockerOpPayload) Result {
	if p.TailLines <= 0 {
		p.TailLines = 100
	}
	u := fmt.Sprintf("%s/containers/%s/logs?tail=%d&stdout=1&stderr=1", c.base, url.PathEscape(p.Container), p.TailLines)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fail(nil, "Docker API 请求失败: "+err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fail(nil, fmt.Sprintf("Docker API %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxPayloadBytes+64*1024))
	out := demuxDockerLogs(raw)
	if len(out) > protocol.MaxPayloadBytes {
		out = out[:protocol.MaxPayloadBytes]
		return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultSuccess, Stdout: string(out), Truncated: true}}
	}
	return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultSuccess, Stdout: string(out)}}
}

// pull POST /images/create?fromImage=…。image 缺省取容器当前镜像。
func (c *dockerClient) pull(ctx context.Context, p protocol.DockerOpPayload) Result {
	image := p.Image
	if image == "" {
		inspect, err := c.containerImage(ctx, p.Container)
		if err != nil {
			return fail(nil, "取容器当前镜像失败: "+err.Error())
		}
		image = inspect
	}
	u := fmt.Sprintf("%s/images/create?fromImage=%s", c.base, url.QueryEscape(image))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fail(nil, "Docker API 请求失败: "+err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fail(nil, fmt.Sprintf("Docker API %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	// 拉取进度流读到 eof;尾部截取关键行(层状态/Pulling from)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxPayloadBytes))
	var last string
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Status string `json:"status"`
			ID     string `json:"id"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Status != "" {
			if strings.HasPrefix(ev.Status, "Pulling from") || ev.Status == "Downloaded" ||
				ev.Status == "Pull complete" || strings.HasPrefix(ev.Status, "Status:") {
				last = line
			}
		}
	}
	return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultSuccess,
		Stdout: image + " 拉取完成;" + strconv.Itoa(len(body)) + "B 进度;" + last}}
}

// containerImage GET /containers/{id}/json → Config.Image。
func (c *dockerClient) containerImage(ctx context.Context, container string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/containers/"+url.PathEscape(container)+"/json", nil)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var insp struct {
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&insp); err != nil {
		return "", err
	}
	return insp.Config.Image, nil
}

// demuxDockerLogs 剥离 Engine API 非日志流的 8 字节帧头(stream/size)。
// 参考 docs.docker api: [type,0,0,0,size(4be)]payload…;容错:头非法时原样返回。
func demuxDockerLogs(raw []byte) []byte {
	var out []byte
	for len(raw) >= 8 {
		size := int(raw[4])<<24 | int(raw[5])<<16 | int(raw[6])<<8 | int(raw[7])
		if size < 0 || 8+size > len(raw) {
			return raw // 帧异常:按原始数据处理
		}
		out = append(out, raw[8:8+size]...)
		raw = raw[8+size:]
	}
	return out
}

// ---------- CLI 降级 ----------

func dockerOpCLI(ctx context.Context, p protocol.DockerOpPayload) Result {
	var args []string
	switch p.Op {
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart:
		args = []string{p.Op, p.Container}
	case protocol.DockerOpLogs:
		if p.TailLines <= 0 {
			p.TailLines = 100
		}
		args = []string{"logs", "--tail", strconv.Itoa(p.TailLines), p.Container}
	case protocol.DockerOpPull:
		image := p.Image
		if image == "" {
			insp := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Config.Image}}", p.Container)
			out, err := insp.CombinedOutput()
			if err != nil {
				return fail(nil, "取容器镜像失败: "+strings.TrimSpace(string(out)))
			}
			image = strings.TrimSpace(string(out))
		}
		args = []string{"pull", image}
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	SetSysProcAttr(cmd)
	out, err := cmd.CombinedOutput()
	res := protocol.CommandResultPayload{Status: protocol.ResultSuccess, Stdout: limitOut(string(out))}
	if err != nil {
		res.Status = protocol.ResultFailed
		res.Stderr = limitOut(string(out))
		res.Error = err.Error()
	}
	return Result{Reply: res}
}

func limitOut(s string) string {
	if len(s) > protocol.MaxPayloadBytes {
		return s[:protocol.MaxPayloadBytes]
	}
	return s
}
