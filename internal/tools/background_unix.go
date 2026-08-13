//go:build !windows

package tools

import (
	"os"
	"syscall"
)

// processTreeHandle POSIX = 零值占位：杀树走进程组语义（applyProcessGroup 的
// Setpgid，进程组 ID = 进程 PID），无需额外句柄资源。
type processTreeHandle = struct{}

func createProcessTree() (processTreeHandle, error) { return struct{}{}, nil }
func attachProcessTree(h processTreeHandle, proc *os.Process) error {
	return nil
}

// killProcessTree POSIX：杀进程组（含后台派生进程，Bug06(b) 语义）。
// 防御 pid<=0：kill(-0) = kill(0) 会杀 harness 自身进程组（未 Start 时 pid=0）。
func killProcessTree(h processTreeHandle, pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func closeProcessTree(h processTreeHandle) {}
