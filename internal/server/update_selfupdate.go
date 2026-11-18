// 面板自更新:检查(GitHub releases/latest)与执行(下载 tar.gz → sha256 → 替换 → 换身)。
// 私有仓库的检查/下载需 HERD_GH_TOKEN(contents:read);仓库转公开后免配置。
// 部署形态:binary(linux)支持完整自更新;docker 容器不可变,仅提示前往 Release;
// 非 linux/手动部署提示手动更新。替换复用 Agent selfupdate 的成熟模式
// (旁路写入 → sha256 → rename 原子就位 → 失败回滚)。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aealen/moonshepherd/version"
)

const ghRepo = "Aealen/moonshepherd"

// ghRelease GitHub 最新发布(面向更新检查的裁剪视图)。
type ghRelease struct {
	Version     string    `json:"version"`      // tag_name,如 v0.1.1
	Notes       string    `json:"notes"`        // release 说明(展示用原文)
	URL         string    `json:"url"`          // Release 页面(跳转用)
	PublishedAt int64     `json:"published_at"` // unix 秒
	Assets      []ghAsset `json:"-"`            // 内部:资产清单(execute 用,不下发前端)
}
type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"` // API 地址(下载需 Accept: application/octet-stream)
	ID   int64  `json:"id"`
}

// updateInfo 更新检查响应。
type updateInfo struct {
	Current   string     `json:"current"`          // 面板运行版本
	Available bool       `json:"available"`        // 是否有新版本
	Latest    *ghRelease `json:"latest,omitempty"` // 最新发布(不可达时为空)
	Mode      string     `json:"mode"`             // binary(linux 裸跑/systemd)/docker/manual
	Arch      string     `json:"arch"`             // GOARCH
	Reason    string     `json:"reason,omitempty"` // unreachable:GitHub 不可达(私有仓库且未配 token 等)
}

// gh 查询缓存:匿名限流 60 次/小时/IP,30 分钟内存缓存足够。
var ghCache struct {
	at  time.Time
	rel *ghRelease
}

// deployMode 部署形态:binary(linux)/docker(容器不可变)/manual(非 linux)。
func deployMode() string {
	if runtime.GOOS != "linux" {
		return "manual"
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	return "binary"
}

// handleUpdateCheck GET /system/update-check。
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	// force=1 绕过 30 分钟缓存(前端手动「检查更新」)
	if r.URL.Query().Get("force") == "1" {
		ghCache.at = time.Time{}
	}
	info := updateInfo{Current: version.Version, Mode: deployMode(), Arch: runtime.GOARCH}
	if info.Mode == "manual" || info.Mode == "docker" {
		// docker/manual 也检查最新版本(供展示),但不提供自更新入口
	}
	rel, err := ghLatestRelease(r.Context())
	if err != nil {
		info.Reason = "unreachable"
		writeJSON(w, http.StatusOK, info) // 检查不可达不是错误,前端按「无法检查」展示
		return
	}
	info.Latest = rel
	info.Available = semverNewer(rel.Version, version.Version)
	writeJSON(w, http.StatusOK, info)
}

// handleUpdateExecute POST /system/update-execute:binary 模式下载最新版并原地换身。
// exec 成功时原连接随旧进程映像消失(无 HTTP 响应)——前端以「重连后版本变化」
// 确认更新完成;只有失败路径会拿到响应。
func (s *Server) handleUpdateExecute(w http.ResponseWriter, r *http.Request) {
	if deployMode() != "binary" {
		writeErr(w, http.StatusConflict, "mode_unsupported",
			"当前部署形态("+deployMode()+")不支持面板内自更新;Docker 部署请按 Release 页指引 pull 新镜像后重建容器")
		return
	}
	rel, err := ghLatestRelease(r.Context())
	if err != nil {
		writeErr(w, http.StatusNotFound, "no_release", "检查最新发布失败: "+err.Error())
		return
	}
	if !semverNewer(rel.Version, version.Version) {
		writeErr(w, http.StatusConflict, "same_version", "面板已是最新版本 "+version.Version)
		return
	}
	if err := panelSelfReplace(r.Context(), rel); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 走到此处即 exec 后的新版本进程在应答
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": true, "current": version.Version,
	})
}

// ---------- GitHub 查询 ----------

func ghToken() string { return os.Getenv("HERD_GH_TOKEN") }

func ghRequest(ctx context.Context, url string, octetStream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if octetStream {
		req.Header.Set("Accept", "application/octet-stream")
	}
	if tok := ghToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	return client.Do(req)
}

// ghLatestRelease 查询最新发布(不含 draft/prerelease);30 分钟缓存。
func ghLatestRelease(ctx context.Context) (*ghRelease, error) {
	if ghCache.rel != nil && time.Since(ghCache.at) < 30*time.Minute {
		return ghCache.rel, nil
	}
	resp, err := ghRequest(ctx, "https://api.github.com/repos/"+ghRepo+"/releases/latest", false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API %d", resp.StatusCode)
	}
	var raw struct {
		TagName     string    `json:"tag_name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt string    `json:"published_at"`
		Assets      []ghAsset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	var pub int64
	pub, _ = strconv.ParseInt(raw.PublishedAt, 10, 64) // RFC3339 解析失败保持 0,不影响展示
	if t, err := time.Parse(time.RFC3339, raw.PublishedAt); err == nil {
		pub = t.Unix()
	}
	rel := &ghRelease{Version: raw.TagName, Notes: raw.Body, URL: raw.HTMLURL, PublishedAt: pub, Assets: raw.Assets}
	ghCache.at, ghCache.rel = time.Now(), rel
	return rel, nil
}

// semverNewer latest 是否比 current 新:三段数字比较;current 无法解析(开发构建)
// 视为有更新(宁可提示);latest 无法解析则不提示。
func semverNewer(latest, current string) bool {
	l, ok1 := parseSemver(latest)
	c, ok2 := parseSemver(current)
	if !ok1 {
		return false
	}
	if !ok2 {
		return true
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// parseSemver 提取 v 主.次.补丁 三段数字(容忍 tag 前后缀,如 v0.1.1 / 0.2.0-beta)。
func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	i := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return out, false
	}
	parts := strings.SplitN(s[i:], ".", 3)
	if len(parts) < 3 {
		return out, false
	}
	for j, p := range parts {
		digits := strings.Builder{}
		for _, r := range p {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			} else {
				break
			}
		}
		v, err := strconv.Atoi(digits.String())
		if err != nil {
			return out, false
		}
		out[j] = v
	}
	return out, true
}

// panelSelfReplace 平台相关:下载校验替换后以新版本接管进程(linux 实现)。
// 非 linux 由 handler 的 mode=manual 挡住,不会走到这里;保留桩避免编译分叉。
var panelSelfReplace = func(ctx context.Context, rel *ghRelease) error {
	return fmt.Errorf("当前平台不支持自更新")
}
