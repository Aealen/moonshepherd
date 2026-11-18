//go:build linux

// 面板自更新 linux 实现:下载 Release 面板 tar.gz(moon + distro-bin/ 三架构)
// → sha256 校验 → 旁路解压 → 原子替换 moon 与 distro-bin/ → syscall.Exec 原地换身。
// 替换模式与 Agent selfupdate 一致:失败即回滚旧版,任何一步不完整都不会丢面板。
package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func init() { panelSelfReplace = panelSelfReplaceLinux }

func panelSelfReplaceLinux(ctx context.Context, rel *ghRelease) error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("不支持架构: %s(Release 提供 amd64/arm64)", runtime.GOARCH)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位面板二进制失败: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("解析二进制路径失败: %w", err)
	}
	workDir := filepath.Dir(self)

	// ① 从资产里定位面板包与校验和
	tarAsset, err := findAsset(rel, "moonshepherd-panel-linux-"+runtime.GOARCH+".tar.gz")
	if err != nil {
		return err
	}
	sumAsset, err := findAsset(rel, "checksums.txt")
	if err != nil {
		return err
	}

	// ② 下载校验和并解析本平台包的期望 sha256
	sums, err := downloadTo(ctx, sumAsset, 64<<10)
	if err != nil {
		return fmt.Errorf("下载校验和失败: %w", err)
	}
	want := parseChecksum(string(sums), filepath.Base(tarAsset.Name))
	if want == "" {
		return fmt.Errorf("checksums.txt 中缺少 %s", filepath.Base(tarAsset.Name))
	}

	// ③ 下载面板 tar.gz(约 15MB,落临时文件)
	tarPath := filepath.Join(workDir, ".upgrade.tar.gz")
	if err := downloadToFile(ctx, tarAsset, tarPath); err != nil {
		return fmt.Errorf("下载面板包失败: %w", err)
	}
	defer os.Remove(tarPath)
	got := sha256SumFile(tarPath)
	if got != want {
		return fmt.Errorf("sha256 校验失败:期望 %s… 实得 %s…", want[:12], got[:12])
	}

	// ④ 解压到旁路目录(同文件系统保证 rename 原子)
	tmp := filepath.Join(workDir, ".upgrade-tmp")
	_ = os.RemoveAll(tmp)
	if err := untar(tarPath, tmp); err != nil {
		return fmt.Errorf("解压面板包失败: %w", err)
	}
	newMoon := filepath.Join(tmp, "moon")
	if fi, err := os.Stat(newMoon); err != nil || fi.Size() < 1<<20 { // 二进制至少 MB 级,防错位
		return fmt.Errorf("面板包内容异常(缺少 moon 二进制)")
	}
	if err := os.Chmod(newMoon, 0o755); err != nil {
		return fmt.Errorf("设置可执行位失败: %w", err)
	}

	// ⑤ 原子替换 moon(旧版备份 .bak,失败即回滚)
	bak := self + ".bak"
	_ = os.Remove(bak)
	if err := os.Rename(self, bak); err != nil {
		return fmt.Errorf("备份旧版失败(未变更): %w", err)
	}
	if err := os.Rename(newMoon, self); err != nil {
		_ = os.Rename(bak, self)
		return fmt.Errorf("新版就位失败(已回滚): %w", err)
	}

	// ⑥ 替换 distro-bin/(agent.update 与 install.sh 的分发源随面板同版本)
	oldBin := filepath.Join(workDir, "distro-bin")
	newBin := filepath.Join(tmp, "distro-bin")
	if fi, err := os.Stat(newBin); err == nil && fi.IsDir() {
		oldBinBak := oldBin + ".bak"
		_ = os.RemoveAll(oldBinBak)
		renamed := false
		if _, statErr := os.Stat(oldBin); statErr == nil {
			if os.Rename(oldBin, oldBinBak) == nil {
				renamed = true
			}
		}
		if err := os.Rename(newBin, oldBin); err != nil {
			if renamed {
				_ = os.Rename(oldBinBak, oldBin)
			}
			return fmt.Errorf("distro-bin 就位失败(已回滚): %w", err)
		}
		_ = os.RemoveAll(oldBinBak)
	}
	_ = os.RemoveAll(tmp)

	// ⑦ 原地换身:同一 PID 直接以新版本二进制运行,无需外部拉起者;
	// SQLite(WAL)与连接的 fd 随 exec 关闭,前端以重连+版本变化确认更新完成
	return syscall.Exec(self, os.Args, os.Environ())
}

// findAsset 按名称找 Release 资产。
func findAsset(rel *ghRelease, name string) (ghAsset, error) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, nil
		}
	}
	return ghAsset{}, fmt.Errorf("Release %s 缺少资产 %s", rel.Version, name)
}

// downloadTo 下载资产到内存(小文件:校验和)。
func downloadTo(ctx context.Context, a ghAsset, limit int64) ([]byte, error) {
	resp, err := ghRequest(ctx, assetAPIURL(a), true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// downloadToFile 下载资产到文件(大文件:面板包)。
func downloadToFile(ctx context.Context, a ghAsset, dest string) error {
	resp, err := ghRequest(ctx, assetAPIURL(a), true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, 256<<20))
	return err
}

// assetAPIURL 资产下载走 API 地址(Accept: octet-stream;私有仓库鉴权统一)。
func assetAPIURL(a ghAsset) string {
	return "https://api.github.com/repos/" + ghRepo + "/releases/assets/" + fmt.Sprint(a.ID)
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
