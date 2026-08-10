//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// applyProcessGroup 让命令在自己的进程组运行（POSIX）：超时/中断时用
// killProcessGroup 杀整个进程组，防后台派生进程（nohup ... &、npm run dev &）
// 成为孤儿残留（Bug06(b)，2026-08-10）。
func applyProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 杀掉命令所在进程组（含后台派生进程）。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
