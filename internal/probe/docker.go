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

	"github.com/aealen/moonshepherd/internal/protocol"
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
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

// inspectDetail /containers/{id}/json 的关注子集（docs/06 应用详情）。
type inspectDetail struct {
	Config struct {
		Env        []string          `json:"Env"`
		Labels     map[string]string `json:"Labels"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []mountEntryRaw `json:"Mounts"`
}

// mountEntryRaw Engine API 挂载条目(与 mountEntry 字段同名)。
type mountEntryRaw struct {
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
}

// portEntry 详情端口条目。
type portEntry struct {
	IP      string `json:"ip"`
	Public  int    `json:"public"`
	Private int    `json:"private"`
	Type    string `json:"type"`
}

// mountEntry 详情挂载条目。
type mountEntry struct {
	Source string `json:"source"`
	Dest   string `json:"dest"`
	RW     bool   `json:"rw"`
	Mode   string `json:"mode,omitempty"`
}

// netEntry 详情网络条目。
type netEntry struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// cliContainer docker ps --format {{json .}} 条目。
type cliContainer struct {
	ID     string            `json:"ID"`
	Names  string            `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
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
		detail := map[string]any{"image": c.Image}
		// 启动方式:带 compose 项目标签 = Docker Compose,否则视为 docker run
		if _, compose := c.Labels["com.docker.compose.project"]; compose {
			detail["run_mode"] = "compose"
		} else {
			detail["run_mode"] = "run"
		}
		if len(c.Ports) > 0 {
			ports := make([]portEntry, 0, len(c.Ports))
			for _, p := range c.Ports {
				ports = append(ports, portEntry{IP: p.IP, Public: p.PublicPort, Private: p.PrivatePort, Type: p.Type})
			}
			detail["ports"] = ports
		}
		for k, v := range c.Labels {
			if strings.HasPrefix(k, "com.docker.compose.") && v != "" {
				detail[composeKey(k)] = v
			}
		}
		if c.ID != "" {
			detail["id"] = c.ID
			// inspect 补全：挂载 / 网络 / 环境变量（失败降级，保持 list 级详情）
			if insp := inspectContainer(ctx, client, c.ID); insp != nil {
				// 启动命令与重启策略(容器进程定义,如实展示不拼 docker run 串)
				if len(insp.Config.Entrypoint) > 0 {
					detail["entrypoint"] = insp.Config.Entrypoint
				}
				if len(insp.Config.Cmd) > 0 {
					detail["cmd"] = insp.Config.Cmd
				}
				if rp := insp.HostConfig.RestartPolicy.Name; rp != "" && rp != "no" {
					detail["restart"] = rp
				}
				if len(insp.Mounts) > 0 {
					ms := make([]mountEntry, 0, len(insp.Mounts))
					for _, m := range insp.Mounts {
						ms = append(ms, mountEntry{Source: m.Source, Dest: m.Destination, RW: m.RW, Mode: m.Mode})
					}
					detail["mounts"] = ms
					// nginx/caddy 容器:配置卷挂载源即纳管候选(docs/01 §6 配置发现)
					if cands := containerConfCandidates(c.Image, insp.Mounts); len(cands) > 0 {
						detail["config_candidates"] = cands
					}
				}
				var nets []netEntry
				for nname, n := range insp.NetworkSettings.Networks {
					nets = append(nets, netEntry{Name: nname, IP: n.IPAddress})
				}
				if len(nets) > 0 {
					detail["networks"] = nets
				}
				if len(insp.Config.Env) > 0 {
					detail["env"] = insp.Config.Env
				}
			}
		}
		apps = append(apps, protocol.App{
			AppType: "docker", Name: name, Version: c.Image,
			Status: containerState(c.State), Detail: detail,
		})
	}
	return apps, nil
}

// inspectContainer 拉单个容器完整信息；任何失败返回 nil（探测降级原则：list 数据仍在）。
func inspectContainer(ctx context.Context, client *http.Client, id string) *inspectDetail {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+id+"/json", nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var d inspectDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil
	}
	return &d
}

// composeKey com.docker.compose.project → compose_project。
func composeKey(label string) string {
	return strings.ReplaceAll(strings.TrimPrefix(label, "com.docker.compose."), ".", "_")
}

func containerState(state string) string {
	switch state {
	case "running":
		return protocol.AppRunning
	case "exited", "dead", "created", "paused", "restarting":
		return protocol.AppStopped
	}
	return protocol.AppUnknown
}

// detectViaCLI 降级：docker ps -a --format {{json .}}（BusyBox 环境兜底；无端口/挂载详情，如实少报）。
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
		name := c.Names
		if name == "" {
			name = shortID(c.ID)
		}
		detail := map[string]any{"image": c.Image, "id": c.ID, "run_mode": "run"}
		if _, compose := c.Labels["com.docker.compose.project"]; compose {
			detail["run_mode"] = "compose"
			for k, v := range c.Labels {
				if strings.HasPrefix(k, "com.docker.compose.") && v != "" {
					detail[composeKey(k)] = v
				}
			}
		}
		apps = append(apps, protocol.App{
			AppType: "docker", Name: name, Version: c.Image,
			Status: containerState(c.State),
			Detail: detail,
		})
	}
	return apps, nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
