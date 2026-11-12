// 配置文件发现增强(docs/01 §6 前置):为 nginx/caddy 产出可纳管的配置候选清单。
// 三条发现链,结果统一并入 App.Detail["config_candidates"]:
//  1. nginx -T ——最准:nginx 自己交代实际加载的全部配置(include 全展开);
//  2. 目录扫描兜底(原有 listConfFiles,非标准路径的补充靠候选合并);
//  3. Docker 容器挂载推断(docker 探测器上报的 mounts;镜像名含 nginx/caddy
//     的容器,其配置卷挂载源即候选)——由探测侧合并,见 docker.go。
package probe

import (
	"context"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ConfCandidate 配置候选(面板「配置」页一键纳管的数据源)。
type ConfCandidate struct {
	Path   string `json:"path"`           // 绝对路径
	Source string `json:"source"`         // nginx -T / dir-scan / docker-mount
	Main   bool   `json:"main,omitempty"` // 主配置(nginx -T 的首个文件)
}

// nginxTFiles nginx -T 解析实际加载的配置文件清单。
// 输出形如(每段配置头):"nginx: [emerg]..." 错误即失败;
// 正常头:"# configuration file /etc/nginx/nginx.conf:"。
var confFileHeader = regexp.MustCompile(`^#\s*configuration file\s+(\S+):\s*$`)

func nginxTFiles(ctx context.Context) (files []ConfCandidate, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nginx", "-T").CombinedOutput()
	if err != nil {
		return nil, false // 配置本身有错/无 nginx:退回目录扫描,不臆造
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if m := confFileHeader.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			p := m[1]
			if !seen[p] {
				seen[p] = true
				files = append(files, ConfCandidate{Path: p, Source: "nginx -T", Main: len(files) == 0})
			}
		}
	}
	return files, len(files) > 0
}

// caddyConfigCandidates caddy 发现:主配置 + import 链展开。
// 主配置按优先级探测:Caddyfile 常见位置;caddy adapt 能跑通即认为有效。
func caddyConfigCandidates(ctx context.Context) []ConfCandidate {
	var cands []ConfCandidate
	for _, dir := range []string{"/etc/caddy", "/etc", "$HOME/.local/share/caddy"} {
		main := strings.TrimSuffix(dir, "/") + "/Caddyfile"
		if !fileReadable(main) {
			continue
		}
		ctxT, cancel := context.WithTimeout(ctx, 8*time.Second)
		out, err := exec.CommandContext(ctxT, "caddy", "adapt", "--config", main).CombinedOutput()
		cancel()
		if err != nil {
			continue // 解析失败:Caddyfile 有错,不当候选
		}
		cands = append(cands, ConfCandidate{Path: main, Source: "caddy adapt", Main: true})
		cands = append(cands, caddyImports(string(out))...)
		break
	}
	return cands
}

// caddyImports 从 adapt 输出(JSON)提取 import 进来的源文件路径。
// adapt 输出里 import 的文件记录在 "file" 字段;正则抽取即够(不建完整 JSON 模型)。
var caddyFileField = regexp.MustCompile(`"file"\s*:\s*"((?:[^"\\]|\\.)*)"`)

func caddyImports(adaptOut string) []ConfCandidate {
	seen := map[string]bool{}
	var out []ConfCandidate
	for _, m := range caddyFileField.FindAllStringSubmatch(adaptOut, -1) {
		p := strings.ReplaceAll(m[1], `\/`, `/`)
		if p == "" || seen[p] || strings.HasSuffix(p, "/Caddyfile") {
			continue // 主配置已单列;跳过重复
		}
		seen[p] = true
		out = append(out, ConfCandidate{Path: p, Source: "caddy import"})
	}
	return out
}

// mergeCandidates 合并候选(去重,nginx -T/主配置优先,排序稳定)。
func mergeCandidates(groups ...[]ConfCandidate) []ConfCandidate {
	seen := map[string]bool{}
	var out []ConfCandidate
	for _, g := range groups {
		for _, c := range g {
			if !seen[c.Path] {
				seen[c.Path] = true
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Main != out[j].Main {
			return out[i].Main // 主配置排最前
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// dirScanCandidates 目录扫描转候选(相对名 → 绝对路径)。
func dirScanCandidates(dir string, names []string) []ConfCandidate {
	if len(names) == 0 {
		return nil
	}
	out := make([]ConfCandidate, 0, len(names))
	for _, n := range names {
		out = append(out, ConfCandidate{Path: strings.TrimSuffix(dir, "/") + "/" + n, Source: "dir-scan"})
	}
	return out
}

// containerConfCandidates 容器化部署应用的配置发现(Agent 在宿主机上经 docker.sock inspect 容器挂载):
// 镜像名含 nginx/caddy 时,挂载目标命中该镜像的配置惯例路径 → 挂载源为候选。
// Source 携带容器名供面板归组展示。
func containerConfCandidates(image string, mounts []mountEntryRaw) []ConfCandidate {
	img := strings.ToLower(image)
	var kind string
	switch {
	case strings.Contains(img, "nginx"):
		kind = "nginx"
	case strings.Contains(img, "caddy"):
		kind = "caddy"
	default:
		return nil
	}
	var out []ConfCandidate
	for _, m := range mounts {
		if !isConfMountDest(kind, m.Destination) {
			continue
		}
		if m.Source == "" || strings.HasPrefix(m.Source, "/var/lib/docker/volumes/") {
			continue // 匿名卷/命名卷源路径无纳管意义
		}
		out = append(out, ConfCandidate{Path: m.Source, Source: "docker-mount(" + kind + ")"})
	}
	return out
}

// isConfMountDest 镜像内配置路径惯例:nginx /etc/nginx(树),caddy /etc/caddy + /srv。
func isConfMountDest(kind, dest string) bool {
	switch kind {
	case "nginx":
		return dest == "/etc/nginx" || strings.HasPrefix(dest, "/etc/nginx/") ||
			dest == "/etc/nginx/nginx.conf"
	case "caddy":
		return dest == "/etc/caddy" || strings.HasPrefix(dest, "/etc/caddy/") ||
			dest == "/srv" || strings.HasPrefix(dest, "/srv/")
	}
	return false
}

func fileReadable(p string) bool {
	return p != "" && !strings.HasPrefix(p, "$") && func() bool {
		c := exec.Command("test", "-r", p)
		return c.Run() == nil
	}()
}
