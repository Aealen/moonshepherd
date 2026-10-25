package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/maxonphong/herd/internal/protocol"
)

func init() { Register("docker", func() Probe { return &DockerProbe{} }) }

// DockerProbe 探测 Docker 容器：优先 docker.sock HTTP API，降级 docker ps（docs/01 §3.3）。
type DockerProbe struct{}

func (p *DockerProbe) Name() string { return "docker" }

// dockerSockPaths 默认探测路径（容器形态挂载后生效）。
var dockerSockPaths = []string{"/var/run/docker.sock", "/run/docker.sock"}

func (p *DockerProbe) Detect(ctx context.Context) ([]protocol.App, error) {
	for _, sock := range dockerSockPaths {
		if apps, err := detectViaSock(ctx, sock); err == nil {
			return apps, nil
		}
	}
	return detectViaCLI(ctx)
}

// sockContainer Docker Engine API /containers/json 条目。
type sockContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		PrivatePort int `json:"PrivatePort"`
		PublicPort  int `json:"PublicPort"`
	} `json:"Ports"`
}

// cliContainer docker ps --format {{json .}} 条目。
type cliContainer struct {
	ID    string `json:"ID"`
	Names string `json:"Names"`
	Image string `json:"Image"`
	State string `json:"State"`
}

// detectViaSock unix socket 上的 Docker Engine API。
func detectViaSock(ctx context.Context, sock string) ([]protocol.App, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=1", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker api status %d", resp.StatusCode)
	}
	var containers []sockContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}
	apps := make([]protocol.App, 0, len(containers))
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		var portStrs []string
		for _, p := range c.Ports {
			if p.PublicPort != 0 {
				portStrs = append(portStrs, fmt.Sprintf("%d:%d", p.PublicPort, p.PrivatePort))
			}
		}
		apps = append(apps, containerApp(c.ID, name, c.Image, c.State, c.Labels, strings.Join(portStrs, ",")))
	}
	return apps, nil
}

// detectViaCLI 降级：docker ps -a --format {{json .}}（BusyBox 环境兜底）。
func detectViaCLI(ctx context.Context) ([]protocol.App, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, err
	}
	var apps []protocol.App
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var c cliContainer
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		apps = append(apps, containerApp(c.ID, c.Names, c.Image, c.State, nil, ""))
	}
	return apps, nil
}

func containerApp(id, name, image, state string, labels map[string]string, ports string) protocol.App {
	if name == "" {
		name = shortID(id)
	}
	status := protocol.AppUnknown
	switch state {
	case "running":
		status = protocol.AppRunning
	case "exited", "dead", "created", "paused", "restarting":
		status = protocol.AppStopped
	}
	detail := map[string]any{"image": image}
	if labels["com.docker.compose.project"] != "" {
		detail["compose_project"] = labels["com.docker.compose.project"]
	}
	if ports != "" {
		detail["ports"] = ports
	}
	if id != "" {
		detail["id"] = id
	}
	return protocol.App{AppType: "docker", Name: name, Version: image, Status: status, Detail: detail}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
