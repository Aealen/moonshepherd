package probe

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// ---------- nginx ----------

func init() { Register("nginx", func() Probe { return &NginxProbe{} }) }

// NginxProbe nginx 探测：nginx -v / 进程 / 配置目录扫描（docs/01 §3.3）。
type NginxProbe struct{}

func (p *NginxProbe) Name() string { return "nginx" }

func (p *NginxProbe) Detect(ctx context.Context) ([]protocol.App, error) {
	a := protocol.App{AppType: "nginx", Name: "nginx", Status: protocol.AppUnknown}

	version, hasVersion := runVersion(ctx, "nginx", "-v")
	if hasVersion {
		a.Version = version
	}
	running := isProcessAlive(ctx, "nginx")
	confFiles := listConfFiles("/etc/nginx")

	switch {
	case running:
		a.Status = protocol.AppRunning
	case hasVersion || len(confFiles) > 0:
		a.Status = protocol.AppInstalled
	default:
		// 版本、进程、配置目录三路全空：明确 unknown，不臆造（docs/01 §3.3）
		return []protocol.App{{AppType: "nginx", Name: "nginx", Status: protocol.AppUnknown}}, nil
	}
	if len(confFiles) > 0 {
		a.Detail = map[string]any{"conf_files": confFiles, "conf_dir": "/etc/nginx"}
	}
	return []protocol.App{a}, nil
}

// ---------- caddy ----------

func init() { Register("caddy", func() Probe { return &CaddyProbe{} }) }

// CaddyProbe caddy 探测：caddy version / 进程 / /etc/caddy 扫描。
type CaddyProbe struct{}

func (p *CaddyProbe) Name() string { return "caddy" }

func (p *CaddyProbe) Detect(ctx context.Context) ([]protocol.App, error) {
	a := protocol.App{AppType: "caddy", Name: "caddy", Status: protocol.AppUnknown}

	version, hasVersion := runVersion(ctx, "caddy", "version")
	if hasVersion {
		a.Version = version
	}
	running := isProcessAlive(ctx, "caddy")
	confFiles := listConfFiles("/etc/caddy")

	switch {
	case running:
		a.Status = protocol.AppRunning
	case hasVersion || len(confFiles) > 0:
		a.Status = protocol.AppInstalled
	default:
		return []protocol.App{{AppType: "caddy", Name: "caddy", Status: protocol.AppUnknown}}, nil
	}
	if len(confFiles) > 0 {
		a.Detail = map[string]any{"conf_files": confFiles, "conf_dir": "/etc/caddy"}
	}
	return []protocol.App{a}, nil
}

// ---------- systemd ----------

func init() { Register("systemd", func() Probe { return &SystemdProbe{} }) }

// SystemdProbe systemd 服务探测（仅标准 Linux；BusyBox 环境探测失败即返回 unknown，docs/01 §3.3）。
type SystemdProbe struct{}

func (p *SystemdProbe) Name() string { return "systemd" }

func (p *SystemdProbe) Detect(ctx context.Context) ([]protocol.App, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--plain").Output()
	if err != nil {
		// 无 systemd（如 Unraid BusyBox）：unknown，不臆造
		return []protocol.App{{AppType: "systemd", Name: "systemd", Status: protocol.AppUnknown,
			Detail: map[string]any{"reason": "systemctl 不可用"}}}, nil
	}
	apps := []protocol.App{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit := strings.TrimSuffix(fields[0], ".service")
		desc := strings.TrimSpace(strings.Trim(strings.Join(fields[3:], " "), "•"))
		apps = append(apps, protocol.App{
			AppType: "systemd", Name: unit, Status: protocol.AppRunning,
			Detail: map[string]any{"description": desc},
		})
	}
	return apps, nil
}

// ---------- 共享辅助 ----------

// runVersion 执行取版本命令；nginx -v 输出在 stderr，caddy 在 stdout，都看。
func runVersion(ctx context.Context, bin string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	// "nginx version: nginx/1.24.0" → "1.24.0"；"v2.7.6" → "2.7.6"
	for _, prefix := range []string{"nginx version: nginx/", "nginx/", "v"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
		}
	}
	if s == "" {
		return "", false
	}
	if i := strings.IndexAny(s, " \n"); i > 0 {
		s = s[:i]
	}
	return s, true
}

// isProcessAlive pgrep 探测（BusyBox 有 pgrep）。
func isProcessAlive(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "pgrep", "-x", name).Run() == nil
}

func dirExists(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}

// listConfFiles 扫描配置目录（一层，至多 50 项）。
func listConfFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if len(files) >= 50 {
			break
		}
		if e.IsDir() {
			continue
		}
		files = append(files, e.Name())
	}
	return files
}
