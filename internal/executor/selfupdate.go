// Agent 自更新(docs/01 §3.5;docs/04 §5.3 agent.update)。
//
// 防失联三重保障:
//  1. 下载/校验全程不动正在运行的旧二进制——失败即回执,连接保持;
//  2. 校验通过后写到旁路文件(<self>.new),syscall.Exec 原地接管(裸跑形态),
//     或 systemctl restart(systemd 形态)——两者都不销毁旧文件,回滚 = 换回文件名;
//  3. 拉起者无法判定(unknown)时拒绝更新——宁可不动,不可失联。
package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/aealen/moonshepherd/internal/protocol"
)

// DetectSupervisor 探测当前进程的拉起者形态(多部署形态兼容核心)。
func DetectSupervisor() string {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		if b, err := os.ReadFile("/proc/self/cgroup"); err == nil && strings.Contains(string(b), "systemd") {
			// systemd 机且本进程在 systemd cgroup 里 → unit 托管
			return protocol.UpdateSupervisorSystemd
		}
	}
	// 其余(裸 nohup/容器内无 systemd/手跑):按裸跑处理。
	// 容器内 Agent(scratch/自定义镜像)由容器 restart 策略兜底,exec 接管同样成立。
	return protocol.UpdateSupervisorBare
}

// SelfPath 当前二进制的绝对路径(已解析符号链接)。
func SelfPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// SelfUpdate 执行自更新:下载 → sha256 → 落旁路 → 按形态接管。
// 任何失败都不触碰旧二进制;成功即不返回(进程被替换)。
func SelfUpdate(ctx context.Context, p protocol.AgentUpdatePayload, timeoutS int) error {
	if p.Version == "" || p.DownloadURL == "" || len(p.SHA256) != 64 {
		return fmt.Errorf("payload 非法:version/download_url/sha256 必填")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("自更新仅支持 Linux;当前 %s", runtime.GOOS)
	}
	if !strings.HasPrefix(p.DownloadURL, "https://") && !strings.HasPrefix(p.DownloadURL, "http://localhost") &&
		!strings.Contains(p.DownloadURL, "127.0.0.1") {
		return fmt.Errorf("download_url 必须 https(本机调试除外)")
	}
	self := SelfPath()
	if self == "" {
		return fmt.Errorf("无法定位自身二进制路径")
	}
	if timeoutS <= 0 || timeoutS > 600 {
		timeoutS = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	defer cancel()

	// ① 下载到旁路临时文件(同目录保证 rename 原子性)
	tmp := self + ".new"
	if err := downloadTo(ctx, p.DownloadURL, tmp); err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer os.Remove(tmp) // 失败/中断时清理;成功路径文件已被 rename 消耗

	// ② sha256 校验(校验不过旧版原封不动)
	want, _ := hex.DecodeString(p.SHA256)
	f, err := os.Open(tmp)
	if err != nil {
		return fmt.Errorf("打开下载文件失败: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return fmt.Errorf("读取下载文件失败: %w", err)
	}
	f.Close()
	if !bytesEqual(h.Sum(nil), want) {
		return fmt.Errorf("sha256 校验失败:期望 %s… 实得 %s…", p.SHA256[:12], hex.EncodeToString(h.Sum(nil))[:12])
	}

	// ③ 可执行位 + 原子就位(rename 覆盖 .new → .updated;旧 self 仍在原地运行)
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("chmod 失败: %w", err)
	}
	updated := self + ".updated"
	if err := os.Rename(tmp, updated); err != nil {
		return fmt.Errorf("就位失败: %w", err)
	}

	// ④ 按形态接管重启
	switch DetectSupervisor() {
	case protocol.UpdateSupervisorSystemd:
		// systemd 托管:先把新二进制换到 unit 指向的路径,再由 systemd 拉起
		// (旧文件保留为 <self>.bak 供回滚;换名是原子的,restart 前后进程不受影响)
		bak := self + ".bak"
		_ = os.Remove(bak)
		if err := os.Rename(self, bak); err != nil {
			return fmt.Errorf("备份旧版失败(未重启,安全): %w", err)
		}
		if err := os.Rename(updated, self); err != nil {
			_ = os.Rename(bak, self) // 回滚
			return fmt.Errorf("新版就位失败(已回滚,未重启): %w", err)
		}
		out, err := exec.CommandContext(ctx, "systemctl", "restart", "herd").CombinedOutput()
		if err != nil {
			// restart 失败:恢复旧版再重启一次,保证节点回到可用状态
			_ = os.Rename(self, updated)
			_ = os.Rename(bak, self)
			_, _ = exec.Command("systemctl", "restart", "herd").CombinedOutput()
			return fmt.Errorf("systemctl restart 失败(已回滚旧版): %s", strings.TrimSpace(string(out)))
		}
		// 成功后本进程随 restart 终止,不再有回执——面板以重连+新版本号确认
		go func() { time.Sleep(300 * time.Millisecond); os.Exit(0) }()
		time.Sleep(5 * time.Second) // 给 exit 留窗口
		return nil

	default: // bare:nohup/User Scripts/容器 restart 策略
		// 备份旧版 → 新版换到 self → syscall.Exec 原地接管
		// (Exec 不产生新进程:同一 PID 直接换身,连接由新进程继承前关闭;
		//  User Scripts 场景进程退出后由脚本/人重启——Exec 的意义在于
		//  校验过的新版已在运行路径上,重启即新版)
		bak := self + ".bak"
		_ = os.Remove(bak)
		if err := os.Rename(self, bak); err != nil {
			return fmt.Errorf("备份旧版失败(未切换,安全): %w", err)
		}
		if err := os.Rename(updated, self); err != nil {
			_ = os.Rename(bak, self)
			return fmt.Errorf("新版就位失败(已回滚): %w", err)
		}
		argv0 := self
		argv := []string{argv0}
		return syscall.Exec(argv0, argv, os.Environ()) // 成功不返回
	}
}

// downloadTo 流式下载(上限保护)。
func downloadTo(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, protocol.UpdateDownloadMaxBytes+1))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if n > protocol.UpdateDownloadMaxBytes {
		os.Remove(tmp)
		return fmt.Errorf("超过 %dMB 上限", protocol.UpdateDownloadMaxBytes/(1<<20))
	}
	return os.Rename(tmp, dst)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
