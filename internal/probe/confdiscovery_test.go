package probe

import (
	"strings"
	"testing"
)

// TestNginxTParse nginx -T 输出解析:配置头识别、去重、首个标 main。
func TestNginxTParse(t *testing.T) {
	out := `nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
# configuration file /etc/nginx/nginx.conf:

user  nginx;

# configuration file /etc/nginx/conf.d/default.conf:

server {
    listen 80;
}

# configuration file /etc/nginx/nginx.conf:

(重复段应去重)
`
	seen := map[string]bool{}
	var files []ConfCandidate
	for _, line := range strings.Split(out, "\n") {
		if m := confFileHeader.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			if !seen[m[1]] {
				seen[m[1]] = true
				files = append(files, ConfCandidate{Path: m[1], Source: "nginx -T", Main: len(files) == 0})
			}
		}
	}
	if len(files) != 2 {
		t.Fatalf("应解析出 2 个文件(去重后),实得 %d: %+v", len(files), files)
	}
	if !files[0].Main || files[0].Path != "/etc/nginx/nginx.conf" {
		t.Fatalf("首个应为 main 的 nginx.conf: %+v", files[0])
	}
	if files[1].Path != "/etc/nginx/conf.d/default.conf" {
		t.Fatalf("第二个文件异常: %+v", files[1])
	}
}

// TestCaddyImports adapt 输出的 "file" 字段抽取(转义路径、主配置排除)。
func TestCaddyImports(t *testing.T) {
	adapt := `{"apps":{"http":{"servers":{"srv0":` +
		`{"file":"\/etc\/caddy\/imported.conf","routes":[` +
		`{"file":"\/etc\/caddy\/Caddyfile"},{"file":"\/etc\/caddy\/snippet.caddy"}]}}}}}`
	cands := caddyImports(adapt)
	if len(cands) != 2 {
		t.Fatalf("应抽出 2 个 import(排除主配置),实得 %d: %+v", len(cands), cands)
	}
	if cands[0].Path != "/etc/caddy/imported.conf" || cands[1].Path != "/etc/caddy/snippet.caddy" {
		t.Fatalf("路径解析异常: %+v", cands)
	}
}

// TestMergeCandidates 合并去重与排序:main 最前,其余按路径。
func TestMergeCandidates(t *testing.T) {
	g1 := []ConfCandidate{
		{Path: "/etc/nginx/nginx.conf", Source: "nginx -T", Main: true},
		{Path: "/etc/nginx/conf.d/a.conf", Source: "nginx -T"},
	}
	g2 := []ConfCandidate{
		{Path: "/etc/nginx/conf.d/a.conf", Source: "dir-scan"},        // 重复
		{Path: "/etc/nginx/conf.d/0-legacy.conf", Source: "dir-scan"}, // 排序应前移
	}
	got := mergeCandidates(g1, g2)
	if len(got) != 3 {
		t.Fatalf("去重后应 3 项,实得 %d: %+v", len(got), got)
	}
	if !got[0].Main || got[0].Path != "/etc/nginx/nginx.conf" {
		t.Fatalf("main 应排最前: %+v", got[0])
	}
	if got[1].Path != "/etc/nginx/conf.d/0-legacy.conf" {
		t.Fatalf("路径序异常: %+v", got)
	}
}

// TestContainerConfCandidates Docker 容器配置挂载推断。
func TestContainerConfCandidates(t *testing.T) {
	mounts := []mountEntryRaw{
		{Source: "/mnt/user/appdata/nginx/nginx.conf", Destination: "/etc/nginx/nginx.conf", RW: true},
		{Source: "/mnt/user/appdata/web", Destination: "/usr/share/nginx/html", RW: true},         // 非配置挂载
		{Source: "/var/lib/docker/volumes/abc/_data", Destination: "/etc/nginx/conf.d", RW: true}, // 匿名卷跳过
	}
	cands := containerConfCandidates("nginx:1.27-alpine", mounts)
	if len(cands) != 1 || cands[0].Path != "/mnt/user/appdata/nginx/nginx.conf" {
		t.Fatalf("nginx 容器应只识别出 1 个有效候选: %+v", cands)
	}

	caddyMounts := []mountEntryRaw{
		{Source: "/mnt/user/appdata/caddy", Destination: "/etc/caddy", RW: true},
		{Source: "/mnt/user/appdata/site", Destination: "/srv", RW: true},
	}
	cc := containerConfCandidates("caddy:2.8", caddyMounts)
	if len(cc) != 2 {
		t.Fatalf("caddy 容器应识别 /etc/caddy 与 /srv 两个候选: %+v", cc)
	}

	if got := containerConfCandidates("postgres:17", caddyMounts); got != nil {
		t.Fatalf("非 nginx/caddy 镜像不应产候选: %+v", got)
	}
}
