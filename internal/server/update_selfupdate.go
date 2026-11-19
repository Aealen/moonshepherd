// 面板自更新:检查(GitHub releases/latest)与执行(下载 tar.gz → sha256 → 替换 → 换身)。
// 私有仓库的检查/下载需 HERD_GH_TOKEN(contents:read);仓库转公开后免配置。
// 部署形态:binary(linux)支持完整自更新;docker 容器不可变,仅提示前往 Release;
// 非 linux/手动部署提示手动更新。替换复用 Agent selfupdate 的成熟模式
// (旁路写入 → sha256 → rename 原子就位 → 失败回滚)。
package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second) // 元数据查询小请求,短超时
	defer cancel()
	rel, err := ghLatestRelease(ctx)
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
	if !tryAcquireUpdate() {
		writeErr(w, http.StatusConflict, "busy", "更新正在进行中,请等待本次完成或失败后再试(可查看进度接口)")
		return
	}
	defer releaseUpdate() // exec 换身成功时本进程映像已替换,defer 不会执行,无需处理
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	rel, err := ghLatestRelease(ctx)
	cancel()
	if err != nil {
		writeErr(w, http.StatusNotFound, "no_release", "检查最新发布失败: "+err.Error())
		return
	}
	if !semverNewer(rel.Version, version.Version) {
		writeErr(w, http.StatusConflict, "same_version", "面板已是最新版本 "+version.Version)
		return
	}
	if err := panelSelfReplace(r.Context(), rel); err != nil {
		slog.Warn("面板自更新失败", "current", version.Version, "target", rel.Version, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 走到此处即 exec 后的新版本进程在应答
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": true, "current": version.Version,
	})
}

// handleUpdateProgress GET /system/update-progress:自更新实时进度(前端轮询)。
func (s *Server) handleUpdateProgress(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, updateProgressSnapshot())
}

// ---------- 更新进度跟踪 ----------

// updateProgress 自更新各阶段实时进度(前端轮询 /system/update-progress)。
type updateProgress struct {
	Stage  string `json:"stage"`            // idle/download/verify/unpack/replace/exec/error
	Detail string `json:"detail,omitempty"` // 阶段说明或失败原因
	Total  int64  `json:"total"`            // 下载总字节(0=未知)
	Done   int64  `json:"done"`             // 已下载字节
	Via    string `json:"via,omitempty"`    // 当前线路:direct(直连)/proxy(HERD_GH_PROXY)
}

var (
	upBusyMu sync.Mutex
	upProgMu sync.Mutex
	upProg   = updateProgress{Stage: "idle"}
	upBusy   bool // 更新执行中:拒绝并发触发,防两次下载交叉写同一临时文件
)

func tryAcquireUpdate() bool {
	upBusyMu.Lock()
	defer upBusyMu.Unlock()
	if upBusy {
		return false
	}
	upBusy = true
	return true
}

func releaseUpdate() {
	upBusyMu.Lock()
	defer upBusyMu.Unlock()
	upBusy = false
}

func setUpdateProgress(stage, detail string, total, done int64, via string) {
	upProgMu.Lock()
	defer upProgMu.Unlock()
	upProg = updateProgress{Stage: stage, Detail: detail, Total: total, Done: done, Via: via}
}

func updateProgressSnapshot() updateProgress {
	upProgMu.Lock()
	defer upProgMu.Unlock()
	return upProg
}

// ---------- GitHub 查询(直连/代理自动选路) ----------

func ghToken() string { return os.Getenv("HERD_GH_TOKEN") }

// ghProxyURL 本地代理地址(HERD_GH_PROXY,http://host:port):国内机器直连 GitHub
// 慢或不可达时,下载自动切走代理;未配置则始终直连。
func ghProxyURL() string { return os.Getenv("HERD_GH_PROXY") }

var (
	ghClientMu   sync.Mutex
	ghDirectHTTP = &http.Client{} // 不设全局超时:元数据由调用方 30s ctx 控制,下载给长 ctx
	ghProxyCur   string
	ghProxyHTTP  *http.Client
)

// ghProbeTimeout 直连线路探测时限:头未按时到达即判慢线路,切代理。
var ghProbeTimeout = 3 * time.Second

func ghHTTPClients() (direct, proxied *http.Client) {
	ghClientMu.Lock()
	defer ghClientMu.Unlock()
	cur := ghProxyURL()
	if cur != ghProxyCur { // 环境变量变化即重建(进程生命周期内不变;测试可注入)
		ghProxyCur = cur
		ghProxyHTTP = nil
		if u, err := url.Parse(cur); err == nil && u.Host != "" {
			ghProxyHTTP = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
		}
	}
	return ghDirectHTTP, ghProxyHTTP
}

// ghDo 单次请求(via 指定线路)。
func ghDo(ctx context.Context, via, apiURL string, octet bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if octet {
		req.Header.Set("Accept", "application/octet-stream")
	}
	if tok := ghToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	direct, proxied := ghHTTPClients()
	if via == "proxy" && proxied != nil {
		return proxied.Do(req)
	}
	return direct.Do(req)
}

