//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// applyProcessGroup 让命令在自己的进程组运行（POSIX）：Esc/超时/退出时用
// killProcessTree（background_unix.go）杀整个进程组，防后台派生进程
// （nohup ... &、npm run dev &）成为孤儿残留（Bug06(b) 语义，ADR-038）。
func applyProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
