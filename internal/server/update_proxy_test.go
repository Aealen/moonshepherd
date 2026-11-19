// 自更新 GitHub 选路单测:直连慢/失败自动切代理(HERD_GH_PROXY),
// 用本地 httptest 模拟目标源与正向代理,不依赖真实 GitHub。
package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeProxy 正向代理(仅 http 目标):收到绝对 URI 请求后转发到真实目标。
func fakeProxy(backend *httptest.Server) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			http.Error(w, "proxy: expect absolute-uri request", http.StatusBadRequest)
			return
		}
		out, err := http.NewRequest(r.Method, r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
}

func resetProxyEnv(t *testing.T, proxyURL string) {
	t.Helper()
	t.Setenv("HERD_GH_PROXY", proxyURL)
	ghHTTPClients() // 环境变量已变,触发 client 重建
}

func TestGhFetchFallsBackToProxyWhenDirectSlow(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond) // 头迟迟不到,超探测窗
		_, _ = w.Write([]byte(`{"ok":"direct"}`))
	}))
	defer backend.Close()
	proxy := fakeProxy(backend)
	defer proxy.Close()

	resetProxyEnv(t, proxy.URL)
	old := ghProbeTimeout
	ghProbeTimeout = 100 * time.Millisecond
	defer func() { ghProbeTimeout = old }()

	// 直连(backend 头 500ms 才到 > 100ms 探测窗)必超时 → 切代理(代理立刻转发 backend)。
	resp, via, err := ghFetch(context.Background(), backend.URL, false)
	if err != nil {
		t.Fatalf("ghFetch: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if via != "proxy" {
		t.Fatalf("expect via=proxy, got %s", via)
	}
	if !strings.Contains(string(body), `"ok":"direct"`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestGhFetchStaysDirectWhenFast(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":"direct"}`))
	}))
	defer backend.Close()

	resetProxyEnv(t, "http://127.0.0.1:1") // 配置了代理但目标直连秒回
	old := ghProbeTimeout
	ghProbeTimeout = 300 * time.Millisecond
	defer func() { ghProbeTimeout = old }()

	resp, via, err := ghFetch(context.Background(), backend.URL, false)
	if err != nil {
		t.Fatalf("ghFetch: %v", err)
	}
	defer resp.Body.Close()
	if via != "direct" {
		t.Fatalf("expect via=direct, got %s", via)
	}
}

func TestGhFetchDirectWhenNoProxyConfigured(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":"direct"}`))
	}))
	defer backend.Close()

	resetProxyEnv(t, "")

	_, via, err := ghFetch(context.Background(), backend.URL, false)
	if err != nil {
		t.Fatalf("ghFetch: %v", err)
	}
	if via != "direct" {
		t.Fatalf("expect via=direct, got %s", via)
	}
}

// TestDownloadToFileProxyRetry 直连"响应头快、body 中途断流"→ 代理重试应完整落盘。
// hits=1 为直连(断流),hits=2 为代理重试(完整 body)。
func TestDownloadToFileProxyRetry(t *testing.T) {
	var hits int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(strings.Repeat("a", 40)))
		w.(http.Flusher).Flush()
		if hits > 1 { // 第二次(代理重试)补全剩余 60 字节
			_, _ = w.Write([]byte(strings.Repeat("b", 60)))
		}
		// 第一次:不再写 → 客户端按 Content-Length 100 等待,连接关闭即 UnexpectedEOF
	}))
	defer backend.Close()

	proxy := fakeProxy(backend)
	defer proxy.Close()
	resetProxyEnv(t, proxy.URL)

	// 下载目标指到本地 backend(生产为 GitHub API 地址)
	oldBase, oldProbe := ghAssetBase, ghProbeTimeout
	ghAssetBase = backend.URL
	ghProbeTimeout = 300 * time.Millisecond
	defer func() { ghAssetBase, ghProbeTimeout = oldBase, oldProbe }()

	dest := t.TempDir() + "/out.bin"
	if err := downloadToFile(context.Background(), ghAsset{URL: backend.URL, ID: 1}, dest); err != nil {
		t.Fatalf("downloadToFile: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 100 {
		t.Fatalf("expect 100 bytes, got %d", len(data))
	}
	if hits < 2 {
		t.Fatalf("expect proxy retry (hits>=2), got %d", hits)
	}
}
