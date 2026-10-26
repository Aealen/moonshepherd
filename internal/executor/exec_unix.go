//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// SetSysProcAttr 建立进程组：超时/关闭时杀整组（sh 的子进程不残留）。
// 导出供 agentd 终端会话复用。
func SetSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