// ghFetch GET GitHub 资源,自动选路:未配置代理走直连;配置了 HERD_GH_PROXY 时,
// 直连 3s 内响应头未达(国内机器典型症状:连接悬死/极慢)自动改走代理。
// 返回 resp(调用方负责关闭 Body)与实际线路 direct/proxy。
func ghFetch(ctx context.Context, apiURL string, octet bool) (*http.Response, string, error) {
	_, proxied := ghHTTPClients()
	if proxied == nil { // 未配置代理:始终直连
		resp, err := ghDo(ctx, "direct", apiURL, octet)
		return resp, "direct", err
	}
	// 直连探测:ghProbeTimeout(默认 3s)头超时。AfterFunc 到点取消,探测成功及时
	// 停表——Body 可继续读,且 ctx 派生自调用方,外层取消依然传播。
	pctx, pcancel := context.WithCancel(ctx)
	timer := time.AfterFunc(ghProbeTimeout, pcancel)
	resp, err := ghDo(pctx, "direct", apiURL, octet)
	if err == nil {
		timer.Stop()
		return resp, "direct", nil
	}
	pcancel()
	if ctx.Err() != nil {
		return nil, "direct", ctx.Err() // 调用方已取消,别再试代理
	}
	resp, err = ghDo(ctx, "proxy", apiURL, octet)
	return resp, "proxy", err
}

// ghLatestRelease 查询最新发布(不含 draft/prerelease);30 分钟缓存。
// 调用方以 ctx 控制超时(元数据建议 30s)。
func ghLatestRelease(ctx context.Context) (*ghRelease, error) {
	if ghCache.rel != nil && time.Since(ghCache.at) < 30*time.Minute {
		return ghCache.rel, nil
	}
	resp, via, err := ghFetch(ctx, "https://api.github.com/repos/"+ghRepo+"/releases/latest", false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API %d(%s)", resp.StatusCode, via)
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

// ---------- 资产下载与校验工具(平台无关;linux 由 panelSelfReplaceLinux 调用) ----------

// findAsset 按名称找 Release 资产。
func findAsset(rel *ghRelease, name string) (ghAsset, error) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, nil
		}
	}
	return ghAsset{}, fmt.Errorf("Release %s 缺少资产 %s", rel.Version, name)
}

// ghAssetBase 资产 API 地址前缀;包级变量便于单测注入本地目标。
var ghAssetBase = "https://api.github.com/repos/" + ghRepo

func assetAPIURL(a ghAsset) string {
	return ghAssetBase + "/releases/assets/" + fmt.Sprint(a.ID)
}

// downloadTo 下载资产到内存(小文件:校验和)。
func downloadTo(ctx context.Context, a ghAsset, limit int64) ([]byte, error) {
	resp, _, err := ghFetch(ctx, assetAPIURL(a), true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// downloadToFile 下载资产到文件(大文件:面板包),全程经 setUpdateProgress 上报
// 进度与线路;直连中途失败(国内网络常态)且配置了代理时,自动换代理整体重试一次。
func downloadToFile(ctx context.Context, a ghAsset, dest string) error {
	resp, via, err := ghFetch(ctx, assetAPIURL(a), true)
	if err != nil {
		return err
	}
	done, derr := copyBodyToFile(resp, dest, via)
	resp.Body.Close()
	if derr == nil {
		return nil
	}
	if via != "direct" || ghProxyURL() == "" || ctx.Err() != nil {
		return derr // 代理线路失败不重试(避免双倍慢);调用方取消同理
	}
	slog.Warn("面板包直连下载失败,切换代理重试", "downloaded", done, "err", derr)
	resp2, perr := ghDo(ctx, "proxy", assetAPIURL(a), true)
	if perr != nil {
		return fmt.Errorf("%w(代理重试连接失败: %v)", derr, perr)
	}
	_, derr2 := copyBodyToFile(resp2, dest, "proxy")
	resp2.Body.Close()
	if derr2 != nil {
		return fmt.Errorf("直连与代理下载均失败(直连: %v;代理: %v)", derr, derr2)
	}
	return nil
}

// copyBodyToFile 把响应体落盘,按 Content-Length 上报下载进度(含线路标识)。
func copyBodyToFile(resp *http.Response, dest, via string) (int64, error) {
	total := resp.ContentLength
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var done int64
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			setUpdateProgress("download", "下载面板包", total, done, via)
		}
		if rerr == io.EOF {
			return done, nil
		}
		if rerr != nil {
			return done, rerr
		}
	}
}

// parseChecksum 从 checksums.txt 提取指定文件名的 sha256。
func parseChecksum(content, filename string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.HasSuffix(fields[1], filename) {
			return fields[0]
		}
	}
	return ""
}

func sha256SumFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// untar 解压 tar.gz 到目标目录(防御性:拒绝绝对路径与 ../ 穿越)。
func untar(tarPath, dest string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("包内非法路径: %s", hdr.Name)
		}
		target := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
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
