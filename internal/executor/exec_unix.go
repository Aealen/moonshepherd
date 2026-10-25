//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr 建立进程组：超时时杀整组（sh 的子进程不残留）。
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
