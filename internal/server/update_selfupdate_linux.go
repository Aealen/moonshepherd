//go:build linux

// 面板自更新 linux 实现:下载 Release 面板 tar.gz(moon + distro-bin/ 三架构)
// → sha256 校验 → 旁路解压 → 原子替换 moon 与 distro-bin/ → syscall.Exec 原地换身。
// 替换模式与 Agent selfupdate 一致:失败即回滚旧版,任何一步不完整都不会丢面板。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func init() { panelSelfReplace = panelSelfReplaceLinux }

// panelSelfReplaceLinux 全程经 setUpdateProgress 上报阶段(前端轮询展示);
// 失败统一打点 error 进度与日志——此前失败被前端误当「重启中」静默吞掉。
func panelSelfReplaceLinux(ctx context.Context, rel *ghRelease) (err error) {
	defer func() {
		if err != nil {
			setUpdateProgress("error", err.Error(), 0, 0, "")
			slog.Warn("面板自更新失败", "target", rel.Version, "err", err)
		}
	}()
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

	// ③ 下载面板 tar.gz(约 14MB;ctx 给足 15 分钟,国内直连慢时可走代理)
	tarPath := filepath.Join(workDir, ".upgrade.tar.gz")
	tctx, tcancel := context.WithTimeout(ctx, 15*time.Minute)
	err = downloadToFile(tctx, tarAsset, tarPath)
	tcancel()
	if err != nil {
		return fmt.Errorf("下载面板包失败: %w", err)
	}
	defer os.Remove(tarPath)
	setUpdateProgress("verify", "校验 sha256", 0, 0, "")
	got := sha256SumFile(tarPath)
	if got != want {
		return fmt.Errorf("sha256 校验失败:期望 %s… 实得 %s…", want[:12], got[:12])
	}

	// ④ 解压到旁路目录(同文件系统保证 rename 原子)
	setUpdateProgress("unpack", "解压面板包", 0, 0, "")
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
	setUpdateProgress("replace", "替换面板文件", 0, 0, "")
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
	// SQLite(WAL)与连接的 fd 随 exec 关闭,前端以重连+版本变化确认更新完成。
	// argv 必须补回 "server":main() 的子命令分派会把 os.Args 重写为去掉子命令的
	// 形式([moon, --data ...]),原样传给新进程会被再次当子命令解析而启动失败
	// (报「未知子命令 --data」)。
	argv := os.Args
	if len(argv) < 2 || argv[1] != "server" {
		argv = append([]string{argv[0], "server"}, argv[1:]...)
	}
	setUpdateProgress("exec", "面板原地重启", 0, 0, "")
	return syscall.Exec(self, argv, os.Environ())
}
