package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aealen/moonshepherd/internal/credenc"
	"github.com/aealen/moonshepherd/internal/store"
)

// newDistTestServer 按自定义 Options 组装测试面板。
func newDistTestServer(t *testing.T, opt Options) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := credenc.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	opt.DataDir = dir
	srv, err := New(opt, st, box)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestInstallShRender 本地 http(dev)面板:脚本渲染含 ws 地址、arch 探测与 token 校验。
func TestInstallShRender(t *testing.T) {
	ts := newDistTestServer(t, Options{Dev: true})
	resp, err := http.Get(ts.URL + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Fatalf("Content-Type 异常: %s", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	script := string(b)
	for _, want := range []string{
		"#!/bin/sh",
		`SERVER="ws://`,                       // dev 模式推导 ws
		strings.TrimPrefix(ts.URL, "http://"), // Host 填充
		"uname -m",                            // arch 探测
		"--token",                             // 参数
		"/run/systemd/system",                 // systemd 探测
		"conn_script.sh",                      // F3 降级模板
	} {
		if !strings.Contains(script, want) {
			t.Errorf("安装脚本缺少关键内容: %q", want)
		}
	}
}

// TestInstallShBehindProxy 反代场景:TrustProxy + X-Forwarded-Proto=https → wss 地址。
func TestInstallShBehindProxy(t *testing.T) {
	ts := newDistTestServer(t, Options{Dev: false, TrustProxy: true})
	req, _ := http.NewRequest("GET", ts.URL+"/install.sh", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	script := string(b)
	if !strings.Contains(script, `SERVER="wss://`) {
		t.Fatalf("反代 https 下应推导 wss,脚本片段:\n%s", script[:min(200, len(script))])
	}
}

// TestDistBinaryServing 二进制下发:存在→字节一致;缺失→404 提示;非法架构→404。
func TestDistBinaryServing(t *testing.T) {
	dir := t.TempDir()
	fake := []byte("\x7fELF-fake-binary-herd")
	if err := os.WriteFile(filepath.Join(dir, "herd-linux-amd64"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := newDistTestServer(t, Options{Dev: true, DistDir: dir})

	// 存在:200 且内容一致
	resp, err := http.Get(ts.URL + "/dist/herd-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(b) != string(fake) {
		t.Fatalf("下发异常: code=%d len=%d", resp.StatusCode, len(b))
	}

	// 缺失:404 + no_binary 提示
	resp2, err := http.Get(ts.URL + "/dist/herd-linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("缺失二进制应 404,实得 %d", resp2.StatusCode)
	}
	body2, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body2), "no_binary") {
		t.Fatalf("应含 no_binary 错误码: %s", body2)
	}

	// 非法架构:404
	resp3, err := http.Get(ts.URL + "/dist/herd-linux-mips")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("非法架构应 404,实得 %d", resp3.StatusCode)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
