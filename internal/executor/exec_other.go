//go:build !unix

package executor

import "os/exec"

// SetSysProcAttr 非 Unix 平台（Windows 开发机）：无进程组语义，跳过。
func SetSysProcAttr(cmd *exec.Cmd) {}
