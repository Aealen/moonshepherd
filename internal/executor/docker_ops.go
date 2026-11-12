// docker.op 容器生命周期白名单操作(docs/01 §3.4;docs/03 P1)。
// 执行层优先 Docker Engine API(unix socket,与探测器共用降级探测链),
// 无 socket 时降级 docker CLI;白名单在此硬校验——面板传什么都越不过这道闸。
package executor

import (
	"bytes"
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
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart, protocol.DockerOpLogs, protocol.DockerOpUpdate:
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
	case protocol.DockerOpUpdate:
		return c.update(ctx, p)
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

// ---------- update:拉新镜像 + 按原配置重建(restart 不换镜像,更新必须 recreate) ----------
//
// 流程:inspect 取全量配置 → 旧容器改名让位 → 停旧 → pull → 复用 Config/HostConfig/NetworkingConfig
// 原样 create 新容器 → 按原运行态起新 → 删旧。任一步失败即回滚(删新/旧名还原/按原运行态拉起),
// 参数零丢失——全程走 Engine API 的配置对象,不拼 docker run 命令行(Docker 本就不存原始命令行)。

// dockerInspect 通用 GET + JSON 解码 helper。
func (c *dockerClient) dockerInspect(ctx context.Context, path string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// dockerDo 通用请求(POST/DELETE + 可选 JSON body),>=400 返回错误。
func (c *dockerClient) dockerDo(ctx context.Context, method, path string, body any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 8192)) // drain:连接复用
	return resp.StatusCode, nil
}

// update 拉最新镜像并按原配置重建容器。
func (c *dockerClient) update(ctx context.Context, p protocol.DockerOpPayload) Result {
	// 1. 全量 inspect:Config/HostConfig/NetworkingConfig 原样透传重建
	var insp struct {
		Name   string          `json:"Name"`
		Image  string          `json:"Image"` // 创建时解析的 ImageID
		Config json.RawMessage `json:"Config"`
		Host   json.RawMessage `json:"HostConfig"`
		Net    struct {
			NetworkingConfig json.RawMessage `json:"NetworkingConfig"`
		} `json:"NetworkSettings"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := c.dockerInspect(ctx, "/containers/"+url.PathEscape(p.Container)+"/json", &insp); err != nil {
		return fail(nil, "inspect 容器失败: "+err.Error())
	}
	name := strings.TrimPrefix(insp.Name, "/")
	if name == "" {
		return fail(nil, "容器名为空,拒绝重建")
	}
	oldID := p.Container // 对旧容器一律用 ID 操作(rename 不影响 ID)
	image := p.Image
	if image == "" {
		image = insp.Image
	}

	// 回滚:尽力而为序列,各步幂等无害(未到的阶段 404/409 被忽略)
	rollback := func(stage string) Result {
		_, _ = c.dockerDo(ctx, http.MethodDelete, "/containers/"+url.PathEscape(name)+"?force=1", nil) // 删可能已创建的新容器(原名)
		_, _ = c.dockerDo(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/rename?name="+url.QueryEscape(name), nil)
		if insp.State.Running {
			_, _ = c.dockerDo(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/start", nil)
		}
		return fail(nil, "update 在「"+stage+"」阶段失败,已回滚:旧容器原样保留,新镜像已在本机(下次重建即生效)")
	}

	// 2. 旧容器改名让位(腾出原名给新容器;先改名后停,即使 restart 策略在停后拉起旧容器也不会与新容器撞名)
	oldName := name + "_old"
	if code, err := c.dockerDo(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/rename?name="+url.QueryEscape(oldName), nil); err != nil || code >= 400 {
		return fail(nil, "重命名旧容器失败: "+err.Error())
	}

	// 3. 停旧容器(手动 stop 不触发 restart 策略;此处停止是重建流程的一部分)
	if insp.State.Running {
		if code, err := c.dockerDo(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/stop?t=10", nil); err != nil || code >= 400 {
			return rollback("停止旧容器")
		}
	}

	// 4. 拉最新镜像
	if r := c.pull(ctx, protocol.DockerOpPayload{Op: protocol.DockerOpPull, Image: image}); r.Reply.Status != protocol.ResultSuccess {
		return rollback("拉取镜像")
	}

	// 5. 复用配置对象创建新容器(端口/挂载/环境/限制/网络全部原样)
	createBody := map[string]any{
		"name":             name,
		"Config":           json.RawMessage(insp.Config),
		"HostConfig":       json.RawMessage(insp.Host),
		"NetworkingConfig": json.RawMessage(insp.Net.NetworkingConfig),
	}
	if code, err := c.dockerDo(ctx, http.MethodPost, "/containers/create", createBody); err != nil || code >= 400 {
		return rollback("创建新容器")
	}

	// 6. 按原运行态启动新容器
	if insp.State.Running {
		if code, err := c.dockerDo(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start", nil); err != nil || code >= 400 {
			return rollback("启动新容器")
		}
	}

	// 7. 删除旧容器(删旧失败不判失败:新容器已在原名上运行,仅提示手动清理)
	msg := "容器重建完成,新镜像已生效;端口/挂载/环境变量等全部原配置保留"
	if code, err := c.dockerDo(ctx, http.MethodDelete, "/containers/"+url.PathEscape(oldID)+"?v=false", nil); err != nil || code >= 400 {
		msg += ";注意:旧容器 " + oldName + " 清理失败,请手动删除"
	}
	return Result{Reply: protocol.CommandResultPayload{Status: protocol.ResultSuccess, Stdout: msg}}
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
	case protocol.DockerOpUpdate:
		// CLI 降级不支持 recreate:拼 docker run 字符串会丢参数,宁可拒绝(宁可不动,不可失联)
		return fail(nil, "update 需 Docker Engine API(docker.sock 不可达时不可用),请检查 Agent 运行环境")
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
